// SPDX-License-Identifier: Elastic-2.0

package queue

import "testing"

// A bee whose claim_task reply was lost still holds the claim server-side. Its
// next poll — same name, same instance — must hand its own task back
// (re-issue), not return nil: otherwise the task is orphaned forever, the bee
// heartbeats idle, presence keeps it un-reapable, and every dependent task
// deadlocks. (The 2026-07-02 demo hang: Bob held build#1 for 20+ minutes while
// polling claim_task and heartbeating idle.)
func TestClaimNextAsReissuesOwnOrphanedClaim(t *testing.T) {
	s := open(t)
	if err := s.Enqueue(1, []TaskSpec{
		{Key: "build", Role: "builder", Title: "build", Instruction: "do build"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PromoteReady(1); err != nil {
		t.Fatal(err)
	}

	first, err := s.ClaimNextAs("Bob", "host-1", []string{"builder"}, 300)
	if err != nil || first == nil {
		t.Fatalf("first claim: task=%v err=%v", first, err)
	}
	if first.Reissued {
		t.Fatalf("fresh claim must not be marked reissued")
	}

	// The reply to the first claim is lost; the SAME instance polls again.
	again, err := s.ClaimNextAs("Bob", "host-1", []string{"builder"}, 300)
	if err != nil {
		t.Fatal(err)
	}
	if again == nil || again.ID != first.ID {
		t.Fatalf("second poll should re-issue Bob's own claim, got %v", again)
	}
	if !again.Reissued {
		t.Fatalf("re-issued claim should be flagged Reissued")
	}

	// A same-NAME sibling on another host (compose --scale) must NOT steal it
	// while the lease is live...
	sibling, err := s.ClaimNextAs("Bob", "host-2", []string{"builder"}, 300)
	if err != nil {
		t.Fatal(err)
	}
	if sibling != nil {
		t.Fatalf("live-lease claim handed to a same-name sibling: %v", sibling)
	}
	// ...and neither may a different bee.
	other, err := s.ClaimNextAs("Eve", "host-3", []string{"builder"}, 300)
	if err != nil {
		t.Fatal(err)
	}
	if other != nil {
		t.Fatalf("claimed task handed to another bee: %v", other)
	}
}

// Without an instance identity (legacy callers), re-issue only happens after
// the lease expires — so a same-named replica can inherit a dead sibling's
// task, but can't thrash a live one.
func TestClaimNextLeaseExpiryReissue(t *testing.T) {
	s := open(t)
	clock := 1000.0
	now = func() float64 { return clock }
	defer restoreNow()

	if err := s.Enqueue(1, []TaskSpec{
		{Key: "build", Role: "builder", Title: "build", Instruction: "do build"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PromoteReady(1); err != nil {
		t.Fatal(err)
	}

	first, _ := s.ClaimNext("Bob", []string{"builder"}, 300)
	if first == nil {
		t.Fatal("expected a claim")
	}
	// Lease alive: no re-issue (this is what keeps 50 same-named concurrent
	// claimers draining the pool instead of re-claiming one task).
	if got, _ := s.ClaimNext("Bob", []string{"builder"}, 300); got != nil {
		t.Fatalf("live-lease claim re-issued without instance identity: %v", got)
	}
	// Lease expired: the next same-name poll inherits it.
	clock += 301
	got, _ := s.ClaimNext("Bob", []string{"builder"}, 300)
	if got == nil || got.ID != first.ID || !got.Reissued {
		t.Fatalf("expired-lease claim should re-issue, got %v", got)
	}
}

// The brain's slacker rule: a bee that heartbeats status=idle while holding a
// claimed task whose lease has EXPIRED is contradicting itself — the brain
// takes the work back. Live leases are untouched (a same-named --scale sibling
// may legitimately be working).
func TestReclaimIdleRequeuesExpiredClaims(t *testing.T) {
	s := open(t)
	clock := 1000.0
	now = func() float64 { return clock }
	defer restoreNow()

	if err := s.Enqueue(1, []TaskSpec{
		{Key: "build", Role: "builder", Title: "build", Instruction: "do build"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PromoteReady(1); err != nil {
		t.Fatal(err)
	}

	old, _ := s.ClaimNextAs("Bob", "host-1", []string{"builder"}, 300)
	if old == nil {
		t.Fatal("expected a claim")
	}

	// Lease still live: an idle heartbeat reclaims nothing.
	clock += 60
	got, err := s.ReclaimIdle("Bob", 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("reclaimed a live-lease claim: %v", got)
	}

	// Lease expired: the idle bee's claim is requeued...
	clock += 300
	got, err = s.ReclaimIdle("Bob", 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != old.ID {
		t.Fatalf("expected [%d] reclaimed, got %v", old.ID, got)
	}
	// ...and is claimable again by anyone.
	back, _ := s.ClaimNextAs("Tess", "host-9", []string{"builder"}, 300)
	if back == nil || back.ID != old.ID {
		t.Fatalf("reclaimed task should be ready again, got %v", back)
	}
	if back.Reissued {
		t.Fatalf("a requeued task claimed fresh must not be marked reissued")
	}

	// Only the named bee's claims are candidates.
	clock += 400
	if r, _ := s.ReclaimIdle("Eve", 30); len(r) != 0 {
		t.Fatalf("Eve holds nothing; reclaimed %v", r)
	}
}
