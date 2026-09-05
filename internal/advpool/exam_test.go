// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"

	golang "github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/repoindex"
)

// api.py, both ways: 39 mutants on the same eight return lines cover eight
// symbols and none of the decision points a real file has; 8 mutants spread
// across a file's branches cover them. The count alone said 39 > 8.
func TestExamCoveragePlacesMutantsOnSymbolsAndDecisions(t *testing.T) {
	sigs := []repoindex.Signature{
		{Name: "get", Line: 10, Lines: 3},
		{Name: "post", Line: 20, Lines: 3},
		{Name: "request", Line: 30, Lines: 20, Decisions: []repoindex.Decision{{Start: 33, End: 36}, {Start: 40, End: 45}}},
	}
	s := newExamSurface(sigs)
	if len(s.symbols) != 3 || len(s.decisions) != 2 {
		t.Fatalf("surface = %+v", s)
	}
	// Pile-up: many mutants, two symbols, no decision.
	pile := []MutantRef{
		{ID: "m1", Span: golang.LineRange{Start: 11, End: 11}},
		{ID: "m2", Span: golang.LineRange{Start: 11, End: 11}},
		{ID: "m3", Span: golang.LineRange{Start: 21, End: 21}},
	}
	c := s.coverage(pile)
	if !c.Measured || c.SymbolsProbed != 2 || c.DecisionsProbed != 0 || c.Symbols != 3 || c.Decisions != 2 {
		t.Errorf("pile-up coverage = %+v, want 2/3 symbols, 0/2 decisions", c)
	}
	// Spread: fewer mutants, every decision.
	spread := []MutantRef{
		{ID: "m1", Span: golang.LineRange{Start: 34, End: 34}},
		{ID: "m2", Span: golang.LineRange{Start: 42, End: 43}},
	}
	c = s.coverage(spread)
	if c.SymbolsProbed != 1 || c.DecisionsProbed != 2 {
		t.Errorf("spread coverage = %+v, want 1/3 symbols, 2/2 decisions", c)
	}
	// A mutant with no span is counted as UNPLACED, never as probing nothing.
	c = s.coverage([]MutantRef{{ID: "m1"}}, spread)
	if c.Unplaced != 1 || c.DecisionsProbed != 2 {
		t.Errorf("unplaced = %+v", c)
	}
	// No surface: unmeasured, every count zero for want of a measurement.
	if c := newExamSurface(nil).coverage(spread); c.Measured {
		t.Errorf("no signatures must not measure: %+v", c)
	}
	if c := s.coverage(); c.Measured {
		t.Errorf("no mutants must not measure: %+v", c)
	}
}

// The two api.py runs, as intervals: 3/39 and 5/8 do not overlap, and the
// small exam's band is wide — which is what a reader needs to see.
func TestWilsonIntervalOnTheTwoRequestsRuns(t *testing.T) {
	lo, hi, ok := WilsonInterval(3, 39)
	if !ok || math.Abs(lo-0.026) > 0.01 || math.Abs(hi-0.205) > 0.01 {
		t.Errorf("3/39: [%.3f, %.3f] ok=%v, want ≈[0.026, 0.205]", lo, hi, ok)
	}
	lo, hi, ok = WilsonInterval(5, 8)
	if !ok || math.Abs(lo-0.305) > 0.01 || math.Abs(hi-0.863) > 0.01 {
		t.Errorf("5/8: [%.3f, %.3f] ok=%v, want ≈[0.305, 0.863]", lo, hi, ok)
	}
	// Edges stay inside [0, 1] and an empty exam has no interval.
	if lo, hi, ok := WilsonInterval(8, 8); !ok || lo < 0.6 || hi != 1 {
		t.Errorf("8/8: [%.3f, %.3f]", lo, hi)
	}
	if _, _, ok := WilsonInterval(0, 0); ok {
		t.Error("an interval over nothing is not [0, 1]")
	}
}

// The verdict's coverage is computed from the refs the verdict carries —
// the first cut computed it before the refs were assigned and every real
// run measured nothing. A converged per-mutant run with spanned mutants and
// a symbol surface must report a measured reach.
func TestVerdictCarriesExamCoverageFromItsOwnRefs(t *testing.T) {
	mutants := []adequacy.Mutant{
		{ID: "m1", Replace: "c1", ParentSHA256: "p1", Span: golang.LineRange{Start: 41, End: 41}},
		{ID: "m2", Replace: "c2", ParentSHA256: "p2", Span: golang.LineRange{Start: 2, End: 2}},
	}
	scorer := &recordingPerMutantScorer{fakeScorer: fakeScorer{
		devKillRate: 0.5, devSurvivors: mutants[1:], devReported: true,
		reportFn: func(_ context.Context, _, _, _ string, _ []adequacy.Mutant, _ string) (adequacy.Report, error) {
			return adequacy.Report{CompliantPass: true, CanaryKilled: true, Total: 2, Killed: []string{"m1"}, Survived: []string{"m2"}}, nil
		},
	}}
	validator := &fakeValidator{mutants: mutants}
	q := newTestQueue(t)
	d, err := NewDriver(q, scorer, validator, decorrelatedAssign(), 0.1)
	if err != nil {
		t.Fatal(err)
	}
	d.CertifyIntervalWidth = 0 // small fixture; the exam-size rule is tested on its own
	// Two symbols: one holds line 41 and a decision spanning it; the other
	// holds line 2 and no decision.
	sigs := []repoindex.Signature{
		{Name: "validate", Line: 40, Lines: 5, Complexity: 2, Decisions: []repoindex.Decision{{Start: 41, End: 42}}},
		{Name: "helper", Line: 1, Lines: 3, Complexity: 1},
	}
	if err := d.StartRun(93, perMutantRunSpec(), sigs); err != nil {
		t.Fatal(err)
	}
	if _, err := q.PromoteReady(93); err != nil {
		t.Fatal(err)
	}
	v := drivePoolToConvergence(t, d, 93)
	want := ExamCoverage{Symbols: 2, SymbolsProbed: 2, Decisions: 1, DecisionsProbed: 1, Measured: true}
	if v.ExamCoverage != want {
		t.Fatalf("ExamCoverage = %+v, want %+v", v.ExamCoverage, want)
	}
}

// A rate over five mutants is not a grade. 5 of 5 killed clears any
// threshold, and its 95% interval is 0.57–1.00: too wide to certify. The
// verdict says so, with the numbers, instead of signing CERTIFIED over five
// decision points. 8 of 8 (0.68–1.00, width 0.32) is the narrowest small
// exam that can; 7 of 8 (0.53–0.98) cannot.
func TestAnExamTooSmallToCertifyIsIndicativeNotCertified(t *testing.T) {
	rs := testRunSpec()
	v := aggregate(rs, decorrelatedAssign(), 1.0, 5, 0, 0, nil, 0.8, MaxCertifiableIntervalWidth, false, false, false)
	if v.Status != StatusNeedsReview || !v.ExamIndicative {
		t.Fatalf("5 of 5: status=%q indicative=%v, want needs-review + indicative", v.Status, v.ExamIndicative)
	}
	if !strings.Contains(v.IndicativeReason, "5 mutants") || !strings.Contains(v.IndicativeReason, "0.57–1.00") {
		t.Errorf("reason = %q, want the counts and the band", v.IndicativeReason)
	}
	v = aggregate(rs, decorrelatedAssign(), 0.875, 8, 1, 1, nil, 0.8, MaxCertifiableIntervalWidth, false, false, false)
	if v.Status != StatusNeedsReview || !v.ExamIndicative {
		t.Errorf("7 of 8: status=%q indicative=%v, want indicative (0.53–0.98)", v.Status, v.ExamIndicative)
	}
	v = aggregate(rs, decorrelatedAssign(), 1.0, 8, 0, 0, nil, 0.8, MaxCertifiableIntervalWidth, false, false, false)
	if v.Status != StatusCertified || v.ExamIndicative {
		t.Errorf("8 of 8: status=%q indicative=%v, want certified (0.68–1.00, width 0.32)", v.Status, v.ExamIndicative)
	}
	// 36 of 40 (0.90): 0.77–0.96, width 0.19 — certifies.
	v = aggregate(rs, decorrelatedAssign(), 0.9, 40, 4, 4, nil, 0.8, MaxCertifiableIntervalWidth, false, false, false)
	if v.Status != StatusCertified || v.ExamIndicative {
		t.Errorf("36 of 40: status=%q indicative=%v, want certified", v.Status, v.ExamIndicative)
	}
	// Below threshold stays needs-review for the threshold's reason, not this one.
	v = aggregate(rs, decorrelatedAssign(), 0.5, 8, 4, 4, nil, 0.8, MaxCertifiableIntervalWidth, false, false, false)
	if v.Status != StatusNeedsReview || v.ExamIndicative {
		t.Errorf("4 of 8: status=%q indicative=%v, want needs-review by threshold, not marked indicative", v.Status, v.ExamIndicative)
	}
}
