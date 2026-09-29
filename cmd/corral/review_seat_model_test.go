// SPDX-License-Identifier: Elastic-2.0

package main

import "testing"

// TestSeatNamesAModel pins the distinction the record depends on: whether a
// seat spec identifies a MODEL or merely the TOOL that ran one.
//
// This is the question a per-model comparison asks, and it is deliberately
// not the question seatModelOf answers. seatModelOf resolves an unpinned
// agentic seat to the AGENT, which is right for decorrelation (two unpinned
// claude-code seats really are correlated) and wrong as a model: the CLI
// chose its own and never said which. A row like that is a could-not-measure
// and must not read as a measurement — 24 of this repository's first 50
// recorded reviews are exactly that shape, unrecoverably.
func TestSeatNamesAModel(t *testing.T) {
	for _, c := range []struct {
		spec string
		want bool
		why  string
	}{
		{"claude-code", false, "agentic seat, unpinned — the CLI picks the model"},
		{"codex", false, "agentic seat, unpinned"},
		{"antigravity", false, "agentic seat, unpinned"},
		{"codex:gpt-6-astra", true, "agentic seat pinned to a model"},
		{"claude-code:claude-fable-5-1", true, "agentic seat pinned to a model"},
		{"antigravity:gemini-3.1-pro-high", true, "agentic seat pinned to a model"},
		{"gemini-3.6-flash", true, "API seat — the spec IS the model name"},
		{"claude-sonnet-5", true, "API seat"},
		{"", false, "no seat at all is not a resolved model"},
		{"   ", false, "whitespace is not a resolved model"},
	} {
		if got := seatNamesAModel(c.spec); got != c.want {
			t.Errorf("seatNamesAModel(%q) = %v, want %v (%s)", c.spec, got, c.want, c.why)
		}
	}
}

// TestSeatNamesAModelDisagreesWithSeatModelOf is the point of the new
// function stated as an assertion: for an unpinned agentic seat the two
// answer differently, and a change that collapsed them would silently
// restore the defect this field exists to prevent.
func TestSeatNamesAModelDisagreesWithSeatModelOf(t *testing.T) {
	const spec = "claude-code"
	if got := seatModelOf(spec); got != spec {
		t.Fatalf("seatModelOf(%q) = %q, want the agent name — the decorrelation rule depends on it", spec, got)
	}
	if seatNamesAModel(spec) {
		t.Errorf("seatNamesAModel(%q) = true, but %q is a CLI, not a model: "+
			"recording it as resolved is how a tool name becomes a measurement", spec, spec)
	}
}
