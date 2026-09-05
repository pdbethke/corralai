// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"math"

	golang "github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/repoindex"
)

// ExamCoverage says how much of the file the exam actually reached: of the
// named symbols and the decision points the extractor found, how many had a
// fault planted on them. It is the second term a kill rate needs beside its
// sample size — 39 mutants that all landed on the same eight return lines
// covered less of a file than 8 that hit every decision point, and a bare
// count cannot say so. Computed from the SAME spans the ledger records per
// mutant, so a reader with the warehouse can recompute it.
type ExamCoverage struct {
	// Symbols is the named symbols the extractor found; SymbolsProbed how
	// many a mutant's span fell inside.
	Symbols       int `json:"symbols"`
	SymbolsProbed int `json:"symbols_probed"`
	// Decisions is the decision points (branch, loop, case, catch, boolean
	// operator) across those symbols — Σ(Complexity−1); DecisionsProbed how
	// many a mutant's span overlapped.
	Decisions       int `json:"decisions"`
	DecisionsProbed int `json:"decisions_probed"`
	// Unplaced is how many graded mutants carried no span and so could not
	// be placed. A term of the coverage that says "we do not know where
	// these landed", never silently counted as probing nothing.
	Unplaced int `json:"unplaced,omitempty"`
	// Measured is false when there was no surface to cover (no signatures)
	// or no mutants: every count above is then 0 for want of a measurement,
	// not because nothing was probed.
	Measured bool `json:"measured"`
}

// examSurface is what StartRun keeps of the signatures for the coverage
// computed at the end of the run: line spans only.
type examSurface struct {
	symbols   []golang.LineRange
	decisions []golang.LineRange
}

func newExamSurface(sigs []repoindex.Signature) examSurface {
	var s examSurface
	for _, sig := range sigs {
		if sig.Name == "" || sig.Line <= 0 {
			continue
		}
		end := sig.Line
		if sig.Lines > 0 {
			end = sig.Line + sig.Lines - 1
		}
		s.symbols = append(s.symbols, golang.LineRange{Start: sig.Line, End: end})
		for _, d := range sig.Decisions {
			s.decisions = append(s.decisions, golang.LineRange{Start: d.Start, End: d.End})
		}
	}
	return s
}

// coverage places every graded mutant on the surface.
func (s examSurface) coverage(refs ...[]MutantRef) ExamCoverage {
	var spans []golang.LineRange
	unplaced := 0
	graded := 0
	for _, rs := range refs {
		for _, r := range rs {
			graded++
			if r.Span.IsZero() {
				unplaced++
				continue
			}
			spans = append(spans, r.Span)
		}
	}
	c := ExamCoverage{Symbols: len(s.symbols), Decisions: len(s.decisions), Unplaced: unplaced}
	if len(s.symbols) == 0 || graded == 0 {
		return c
	}
	c.Measured = true
	probed := func(r golang.LineRange) bool {
		for _, sp := range spans {
			if sp.Start <= r.End && r.Start <= sp.End {
				return true
			}
		}
		return false
	}
	for _, r := range s.symbols {
		if probed(r) {
			c.SymbolsProbed++
		}
	}
	for _, r := range s.decisions {
		if probed(r) {
			c.DecisionsProbed++
		}
	}
	return c
}

// MaxCertifiableIntervalWidth is the widest 95% interval a CERTIFIED verdict
// may carry. 0.35 lets a 40-mutant exam certify at the 0.8 threshold
// (32 of 40: 0.65–0.89, width 0.24) and a 20-mutant one at 0.9 (18 of 20:
// 0.70–0.97, width 0.27), and refuses 9 of 10 (0.60–0.98, width 0.38) and
// every floor-of-five exam. See aggregate's "exam too small to certify".
const MaxCertifiableIntervalWidth = 0.35

// WilsonInterval is the 95% Wilson score interval for a kill rate of
// killed out of graded mutants — the sampling term of a verdict's
// confidence, the one a bare rate hides. Wilson rather than the normal
// approximation because the exams here are small (a floor of 5) and the
// rates sit near 0 and 1, exactly where the approximation fails. Returns
// (0, 0, false) for graded == 0: an interval over nothing is not [0, 1].
func WilsonInterval(killed, graded int) (low, high float64, ok bool) {
	if graded <= 0 || killed < 0 || killed > graded {
		return 0, 0, false
	}
	const z = 1.959964 // 95%
	n := float64(graded)
	p := float64(killed) / n
	denom := 1 + z*z/n
	centre := (p + z*z/(2*n)) / denom
	half := z * math.Sqrt(p*(1-p)/n+z*z/(4*n*n)) / denom
	low, high = centre-half, centre+half
	if low < 0 {
		low = 0
	}
	if high > 1 {
		high = 1
	}
	return low, high, true
}
