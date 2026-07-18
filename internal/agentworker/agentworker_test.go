// SPDX-License-Identifier: Elastic-2.0

package agentworker

import (
	"context"
	"encoding/json"
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
