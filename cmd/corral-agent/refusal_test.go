// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"strings"
	"testing"
)

// A refused completion must reach the NEXT attempt's conversation — without
// this, a model that doesn't spontaneously exec loops forever against the
// verify gate (observed live: 41 refusal cycles on gemini-flash).
func TestWithRefusalFeedback(t *testing.T) {
	base := "Build the smallest working core."
	if got := withRefusalFeedback(base, ""); got != base {
		t.Fatalf("no refusal should leave the instruction untouched, got %q", got)
	}
	got := withRefusalFeedback(base, "no successful 'go build' run is on record")
	if !strings.Contains(got, base) {
		t.Fatalf("augmented instruction should keep the original, got %q", got)
	}
	if !strings.Contains(got, "REFUSED") || !strings.Contains(got, "go build") {
		t.Fatalf("augmented instruction should carry the gate's message, got %q", got)
	}
	if !strings.Contains(got, "run_command") {
		t.Fatalf("augmented instruction should point at the exec tool, got %q", got)
	}
}

// A native tool call carries empty content — echoing that erases the model's
// own action history and it repeats the identical call forever.
func TestAssistantEchoSynthesizesForEmptyContent(t *testing.T) {
	if got := assistantEcho("I'll search first.", "search_memory", nil); got != "I'll search first." {
		t.Fatalf("non-empty content must pass through, got %q", got)
	}
	got := assistantEcho("  ", "search_memory", map[string]any{"query": "calc"})
	if !strings.Contains(got, "search_memory") || !strings.Contains(got, "calc") {
		t.Fatalf("empty content must synthesize the call description, got %q", got)
	}
}

// Some models drop out of native tool-calling and write the call as prose
// (observed live: gemini-flash emitting `[I am calling run_command({...})]` as
// its final message). Executing the stated intent beats discarding it.
func TestExtractCallTextualFallback(t *testing.T) {
	m := omsg{Content: `[I am calling run_command({"command":"go build ./..."})]`}
	name, args, ok := extractCall(m)
	if !ok || name != "run_command" {
		t.Fatalf("textual call should be extracted, got ok=%v name=%q", ok, name)
	}
	if args["command"] != "go build ./..." {
		t.Fatalf("args should parse, got %v", args)
	}
	if _, _, ok := extractCall(omsg{Content: "The build is green; task complete."}); ok {
		t.Fatalf("plain prose must not be mistaken for a call")
	}
}
