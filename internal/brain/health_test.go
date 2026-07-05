// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/queue"
)

// fakeClock lets a test move HealthBook's notion of "now" without sleeping.
func fakeClock(start time.Time) (*time.Time, func() time.Time) {
	now := start
	return &now, func() time.Time { return now }
}

func TestHealthBookNeverSeenIsIdle(t *testing.T) {
	hb := NewHealthBook()
	got := hb.Health("Nobody")
	if got.Health != "idle" {
		t.Fatalf("health = %q, want idle for an agent with no recorded activity", got.Health)
	}
}

func TestHealthBookCompletingAgentIsWorking(t *testing.T) {
	hb := NewHealthBook()
	hb.RecordClaim("Ada")
	hb.RecordSuccess("Ada")
	got := hb.Health("Ada")
	if got.Health != "working" {
		t.Fatalf("health = %q, want working right after a completion", got.Health)
	}
	if got.LastSuccessTS == 0 {
		t.Fatalf("expected LastSuccessTS to be set")
	}
}

func TestHealthBookClaimWithoutCompletionGoesStaleFailing(t *testing.T) {
	nowPtr, clock := fakeClock(time.Unix(1_000_000, 0))
	hb := NewHealthBook()
	hb.now = clock

	hb.RecordClaim("Copilot-1")
	if got := hb.Health("Copilot-1"); got.Health == "failing" {
		t.Fatalf("health = failing immediately after a fresh claim, want a grace period")
	}

	// Advance past stallWindow with no completion — this is the dead-quota
	// Copilot worker: it claimed, then its CLI invocation died before it
	// could ever call complete_task.
	*nowPtr = nowPtr.Add(stallWindow + time.Second)
	got := hb.Health("Copilot-1")
	if got.Health != "failing" {
		t.Fatalf("health = %q, want failing for a stale claim with no success", got.Health)
	}
	if got.ClaimsSinceSuccess != 1 {
		t.Fatalf("ClaimsSinceSuccess = %d, want 1", got.ClaimsSinceSuccess)
	}
}

func TestHealthBookReclaimedIsImmediatelyFailing(t *testing.T) {
	hb := NewHealthBook()
	hb.RecordClaim("Flaky")
	hb.RecordReclaimed("Flaky") // its expired claim was force-reclaimed by the idle heartbeat path
	got := hb.Health("Flaky")
	if got.Health != "failing" {
		t.Fatalf("health = %q, want failing right after a force-reclaim (direct stall evidence)", got.Health)
	}
}

func TestHealthBookIdleAgentIsNeverFlagged(t *testing.T) {
	// A genuinely idle worker — present, polling, but no ready work for it to
	// claim — must NOT be flagged failing even a long time later. Under-flag,
	// don't false-alarm.
	nowPtr, clock := fakeClock(time.Unix(2_000_000, 0))
	hb := NewHealthBook()
	hb.now = clock
	*nowPtr = nowPtr.Add(24 * time.Hour)
	got := hb.Health("Idler")
	if got.Health != "idle" {
		t.Fatalf("health = %q, want idle for an agent that never claimed anything", got.Health)
	}
}

func TestHealthBookSuccessResetsCountersAfterAStall(t *testing.T) {
	nowPtr, clock := fakeClock(time.Unix(3_000_000, 0))
	hb := NewHealthBook()
	hb.now = clock

	hb.RecordClaim("Recovered")
	*nowPtr = nowPtr.Add(stallWindow + time.Second)
	if got := hb.Health("Recovered"); got.Health != "failing" {
		t.Fatalf("health = %q, want failing before it recovers", got.Health)
	}
	hb.RecordSuccess("Recovered")
	got := hb.Health("Recovered")
	if got.Health != "working" {
		t.Fatalf("health = %q, want working immediately after recovering with a completion", got.Health)
	}
	if got.ClaimsSinceSuccess != 0 || got.ReclaimedSinceSuccess != 0 {
		t.Fatalf("counters not reset on success: %+v", got)
	}
}

func TestHealthBookNilSafe(t *testing.T) {
	var hb *HealthBook
	hb.RecordClaim("x")
	hb.RecordSuccess("x")
	hb.RecordReclaimed("x")
	if got := hb.Health("x"); got.Health != "idle" {
		t.Fatalf("nil HealthBook.Health = %q, want idle (degrade-never-block)", got.Health)
	}
}

// TestTaskToolsFeedHealthBook is the end-to-end wiring check over the real
// MCP tools: claim_task/complete_task must actually update the HealthBook
// the brain exposes via /api/state, not just the standalone unit above.
func TestTaskToolsFeedHealthBook(t *testing.T) {
	dir := t.TempDir()
	cstore, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cstore.Close() })
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })

	if err := q.Enqueue(1, []queue.TaskSpec{
		{Key: "build#1", Role: "builder", Title: "build", Instruction: "do it"},
		{Key: "build#2", Role: "builder", Title: "build", Instruction: "do it too"},
	}); err != nil {
		t.Fatal(err)
	}
	q.PromoteReady(1)

	hb := NewHealthBook()
	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() { _ = NewServer(cstore, nil, Options{Queue: q, Health: hb}).Run(ctx, serverT) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "bee", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()

	// Ada claims and completes — working.
	var claimedAda claimTaskOut
	callTask(t, sess, "claim_task", map[string]any{"name": "Ada", "roles": []string{"builder"}}, &claimedAda)
	if claimedAda.Task == nil {
		t.Fatalf("Ada's claim_task got nothing ready")
	}
	var completedAda completeTaskOut
	callTask(t, sess, "complete_task", map[string]any{"id": claimedAda.Task.ID, "name": "Ada", "result": "done"}, &completedAda)
	if !completedAda.OK {
		t.Fatalf("Ada's complete_task refused: %+v", completedAda)
	}
	if got := hb.Health("Ada").Health; got != "working" {
		t.Fatalf("Ada health = %q, want working after a real completion", got)
	}

	// Bob claims and never completes — after the stall window, failing.
	var claimedBob claimTaskOut
	callTask(t, sess, "claim_task", map[string]any{"name": "Bob", "roles": []string{"builder"}}, &claimedBob)
	if claimedBob.Task == nil {
		t.Fatalf("Bob's claim_task got nothing ready")
	}
	hb.mu.Lock()
	hb.items["Bob"].lastClaim -= int64(stallWindow.Seconds()) + 1
	hb.mu.Unlock()
	if got := hb.Health("Bob").Health; got != "failing" {
		t.Fatalf("Bob health = %q, want failing for a claim with no completion since", got)
	}

	// Cleo never claimed anything (no ready work for her role) — idle, not flagged.
	if got := hb.Health("Cleo").Health; got != "idle" {
		t.Fatalf("Cleo health = %q, want idle for a genuinely idle agent", got)
	}
}
