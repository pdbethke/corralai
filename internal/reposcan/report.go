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
	// ProvenMissed sums WeakFile.ProvenMissed over every audited file — the
	// repo-wide count of survivors execution actually proved catchable.
	// Corral's strongest claim, rolled up. Like the per-file field, 0 here
	// is ambiguous on its own (see WeakFile.ProvenMissed's three cases) and
	// a caller must not print it without the TestWriterFailed/TimedOut
	// caveats alongside it.
	ProvenMissed int

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
		sum += r.Verdict.DevKillRate
		if r.Verdict.TimedOut {
			rep.TimedOut++
		}
		if r.Verdict.TestWriterFailed {
			rep.TestWriterFailed++
		}
		rep.ProvenMissed += r.Verdict.ProvenMissed
		rep.Weakest = append(rep.Weakest, WeakFile{
			Path:             r.Job.Path,
			KillRate:         r.Verdict.DevKillRate,
			Survivors:        r.Verdict.Survivors,
			TimedOut:         r.Verdict.TimedOut,
			TestWriterFailed: r.Verdict.TestWriterFailed,
			ProvenMissed:     r.Verdict.ProvenMissed,
		})
	}

	if rep.Audited == 0 {
		rep.KillRate = math.NaN()
	} else {
		rep.KillRate = sum / float64(rep.Audited)
	}

	sort.SliceStable(rep.Weakest, func(i, j int) bool {
		return rep.Weakest[i].KillRate < rep.Weakest[j].KillRate
	})
	return rep
}
