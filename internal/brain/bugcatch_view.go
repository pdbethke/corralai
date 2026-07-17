// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"

	"github.com/pdbethke/corralai/internal/bugcatch"
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
	// observations to rank, even though the numbers are real.
	Provisional bool `json:"provisional"`
}

// Scorecard is the full model×role bug-catching matrix.
type Scorecard struct {
	Cells []ScorecardCell `json:"cells"`
}

// BuildBugCatchScorecard computes the bug-catching scorecard from the
// adversarial pool's execution-proven observations (internal/bugcatch). A
// nil store (feature disabled / not opened) degrades to an empty scorecard
// rather than an error.
func BuildBugCatchScorecard(store *bugcatch.Store) (Scorecard, error) {
	if store == nil {
		return Scorecard{}, nil
	}
	cells, err := store.Scorecard(context.Background())
	if err != nil {
		return Scorecard{}, err
	}
	out := make([]ScorecardCell, 0, len(cells))
	for _, c := range cells {
		out = append(out, ScorecardCell{Cell: c, Provisional: c.Runs < provisionalBelow})
	}
	return Scorecard{Cells: out}, nil
}
