// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"sort"

	"github.com/pdbethke/corralai/internal/queue"
)

// MethodCoverageLines is TestSelection.Method for a run that graded each
// mutant with the tests that reach its own span. It is deliberately NOT the
// selection's own "coverage-context": that names the file-level narrowing,
// and a verdict earned per mutant answers a different question — so it keys
// differently in the cache and reads differently in the record.
const MethodCoverageLines = "coverage-lines"

// verdictFromSpec builds the spec-derived fields of a Verdict: the run's own
// identity (Repo, Commit, Lang) and how it discloses its Selection
// (TestSelection, Uncovered). Factored out of aggregate so a test can assert
// the Selection-to-Verdict mapping without also supplying every scored
// component aggregate itself requires.
func verdictFromSpec(rs RunSpec) Verdict {
	return Verdict{
		Repo:   rs.Repo,
		Commit: rs.Commit,
		Lang:   rs.Lang,
		TestSelection: TestSelection{
			Method: rs.Selection.Method, Selected: len(rs.Selection.Tests),
			Of: rs.Selection.Of, Fallback: rs.Selection.Fallback,
		},
		Uncovered: rs.Selection.Method != "" && len(rs.Selection.Tests) == 0,
	}
}

// applyPerMutantStats fills the Verdict's per-mutant disclosure from the
// mutant refs the run finished with — the ONE place that turns "each mutant
// was graded by its own command" into something a reader (and the ledger) can
// check: how many tests each mutant really ran, and why it got them.
//
// graded says whether the run graded per mutant at all. It is passed rather
// than inferred from the refs because an exam with no graded mutants left
// (every mutant rejected by the compile gate) would otherwise read as an
// ordinary whole-selection run, which is a different claim about what
// happened.
//
// Method becomes "coverage-lines": the kill rate is no longer the file
// selection's measurement, and a cached verdict keyed on the old Method would
// be served for a run that measured something else.
func applyPerMutantStats(v *Verdict, graded bool, refs ...[]MutantRef) {
	if !graded {
		return
	}
	v.TestSelection.PerMutant = true
	v.TestSelection.Method = MethodCoverageLines
	rules := map[string]int{}
	var counts []int
	for _, group := range refs {
		for _, r := range group {
			if r.Rule != "" {
				rules[r.Rule]++
			}
			// Only a mutant whose grading recorded a count: a zero is "not
			// known", and averaging it in would understate every spread.
			if r.TestsRun > 0 {
				counts = append(counts, r.TestsRun)
			}
		}
	}
	v.TestSelection.Rules = rules
	if len(counts) == 0 {
		return
	}
	sort.Ints(counts)
	v.TestSelection.TestsPerMutant.Min = counts[0]
	v.TestSelection.TestsPerMutant.Max = counts[len(counts)-1]
	// The upper of the two middle values on an even count, not their mean:
	// this is a count of tests, and reporting a median of 2 when no mutant
	// ran 2 tests would be a number nothing measured.
	v.TestSelection.TestsPerMutant.Median = counts[len(counts)/2]
}

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
	testWriterFailed bool,
	poolTestUnsound bool,
) Verdict {
	v := verdictFromSpec(rs)
	v.DevKillRate = devKillRate
	v.MutantsTotal = mutantsTotal
	v.Survivors = survivors
	v.ProvenMissed = provenMissed
	v.VacuousFindings = vacuousFindings
	v.ModelsByRole = map[string]string(assign)
	v.Status = StatusCertified
	v.TestWriterFailed = testWriterFailed
	v.PoolTestUnsound = poolTestUnsound
	// aggregate is only ever reached via tickAggregate, which itself is
	// gated on run.poolScored — reachable only once tickDevAdequacy has
	// already set run.devScored (see driver.go's Tick). A converged
	// verdict's numbers are always real measurements, never a fabricated
	// zero — see Verdict.DevScored's doc.
	v.DevScored = true
	// The SIGNED certify/needs-review decision rests on execution-proven signals:
	// the mutation kill-rate against the threshold, run in the jail. The
	// test-critic's vacuous-test flags are a SECOND MODEL'S UNVERIFIED OPINION
	// (VacuousFindings) — carried as advisory review but never gating the signed
	// record, because an LLM opinion can be wrong (it once "flagged" a valid test
	// by hallucinating that islice doesn't raise on a negative index). A
	// tamper-evident record must assert only what execution proves; the caller
	// passes blockingFindingOpen=false for critic findings, keeping the parameter
	// for a future EXECUTION-VERIFIED finding path.
	// testWriterFailed forces needs-review UNCONDITIONALLY, regardless of
	// where devKillRate lands against threshold: it means real survivors were
	// found (Survivors > 0) that the pool could NOT prove killable with a
	// compiling test. A high devKillRate (e.g. 96%, 1 survivor) must never
	// sail past the threshold check and auto-certify an unproven gap — that
	// would silently misrepresent "gap found, not proven" as "clean."
	// poolTestUnsound forces it the same way: a compiling test WAS produced,
	// but its own report never genuinely graded — auto-certifying past that
	// would be the same silent misrepresentation testWriterFailed guards
	// against, from a different cause.
	if blockingFindingOpen || devKillRate < threshold || testWriterFailed || poolTestUnsound {
		v.Status = StatusNeedsReview
	}
	return v
}
