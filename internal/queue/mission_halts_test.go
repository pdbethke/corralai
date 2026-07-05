// SPDX-License-Identifier: Elastic-2.0

package queue

import "testing"

// TestClaimNextExcludesHaltedMission proves #58's claim-path enforcement: a
// paused mission's ready task is never handed out by ClaimNext, even though
// it sits in the ready pool exactly like any other claimable task.
func TestClaimNextExcludesHaltedMission(t *testing.T) {
	s := open(t)
	if err := s.Enqueue(1, []TaskSpec{{Key: "build", Role: "builder", Title: "build", Instruction: "do build"}}); err != nil {
		t.Fatal(err)
	}
	if n, err := s.PromoteReady(1); err != nil || n != 1 {
		t.Fatalf("PromoteReady = %d, %v; want 1, nil", n, err)
	}

	if err := s.HaltMission(1, "paused"); err != nil {
		t.Fatalf("HaltMission: %v", err)
	}

	if got, err := s.ClaimNext("Ada", []string{"builder"}, 300); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	} else if got != nil {
		t.Fatalf("claimed %q from a paused mission; want nothing claimable", got.Key)
	}

	// Resume: UnhaltMission restores normal claim flow.
	if err := s.UnhaltMission(1); err != nil {
		t.Fatalf("UnhaltMission: %v", err)
	}
	got, err := s.ClaimNext("Ada", []string{"builder"}, 300)
	if err != nil {
		t.Fatalf("ClaimNext after resume: %v", err)
	}
	if got == nil || got.Key != "build" {
		t.Fatalf("ClaimNext after resume = %v, want the build task", got)
	}
}

// TestClaimNextExcludesCancelledMission proves cancel gets the same
// claim-path treatment as pause — the reason differs but the enforcement
// (mission_id NOT IN mission_halts) does not distinguish reasons, and cancel
// never resumes.
func TestClaimNextExcludesCancelledMission(t *testing.T) {
	s := open(t)
	if err := s.Enqueue(1, []TaskSpec{{Key: "build", Role: "builder", Title: "build", Instruction: "do build"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PromoteReady(1); err != nil {
		t.Fatal(err)
	}
	if err := s.HaltMission(1, "cancelled"); err != nil {
		t.Fatalf("HaltMission: %v", err)
	}
	if got, err := s.ClaimNext("Ada", []string{"builder"}, 300); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	} else if got != nil {
		t.Fatalf("claimed %q from a cancelled mission; want nothing claimable", got.Key)
	}
	reason, err := s.MissionHaltReason(1)
	if err != nil {
		t.Fatalf("MissionHaltReason: %v", err)
	}
	if reason != "cancelled" {
		t.Fatalf("MissionHaltReason = %q, want cancelled", reason)
	}
}

// TestClaimNextUnaffectedForOtherMissions proves the halt is per-mission,
// not global: a second, un-halted mission's ready task is claimed normally
// while the first stays paused.
func TestClaimNextUnaffectedForOtherMissions(t *testing.T) {
	s := open(t)
	if err := s.Enqueue(1, []TaskSpec{{Key: "build", Role: "builder", Title: "build", Instruction: "do build"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Enqueue(2, []TaskSpec{{Key: "build", Role: "builder", Title: "build", Instruction: "do build"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PromoteReady(1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PromoteReady(2); err != nil {
		t.Fatal(err)
	}
	if err := s.HaltMission(1, "paused"); err != nil {
		t.Fatal(err)
	}
	got, err := s.ClaimNext("Ada", []string{"builder"}, 300)
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if got == nil || got.MissionID != 2 {
		t.Fatalf("ClaimNext = %v, want mission 2's task (mission 1 is paused)", got)
	}
}
