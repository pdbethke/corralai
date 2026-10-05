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
	usage agentbackend.Usage
}

func (f *fakeBackend) Chat(msgs []agentbackend.Message, tools []any) (agentbackend.Message, error) {
	f.sent = msgs
	if f.err != nil {
		return agentbackend.Message{}, f.err
	}
	return agentbackend.Message{Role: "assistant", Content: f.reply, Usage: f.usage}, nil
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

// TestTheGoalDeriverRecordsWhatItSpent: the goal-deriver was the one seat in
// a repo scan whose spend corral threw away — its reply's Usage was read for
// the content and dropped — so a scan's recorded cost silently left out a
// call per candidate file. Every call that reached the provider is now
// recorded against the file it derived for, and that includes a NONE answer:
// the model was asked, and paid for, whether or not it found a property.
func TestTheGoalDeriverRecordsWhatItSpent(t *testing.T) {
	cached := int64(30)
	fb := &fakeBackend{reply: "NONE", usage: agentbackend.Usage{InputTokens: 100, OutputTokens: 2, CachedInputTokens: &cached}}
	d := newMeteredDeriver(fb, "m-derive")
	ctx := context.Background()
	if _, ok, err := d.Derive(ctx, reposcan.Candidate{Path: "a.go", Lang: "go"}, "package a"); err != nil || ok {
		t.Fatalf("NONE must come back as no goal: ok=%v err=%v", ok, err)
	}
	fb.reply = "must reject negative balances"
	fb.usage = agentbackend.Usage{InputTokens: 50, OutputTokens: 8}
	_, _, _ = d.Derive(ctx, reposcan.Candidate{Path: "b.go", Lang: "go"}, "package b")
	_, _, _ = d.Derive(ctx, reposcan.Candidate{Path: "b.go", Lang: "go"}, "package b")

	rows := d.(usageReporter).modelCallRows()
	if len(rows) != 2 || rows[0].Path != "a.go" || rows[1].Path != "b.go" {
		t.Fatalf("one row per file the deriver was asked about, by path: %+v", rows)
	}
	a, b := rows[0], rows[1]
	if a.Role != roleGoalDeriver || a.Model != "m-derive" || a.Calls != 1 || a.InputTokens != 100 || a.OutputTokens != 2 || a.CachedInputTokens == nil || *a.CachedInputTokens != 30 {
		t.Fatalf("a.go (a NONE answer) must be recorded in full: %+v", a)
	}
	if b.Calls != 2 || b.InputTokens != 100 || b.OutputTokens != 16 || b.CachedInputTokens != nil {
		t.Fatalf("b.go was asked twice; its calls sum, and cache stays unmeasured when no call reported it: %+v", b)
	}
}

// A call that failed reported nothing; it is not recorded as a measured
// zero. (Its file lands in derive-failed, which says so on its own.)
func TestAFailedDeriveCallRecordsNothing(t *testing.T) {
	d := newMeteredDeriver(&fakeBackend{err: errors.New("connection refused")}, "m")
	_, _, _ = d.Derive(context.Background(), reposcan.Candidate{Path: "a.go"}, "x")
	if rows := d.(usageReporter).modelCallRows(); len(rows) != 0 {
		t.Fatalf("a failed call has no usage to record: %+v", rows)
	}
}

// The deriver's rows join the scan's model-call rows — the ONE set the
// ledger entry, the warehouse and the end-of-scan cost line are all built
// from — so the cost line cannot leave the deriver out while the record
// includes it, or the other way round.
func TestTheDeriverJoinsTheScansModelCalls(t *testing.T) {
	fb := &fakeBackend{reply: "a goal", usage: agentbackend.Usage{InputTokens: 70, OutputTokens: 5}}
	d := newMeteredDeriver(fb, "m-derive")
	_, _, _ = d.Derive(context.Background(), reposcan.Candidate{Path: "c.go"}, "x")

	rows := scanModelCallRowsWith(nil, d)
	if len(rows) != 1 || rows[0].Role != roleGoalDeriver || rows[0].InputTokens != 70 {
		t.Fatalf("scan rows must carry the deriver's: %+v", rows)
	}
	line := costLine(scanModelCallTotalsWith(nil, d))
	if !strings.Contains(line, roleGoalDeriver) || !strings.Contains(line, "1 call") {
		t.Fatalf("the cost line must name the goal-deriver's spend: %q", line)
	}
	// A deriver that is not metered (a test double, or none at all)
	// contributes nothing and does not break the scan's rows.
	if rows := scanModelCallRowsWith(nil, nil); len(rows) != 0 {
		t.Fatalf("no deriver, no rows: %+v", rows)
	}
}
