// SPDX-License-Identifier: Elastic-2.0

package killrate

import (
	"math"
	"testing"
)

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

// The Warp fuzzy_match run (ledger entry 94d9296c5801): 34 graded, 23
// survived, so 11 killed and the band the field note quotes.
func TestIntervalFromARowsCounts(t *testing.T) {
	if k := Killed(34, 23); k != 11 {
		t.Errorf("Killed(34, 23) = %d, want 11", k)
	}
	lo, hi, ok := Interval(34, 23)
	if !ok || math.Abs(lo-0.19) > 0.005 || math.Abs(hi-0.49) > 0.005 {
		t.Errorf("34/23: [%.3f, %.3f] ok=%v, want ≈[0.19, 0.49]", lo, hi, ok)
	}
	if k := Killed(3, 5); k != 0 {
		t.Errorf("a malformed row must not kill a negative count, got %d", k)
	}
}
