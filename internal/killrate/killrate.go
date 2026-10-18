// SPDX-License-Identifier: Elastic-2.0

// Package killrate holds the arithmetic every reader of a kill rate shares:
// the 95% interval a bare rate hides, and the killed count a row implies.
// It is a leaf with no dependencies beyond the standard library, so a reader
// of the record (the public ledger page, scripts/ledgersite) prints the same
// band the CLI does without linking the audit engine to get it.
package killrate

import "math"

// WilsonInterval is the 95% Wilson score interval for a kill rate of
// killed out of graded mutants — the sampling term of a verdict's
// confidence, the one a bare rate hides. Wilson rather than the normal
// approximation because the exams here are small (a floor of 5) and the
// rates sit near 0 and 1, exactly where the approximation fails. Returns
// (0, 0, false) for graded == 0: an interval over nothing is not [0, 1].
// Its usual coverage interpretation assumes independent Bernoulli observations
// sampled representatively from a defined population. Goal-directed generated
// mutants do not establish those assumptions, even after duplicate edits are
// collapsed. This is not a probability of software correctness.
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

// Killed is the number of graded mutants the suite caught, from the two
// counts a row records. A row whose survivors exceed what was graded is
// malformed; it reads as nothing killed rather than a negative count.
func Killed(graded, survivors int) int {
	if k := graded - survivors; k > 0 {
		return k
	}
	return 0
}

// Interval is WilsonInterval over a row's graded and survivor counts.
func Interval(graded, survivors int) (low, high float64, ok bool) {
	return WilsonInterval(Killed(graded, survivors), graded)
}
