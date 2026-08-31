// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"math"
	"sort"
	"time"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/modelcorr"
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
	// AuthoredExtra mirrors advpool.Verdict.AuthoredExtra: proven authored
	// tests the language's concatenator would not fold into AuthoredTest.
	//
	// Every reader MUST render these. Each one is a test corral wrote,
	// compiled and ran to kill a specific survivor, and ProvenMissed counts
	// it — so a report that prints AuthoredTest alone tells a developer that
	// N gaps are provable and hands them fewer than N tests. That is the same
	// "told a gap is provable and handed nothing to act on" failure
	// AuthoredTest itself was added to fix, in a narrower form.
	AuthoredExtra []lang.AuthoredPart
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
	// WriterMode and WriterCalls mirror advpool.Verdict.WriterMode and the
	// test-writer's own entry in advpool.Verdict.ModelCalls: whether this
	// file's survivors were attacked one call each ("per-survivor") or all in
	// one ("batched"), and how many calls that actually took.
	//
	// The COUNT rides beside the mode because the mode alone understates the
	// shape: "per-survivor" over 24 survivors that needed 31 calls says
	// something the label does not (seven repairs). Empty/0 is NOT RECORDED
	// — a run that named no mode, or a verdict from before the mode existed —
	// and a reader must print nothing rather than pick a mode.
	WriterMode  string
	WriterCalls int
	// WriterSeatsUngraded mirrors advpool.Verdict.WriterSeatsUngraded: how
	// many of a per-survivor run's seats never produced a test that genuinely
	// graded. It rides beside the mode because a proven count over
	// twenty-four survivors means something different when three of them were
	// never actually attempted. 0 on a batched run and on a fully-graded
	// fan-out, and the line prints nothing for it.
	WriterSeatsUngraded int
	// PerMutant and TestsPerMutant mirror
	// advpool.Verdict.TestSelection.PerMutant / .TestsPerMutant: each mutant
	// was graded by the tests that reach ITS OWN lines, not by one command
	// shared across the file. SelectedTests is then the file's UNION — the
	// tests any mutant faced — and no mutant's own denominator, so the
	// spread is what says how much the narrowing actually narrowed. A Min
	// equal to the Max means every mutant faced the same set after all.
	// nil on a run that graded the file with one shared command, and also on
	// a per-mutant run whose every mutant was rejected by the compile gate:
	// an unmeasured spread is ABSENT, never {0,0,0}, so no printer, signer
	// or warehouse row can mistake three zeros for a measurement. Ask
	// MeasuredSpread rather than testing the numbers.
	PerMutant      bool
	TestsPerMutant *advpool.TestsPerMutantSpread
	// ProvenByAuthoredAlone mirrors advpool.Verdict.TestSelection.AuthoredAlone:
	// ProvenMissed was established by the authored test alone, not by any
	// test in a shared command.
	ProvenByAuthoredAlone bool
	// Rules mirrors advpool.Verdict.TestSelection.Rules: how many mutants got
	// their command by each rule (lang.SpanRule*). The spread says how much
	// the narrowing narrowed; this says how much of it was narrowing at all —
	// a run whose mutants are mostly "static" or "unreached" ran the file's
	// whole selection for them, and only the breakdown says so. nil on a run
	// that did not grade per mutant.
	Rules map[string]int
	// Uncovered mirrors advpool.Verdict.Uncovered: the evidence ran and found
	// NO test executing this file. Its kill rate is not a measurement of the
	// suite's strength — nothing graded the file — so a caller must withhold
	// the number rather than print the 0.00 that would read as "your tests
	// caught nothing here".
	Uncovered bool
	// Trees and ConcurrencyNote mirror advpool.Verdict.Concurrency: how many
	// private trees the workspace substrate's probe scored this file with
	// at once, or — when it granted only one — why (a downgrade after a
	// baseline that failed under concurrency, or simply the substrate that
	// builds no trees at all). Trees < 1 is the one "not recorded" state —
	// the jail substrate, or a verdict served from a cache row written
	// before this column existed — and every reader of it (the printer, the
	// signer, the ledger, the warehouse) says so rather than inventing a 1.
	// See advpool.Concurrency's doc.
	Trees           int
	ConcurrencyNote string
	// SharedDirs mirrors advpool.Verdict.Concurrency.Shared: the dependency
	// directories symlinked into every tree rather than copied. Disclosure,
	// not decoration — they are the one thing the trees did not hold
	// privately. Empty when nothing was shared.
	SharedDirs []string
	// Timing mirrors advpool.Verdict.Timing: where this file's audit spent
	// its wall clock, phase by phase. Carried onto the report so the printer
	// never reaches back into the verdict — and so the one line an operator
	// reads to find the slow phase is built from the same numbers the ledger
	// stores. Every phase that did not run is zero, and every reader renders
	// that as "—" rather than as a phase that cost nothing.
	Timing advpool.Timing
	// CacheHit mirrors FileResult.CacheHit: this file's verdict was REUSED,
	// not earned by this run. It rides onto the report because Timing and
	// ModelCalls round-trip through verdict_json and come back off a cache
	// hit fully populated — with ANOTHER run's minutes and tokens. Every
	// reader that reports what this scan spent (the per-file `time:` line,
	// the scan totals, the `cost:` line, the ledger's timing columns and
	// scan_model_calls) must exclude a row carrying this flag.
	CacheHit bool
	// ModelCalls mirrors advpool.Verdict.ModelCalls: what this file's audit
	// cost, broken out by role. Carried onto the report for the same reason
	// Timing is — so the ledger mapping and the cost line are built from the
	// SAME rows the printer would read, never a second derivation from the
	// verdict.
	ModelCalls []advpool.ModelCall
	// MutantsGraded mirrors advpool.Verdict.MutantsTotal: the denominator the
	// per-mutant spread below is over. The dev-pass duration alone cannot say
	// whether a slow file had four mutants or four hundred.
	MutantsGraded int
	// MutantMillisMedian and MutantMillisMax are how long grading ONE mutant
	// took — the middle and the worst, in milliseconds, over the mutants that
	// were actually graded. They answer the question the file total cannot:
	// one pathological mutant, or all of them. 0 means nothing timed a
	// mutant, and the line prints no spread rather than "median 0s".
	//
	// Under mutant concurrency each is a CONTENDED wall clock, so median x
	// MutantsGraded can exceed Timing.DevPass — see
	// adequacy.MutantGrading.Duration. A distribution, not a budget.
	MutantMillisMedian int64
	MutantMillisMax    int64
	// Challenger mirrors advpool.Verdict.ChallengerAgreement: the primary
	// writer's agreement with the challenger writer over the survivors BOTH
	// seats genuinely attempted — which under the per-survivor writer mode is
	// the overlap of their measured sets, not necessarily every survivor the
	// file had (see ChallengerAgreement's own doc for why counting the rest
	// would invent a shared blind spot). nil whenever no comparable pair
	// exists — no challenger ran, either seat's kill vector was never
	// measured, the primary salvaged, or the two measured sets do not
	// overlap. Non-nil does NOT mean Jaccard/Kappa are individually
	// meaningful: a caller must still check Challenger.Sufficient /
	// Challenger.KappaDefined before printing or storing either coefficient,
	// and Challenger.Mutants is how many survivors the pair actually covers.
	Challenger *modelcorr.Pair
	// PromptShape mirrors advpool.Verdict.PromptShape: "chunk" when every
	// mutant-generator shard saw only its own symbols' bodies plus the
	// file's preamble, "file" when even one shard fell back to showing the
	// whole file (including an unsharded run, which always showed the whole
	// file). "" — never fabricated — for a run that predates this
	// disclosure, or a preset (`--mutants`) run that generated nothing.
	PromptShape string
}

// MeasuredSpread reports whether this file's run actually measured a
// per-mutant spread — the one question every reader of TestsPerMutant has to
// answer before printing, signing or storing it. A method rather than a
// nil-check repeated at each call site, because the three that used to test
// `Max > 0` were three chances to read an unmeasured zero as a range.
func (w WeakFile) MeasuredSpread() bool { return w.TestsPerMutant != nil }

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
			SelectionMethod:     r.Verdict.TestSelection.Method,
			SelectedTests:       r.Verdict.TestSelection.Selected,
			SuiteTests:          r.Verdict.TestSelection.Of,
			SelectionFallback:   r.Verdict.TestSelection.Fallback,
			AuthoredExtra:       r.Verdict.AuthoredExtra,
			WriterMode:          r.Verdict.WriterMode,
			WriterCalls:         writerCallsOf(r.Verdict),
			WriterSeatsUngraded: r.Verdict.WriterSeatsUngraded,
			// And at which GRAIN it was measured: a rate averaged over
			// mutants that each faced a different test set is not one
			// measurement unless the report carries the spread.
			PerMutant:             r.Verdict.TestSelection.PerMutant,
			ProvenByAuthoredAlone: r.Verdict.TestSelection.AuthoredAlone,
			TestsPerMutant:        r.Verdict.TestSelection.TestsPerMutant,
			Rules:                 r.Verdict.TestSelection.Rules,
			Uncovered:             r.Verdict.Uncovered,
			// How many trees scored this file at once, or why it only got
			// one — see advpool.Verdict.Concurrency's doc.
			Trees:           r.Verdict.Concurrency.Trees,
			ConcurrencyNote: r.Verdict.Concurrency.Note,
			SharedDirs:      r.Verdict.Concurrency.Shared,
			// And where the minutes went — see WeakFile.Timing. CacheHit
			// rides beside them because on a reused verdict they are not
			// this run's minutes; see WeakFile.CacheHit.
			CacheHit:           r.CacheHit,
			Timing:             r.Verdict.Timing,
			ModelCalls:         r.Verdict.ModelCalls,
			MutantsGraded:      r.Verdict.MutantsTotal,
			MutantMillisMedian: r.Verdict.MutantDurationMedian.Milliseconds(),
			MutantMillisMax:    r.Verdict.MutantDurationMax.Milliseconds(),
			// The primary/challenger agreement, carried straight through —
			// nil whenever no comparable pair exists (see
			// advpool.Verdict.ChallengerAgreement's doc).
			Challenger: r.Verdict.ChallengerAgreement,
			// What a generator shard actually saw — see WeakFile.PromptShape.
			PromptShape: r.Verdict.PromptShape,
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

// writerCallsOf reads the test-writer seat's call count out of a verdict's
// per-role cost rows — the SAME numbers the cost line and the ledger use,
// never a second derivation from the survivor count (which would be a
// prediction, not a measurement: repairs are calls too).
//
// 0 when the verdict carries no test-writer row at all, which the printer
// renders as "not recorded" rather than as a writer that made no calls.
func writerCallsOf(v advpool.Verdict) int {
	for _, c := range v.ModelCalls {
		if c.Role == advpool.RoleTestWriter {
			return c.Calls
		}
	}
	return 0
}
