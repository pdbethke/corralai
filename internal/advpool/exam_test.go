// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"math"
	"testing"

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
