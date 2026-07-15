// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"errors"
	"strings"
	"testing"
)

// TestIsPoolCriticRole is a table test on the pure role classifier: only
// test-critic is a pool critic role — every builder/tester/structured role
// stays on its existing path (general 15-step loop or the structured fast
// path), completely unaffected.
func TestIsPoolCriticRole(t *testing.T) {
	if !isPoolCriticRole("test-critic") {
		t.Fatal("test-critic must be a pool critic role")
	}
	for _, r := range []string{"mutant-generator", "test-writer", "builder", "tester", ""} {
		if isPoolCriticRole(r) {
			t.Fatalf("%q must not be a pool critic role", r)
		}
	}
}

// toolNames extracts the "function.name" field from the []any tool-schema
// slice agentTools/criticTools produce, for assertions in tests.
func toolNames(tools []any) []string {
	names := make([]string, 0, len(tools))
	for _, tl := range tools {
		m, ok := tl.(map[string]any)
		if !ok {
			continue
		}
		fnMap, ok := m["function"].(map[string]any)
		if !ok {
			continue
		}
		if n, ok := fnMap["name"].(string); ok {
			names = append(names, n)
		}
	}
	return names
}

// TestCriticToolsRestricted: the critic tool set excludes builder tools and
// includes only report_finding + report_thought — this is what stops the
// model groping for write_file/run_command/edit_file/claim_paths (or a
// hallucinated tool) instead of concluding.
func TestCriticToolsRestricted(t *testing.T) {
	names := toolNames(criticTools())
	must := map[string]bool{"report_finding": false, "report_thought": false}
	for _, n := range names {
		if _, ok := must[n]; ok {
			must[n] = true
		}
		if n == "write_file" || n == "run_command" || n == "edit_file" || n == "claim_paths" {
			t.Fatalf("critic tools must not include builder tool %q", n)
		}
	}
	for n, seen := range must {
		if !seen {
			t.Fatalf("critic tools missing %q", n)
		}
	}
	if len(names) != 2 {
		t.Fatalf("critic tools should be exactly {report_finding, report_thought}; got %v", names)
	}
}

// alwaysThoughtBackend ALWAYS returns a report_thought tool call, no matter
// how many times Chat is called — the worst case for a model that never
// concludes. Used to prove the critic loop is bounded by critFreeformSteps
// and that the report_thought cap injects the "conclude" nudge.
type alwaysThoughtBackend struct {
	calls        int
	lastMessages []omsg
}

func (b *alwaysThoughtBackend) Chat(messages []omsg, tools []any) (omsg, error) {
	b.calls++
	b.lastMessages = messages
	return omsg{Role: "assistant", ToolCalls: []otoolcal{
		toolCall("report_thought", map[string]any{"text": "still reflecting"}),
	}}, nil
}

// TestCriticLoopBoundsReportThought verifies the critic loop (a) terminates
// within critFreeformSteps calls even when the model never stops calling
// report_thought, and (b) after the 2nd report_thought, the next injected
// user message nudges the model to file findings/conclude instead of
// accepting more reflection.
func TestCriticLoopBoundsReportThought(t *testing.T) {
	backend := &alwaysThoughtBackend{}
	brain := func(tool string, args map[string]any) string { return `{"ok":true}` }

	_, _ = runCriticLoop(backend, "test-agent", "test-critic", "critique tests", "TESTS HERE", brain, 1, 1)

	if backend.calls > critFreeformSteps {
		t.Fatalf("critic loop must stop within %d steps; backend.Chat was called %d times", critFreeformSteps, backend.calls)
	}
	if backend.calls < 3 {
		t.Fatalf("expected the loop to run at least 3 steps to exercise the cap; got %d", backend.calls)
	}
	// The last messages sent to the backend must include the nudge text,
	// injected as the result of the 2nd (or later) report_thought call.
	found := false
	for _, m := range backend.lastMessages {
		if strings.Contains(m.Content, "You have reflected enough") {
			found = true
		}
	}
	if !found {
		t.Fatalf("after 2 report_thought calls, the loop must inject a nudge to conclude; messages: %+v", backend.lastMessages)
	}
}

// unreachableBackend simulates a model that can't be reached at all (404 /
// connection-refused), the same failure runTask's general loop and structured
// fast path already treat as ErrModelUnreachable.
type unreachableBackend struct{}

func (b *unreachableBackend) Chat(messages []omsg, tools []any) (omsg, error) {
	return omsg{}, ErrModelUnreachable
}

// TestCriticLoopPropagatesModelUnreachable is the RED/GREEN case for the
// honesty-hole fix: previously runCriticLoop swallowed backend.Chat errors
// (including ErrModelUnreachable) into a "critic error: ..." summary string
// with a nil error, so a down critic model would still let the task
// COMPLETE — and a mission could converge/certify with no critic having
// actually run. It must now mirror the general loop and propagate
// ("", ErrModelUnreachable) so runQueueLoop releases the claim for
// reassignment instead of completing past a down critic.
func TestCriticLoopPropagatesModelUnreachable(t *testing.T) {
	backend := &unreachableBackend{}
	brain := func(tool string, args map[string]any) string { return `{"ok":true}` }

	summary, err := runCriticLoop(backend, "test-agent", "test-critic", "critique tests", "TESTS HERE", brain, 1, 1)

	if !errors.Is(err, ErrModelUnreachable) {
		t.Fatalf("expected errors.Is(err, ErrModelUnreachable); got err=%v", err)
	}
	if summary != "" {
		t.Fatalf("expected empty summary when the model is unreachable; got %q", summary)
	}
}
