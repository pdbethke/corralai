package advpool

import (
	"testing"

	"github.com/pdbethke/corralai/internal/matrix"
)

func TestNormalizeScope(t *testing.T) {
	for in, want := range map[string]string{
		"whole-test": "whole-test", "dead-check": "dead-check",
		"": "dead-check", "garbage": "dead-check",
	} {
		if got := NormalizeScope(in); got != want {
			t.Errorf("NormalizeScope(%q)=%q want %q", in, got, want)
		}
	}
}

func TestAutoAdjudication(t *testing.T) {
	cases := []struct {
		scope string
		ran   bool
		kills int
		want  string
	}{
		{"whole-test", true, 1, AdjRefuted}, // proven can-fail => hallucination refuted
		{"whole-test", true, 3, AdjRefuted},
		{"whole-test", true, 0, AdjUnadjudicated},  // 0 kills is inconclusive, NEVER auto-confirm
		{"whole-test", false, 0, AdjUnadjudicated}, // couldn't run => no signal
		{"dead-check", true, 5, AdjUnadjudicated},  // THE GUARDRAIL: live test kills, but the claim was about a check
		{"dead-check", true, 0, AdjUnadjudicated},
		{"", true, 9, AdjUnadjudicated}, // empty scope => dead-check => never auto
	}
	for _, c := range cases {
		if got := AutoAdjudication(c.scope, c.ran, c.kills); got != c.want {
			t.Errorf("AutoAdjudication(%q,%v,%d)=%q want %q", c.scope, c.ran, c.kills, got, c.want)
		}
	}
}

func TestCapMatrixTests(t *testing.T) {
	small := make([]string, 10)
	if kept, dropped := capMatrixTests(small); len(kept) != 10 || dropped != 0 {
		t.Fatalf("under cap: kept=%d dropped=%d, want 10/0", len(kept), dropped)
	}
	big := make([]string, maxMatrixTests+37)
	kept, dropped := capMatrixTests(big)
	if len(kept) != maxMatrixTests {
		t.Fatalf("over cap: kept=%d, want %d", len(kept), maxMatrixTests)
	}
	if dropped != 37 {
		t.Fatalf("over cap: dropped=%d, want 37", dropped)
	}
	// exactly at the cap: not truncated
	if _, d := capMatrixTests(make([]string, maxMatrixTests)); d != 0 {
		t.Fatalf("exactly at cap must not drop, got dropped=%d", d)
	}
}

func TestMatrixAdjudication(t *testing.T) {
	sc := func(scored bool, kills int) matrix.TestAdequacy {
		return matrix.TestAdequacy{Scored: scored, Kills: kills}
	}
	cases := []struct {
		name      string
		row       matrix.TestAdequacy
		catchable bool
		want      string
	}{
		{"scored kills>=1 -> refuted", sc(true, 2), true, AdjRefuted},
		{"scored kills==0 catchable -> confirmed", sc(true, 0), true, AdjConfirmed},
		{"scored kills==0 NOT catchable -> unadjudicated (guardrail)", sc(true, 0), false, AdjUnadjudicated},
		{"not scored -> unadjudicated", sc(false, 0), true, AdjUnadjudicated},
	}
	for _, c := range cases {
		if got := matrixAdjudication(c.row, c.catchable); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestMatrixRowFor(t *testing.T) {
	rows := []matrix.TestAdequacy{{Selector: "a"}, {Selector: "b"}}
	if r := matrixRowFor(rows, "b"); r == nil || r.Selector != "b" {
		t.Fatalf("want row b, got %+v", r)
	}
	if r := matrixRowFor(rows, "missing"); r != nil {
		t.Fatalf("no-match must return nil, got %+v", r)
	}
}
