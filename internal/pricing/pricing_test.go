// SPDX-License-Identifier: Elastic-2.0

package pricing

import "testing"

var synth = Table{
	Source: "synthetic", Date: "2000-01-01",
	Prices: map[string]Price{
		"m":       {InputPerMTokMicroUSD: 3_000_000, CachedInputPerMTokMicroUSD: 300_000, CacheWritePerMTokMicroUSD: 3_750_000, OutputPerMTokMicroUSD: 15_000_000},
		"nocache": {InputPerMTokMicroUSD: 3_000_000, OutputPerMTokMicroUSD: 15_000_000},
	},
}

func want(t *testing.T, micro *int64, basis string, wantMicro int64, wantBasis string) {
	t.Helper()
	if micro == nil || *micro != wantMicro {
		t.Fatalf("micro = %v, want %d", micro, wantMicro)
	}
	if basis != wantBasis {
		t.Fatalf("basis = %q, want %q", basis, wantBasis)
	}
}

func wantNil(t *testing.T, micro *int64, basis, wantBasis string) {
	t.Helper()
	if micro != nil {
		t.Fatalf("micro = %d, want nil", *micro)
	}
	if basis != wantBasis {
		t.Fatalf("basis = %q, want %q", basis, wantBasis)
	}
}

func TestInputArithmetic(t *testing.T) {
	m, b := synth.Cost("m", 1_000_000, 0, 0, 0, true, false)
	want(t, m, b, 3_000_000, "price table synthetic, 2000-01-01")
}

func TestEveryComponentUsesItsOwnPrice(t *testing.T) {
	// 1,000,000 prompt = 600,000 plain + 300,000 cached + 100,000 written.
	// 600k*3 + 300k*0.3 + 100k*3.75 + 2k*15 per million = 1.8+0.09+0.375+0.03.
	m, b := synth.Cost("m", 1_000_000, 300_000, 100_000, 2_000, true, false)
	want(t, m, b, 2_295_000, synth.BasisPriced())
}

func TestRoundsHalfUpOnceOnTheSum(t *testing.T) {
	// A cached token is 0.3 micro-USD: 1 rounds down, 2 (0.6) rounds up.
	m, _ := synth.Cost("m", 1, 1, 0, 0, true, false)
	if m == nil || *m != 0 {
		t.Fatalf("1 cached token = %v, want 0", m)
	}
	m, _ = synth.Cost("m", 2, 2, 0, 0, true, false)
	if m == nil || *m != 1 {
		t.Fatalf("2 cached tokens = %v, want 1", m)
	}
	// Rounded once on the sum: 5 x 0.3 = 1.5 -> 2, where per-token rounding
	// would give 0.
	m, _ = synth.Cost("m", 5, 5, 0, 0, true, false)
	if m == nil || *m != 2 {
		t.Fatalf("5 cached tokens = %v, want 2", m)
	}
}

func TestUnknownModelHasNoPrice(t *testing.T) {
	m, b := synth.Cost("other", 10, 0, 0, 10, true, false)
	wantNil(t, m, b, "no price for other in the price table of 2000-01-01")
}

func TestUnmeteredSeatIsNotZero(t *testing.T) {
	m, b := synth.Cost("m", 0, 0, 0, 0, false, false)
	wantNil(t, m, b, "not metered: the seat reported no tokens")
}

func TestAgentSeatIsNeverPriced(t *testing.T) {
	// Even a model that is in the table, with tokens.
	m, b := synth.Cost("m", 1000, 0, 0, 1000, true, true)
	wantNil(t, m, b, "agent seat: billing not known to corral")
	m, b = synth.Cost("m", 0, 0, 0, 0, false, true)
	wantNil(t, m, b, "agent seat: billing not known to corral")
}

func TestAComponentWithoutAPriceGivesNoPartialSum(t *testing.T) {
	// Cache writes used, table has no cache-write price: nil, not the
	// input+output part.
	m, b := synth.Cost("nocache", 1_000_000, 0, 100_000, 1000, true, false)
	wantNil(t, m, b, "no price for nocache in the price table of 2000-01-01")
	m, b = synth.Cost("nocache", 1_000_000, 100_000, 0, 1000, true, false)
	wantNil(t, m, b, "no price for nocache in the price table of 2000-01-01")
	// The same model is priced when the call did not use the component.
	m, b = synth.Cost("nocache", 1_000_000, 0, 0, 0, true, false)
	want(t, m, b, 3_000_000, synth.BasisPriced())
}

func TestInconsistentCountsGiveNoFigure(t *testing.T) {
	m, b := synth.Cost("m", 100, 80, 40, 0, true, false)
	wantNil(t, m, b, BasisInconsistent)
}

func TestPartlyUnmetered(t *testing.T) {
	if got := BasisPartlyUnmetered(2, 5); got != "not metered: 2 of 5 calls reported no tokens" {
		t.Fatal(got)
	}
}

// TestCurrentTableIsDatedAndSourced guards the "never state a number nobody
// read" rule: no price without a source and a date, and no zero input/output
// price (a zero there is a missing reading, not a free model).
func TestCurrentTableIsDatedAndSourced(t *testing.T) {
	if Current.Source == "" || Current.Date == "" || len(Current.Prices) == 0 {
		t.Fatalf("Current = %+v", Current)
	}
	for id, p := range Current.Prices {
		if p.InputPerMTokMicroUSD <= 0 || p.OutputPerMTokMicroUSD <= 0 || p.CachedInputPerMTokMicroUSD <= 0 {
			t.Errorf("%s has an unread input, cached-input or output price: %+v", id, p)
		}
	}
}
