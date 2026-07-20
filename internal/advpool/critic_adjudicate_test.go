package advpool

import "testing"

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
