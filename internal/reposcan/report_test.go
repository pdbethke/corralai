// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"math"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
)

func gradable(path string, kr float64, survivors int) FileResult {
	return FileResult{
		Job:      Job{Path: path},
		Verdict:  advpool.Verdict{DevKillRate: kr, Survivors: survivors, MutantsTotal: 10},
		Gradable: true,
	}
}

// The score's denominator is the AUDITED surface, never the repo.
func TestAggregateScoresOverAuditedSurfaceOnly(t *testing.T) {
	results := []FileResult{
		gradable("a.go", 1.0, 0),
		gradable("b.go", 0.0, 10),
		{Job: Job{Path: "c.go"}, Gradable: false, Reason: ReasonBaselineFailed},
	}
	rep := Aggregate("o", "r", "c1", 50, len(results), results, []Exclusion{{Path: "d.md", Reason: ReasonNoLanguage}})

	if rep.Audited != 2 {
		t.Fatalf("Audited = %d, want 2 (the ungradable file is not audited)", rep.Audited)
	}
	if math.Abs(rep.KillRate-0.5) > 1e-9 {
		t.Errorf("KillRate = %v, want 0.5 over the 2 audited files", rep.KillRate)
	}
	if rep.Ungradable[ReasonBaselineFailed] != 1 {
		t.Errorf("ungradable accounting wrong: %+v", rep.Ungradable)
	}
	if rep.TotalFiles != 50 {
		t.Errorf("TotalFiles = %d", rep.TotalFiles)
	}
}

// A gradable file's Detail (the executor's own error text) survives into
// the report, keyed by reason — an operator reading "ungradable: 1
// (executor-error)" must be able to find out WHY without a code trace.
func TestAggregateCarriesUngradableDetailThrough(t *testing.T) {
	results := []FileResult{
		{Job: Job{Path: "app.py"}, Gradable: false, Reason: ReasonExecutorError, Detail: "python toolchain unavailable: pytest not importable"},
	}
	rep := Aggregate("o", "r", "c1", 1, 1, results, nil)

	details := rep.UngradableDetails[ReasonExecutorError]
	if len(details) != 1 {
		t.Fatalf("UngradableDetails[executor-error] = %v, want 1 entry", details)
	}
	if details[0] != "app.py: python toolchain unavailable: pytest not importable" {
		t.Errorf("detail = %q, want path-prefixed detail text", details[0])
	}
}

// Zero audited files must not produce a 0.0 score that reads like "terrible
// tests". It must be visibly unscored.
func TestAggregateNothingAuditedIsNotZeroScore(t *testing.T) {
	rep := Aggregate("o", "r", "c1", 10, 1, []FileResult{
		{Job: Job{Path: "a.go"}, Gradable: false, Reason: ReasonFlakyBaseline},
	}, nil)

	if rep.Audited != 0 {
		t.Fatalf("Audited = %d", rep.Audited)
	}
	if !math.IsNaN(rep.KillRate) {
		t.Errorf("KillRate = %v, want NaN when nothing was audited", rep.KillRate)
	}
	if rep.AuditedFraction() != 0 {
		t.Errorf("AuditedFraction = %v", rep.AuditedFraction())
	}
}

// TestAggregateMarksTimedOutFiles proves a Gradable-but-unconverged file
// (Verdict.TimedOut, banked by driveLocalRun's bankableTimeoutVerdict) is
// counted separately AND carries the marker into its Weakest entry — a
// report reader must never mistake "measured, but the pool didn't finish"
// for a clean convergence just because the file is still Gradable.
func TestAggregateMarksTimedOutFiles(t *testing.T) {
	results := []FileResult{
		gradable("clean.go", 0.9, 1),
		{
			Job:      Job{Path: "cli.py"},
			Verdict:  advpool.Verdict{DevKillRate: 0.46, Survivors: 13, MutantsTotal: 24, TimedOut: true, DevScored: true},
			Gradable: true,
		},
	}
	rep := Aggregate("o", "r", "c1", 2, 2, results, nil)

	if rep.Audited != 2 {
		t.Fatalf("Audited = %d, want 2 (a timed-out-but-measured file is still audited)", rep.Audited)
	}
	if rep.TimedOut != 1 {
		t.Fatalf("TimedOut = %d, want 1", rep.TimedOut)
	}
	var found bool
	for _, f := range rep.Weakest {
		if f.Path == "cli.py" {
			found = true
			if !f.TimedOut {
				t.Error("cli.py's WeakFile.TimedOut = false, want true")
			}
		}
		if f.Path == "clean.go" && f.TimedOut {
			t.Error("clean.go's WeakFile.TimedOut = true, want false — it converged cleanly")
		}
	}
	if !found {
		t.Fatal("cli.py missing from rep.Weakest")
	}
}

// TestAggregateMarksTestWriterFailedFiles mirrors
// TestAggregateMarksTimedOutFiles for the OTHER "converged but not fully
// gated" state: the pool exhausted its compile-retry budget without
// authoring a killing test (advpool.Verdict.TestWriterFailed). Survivors > 0
// with proven_missed reading 0 here must not be printable as "no real
// bugs" — see WeakFile.TestWriterFailed's doc comment.
func TestAggregateMarksTestWriterFailedFiles(t *testing.T) {
	results := []FileResult{
		gradable("clean.go", 0.9, 1),
		{
			Job:      Job{Path: "cli.py"},
			Verdict:  advpool.Verdict{DevKillRate: 0.457, Survivors: 19, MutantsTotal: 35, TestWriterFailed: true, DevScored: true},
			Gradable: true,
		},
	}
	rep := Aggregate("o", "r", "c1", 2, 2, results, nil)

	if rep.Audited != 2 {
		t.Fatalf("Audited = %d, want 2 (a writer-failed-but-measured file is still audited)", rep.Audited)
	}
	if rep.TestWriterFailed != 1 {
		t.Fatalf("TestWriterFailed = %d, want 1", rep.TestWriterFailed)
	}
	var found bool
	for _, f := range rep.Weakest {
		if f.Path == "cli.py" {
			found = true
			if !f.TestWriterFailed {
				t.Error("cli.py's WeakFile.TestWriterFailed = false, want true")
			}
		}
		if f.Path == "clean.go" && f.TestWriterFailed {
			t.Error("clean.go's WeakFile.TestWriterFailed = true, want false — its writer converged cleanly")
		}
	}
	if !found {
		t.Fatal("cli.py missing from rep.Weakest")
	}
}

// TestAggregateMarksPoolTestUnsoundFiles mirrors
// TestAggregateMarksTestWriterFailedFiles for the F2 fix's new diagnosis: a
// compiling authored test (TestWriterFailed false) whose scoring report
// never genuinely graded (advpool.Verdict.PoolTestUnsound). ProvenMissed
// reading 0 here must be distinguishable from BOTH the writer-failed case
// and a real "tried and missed" result.
func TestAggregateMarksPoolTestUnsoundFiles(t *testing.T) {
	results := []FileResult{
		gradable("clean.go", 0.9, 1),
		{
			Job: Job{Path: "unsound.py"},
			Verdict: advpool.Verdict{DevKillRate: 0.5, Survivors: 5, MutantsTotal: 10,
				PoolTestUnsound: true, ProvenMissed: 0, DevScored: true},
			Gradable: true,
		},
	}
	rep := Aggregate("o", "r", "c1", 2, 2, results, nil)

	if rep.PoolTestUnsound != 1 {
		t.Fatalf("PoolTestUnsound = %d, want 1", rep.PoolTestUnsound)
	}
	var found bool
	for _, f := range rep.Weakest {
		if f.Path == "unsound.py" {
			found = true
			if !f.PoolTestUnsound {
				t.Error("unsound.py's WeakFile.PoolTestUnsound = false, want true")
			}
			if f.TestWriterFailed {
				t.Error("unsound.py's WeakFile.TestWriterFailed = true, want false — the test DID compile")
			}
		}
		if f.Path == "clean.go" && f.PoolTestUnsound {
			t.Error("clean.go's WeakFile.PoolTestUnsound = true, want false")
		}
	}
	if !found {
		t.Fatal("unsound.py missing from rep.Weakest")
	}
}

// TestAggregateExcludesTimedOutFilesFromProvenMissedRollup is F3's
// regression test: a timed-out verdict can still carry a real, nonzero
// ProvenMissed (the pool-adequacy step finished before the deadline hit,
// only test-critic stalled) — but printRepoReport never shows a per-file
// proven-missed count for a [TIMED OUT] file, so the repo-level rollup must
// not include it either: a reader seeing "N proven, catchable gap(s)" with
// nothing but a [TIMED OUT] marker in the per-file listing has no way to
// locate what the report claims.
func TestAggregateExcludesTimedOutFilesFromProvenMissedRollup(t *testing.T) {
	results := []FileResult{
		{Job: Job{Path: "clean.py"}, Gradable: true,
			Verdict: advpool.Verdict{DevKillRate: 0.9, Survivors: 2, MutantsTotal: 10, ProvenMissed: 2, DevScored: true}},
		{Job: Job{Path: "timedout.py"}, Gradable: true,
			Verdict: advpool.Verdict{DevKillRate: 0.5, Survivors: 3, MutantsTotal: 10, ProvenMissed: 3, TimedOut: true, DevScored: true}},
	}
	rep := Aggregate("o", "r", "c1", 2, 2, results, nil)

	if rep.ProvenMissed != 2 {
		t.Fatalf("rep.ProvenMissed = %d, want 2 — the timed-out file's 3 must be EXCLUDED from the rollup", rep.ProvenMissed)
	}
	// The per-file WeakFile still carries its own real ProvenMissed (a
	// caller that prints per-file detail may still want it) — only the
	// REPO-LEVEL sum excludes it.
	for _, f := range rep.Weakest {
		if f.Path == "timedout.py" && f.ProvenMissed != 3 {
			t.Errorf("timedout.py's WeakFile.ProvenMissed = %d, want 3 (unchanged; only the rollup excludes it)", f.ProvenMissed)
		}
	}
}

// TestAggregateCarriesProvenMissedThrough proves the fix for the reporting
// gap this whole change closes: a real converged verdict's ProvenMissed
// (advpool's execution-proven "the pool's authored test killed a survivor")
// must reach WeakFile AND roll up into the repo-level total — before this,
// Aggregate silently dropped it (see report.go's now-updated WeakFile doc,
// which used to say "not carried on WeakFile").
func TestAggregateCarriesProvenMissedThrough(t *testing.T) {
	results := []FileResult{
		{Job: Job{Path: "clean.go"}, Gradable: true,
			Verdict: advpool.Verdict{DevKillRate: 1.0, Survivors: 0, MutantsTotal: 5, ProvenMissed: 0}},
		{Job: Job{Path: "src/flask/cli.py"}, Gradable: true,
			Verdict: advpool.Verdict{DevKillRate: 0.467, Survivors: 16, MutantsTotal: 30, ProvenMissed: 7, DevScored: true}},
	}
	rep := Aggregate("o", "r", "c1", 2, 2, results, nil)

	if rep.ProvenMissed != 7 {
		t.Fatalf("rep.ProvenMissed = %d, want 7 (rolled up from the one file that proved anything)", rep.ProvenMissed)
	}
	var found bool
	for _, f := range rep.Weakest {
		if f.Path == "src/flask/cli.py" {
			found = true
			if f.ProvenMissed != 7 {
				t.Errorf("cli.py's WeakFile.ProvenMissed = %d, want 7", f.ProvenMissed)
			}
		}
		if f.Path == "clean.go" && f.ProvenMissed != 0 {
			t.Errorf("clean.go's WeakFile.ProvenMissed = %d, want 0 — it had no survivors to prove", f.ProvenMissed)
		}
	}
	if !found {
		t.Fatal("cli.py missing from rep.Weakest")
	}
}

func TestAggregateRanksWeakestFirst(t *testing.T) {
	rep := Aggregate("o", "r", "c1", 3, 3, []FileResult{
		gradable("strong.go", 0.9, 1),
		gradable("weak.go", 0.1, 9),
		gradable("mid.go", 0.5, 5),
	}, nil)

	if len(rep.Weakest) != 3 {
		t.Fatalf("want 3 ranked files, got %d", len(rep.Weakest))
	}
	if rep.Weakest[0].Path != "weak.go" || rep.Weakest[2].Path != "strong.go" {
		t.Errorf("ranking wrong: %+v", rep.Weakest)
	}
}

func TestAggregateCountsCacheHits(t *testing.T) {
	a := gradable("a.go", 1, 0)
	a.CacheHit = true
	rep := Aggregate("o", "r", "c1", 2, 2, []FileResult{a, gradable("b.go", 1, 0)}, nil)
	if rep.CacheHits != 1 {
		t.Errorf("CacheHits = %d, want 1", rep.CacheHits)
	}
}

// TestAggregateCountsUngoaledCandidatesInTheDenominator is the accounting
// invariant: `results` covers only candidates that became jobs, so a report
// that took len(results) as the candidate count would hide every ungoaled file
// and claim 100% audited for a repo where one file in five had a goal. The
// audited fraction is the ratio a later slice's coverage floor is applied to.
func TestAggregateCountsUngoaledCandidatesInTheDenominator(t *testing.T) {
	// 5 candidates enumerated; only 1 had a goal and became a job.
	// The other 4 had no goal and are recorded in the exclusions.
	excl := []Exclusion{
		{Path: "b.go", Reason: ReasonUngoaled},
		{Path: "c.go", Reason: ReasonUngoaled},
		{Path: "d.go", Reason: ReasonUngoaled},
		{Path: "e.go", Reason: ReasonUngoaled},
	}
	rep := Aggregate("o", "r", "c1", 12, 5, []FileResult{gradable("a.go", 1.0, 0)}, excl)

	if rep.Candidates != 5 {
		t.Fatalf("Candidates = %d, want 5 (the enumerated candidates, not the jobs)", rep.Candidates)
	}
	if rep.Audited != 1 {
		t.Fatalf("Audited = %d, want 1", rep.Audited)
	}
	if got := rep.AuditedFraction(); math.Abs(got-0.2) > 1e-9 {
		t.Errorf("AuditedFraction = %v, want 0.2 — ungoaled files belong in the denominator", got)
	}
	if rep.Ungradable[ReasonUngoaled] != 4 {
		t.Errorf("Ungradable[%s] = %d, want 4", ReasonUngoaled, rep.Ungradable[ReasonUngoaled])
	}
	// Accounting closes: everything counted once.
	total := rep.Audited
	for _, n := range rep.Ungradable {
		total += n
	}
	if total != rep.Candidates {
		t.Errorf("accounting does not close: audited+ungradable = %d, candidates = %d", total, rep.Candidates)
	}
}

// A caller that under-reports the candidate count must never produce a
// fraction above 1.0 — fail safe toward the less flattering number.
func TestAggregateNeverReportsMoreAuditedThanCandidates(t *testing.T) {
	rep := Aggregate("o", "r", "c1", 3, 0, []FileResult{
		gradable("a.go", 1.0, 0), gradable("b.go", 1.0, 0),
	}, nil)
	if rep.Candidates != 2 {
		t.Fatalf("Candidates = %d, want 2 (clamped up to the results count)", rep.Candidates)
	}
	if got := rep.AuditedFraction(); got != 1 {
		t.Errorf("AuditedFraction = %v, want 1", got)
	}
}

// A per-language prep failure (e.g. `go mod vendor`) is an ungradable with its
// OWN reason, never a fabricated 0.0 kill rate.
func TestAggregateBooksPrepFailed(t *testing.T) {
	rep := Aggregate("o", "r", "c1", 2, 1, []FileResult{
		{Job: Job{Path: "a.go"}, Gradable: false, Reason: ReasonPrepFailed},
	}, nil)
	if rep.Ungradable[ReasonPrepFailed] != 1 {
		t.Fatalf("Ungradable = %+v, want one prep-failed", rep.Ungradable)
	}
}

// Two candidates dropped before becoming jobs: one ungoaled, one for a
// DIFFERENT reason. Subtraction would label both "ungoaled".
func TestAggregateBooksUngoaledFromExclusionsNotSubtraction(t *testing.T) {
	excl := []Exclusion{
		{Path: "a.go", Reason: ReasonUngoaled},
		{Path: "b.go", Reason: ReasonNotRegularFile},
	}
	rep := Aggregate("o", "r", "c1", 10, 3, []FileResult{
		{Job: Job{Path: "c.go"}, Gradable: true},
	}, excl)

	if got := rep.Ungradable[ReasonUngoaled]; got != 1 {
		t.Errorf("ungoaled = %d, want exactly 1 — the other drop is not ungoaled", got)
	}
	if rep.Ungradable[ReasonNotRegularFile] != 0 {
		t.Errorf("a pre-job exclusion was booked as ungradable: %+v", rep.Ungradable)
	}
}

// --- final-review fix wave (I2) --------------------------------------------

// TestAggregateFoldsDeriveFailedIntoUngradable: a scan where 24 of 25 files hit
// rate limits used to print "kill rate 0.95 over 1 audited file(s)" with NO
// ungradable line at all — a broken run reading as a healthy repo.
func TestAggregateFoldsDeriveFailedIntoUngradable(t *testing.T) {
	excl := make([]Exclusion, 0, 24)
	for i := 0; i < 24; i++ {
		excl = append(excl, Exclusion{Path: "f.go", Reason: ReasonDeriveFailed})
	}
	rep := Aggregate("o", "r", "c", 50, 25, []FileResult{
		{Job: Job{Path: "a.go"}, Gradable: true, Verdict: advpool.Verdict{DevKillRate: 0.95}},
	}, excl)
	if rep.Ungradable[ReasonDeriveFailed] != 24 {
		t.Errorf("ungradable[%s] = %d, want 24 — a broken run must not read as a healthy repo",
			ReasonDeriveFailed, rep.Ungradable[ReasonDeriveFailed])
	}
}

// source-too-large is the same kind of fact: a candidate that produced no
// score. It keeps its OWN reason (it is a property of the file, not an outage).
func TestAggregateFoldsSourceTooLargeIntoUngradable(t *testing.T) {
	rep := Aggregate("o", "r", "c", 4, 2, []FileResult{
		{Job: Job{Path: "a.go"}, Gradable: true, Verdict: advpool.Verdict{DevKillRate: 1}},
	}, []Exclusion{{Path: "gen.go", Reason: ReasonSourceTooLarge}})
	if rep.Ungradable[ReasonSourceTooLarge] != 1 {
		t.Errorf("ungradable[%s] = %d, want 1", ReasonSourceTooLarge, rep.Ungradable[ReasonSourceTooLarge])
	}
	if rep.Ungradable[ReasonDeriveFailed] != 0 {
		t.Errorf("an oversized file must not be counted as an outage: %v", rep.Ungradable)
	}
}

// ...but a DELIBERATE bound is not a failure to grade. Folding not-selected
// would report "189 ungradable" for a scan that did exactly what was asked.
func TestAggregateDoesNotFoldNotSelectedIntoUngradable(t *testing.T) {
	excl := make([]Exclusion, 0, 189)
	for i := 0; i < 189; i++ {
		excl = append(excl, Exclusion{Path: "f.go", Reason: ReasonNotSelected})
	}
	rep := Aggregate("o", "r", "c", 400, 214, []FileResult{
		{Job: Job{Path: "a.go"}, Gradable: true, Verdict: advpool.Verdict{DevKillRate: 0.5}},
	}, excl)
	if n, ok := rep.Ungradable[ReasonNotSelected]; ok || n != 0 {
		t.Errorf("not-selected must not appear as ungradable, got %d", n)
	}
	// It IS still accounted, in Excluded and in the AuditedFraction denominator.
	if len(rep.Excluded) != 189 {
		t.Errorf("not-selected must still be fully accounted: %d", len(rep.Excluded))
	}
	if got := rep.AuditedFraction(); got != 1.0/214.0 {
		t.Errorf("AuditedFraction = %v, want 1/214 — the denominator is ALL candidates", got)
	}
}
