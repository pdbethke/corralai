// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"math"
	"sort"
	"time"
)

// maxUngradableDetailsPerReason bounds how many sample detail lines
// Aggregate keeps per ungradable reason — see RepoReport.UngradableDetails.
const maxUngradableDetailsPerReason = 5

// WeakFile is one entry in the ranked weakest-files list.
type WeakFile struct {
	Path      string
	KillRate  float64
	Survivors int
	// TimedOut mirrors advpool.Verdict.TimedOut: true when this file's score
	// was banked from a run that hit its wall-clock deadline before the pool
	// converged, rather than a clean run. The number is real (the dev suite
	// WAS measured — see advpool.Verdict.DevScored, which gates whether a
	// timed-out file is even Gradable at all), but the pool's remaining
	// "make the tests stronger" work (test-writer, shadow, critic) did not
	// finish — a caller must print this distinctly, never silently alongside
	// a clean convergence.
	TimedOut bool
	// TestWriterFailed mirrors advpool.Verdict.TestWriterFailed: true when
	// this file's run exhausted its compile-retry budget without authoring
	// a compiling killing test. HONESTY NOTE: Survivors > 0 here with
	// TestWriterFailed true does NOT mean "no real bugs" — ProvenMissed
	// (below) is 0 because no killing test was ever authored, not because
	// the survivors aren't real. A caller must print this distinctly, the
	// same way TimedOut is — printing bare survivor counts here invites
	// exactly the "corral found nothing" misreading this field exists to
	// prevent.
	TestWriterFailed bool
	// ProvenMissed mirrors advpool.Verdict.ProvenMissed: survivors the
	// pool's authored test then killed by EXECUTION — corral's strongest
	// claim, a specific demonstrated bug the dev suite misses. It is
	// carried on WeakFile precisely so a caller does not have to reach back
	// into the verdict to report it.
	//
	// HONESTY NOTE — ProvenMissed==0 is ambiguous on its own and must never
	// be printed bare. Combined with the other WeakFile fields it resolves
	// to exactly one of three cases:
	//   1. Survivors == 0: there was nothing to prove — the test-writer
	//      never ran (advpool's testWriterMoot). A real "clean" result.
	//   2. Survivors > 0 && TestWriterFailed: the writer never produced a
	//      compiling test — see TestWriterFailed's doc above.
	//   3. Survivors > 0 && !TestWriterFailed: the writer's authored test
	//      ran and killed none of the survivors — a real "nothing proven"
	//      result, distinct from both of the above.
	// A caller printing this field must print enough of the other three
	// (Survivors, TestWriterFailed) alongside it that a reader can tell
	// which case they are looking at without re-deriving it.
	ProvenMissed int
	// AuthoredTest is the test the pool WROTE and RAN to prove a survivor was
	// a real, catchable gap. Carried onto the report because `--repo` — the
	// mode the GitHub Action runs — reported "N proven, catchable gap(s)" and
	// then dropped the one artifact that makes that number actionable. A
	// developer was told a gap is provable and handed nothing to act on; the
	// test existed, had already compiled and executed, and went to the floor.
	//
	// Only meaningful alongside ProvenMissed > 0.
	AuthoredTest string
	// PoolTestUnsound mirrors advpool.Verdict.PoolTestUnsound: true when the
	// pool's authored test DID compile (TestWriterFailed is false) but its
	// scoring report never genuinely graded (it failed on the unmutated
	// compliant code, or the canary was never killed, or nothing was
	// scored). A DIFFERENT diagnosis from TestWriterFailed — a compiling
	// test WAS produced — but the same honesty rule: ProvenMissed reads 0
	// here for a reason that is neither "clean" nor "tried and missed," and
	// a caller must print it distinctly, the same way TestWriterFailed is.
	PoolTestUnsound bool
	// SelectionMethod, SelectedTests and SuiteTests mirror
	// advpool.Verdict.TestSelection: WHICH measurement this kill rate is —
	// the tests coverage evidence showed execute this file (Method, e.g.
	// "coverage-context", SelectedTests of SuiteTests), rather than the whole
	// suite. A rate earned against 14 of 1431 tests and one earned against
	// all 1431 answer different questions, and a line that prints only the
	// number leaves the reader to assume the wrong one.
	SelectionMethod string
	SelectedTests   int
	SuiteTests      int
	// SelectionFallback mirrors advpool.Verdict.TestSelection.Fallback: this
	// file was graded by the WHOLE suite, and this is why (no selector for
	// the language, --whole-suite, an evidence run that failed). Empty when
	// SelectionMethod is set. Never both.
	SelectionFallback string
	// PerMutant and TestsPerMutant{Min,Median,Max} mirror
	// advpool.Verdict.TestSelection.PerMutant / .TestsPerMutant: each mutant
	// was graded by the tests that reach ITS OWN lines, not by one command
	// shared across the file. SelectedTests is then the file's UNION — the
	// tests any mutant faced — and no mutant's own denominator, so the
	// spread is what says how much the narrowing actually narrowed. A Min
	// equal to the Max means every mutant faced the same set after all.
	// Zero on a run that graded the file with one shared command, which is
	// why a printer must gate the clause on PerMutant rather than on the
	// numbers being nonzero.
	PerMutant            bool
	TestsPerMutantMin    int
	TestsPerMutantMedian int
	TestsPerMutantMax    int
	// Uncovered mirrors advpool.Verdict.Uncovered: the evidence ran and found
	// NO test executing this file. Its kill rate is not a measurement of the
	// suite's strength — nothing graded the file — so a caller must withhold
	// the number rather than print the 0.00 that would read as "your tests
	// caught nothing here".
	Uncovered bool
}

// RepoReport is the repo-level result. It is mostly ACCOUNTING, because that
// is what makes the headline number honest: a reader can see exactly what the
// score covers and what it does not.
type RepoReport struct {
	Owner, Repo, Commit string

	TotalFiles int
	// Candidates is the number of files enumeration judged AUDITABLE, before
	// any of them were dropped for want of a goal. It is not the number of
	// jobs: counting jobs would erase every ungoaled file from the ratio
	// below, so a repo with one goal out of five hundred candidates would
	// report 100% audited.
	Candidates int
	Audited    int
	Excluded   []Exclusion
	Ungradable map[string]int
	// UngradableDetails carries, per ungradable reason, a bounded sample of
	// "path: detail" lines drawn from FileResult.Detail — today only
	// executor-error results supply one. Ungradable alone is a COUNT ("1
	// executor-error"); this is the part that lets an operator find out WHY
	// without re-running with extra flags or reading source. Bounded
	// (maxUngradableDetailsPerReason) so a repo-wide failure (e.g. every
	// Python file failing the same toolchain preflight) does not turn the
	// report into a wall of identical lines.
	UngradableDetails map[string][]string

	// KillRate is over Audited ONLY, never over the repo. NaN when nothing
	// was audited — a 0.0 there would read as "terrible tests" when the truth
	// is "no measurement was made".
	KillRate float64
	// TimedOut counts Audited files whose score was banked from a run that
	// hit its wall-clock deadline before the pool converged (see
	// WeakFile.TimedOut) — a report-level caveat so a reader scanning past
	// "kill rate X% over N audited files" cannot mistake a repo with several
	// unconverged runs for a fully clean audit.
	TimedOut int
	// TestWriterFailed counts Audited files whose pool exhausted its
	// compile-retry budget without authoring a killing test for at least
	// one survivor (see WeakFile.TestWriterFailed) — a report-level caveat
	// alongside TimedOut, for the same reason: "kill rate X% over N audited
	// files" must not read as "every survivor was proven, or ruled out" when
	// some were neither.
	TestWriterFailed int
	// PoolTestUnsound counts Audited files whose pool authored a compiling
	// test that never genuinely graded (see WeakFile.PoolTestUnsound) — a
	// third report-level caveat alongside TimedOut/TestWriterFailed.
	PoolTestUnsound int
	// ProvenMissed sums WeakFile.ProvenMissed over every audited file whose
	// score is a clean, converged run — the repo-wide count of survivors
	// execution actually proved catchable. Corral's strongest claim, rolled
	// up.
	//
	// TimedOut files are deliberately EXCLUDED from this sum: a banked
	// timeout verdict can carry a real ProvenMissed from a pool-adequacy
	// step that finished before the deadline hit, but printRepoReport
	// suppresses the per-file "N proven missed" text for a timed-out file
	// (its marker is [TIMED OUT], not a proven-missed count) — summing it in
	// here anyway would make the repo-level total refer to a number the
	// per-file listing never shows, an unlocatable claim. Like the per-file
	// field, 0 here is ambiguous on its own (see WeakFile.ProvenMissed's
	// three cases, plus PoolTestUnsound) and a caller must not print it
	// without the TestWriterFailed/PoolTestUnsound/TimedOut caveats
	// alongside it.
	ProvenMissed int

	// SelectedFiles, WholeSuiteFiles and UncoveredFiles partition the audited
	// files by WHICH MEASUREMENT they got. SelectedFiles + WholeSuiteFiles ==
	// Audited; UncoveredFiles is a SUBSET of SelectedFiles, not a third bucket:
	// "no test executes this file" is a finding of the selection evidence, so
	// an uncovered file was graded under selection — it just had nothing to
	// select. A reader must be told all three, because a repo-level kill rate
	// averaged across two different questions is not one number.
	SelectedFiles   int
	WholeSuiteFiles int
	UncoveredFiles  int
	// GradedFiles is Audited MINUS UncoveredFiles — the denominator KillRate
	// is actually averaged over. It is a separate number because an uncovered
	// file was audited (corral looked at it, and found that nothing executes
	// it) but never GRADED, so including it in the mean would publish a
	// number no measurement supports. A printer must show this denominator
	// whenever it differs from Audited.
	GradedFiles int

	Weakest     []WeakFile
	CacheHits   int
	GeneratedAt time.Time
}

// AuditedFraction is the share of candidates that produced a real score. The
// coverage floor (H1c) is applied to this.
//
// The DENOMINATOR IS ALL CANDIDATES — every file enumeration judged auditable,
// including the ones a bounded scan deliberately left outside --top. That is
// the deliberately safe direction: a top-25 scan of a 431-candidate repo
// reports ~6% coverage rather than 100%, so the number can never overstate what
// was actually measured. It does mean a bounded scan under-reports its coverage
// OF THE BOUND, which is the trade taken on purpose.
//
// A consumer that needs the bounded/unaudited split — "of the 25 we chose, how
// many graded?" — must read Excluded and separate ReasonNotSelected (a
// deliberate bound) from the failure reasons. This method will not do it: one
// number cannot answer both questions, and the safe one is the one that gets
// signed.
func (r RepoReport) AuditedFraction() float64 {
	if r.Candidates == 0 {
		return 0
	}
	return float64(r.Audited) / float64(r.Candidates)
}

// Aggregate rolls per-file results into the repo report.
//
// candidates is the PRE-GOAL candidate count from enumeration, not len(results):
// results only covers candidates that became jobs, and the difference — files
// with no goal — belongs in the audited-fraction denominator, accounted under
// ReasonUngoaled. Passing len(results) here would report "100% audited" for a
// repo where one file in five hundred had a goal.
func Aggregate(owner, repo, commit string, totalFiles, candidates int, results []FileResult, excl []Exclusion) RepoReport {
	// Fail safe rather than print a fraction above 1.0: a caller that
	// under-counts candidates gets the honest floor, never a flattering ratio.
	if candidates < len(results) {
		candidates = len(results)
	}
	rep := RepoReport{
		Owner: owner, Repo: repo, Commit: commit,
		TotalFiles:        totalFiles,
		Candidates:        candidates,
		Excluded:          excl,
		Ungradable:        map[string]int{},
		UngradableDetails: map[string][]string{},
		GeneratedAt:       time.Now(),
	}
	// Candidates dropped BEFORE they became jobs are absent from results, so
	// they are counted from the exclusions. Each is folded under its own
	// reason — never merged — because the distinction is the whole taxonomy:
	// ungoaled and source-too-large are properties of the FILE, derive-failed
	// is infrastructure.
	//
	// ReasonNotSelected is deliberately NOT folded. A bound the operator asked
	// for is not a failure to grade; listing 189 not-selected files as
	// "ungradable" would read as "this repo could not be graded" when the truth
	// is "we graded exactly what you asked for". It is still fully accounted in
	// Excluded, and AuditedFraction already carries it in the denominator.
	for _, e := range excl {
		switch e.Reason {
		case ReasonUngoaled, ReasonDeriveFailed, ReasonSourceTooLarge:
			rep.Ungradable[e.Reason]++
		}
	}

	var sum float64
	for _, r := range results {
		if r.CacheHit {
			rep.CacheHits++
		}
		if !r.Gradable {
			reason := r.Reason
			if reason == "" {
				reason = ReasonExecutorError
			}
			rep.Ungradable[reason]++
			if r.Detail != "" && len(rep.UngradableDetails[reason]) < maxUngradableDetailsPerReason {
				rep.UngradableDetails[reason] = append(rep.UngradableDetails[reason], r.Job.Path+": "+r.Detail)
			}
			continue
		}
		rep.Audited++
		// An UNCOVERED file's rate is not a measurement: the selection
		// evidence found no test that executes it, so nothing graded it, and
		// its 0.0 averaged into the headline would report the repo's suite as
		// weaker than anything actually measured says it is — the same
		// fabricated number the per-file line and the ledger both refuse to
		// print, arriving through the mean instead. It stays counted in
		// Audited (it WAS looked at, and UncoveredFiles says what was found)
		// but out of the numerator and out of GradedFiles below.
		if r.Verdict.TimedOut {
			rep.TimedOut++
		}
		if r.Verdict.TestWriterFailed {
			rep.TestWriterFailed++
		}
		if r.Verdict.PoolTestUnsound {
			rep.PoolTestUnsound++
		}
		// See RepoReport.ProvenMissed's doc: a timed-out file's proven-missed
		// count (if any) is never shown per-file, so it must not be summed
		// into a repo-level total the printed listing can't back up.
		if !r.Verdict.TimedOut {
			rep.ProvenMissed += r.Verdict.ProvenMissed
		}
		if r.Verdict.TestSelection.Method != "" {
			rep.SelectedFiles++
		} else {
			rep.WholeSuiteFiles++
		}
		if r.Verdict.Uncovered {
			rep.UncoveredFiles++
		} else {
			rep.GradedFiles++
			sum += r.Verdict.DevKillRate
		}
		rep.Weakest = append(rep.Weakest, WeakFile{
			Path:             r.Job.Path,
			KillRate:         r.Verdict.DevKillRate,
			Survivors:        r.Verdict.Survivors,
			TimedOut:         r.Verdict.TimedOut,
			TestWriterFailed: r.Verdict.TestWriterFailed,
			ProvenMissed:     r.Verdict.ProvenMissed,
			PoolTestUnsound:  r.Verdict.PoolTestUnsound,
			AuthoredTest:     r.Verdict.AuthoredTest,
			// Which measurement this file's rate IS, carried onto the report
			// so the printer never has to reach back into the verdict — and
			// so it cannot print a rate without the question it answers.
			SelectionMethod:   r.Verdict.TestSelection.Method,
			SelectedTests:     r.Verdict.TestSelection.Selected,
			SuiteTests:        r.Verdict.TestSelection.Of,
			SelectionFallback: r.Verdict.TestSelection.Fallback,
			// And at which GRAIN it was measured: a rate averaged over
			// mutants that each faced a different test set is not one
			// measurement unless the report carries the spread.
			PerMutant:            r.Verdict.TestSelection.PerMutant,
			TestsPerMutantMin:    r.Verdict.TestSelection.TestsPerMutant.Min,
			TestsPerMutantMedian: r.Verdict.TestSelection.TestsPerMutant.Median,
			TestsPerMutantMax:    r.Verdict.TestSelection.TestsPerMutant.Max,
			Uncovered:            r.Verdict.Uncovered,
		})
	}

	// The denominator is GradedFiles, not Audited: see the uncovered note
	// above. NaN when nothing was actually graded — a 0.0 there would read as
	// "terrible tests" when the truth is "no measurement was made", which is
	// exactly the state a repo whose every audited file is uncovered is in.
	if rep.GradedFiles == 0 {
		rep.KillRate = math.NaN()
	} else {
		rep.KillRate = sum / float64(rep.GradedFiles)
	}

	sort.SliceStable(rep.Weakest, func(i, j int) bool {
		return rep.Weakest[i].KillRate < rep.Weakest[j].KillRate
	})
	return rep
}
