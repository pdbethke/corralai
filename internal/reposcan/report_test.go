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
	rep := Aggregate("o", "r", "c1", 12, 5, []FileResult{gradable("a.go", 1.0, 0)}, nil)

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
