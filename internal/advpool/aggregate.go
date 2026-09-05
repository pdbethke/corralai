// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"fmt"
	"sort"
	"time"

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
// THE SHARED BASE OF BOTH Verdict CONSTRUCTION PATHS. Every Verdict starts
// here, but TWO callers then assign scored fields onto it: `tickAggregate` (the
// converged path) and `timeoutVerdict` (the banked-timeout path) — see the
// latter's own comment, which records that it "has now been the place a field
// was forgotten more than once."
//
// So the single `Verdict{}` literal below is REASSURING AND MISLEADING: it
// means a grep finds one construction site while the field assignments live in
// two, which is the field-by-field-converter defect AGENTS.md names. A new
// scored field must be added to BOTH assignment paths, and the only durable
// guard is a test asserting the value survives Score -> aggregate -> report ->
// ledger -> attestation, rather than a reader noticing the second site.
func verdictFromSpec(rs RunSpec) Verdict {
	return Verdict{
		Repo:          rs.Repo,
		Commit:        rs.Commit,
		Lang:          rs.Lang,
		PriorsApplied: rs.PriorsApplied, PriorDigest: rs.PriorDigest, PriorSource: rs.PriorSource,
		TestSelection: TestSelection{
			Method: rs.Selection.Method, Selected: len(rs.Selection.Tests),
			Of: rs.Selection.Of, Fallback: rs.Selection.Fallback,
			AuthoredAlone: AuthoredAlone(rs),
		},
		// Concurrency rides straight through — including onto a timed-out
		// verdict (timeoutVerdict builds off this same function), since the
		// probe ran and the pool's trees were established before the pool
		// itself ever failed to converge.
		Concurrency: rs.Concurrency,
		// Uncovered stays the UNION of both shapes (genuinely dead, or
		// import-only) — every existing consumer withholds the rate on
		// this flag alone, and that withholding is correct for both. See
		// Verdict.Uncovered/ImportOnly's own docs for why ImportOnly is a
		// REFINEMENT, never a replacement.
		Uncovered: rs.Selection.Method != "" && len(rs.Selection.Tests) == 0,
		// ImportOnly narrows Uncovered to the shape where the evidence ALSO
		// recorded static (import-time) coverage — len(rs.Selection.Static)
		// > 0 — the same signal reposcan.WidenCandidacyByEvidence's
		// ReasonImportOnly already keys off at candidacy time, now applied
		// here too since the same false claim ("no test executes this
		// file") can reach a reader through a PAIRED file's grading, not
		// only through candidacy.
		ImportOnly: rs.Selection.Method != "" && len(rs.Selection.Tests) == 0 && len(rs.Selection.Static) > 0,
		// The two phases the driver could not have timed, from the caller
		// that did (see RunSpec.SelectionDuration/PoolDuration). They ride
		// through here — the shared construction site — so the TIMED-OUT
		// verdict carries them too: the scan's instrumented run and the
		// pool's copies were paid for whether or not the pool converged, and
		// a timeout that reported them as unmeasured would understate the
		// cost of exactly the runs that cost the most.
		Timing: Timing{Selection: rs.SelectionDuration, Pool: rs.PoolDuration},
	}
}

// timingWith overlays the phases the DRIVER measured onto the ones the SPEC
// supplied, leaving each side to own the fields it actually measured. Total
// belongs to neither and is set by the caller at the moment the run ends.
//
// A plain assignment would be the bug this function exists to prevent: both
// Verdict construction sites build from verdictFromSpec (which fills
// Selection and Pool) and then have the run's own five to add, and
// `v.Timing = run.timing` at either of them silently erases the caller's two.
func timingWith(spec, run Timing) Timing {
	spec.Generation = run.Generation
	spec.DevPass = run.DevPass
	spec.AuthoredPass = run.AuthoredPass
	spec.Critic = run.Critic
	return spec
}

// totalWith is what THIS FILE's audit cost: the driver's own elapsed time
// plus Pool, which is per file and is paid before StartRun (the checkout is
// copied and probed while the jail wiring is built, so it falls outside the
// "now minus startedAt" window).
//
// SELECTION IS DELIBERATELY NOT ADDED. The instrumented coverage run happens
// ONCE for the whole scan and is shared by every file of it. Charging it to
// each file would make `sum(total_ms)` over a scan's audits count one run
// once per file — a number that grows with the file count and measures
// nothing. It is still REPORTED per file, so a readout can name every phase
// of that file's audit, and it is RECORDED once, on the scan header
// (scanstore.Scan.SelectionMillis / corral_scans.selection_ms), which is the
// column a cost query adds.
func totalWith(t Timing, driverElapsed time.Duration) time.Duration {
	return driverElapsed + t.Pool
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
	// Set only here, and only with counts in hand: the field is a pointer so
	// that every other path leaves it ABSENT rather than {0,0,0}.
	v.TestSelection.TestsPerMutant = &TestsPerMutantSpread{
		Min: counts[0],
		Max: counts[len(counts)-1],
		// The upper of the two middle values on an even count, not their
		// mean: this is a count of tests, and reporting a median of 2 when
		// no mutant ran 2 tests would be a number nothing measured.
		Median: counts[len(counts)/2],
	}
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
	certifyIntervalWidth float64,
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
	// The converged path is, by definition, pool-scored: aggregate is reached
	// only via tickAggregate, which is itself gated on run.poolScored. Setting
	// it here rather than leaving it to the caller keeps the two construction
	// paths saying the same thing about the same field — the drift this
	// function's own note warns about.
	v.PoolScored = true
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
	// AN EXAM TOO SMALL TO CERTIFY. A kill rate is a proportion over the
	// mutants graded, and its 95% interval says how much the rate could move
	// on a re-roll of the same exam: five of eight killed is 0.62 with a band
	// of 0.31–0.86, and no reading of that band says "adequate". The rule is
	// the band's WIDTH, not a minimum n, because width is what the reader
	// actually needs and n alone misjudges the edges (8 of 8 is a narrower
	// claim than 4 of 8). The point estimate still decides the rate; this
	// decides whether the rate is a grade or an indication. The complexity
	// budget's floor of five can never certify, which is the honest answer
	// for a file with five decision points.
	if v.Status == StatusCertified && mutantsTotal > 0 && certifyIntervalWidth > 0 {
		killed := mutantsTotal - survivors
		if killed < 0 {
			killed = 0
		}
		if lo, hi, ok := WilsonInterval(killed, mutantsTotal); ok && hi-lo > certifyIntervalWidth {
			v.Status = StatusNeedsReview
			v.ExamIndicative = true
			v.IndicativeReason = fmt.Sprintf("exam too small to certify: %d mutants, 95%% interval %.2f–%.2f (width %.2f, the most a certified verdict may carry is %.2f)",
				mutantsTotal, lo, hi, hi-lo, certifyIntervalWidth)
		}
	}
	return v
}
