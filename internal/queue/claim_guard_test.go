// SPDX-License-Identifier: Elastic-2.0

package queue

import "testing"

// The claim UPDATE must guard on status=ready — not rely solely on
// SetMaxOpenConns(1) — so a task claimed once can never be re-claimed. A true
// concurrent race isn't reproducible under pool-of-1, so this pins the SQL
// contract: after a task is claimed, its status is no longer 'ready' and a
// second ClaimNextAs must find nothing to claim, exactly as if the guarded
// UPDATE's RowsAffected() had come back 0 for a stale row.
func TestClaimNextAsStatusGuardedUpdate(t *testing.T) {
	s := open(t)
	if err := s.Enqueue(1, []TaskSpec{
		{Key: "build", Role: "coder", Title: "build", Instruction: "do build"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PromoteReady(1); err != nil {
		t.Fatal(err)
	}

	got, err := s.ClaimNextAs("bee", "inst", []string{"coder"}, 60)
	if err != nil || got == nil {
		t.Fatalf("first claim failed: task=%v err=%v", got, err)
	}

	// The claimed task is no longer 'ready'; a second, different bee must
	// find nothing — the guarded UPDATE must never re-claim a non-ready task.
	got2, err := s.ClaimNextAs("bee2", "inst2", []string{"coder"}, 60)
	if err != nil {
		t.Fatal(err)
	}
	if got2 != nil {
		t.Fatalf("second claim returned a task; the guarded UPDATE must not re-claim a non-ready task: %v", got2)
	}
}
