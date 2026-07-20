// SPDX-License-Identifier: Elastic-2.0

package agentworker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/queue"
)

// fakeChatter is a canned-response Chatter test double. It returns one
// scripted Message per call, in order; if scripts run out, it repeats the
// last one so a runaway loop doesn't index out of range.
type fakeChatter struct {
	scripted []Message
	calls    int
}

func (f *fakeChatter) Chat(messages []Message, tools []any) (Message, error) {
	i := f.calls
	if i >= len(f.scripted) {
		i = len(f.scripted) - 1
	}
	f.calls++
	return f.scripted[i], nil
}

func TestRunRole_MutantGenerator_ReturnsRawText(t *testing.T) {
	canned := "--- MUTANT 1 ---\nfile: foo.go\nline: 12\n..."
	fake := &fakeChatter{scripted: []Message{{Role: "assistant", Content: canned}}}

	result, findings, err := RunRole(context.Background(), fake, "mutant-generator", "generate mutants for foo.go")
	if err != nil {
		t.Fatalf("RunRole: %v", err)
	}
	if result != canned {
		t.Errorf("result = %q, want raw canned text %q", result, canned)
	}
	if findings != nil {
		t.Errorf("findings = %v, want nil for a structured role", findings)
	}
	if fake.calls != 1 {
		t.Errorf("calls = %d, want exactly 1 (single-shot)", fake.calls)
	}
}

// TestRunRole_MutantGeneratorShadow_ReturnsRawText is the daemon-dispatch
// proof for the challenger seat: a task claimed off the REAL queue with
// Role == "mutant-generator-shadow" must take the exact same structured
// single-shot path as its primary ("mutant-generator") — one Chat call, the
// model's raw output returned verbatim for the brain-side Validator to parse
// — not fall through to "no single-shot runner for role" (which RunRole
// returns for any role isStructuredRole/isPoolCriticRole don't recognize)
// and not the critic's freeform tool loop. This is the in-process half of
// the daemon dispatch path; cmd/corral-agent's structured_test.go covers the
// claim-loop half (task.Role driving runTask's fast path + model selection).
func TestRunRole_MutantGeneratorShadow_ReturnsRawText(t *testing.T) {
	canned := "--- MUTANT 1 ---\nfile: foo.go\nline: 12\n..."
	fake := &fakeChatter{scripted: []Message{{Role: "assistant", Content: canned}}}

	result, findings, err := RunRole(context.Background(), fake, "mutant-generator-shadow", "generate mutants for foo.go (challenger seat)")
	if err != nil {
		t.Fatalf("RunRole: %v", err)
	}
	if result != canned {
		t.Errorf("result = %q, want raw canned text %q", result, canned)
	}
	if findings != nil {
		t.Errorf("findings = %v, want nil for a structured role", findings)
	}
	if fake.calls != 1 {
		t.Errorf("calls = %d, want exactly 1 (single-shot, not the critic's multi-step loop)", fake.calls)
	}
}

func TestRunRole_TestWriter_ReturnsRawText(t *testing.T) {
	canned := "package foo_test\n\nfunc TestFoo(t *testing.T) { ... }"
	fake := &fakeChatter{scripted: []Message{{Role: "assistant", Content: canned}}}

	result, findings, err := RunRole(context.Background(), fake, "test-writer", "write a killing test for the surviving mutants")
	if err != nil {
		t.Fatalf("RunRole: %v", err)
	}
	if result != canned {
		t.Errorf("result = %q, want raw canned text %q", result, canned)
	}
	if findings != nil {
		t.Errorf("findings = %v, want nil for a structured role", findings)
	}
	if fake.calls != 1 {
		t.Errorf("calls = %d, want exactly 1 (single-shot)", fake.calls)
	}
}

func TestRunRole_TestCritic_ReturnsParsedFindings(t *testing.T) {
	findingArgs := map[string]any{
		"type":             "bug",
		"severity":         "high",
		"target":           "TestFoo",
		"evidence":         "asserts nothing — always passes",
		"suggested_action": "assert on the actual return value",
	}
	raw, _ := json.Marshal(findingArgs)
	fake := &fakeChatter{scripted: []Message{
		// Step 1: file one finding via a tool call.
		{Role: "assistant", ToolCalls: []ToolCall{{Name: "report_finding", Arguments: raw}}},
		// Step 2: conclude with a one-line summary (no further tool call).
		{Role: "assistant", Content: "reviewed the dev tests; filed 1 finding"},
	}}

	result, findings, err := RunRole(context.Background(), fake, "test-critic", "dev tests:\n<the test source>")
	if err != nil {
		t.Fatalf("RunRole: %v", err)
	}
	if result != "reviewed the dev tests; filed 1 finding" {
		t.Errorf("result = %q", result)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %v, want exactly 1", findings)
	}
	f := findings[0]
	if f.Type != "bug" || f.Severity != "high" || f.Target != "TestFoo" ||
		f.Evidence != "asserts nothing — always passes" || f.SuggestedAction != "assert on the actual return value" {
		t.Errorf("finding = %+v, want fields matching findingArgs", f)
	}
	if f.Status != queue.FindingOpen {
		t.Errorf("finding.Status = %q, want %q", f.Status, queue.FindingOpen)
	}
}

// TestRunRole_TestCritic_ReturnsParsedScope verifies the optional
// scope/test_file/test_selector arguments the critic can pass to
// report_finding are plumbed through to the assembled queue.Finding.
func TestRunRole_TestCritic_ReturnsParsedScope(t *testing.T) {
	findingArgs := map[string]any{
		"type":          "bug",
		"severity":      "high",
		"target":        "TestFoo",
		"evidence":      "asserts nothing — always passes",
		"scope":         "whole-test",
		"test_file":     "t.py",
		"test_selector": "t.py::test_a",
	}
	raw, _ := json.Marshal(findingArgs)
	fake := &fakeChatter{scripted: []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{Name: "report_finding", Arguments: raw}}},
		{Role: "assistant", Content: "reviewed the dev tests; filed 1 finding"},
	}}

	_, findings, err := RunRole(context.Background(), fake, "test-critic", "dev tests:\n<the test source>")
	if err != nil {
		t.Fatalf("RunRole: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %v, want exactly 1", findings)
	}
	f := findings[0]
	if f.Scope != "whole-test" || f.TestFile != "t.py" || f.TestSelector != "t.py::test_a" {
		t.Errorf("finding = %+v, want scope=whole-test test_file=t.py test_selector=t.py::test_a", f)
	}
}

func TestRunRole_TestCritic_NoFindings_WhenTestsAreSound(t *testing.T) {
	fake := &fakeChatter{scripted: []Message{
		{Role: "assistant", Content: "the tests are sound; no findings"},
	}}

	result, findings, err := RunRole(context.Background(), fake, "test-critic", "dev tests:\n<solid tests>")
	if err != nil {
		t.Fatalf("RunRole: %v", err)
	}
	if result != "the tests are sound; no findings" {
		t.Errorf("result = %q", result)
	}
	if len(findings) != 0 {
		t.Errorf("findings = %v, want none", findings)
	}
}

// toolNames extracts the "name" field from each tool schema returned by
// criticTools() (each entry is map[string]any{"type":"function","function":
// map[string]any{"name": ..., ...}}).
func toolNames(tools []any) []string {
	var names []string
	for _, t := range tools {
		m, ok := t.(map[string]any)
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

// alwaysThoughtChatter ALWAYS returns a report_thought tool call, no matter
// how many times Chat is called — the worst case for a model that never
// concludes. Used to prove RunRole's test-critic loop is bounded by
// critFreeformSteps and that the report_thought cap injects the "conclude"
// nudge.
type alwaysThoughtChatter struct {
	calls        int
	lastMessages []Message
}

func (b *alwaysThoughtChatter) Chat(messages []Message, tools []any) (Message, error) {
	b.calls++
	b.lastMessages = messages
	raw, _ := json.Marshal(map[string]any{"text": "still reflecting"})
	return Message{Role: "assistant", ToolCalls: []ToolCall{{Name: "report_thought", Arguments: raw}}}, nil
}

// TestCriticLoopBoundsReportThought verifies RunRole's test-critic loop (a)
// terminates within critFreeformSteps calls even when the model never stops
// calling report_thought, and (b) after the 2nd report_thought, the next
// injected user message nudges the model to file findings/conclude instead
// of accepting more reflection. This guards a real bug where a freeform
// critic burned all its steps on report_thought without ever filing
// findings.
func TestCriticLoopBoundsReportThought(t *testing.T) {
	fake := &alwaysThoughtChatter{}

	_, _, err := RunRole(context.Background(), fake, "test-critic", "critique tests:\n<the test source>")
	if err != nil {
		t.Fatalf("RunRole: %v", err)
	}

	if fake.calls > critFreeformSteps {
		t.Fatalf("critic loop must stop within %d steps; Chat was called %d times", critFreeformSteps, fake.calls)
	}
	if fake.calls < 3 {
		t.Fatalf("expected the loop to run at least 3 steps to exercise the cap; got %d", fake.calls)
	}
	// The last messages sent to the model must include the nudge text,
	// injected as the result of the 2nd (or later) report_thought call.
	found := false
	for _, m := range fake.lastMessages {
		if strings.Contains(m.Content, "You have reflected enough") {
			found = true
		}
	}
	if !found {
		t.Fatalf("after 2 report_thought calls, the loop must inject a nudge to conclude; messages: %+v", fake.lastMessages)
	}
}
