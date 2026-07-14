// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"strings"
	"testing"
)

// TestIsStructuredRole is a table test on the pure role classifier: only
// test-writer and mutant-generator are structured (single-call, raw-artifact)
// roles; every other role — including test-critic, which reviews rather than
// produces an artifact — stays on the freeform tool loop.
func TestIsStructuredRole(t *testing.T) {
	cases := []struct {
		role string
		want bool
	}{
		{"test-writer", true},
		{"mutant-generator", true},
		{"test-critic", false},
		{"builder", false},
		{"tester", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.role, func(t *testing.T) {
			if got := isStructuredRole(tc.role); got != tc.want {
				t.Errorf("isStructuredRole(%q) = %v, want %v", tc.role, got, tc.want)
			}
		})
	}
}

// TestRunTaskStructuredRoleReturnsRawOutput verifies the fast path: a
// structured-role task makes exactly ONE backend.Chat call (no tool loop)
// and hands the model's raw content back verbatim as the completion result —
// the brain parses/validates it, so the worker must not summarize, wrap, or
// otherwise mangle it.
func TestRunTaskStructuredRoleReturnsRawOutput(t *testing.T) {
	raw := "package foo_test\n\nfunc TestBar(t *testing.T) { /* generated */ }\n"
	backend := &scriptedRawBackend{content: raw}
	brain := func(tool string, args map[string]any) string { return `{"ok":true}` }

	summary, err := runTask(context.Background(), backend, "test-agent", "test-writer",
		t.TempDir(), brain, nil, 1, 1, "write tests", "RENDERED TESTGEN PROMPT HERE", nil, nil, "")
	if err != nil {
		t.Fatalf("runTask: %v", err)
	}
	if summary != raw {
		t.Errorf("structured fast path must return the model's raw output verbatim; got %q, want %q", summary, raw)
	}
	if backend.calls != 1 {
		t.Errorf("structured fast path must make exactly one backend.Chat call; got %d", backend.calls)
	}
	if len(backend.lastMessages) != 1 || backend.lastMessages[0].Content != "RENDERED TESTGEN PROMPT HERE" {
		t.Errorf("structured fast path must send the task's instruction verbatim as the sole prompt; got %+v", backend.lastMessages)
	}
}

// TestRunTaskFreeformRoleStillUsesLoop confirms a freeform role (e.g.
// test-critic) is unaffected by the structured fast path — it drives the
// existing multi-step tool loop, calling backend.Chat once per step until
// the model stops issuing tool calls.
func TestRunTaskFreeformRoleStillUsesLoop(t *testing.T) {
	backend := &scriptedBackend{calls: []otoolcal{
		toolCall("report_thought", map[string]any{"text": "reviewing the generated test for a tautological assertion"}),
	}}
	brain := func(tool string, args map[string]any) string { return `{"ok":true}` }

	summary, err := runTask(context.Background(), backend, "test-agent", "test-critic",
		t.TempDir(), brain, nil, 1, 1, "critique tests", "do the review", nil, nil, "")
	if err != nil {
		t.Fatalf("runTask: %v", err)
	}
	// scriptedBackend replays the queued tool call, then falls back to a
	// plain "done" answer with no tool call — the loop must have run at
	// least two Chat calls (one to dispatch the tool, one to end the loop)
	// for that fallback to be reachable, proving it's not the fast path.
	if backend.i < 1 {
		t.Errorf("freeform role must still drive the tool loop; scriptedBackend never advanced past its queued call")
	}
	if !strings.Contains(summary, "done") {
		t.Errorf("freeform loop should end on the model's plain-content answer; got %q", summary)
	}
}

// scriptedRawBackend records every Chat call and always returns the same
// plain-content (no tool call) response — the fast path's single call.
type scriptedRawBackend struct {
	content      string
	calls        int
	lastMessages []omsg
}

func (b *scriptedRawBackend) Chat(messages []omsg, tools []any) (omsg, error) {
	b.calls++
	b.lastMessages = messages
	return omsg{Role: "assistant", Content: b.content}, nil
}
