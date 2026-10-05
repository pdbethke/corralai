// SPDX-License-Identifier: Elastic-2.0

package agentworker

import (
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
