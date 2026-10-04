// SPDX-License-Identifier: Elastic-2.0

package shadowpool

import (
	"strings"
	"testing"
)

func TestParsePool(t *testing.T) {
	got, err := ParsePool("shadow-pool", " a, b ,c")
	if err != nil || strings.Join(got, "|") != "a|b|c" {
		t.Fatalf("ParsePool = %v, %v", got, err)
	}
	for _, bad := range []string{"a", "a,,b", "a,off", "a,A", "a,b,a", ""} {
		if _, err := ParsePool("shadow-pool", bad); err == nil {
			t.Errorf("ParsePool(%q) accepted; want refusal", bad)
		} else if !strings.Contains(err.Error(), "--shadow-pool") {
			t.Errorf("ParsePool(%q) error %q does not name the flag", bad, err)
		}
	}
}

func TestDrawPosteriorArithmetic(t *testing.T) {
	sel := Draw("test-writer-shadow", "go",
		[]Candidate{{Typed: "a", Concrete: "a"}, {Typed: "b", Concrete: "b-1"}},
		map[string]Counts{"a": {Successes: 3, Trials: 10}}, true, 4, 42)
	if sel.Members[0].Alpha != 4 || sel.Members[0].Beta != 8 {
		t.Fatalf("a: α,β = %v,%v want 4,8", sel.Members[0].Alpha, sel.Members[0].Beta)
	}
	if sel.Members[1].Alpha != 1 || sel.Members[1].Beta != 1 || sel.Members[1].Concrete != "b-1" {
		t.Fatalf("b (no history) = %+v want flat prior, concrete b-1", sel.Members[1])
	}
	if sel.History != HistoryRead || sel.HistoryRows != 4 || sel.Seed != "0x000000000000002a" {
		t.Fatalf("disclosure = %+v", sel)
	}
}

func TestDrawIsReproducibleFromSeed(t *testing.T) {
	c := []Candidate{{"a", "a"}, {"b", "b"}, {"c", "c"}}
	h := map[string]Counts{"a": {5, 9}, "b": {1, 9}}
	for seed := uint64(0); seed < 50; seed++ {
		if Draw("r", "go", c, h, true, 2, seed).Chosen != Draw("r", "go", c, h, true, 2, seed).Chosen {
			t.Fatalf("seed %d chose differently twice", seed)
		}
	}
}

// The regression guard for the incident in model-ranking.md: greedy routing
// never re-tested an early winner. Over many seeds, an untested candidate
// AND a strong veteran are both drawn often.
func TestDrawKeepsExploring(t *testing.T) {
	c := []Candidate{{"veteran", "veteran"}, {"rookie", "rookie"}}
	h := map[string]Counts{"veteran": {Successes: 60, Trials: 100}}
	n := map[string]int{}
	for seed := uint64(0); seed < 2000; seed++ {
		n[Draw("r", "go", c, h, true, 1, seed).Chosen]++
	}
	if n["rookie"] < 200 || n["veteran"] < 200 {
		t.Fatalf("draw counts %v: each must be drawn at least 10%% of the time", n)
	}
}

func TestDrawNotReadIsLabelled(t *testing.T) {
	sel := Draw("r", "go", []Candidate{{"a", "a"}, {"b", "b"}}, nil, false, 0, 1)
	if sel.History != HistoryNotRead || sel.HistoryRows != 0 {
		t.Fatalf("an unread history must say so: %+v", sel)
	}
}

func TestSeedRoundTrip(t *testing.T) {
	s, err := ParseSeed(FormatSeed(0xdeadbeef))
	if err != nil || s != 0xdeadbeef {
		t.Fatalf("ParseSeed(FormatSeed) = %x, %v", s, err)
	}
	if _, err := ParseSeed("nope"); err == nil {
		t.Fatal("ParseSeed accepted garbage")
	}
}
