// SPDX-License-Identifier: Elastic-2.0

package advpool

import "github.com/pdbethke/corralai/internal/queue"

// aggregate composes a run's Verdict from its scored components and applies
// the human gate: a blocking finding (open, at/above BlockSeverity) OR a
// below-threshold DevKillRate always routes to needs-review. The pool never
// auto-certifies a run it isn't confident in — certification is the
// exception a clean, adequately-tested run earns, not the default.
func aggregate(
	rs RunSpec,
	assign RoleAssignment,
	devKillRate float64,
	mutantsTotal, survivors, provenMissed int,
	vacuousFindings []queue.Finding,
	threshold float64,
	blockingFindingOpen bool,
) Verdict {
	v := Verdict{
		Repo:            rs.Repo,
		Commit:          rs.Commit,
		DevKillRate:     devKillRate,
		MutantsTotal:    mutantsTotal,
		Survivors:       survivors,
		ProvenMissed:    provenMissed,
		VacuousFindings: vacuousFindings,
		ModelsByRole:    map[string]string(assign),
		Status:          StatusCertified,
	}
	if blockingFindingOpen || devKillRate < threshold {
		v.Status = StatusNeedsReview
	}
	return v
}
