// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/reposcan"
)

type fakeBackend struct {
	reply string
	err   error
	sent  []agentbackend.Message
}

func (f *fakeBackend) Chat(msgs []agentbackend.Message, tools []any) (agentbackend.Message, error) {
	f.sent = msgs
	if f.err != nil {
		return agentbackend.Message{}, f.err
	}
	return agentbackend.Message{Role: "assistant", Content: f.reply}, nil
}

func TestLLMDeriverReturnsTheGoalText(t *testing.T) {
	fb := &fakeBackend{reply: "  must reject negative balances  "}
	d := llmDeriver{b: fb}

	text, ok, err := d.Derive(context.Background(), reposcan.Candidate{Path: "pkg/a.go", Lang: "go"}, "package pkg\n")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if strings.TrimSpace(text) != "must reject negative balances" {
		t.Errorf("text = %q", text)
	}
	// The source must actually be in the request, and nothing else should be.
	var joined string
	for _, m := range fb.sent {
		joined += m.Content
	}
	if !strings.Contains(joined, "package pkg") {
		t.Error("the source was not sent to the model")
	}
}

// An empty or refusing reply is the file's property, not an outage.
func TestLLMDeriverEmptyReplyIsNotAnError(t *testing.T) {
	d := llmDeriver{b: &fakeBackend{reply: "   "}}
	_, ok, err := d.Derive(context.Background(), reposcan.Candidate{Path: "a.go"}, "x")
	if err != nil {
		t.Fatalf("empty reply must not be an error: %v", err)
	}
	if ok {
		t.Fatal("empty reply must be ungoaled")
	}
}

// A transport failure must surface as an error so it becomes derive-failed,
// never ungoaled.
func TestLLMDeriverTransportFailureIsAnError(t *testing.T) {
	d := llmDeriver{b: &fakeBackend{err: errors.New("connection refused")}}
	if _, ok, err := d.Derive(context.Background(), reposcan.Candidate{Path: "a.go"}, "x"); err == nil || ok {
		t.Fatalf("want an error and ok=false, got ok=%v err=%v", ok, err)
	}
}

// NONE must be an exact match (post-trim, case-insensitive), not a substring
// check — a real goal sentence that happens to contain the word "none" must
// still come back as a goal.
func TestLLMDeriverNoneIsExactMatchNotSubstring(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply string
		want  bool // want ok
	}{
		{"literal NONE", "NONE", false},
		{"lowercase", "none", false},
		{"padded and mixed case", "  None  ", false},
		{"contains the word but is a real goal", "must accept none of the malformed inputs", true},
		{"ordinary goal", "must never return a negative balance", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := llmDeriver{b: &fakeBackend{reply: tc.reply}}
			_, ok, err := d.Derive(context.Background(), reposcan.Candidate{Path: "a.go"}, "x")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ok != tc.want {
				t.Errorf("ok = %v, want %v for reply %q", ok, tc.want, tc.reply)
			}
		})
	}
}

// TestGoalDeriverPromptDigestIsPinned is the CI trip-wire for the ritual
// goalDeriverPromptDigest's own doc names: a change to EITHER
// goalDeriverSystem or goalDeriverUserTemplate that does not also update
// this constant fails here, before it can ship a prompt whose text no
// longer matches what GoalPromptRev's key claims to key on.
func TestGoalDeriverPromptDigestIsPinned(t *testing.T) {
	sum := sha256.Sum256([]byte(goalDeriverSystem + goalDeriverUserTemplate))
	got := hex.EncodeToString(sum[:])
	if got != goalDeriverPromptDigest {
		t.Fatalf("sha256(goalDeriverSystem+goalDeriverUserTemplate) = %s, want %s (pinned in goalDeriverPromptDigest) — if this prompt edit was intentional, bump GoalPromptRev and update goalDeriverPromptDigest to %s in the same commit", got, goalDeriverPromptDigest, got)
	}
}
