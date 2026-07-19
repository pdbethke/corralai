// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"strings"
	"testing"
)

// TestIsStructuredRole is a table test on the pure role classifier: only the
// artifact-producing generator/writer seats — test-writer, mutant-generator,
// and mutant-generator-shadow — are structured (single-call, raw-artifact)
// roles; every other role — including test-critic, which reviews rather than
// produces an artifact — stays on the freeform tool loop.
func TestIsStructuredRole(t *testing.T) {
	cases := []struct {
		role string
		want bool
	}{
		{"test-writer", true},
		{"mutant-generator", true},
		// The challenger seat renders the SAME testgen prompt as its primary
		// and hands back a raw mutant list — it must take the structured fast
		// path here exactly as internal/agentworker does, or the brain gets a
		// tool-loop summary it cannot parse.
		{"mutant-generator-shadow", true},
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

// TestRunTaskFreeformRoleStillUsesLoop confirms a general freeform role
// (builder) is unaffected by the structured fast path — it drives the
// existing general 15-step multi-step tool loop, calling backend.Chat once
// per step until the model stops issuing tool calls. (test-critic is ALSO a
// freeform role but has its OWN dedicated bounded loop, extracted into
// internal/agentworker's RunRole — see agentworker_test.go for its loop-bound
// coverage and critic_test.go for this call site's brain-forwarding wiring —
// so builder is used here to keep this test's claim about "the existing
// loop" true.)
func TestRunTaskFreeformRoleStillUsesLoop(t *testing.T) {
	backend := &scriptedBackend{calls: []otoolcal{
		toolCall("report_thought", map[string]any{"text": "reviewing the generated test for a tautological assertion"}),
	}}
	brain := func(tool string, args map[string]any) string { return `{"ok":true}` }

	summary, err := runTask(context.Background(), backend, "test-agent", "builder",
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

// effectiveTaskRole must prefer the CLAIMED task's role over the worker's own,
// so a generalist worker takes the structured fast path on a role-typed task.
func TestEffectiveTaskRole(t *testing.T) {
	cases := []struct{ taskRole, workerRole, want string }{
		{"mutant-generator", "any", "mutant-generator"}, // generalist claims structured task -> task role wins
		{"test-writer", "builder", "test-writer"},
		{"", "any", "any"},       // untyped task -> fall back to worker role
		{"", "tester", "tester"}, // untyped task, role-typed worker
		{"test-critic", "", "test-critic"},
	}
	for _, c := range cases {
		if got := effectiveTaskRole(c.taskRole, c.workerRole); got != c.want {
			t.Errorf("effectiveTaskRole(%q,%q) = %q, want %q", c.taskRole, c.workerRole, got, c.want)
		}
		// and the fast-path decision must follow the effective (task) role
		if c.taskRole == "mutant-generator" && !isStructuredRole(effectiveTaskRole(c.taskRole, c.workerRole)) {
			t.Errorf("generalist claiming a mutant-generator task must be treated as structured")
		}
	}
}
