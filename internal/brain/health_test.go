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

// ThrottleClaim is the self-heal backoff: an agent whose task was just
// force-reclaimed (failing, making no progress) is denied a claim for a cooldown
// window so it can't re-enter a tight reclaim loop and starve healthy workers —
// but the window EXPIRES into a probation claim, and a success clears the state,
// so it self-heals rather than being permanently quarantined.
func TestHealthBookThrottleClaimBacksOffReclaimedAgent(t *testing.T) {
	nowPtr, clock := fakeClock(time.Unix(4_000_000, 0))
	hb := NewHealthBook()
	hb.now = clock
	cooldown := 30 * time.Second

	// A healthy agent that was never reclaimed is never throttled.
	hb.RecordClaim("Ada")
	if hb.ThrottleClaim("Ada", cooldown) {
		t.Fatal("a non-reclaimed agent must not be throttled")
	}

	// A force-reclaimed agent is throttled during the cooldown window...
	hb.RecordReclaimed("Flaky")
	if !hb.ThrottleClaim("Flaky", cooldown) {
		t.Fatal("a just-reclaimed agent must be throttled during cooldown")
	}
	// ...and gets a probation claim once the cooldown elapses.
	*nowPtr = nowPtr.Add(cooldown + time.Second)
	if hb.ThrottleClaim("Flaky", cooldown) {
		t.Fatal("after the cooldown elapses the agent must get a probation claim")
	}

	// A success clears the reclaim state → never throttled again (self-heal).
	hb.RecordReclaimed("Flaky")
	hb.RecordSuccess("Flaky")
	if hb.ThrottleClaim("Flaky", cooldown) {
		t.Fatal("a recovered agent (success after reclaim) must not be throttled")
	}
}

func TestThrottleClaimNilSafeAndDisabled(t *testing.T) {
	var hb *HealthBook
	if hb.ThrottleClaim("x", 30*time.Second) {
		t.Fatal("nil HealthBook must never throttle (degrade-never-block)")
	}
	hb2 := NewHealthBook()
	hb2.RecordReclaimed("y")
	if hb2.ThrottleClaim("y", 0) {
		t.Fatal("cooldown 0 disables throttling")
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

func TestDetectRoleStallsFilesFindingOnce(t *testing.T) {
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })
	if err := q.Enqueue(42, []queue.TaskSpec{{Key: "perf-1", Role: "perf", Title: "perf", Instruction: "measure"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.PromoteReady(42); err != nil {
		t.Fatal(err)
	}
	active := []coord.Agent{{Name: "Ada", Role: "builder"}}

	n, err := DetectRoleStalls(q, active, nil, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("first watchdog sweep filed %d findings, want 1", n)
	}
	fs, err := q.Findings(42, queue.FindingOpen)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 1 || fs[0].Reporter != "stall-watchdog" || fs[0].Type != "missing-req" || fs[0].Target != "perf-1" {
		t.Fatalf("unexpected stall finding: %+v", fs)
	}

	// Repeated sweeps should not spam duplicate open findings for same task.
	n, err = DetectRoleStalls(q, active, nil, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("second watchdog sweep filed %d findings, want 0 duplicates", n)
	}
}

func TestDetectRoleStallsSkipsEligibleRole(t *testing.T) {
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })
	if err := q.Enqueue(7, []queue.TaskSpec{{Key: "test-1", Role: "tester", Title: "test", Instruction: "run tests"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.PromoteReady(7); err != nil {
		t.Fatal(err)
	}

	n, err := DetectRoleStalls(q, []coord.Agent{{Name: "Tess", Role: "tester"}}, nil, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("watchdog should not file findings when an eligible agent is present, got %d", n)
	}
}

// A generalist worker (Role="generalist") claims any ready task, so it covers
// every role. The watchdog must not file a bogus stall finding for a role that a
// present generalist actually covers.
func TestDetectRoleStallsSkipsGeneralistCoverage(t *testing.T) {
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })
	if err := q.Enqueue(7, []queue.TaskSpec{{Key: "perf-1", Role: "perf", Title: "perf", Instruction: "measure"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.PromoteReady(7); err != nil {
		t.Fatal(err)
	}

	n, err := DetectRoleStalls(q, []coord.Agent{{Name: "Gigi", Role: "generalist"}}, nil, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("a generalist covers every role; want 0 stall findings, got %d", n)
	}
}

// A multi-role worker registers a "+"-joined Role ("researcher+perf"). The
// watchdog must expand that into its constituent roles, not treat the whole
// string as one opaque role, so a perf task is seen as covered.
func TestDetectRoleStallsSkipsMultiRoleCoverage(t *testing.T) {
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })
	if err := q.Enqueue(7, []queue.TaskSpec{{Key: "perf-1", Role: "perf", Title: "perf", Instruction: "measure"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.PromoteReady(7); err != nil {
		t.Fatal(err)
	}

	n, err := DetectRoleStalls(q, []coord.Agent{{Name: "Remy", Role: "researcher+perf"}}, nil, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("a researcher+perf worker covers perf; want 0 stall findings, got %d", n)
	}
}

// TestDetectRoleStallsFlagsFailingOnlyCoverage: a role whose only present agent is
// FAILING (a claim of theirs was force-reclaimed — a reclaim-looping dead worker)
// is not really covered. The watchdog must surface the stall instead of treating
// that worker as healthy staffing (the invisible reclaim loop from the audit).
func TestDetectRoleStallsFlagsFailingOnlyCoverage(t *testing.T) {
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })
	if err := q.Enqueue(7, []queue.TaskSpec{{Key: "test-1", Role: "tester", Title: "test", Instruction: "run tests"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.PromoteReady(7); err != nil {
		t.Fatal(err)
	}

	book := NewHealthBook()
	book.RecordReclaimed("Tess") // a claim of Tess's was force-reclaimed → failing

	n, err := DetectRoleStalls(q, []coord.Agent{{Name: "Tess", Role: "tester"}}, book, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Fatalf("a role covered only by a failing agent must be flagged as stalled, got %d findings", n)
	}
}
