// SPDX-License-Identifier: Elastic-2.0

package agentworker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type replyChatter struct {
	reply string
	calls int
}

func (c *replyChatter) Chat(messages []Message, tools []any) (Message, error) {
	c.calls++
	return Message{Role: "assistant", Content: c.reply}, nil
}

// The typed critic answers in ONE call with every test's verdict. Vacuous and
// dead-check verdicts become findings with the right scope; sound ones do
// not.
func TestTypedCriticMapsVerdictsToFindings(t *testing.T) {
	c := &replyChatter{reply: "```json\n" + `{"tests":[
	  {"test":"TestA","verdict":"sound","reason":"checks the sum"},
	  {"test":"TestB","verdict":"vacuous","reason":"asserts nothing","test_file":"pkg/a_test.go","test_selector":"TestB"},
	  {"test":"TestC","verdict":"dead_check","reason":"second check is x==x","test_file":"pkg/a_test.go","test_selector":"TestC"}
	]}` + "\n```"}
	out, findings, err := RunCriticTyped(c, "critique tests")
	if err != nil {
		t.Fatal(err)
	}
	if c.calls != 1 {
		t.Fatalf("the typed critic is one call, made %d", c.calls)
	}
	if strings.HasPrefix(out, CriticIncompletePrefix) {
		t.Fatalf("a parsed answer is complete: %q", out)
	}
	if len(findings) != 2 {
		t.Fatalf("want 2 findings (TestB, TestC), got %+v", findings)
	}
	if findings[0].Target != "TestB" || findings[0].Scope != "whole-test" || findings[0].TestSelector != "TestB" {
		t.Errorf("vacuous finding: %+v", findings[0])
	}
	if findings[1].Target != "TestC" || findings[1].Scope != "dead-check" {
		t.Errorf("dead-check finding: %+v", findings[1])
	}
}

// An answer that does not parse is a review that did not happen: incomplete,
// with no findings, and never "clean".
func TestTypedCriticThatDoesNotParseIsIncomplete(t *testing.T) {
	for _, reply := range []string{"", "the tests look fine to me", `{"tests":[{"test":"TestA","verdict":"maybe"}]}`} {
		out, findings, err := RunCriticTyped(&replyChatter{reply: reply}, "critique tests")
		if err != nil {
			t.Fatalf("%q: %v", reply, err)
		}
		if !strings.HasPrefix(out, CriticIncompletePrefix) || len(findings) != 0 {
			t.Errorf("reply %q: want an incomplete review with no findings, got %q / %d findings", reply, out, len(findings))
		}
	}
}

// A suite judged entirely sound is a complete review with no findings.
func TestTypedCriticAllSoundIsAClean(t *testing.T) {
	out, findings, err := RunCriticTyped(&replyChatter{reply: `{"tests":[{"test":"TestA","verdict":"sound","reason":"ok"}]}`}, "x")
	if err != nil || len(findings) != 0 || strings.HasPrefix(out, CriticIncompletePrefix) {
		t.Fatalf("all-sound: %q %d %v", out, len(findings), err)
	}
}

// The pool's critic seat is the typed critic: a reply that parses is the
// review, in one call, and the loop never runs.
func TestRunRoleCriticIsTyped(t *testing.T) {
	fake := &fakeChatter{scripted: []Message{
		{Role: "assistant", Content: `{"tests":[{"test":"TestA","verdict":"sound","reason":"ok"},{"test":"TestB","verdict":"vacuous","reason":"asserts nothing"}]}`},
	}}
	out, findings, err := RunRole(context.Background(), fake, "test-critic", "critique tests")
	if err != nil {
		t.Fatal(err)
	}
	if fake.calls != 1 {
		t.Fatalf("a parsed typed answer is the whole review; Chat was called %d times", fake.calls)
	}
	if len(findings) != 1 || findings[0].Target != "TestB" {
		t.Fatalf("findings = %+v, want TestB", findings)
	}
	if strings.HasPrefix(out, CriticIncompletePrefix) {
		t.Fatalf("a parsed answer is complete: %q", out)
	}
}

// An answer that does not parse is asked for once more before anything else
// happens, and a second answer that parses is the review.
func TestRunRoleCriticRetriesAnUnparseableAnswerOnce(t *testing.T) {
	fake := &fakeChatter{scripted: []Message{
		{Role: "assistant", Content: "the tests look fine"},
		{Role: "assistant", Content: `{"tests":[{"test":"TestB","verdict":"vacuous","reason":"asserts nothing"}]}`},
	}}
	out, findings, err := RunRole(context.Background(), fake, "test-critic", "critique tests")
	if err != nil {
		t.Fatal(err)
	}
	if fake.calls != 2 {
		t.Fatalf("want the typed call and one retry, got %d calls", fake.calls)
	}
	if len(findings) != 1 || strings.HasPrefix(out, CriticIncompletePrefix) {
		t.Fatalf("the retry's parsed answer is the review: %q, %+v", out, findings)
	}
}

// Two unparseable answers hand the review to the tool loop, and the recorded
// result says the loop is what produced it.
func TestRunRoleCriticFallsBackToTheLoop(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"type": "vacuous_test", "severity": "medium", "target": "TestB", "evidence": "asserts nothing"})
	fake := &fakeChatter{scripted: []Message{
		{Role: "assistant", Content: "not json"},
		{Role: "assistant", Content: "still not json"},
		{Role: "assistant", ToolCalls: []ToolCall{{Name: "report_finding", Arguments: raw}}},
		{Role: "assistant", Content: "filed 1"},
	}}
	out, findings, err := RunRole(context.Background(), fake, "test-critic", "critique tests")
	if err != nil {
		t.Fatal(err)
	}
	if fake.calls != 4 {
		t.Fatalf("want 2 typed calls then a 2-step loop, got %d calls", fake.calls)
	}
	if len(findings) != 1 || findings[0].Target != "TestB" {
		t.Fatalf("the loop's findings are the review: %+v", findings)
	}
	if strings.HasPrefix(out, CriticIncompletePrefix) || !strings.Contains(out, "tool loop") || !strings.Contains(out, "filed 1") {
		t.Fatalf("the result must say the loop produced it, and carry its summary: %q", out)
	}
}

// A loop fallback that is itself cut short keeps the incomplete marker at the
// FRONT, where the driver reads it.
func TestRunRoleCriticFallbackThatIsCutShortStaysIncomplete(t *testing.T) {
	out, _, err := RunRole(context.Background(), &alwaysThoughtChatter{}, "test-critic", "critique tests")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, CriticIncompletePrefix) {
		t.Fatalf("an incomplete fallback must lead with the marker: %q", out)
	}
}

type errChatter struct{ calls int }

func (c *errChatter) Chat([]Message, []any) (Message, error) {
	c.calls++
	return Message{}, errors.New("provider down")
}

// A provider error is not an unparseable answer: it is returned, not retried
// and not handed to the loop, exactly as the loop returned one.
func TestRunRoleCriticReturnsAProviderError(t *testing.T) {
	c := &errChatter{}
	if _, _, err := RunRole(context.Background(), c, "test-critic", "critique tests"); err == nil {
		t.Fatal("a provider error must be returned")
	}
	if c.calls != 1 {
		t.Fatalf("a provider error is not retried; Chat was called %d times", c.calls)
	}
}
