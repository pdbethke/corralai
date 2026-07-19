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
		Lang:            rs.Lang,
		DevKillRate:     devKillRate,
		MutantsTotal:    mutantsTotal,
		Survivors:       survivors,
		ProvenMissed:    provenMissed,
		VacuousFindings: vacuousFindings,
		ModelsByRole:    map[string]string(assign),
		Status:          StatusCertified,
	}
	// The SIGNED certify/needs-review decision rests on execution-proven signals:
	// the mutation kill-rate against the threshold, run in the jail. The
	// test-critic's vacuous-test flags are a SECOND MODEL'S UNVERIFIED OPINION
	// (VacuousFindings) — carried as advisory review but never gating the signed
	// record, because an LLM opinion can be wrong (it once "flagged" a valid test
	// by hallucinating that islice doesn't raise on a negative index). A
	// tamper-evident record must assert only what execution proves; the caller
	// passes blockingFindingOpen=false for critic findings, keeping the parameter
	// for a future EXECUTION-VERIFIED finding path.
	if blockingFindingOpen || devKillRate < threshold {
		v.Status = StatusNeedsReview
	}
	return v
}
