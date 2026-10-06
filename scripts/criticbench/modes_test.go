// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"testing"

	"github.com/pdbethke/corralai/internal/agentworker"
)

type typedAnswer struct{ calls int }

func (c *typedAnswer) Chat([]agentworker.Message, []any) (agentworker.Message, error) {
	c.calls++
	return agentworker.Message{Role: "assistant", Content: `{"tests":[{"test":"TestB","verdict":"vacuous","reason":"asserts nothing"}]}`}, nil
}

// The "loop" mode must measure the tool loop itself. RunRole's critic seat
// now answers typed first, so a loop mode routed through it would score the
// typed critic twice under two names. A model that answers in typed JSON and
// never calls a tool files nothing in the loop.
func TestLoopModeMeasuresTheLoop(t *testing.T) {
	for _, m := range modes {
		if m.name != "loop" {
			continue
		}
		c := &typedAnswer{}
		_, findings, err := m.run(c, "critique tests")
		if err != nil {
			t.Fatal(err)
		}
		if len(findings) != 0 {
			t.Fatalf("the loop mode filed %d finding(s) from a typed answer: it is not running the loop", len(findings))
		}
		return
	}
	t.Fatal("no loop mode")
}

// -modes keeps only the named modes, in the bench's own order, and refuses a
// name it does not know rather than benching nothing.
func TestSelectModes(t *testing.T) {
	got, err := selectModes("seat,typed")
	if err != nil || len(got) != 2 || got[0].name != "typed" || got[1].name != "seat" {
		t.Fatalf("got %v, %v", got, err)
	}
	if all, err := selectModes(""); err != nil || len(all) != len(modes) {
		t.Fatalf("empty means every mode: %v %v", all, err)
	}
	if _, err := selectModes("typed,lop"); err == nil {
		t.Fatal("an unknown mode must be refused")
	}
}
