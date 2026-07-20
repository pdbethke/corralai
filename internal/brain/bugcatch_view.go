// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/bugcatch"
	"github.com/pdbethke/corralai/internal/criticscore"
)

// provisionalBelow: a cell with fewer than this many runs is a data point, not a
// ranking (the explore-in-production lesson). Consumers must not present it as a leader.
const provisionalBelow = 3

// ScorecardCell is one (model, role) slice of the bug-catching scorecard,
// embedding the raw aggregate from internal/bugcatch plus the honesty flag a
// consumer (UI or otherwise) MUST weigh before treating the cell as a
// ranking rather than a data point.
type ScorecardCell struct {
	bugcatch.Cell
	// Provisional is true when Runs < provisionalBelow — too few
	// observations to rank, even though the numbers are real. For the
	// test-critic role it is ALSO true when the critic-precision sample
	// (CriticConfirmed+CriticRefuted) is below provisionalBelow, even if
	// the cell has plenty of pool runs: a model can run the critic role
	// many times without any of its findings having been adjudicated yet.
	Provisional bool `json:"provisional"`

	// CriticConfirmed/CriticRefuted/CriticUnadjudicated/CriticPrecision are
	// populated ONLY for the test-critic role, joined in from
	// criticscore.Precision (internal/criticscore) — how often this
	// model's critic findings, once a human adjudicated them, turned out
	// to be real. CriticPrecision is nil when there's no adjudicated
	// evidence yet, same honesty convention as bugcatch.Cell.Precision.
	CriticConfirmed     int      `json:"critic_confirmed,omitempty"`
	CriticRefuted       int      `json:"critic_refuted,omitempty"`
	CriticUnadjudicated int      `json:"critic_unadjudicated,omitempty"`
	CriticPrecision     *float64 `json:"critic_precision,omitempty"`
}

// Scorecard is the full model×role bug-catching matrix.
type Scorecard struct {
	Cells []ScorecardCell `json:"cells"`
}

// BuildBugCatchScorecard computes the bug-catching scorecard from the
// adversarial pool's execution-proven observations (internal/bugcatch),
// joining in per-model critic precision (internal/criticscore) onto the
// test-critic cells. A nil bugcatch store (feature disabled / not opened)
// degrades to an empty scorecard rather than an error. A nil criticStore
// (feature disabled) leaves the critic fields at their zero value — the
// scorecard still renders, just without the critic-precision column.
func BuildBugCatchScorecard(store *bugcatch.Store, criticStore *criticscore.Store) (Scorecard, error) {
	if store == nil {
		return Scorecard{}, nil
	}
	cells, err := store.Scorecard(context.Background())
	if err != nil {
		return Scorecard{}, err
	}
	var criticByModel map[string]criticscore.CriticCell
	if criticStore != nil {
		criticCells, err := criticStore.Precision(context.Background())
		if err != nil {
			return Scorecard{}, err
		}
		criticByModel = make(map[string]criticscore.CriticCell, len(criticCells))
		for _, cc := range criticCells {
			criticByModel[cc.Model] = cc
		}
	}
	out := make([]ScorecardCell, 0, len(cells))
	for _, c := range cells {
		cell := ScorecardCell{Cell: c, Provisional: c.Runs < provisionalBelow}
		if c.Role == advpool.RoleTestCritic {
			if cc, ok := criticByModel[c.Model]; ok {
				cell.CriticConfirmed = cc.Confirmed
				cell.CriticRefuted = cc.Refuted
				cell.CriticUnadjudicated = cc.Unadjudicated
				cell.CriticPrecision = cc.Precision
			}
			if cell.CriticConfirmed+cell.CriticRefuted < provisionalBelow {
				cell.Provisional = true
			}
		}
		out = append(out, cell)
	}
	return Scorecard{Cells: out}, nil
}
