// SPDX-License-Identifier: Elastic-2.0

package mission

import "testing"

// TestPauseThenResumeMission proves #58's pause/resume round trip: pausing a
// running mission flips its status to "paused" and halts the claim path
// (queue.Store.MissionHaltReason); resuming clears the halt and restores
// "running".
func TestPauseThenResumeMission(t *testing.T) {
	dir := t.TempDir()
	q := openQueue(t, dir)
	m := openMissionStore(t, dir)

	mid, err := CreateMission(m, q, "steer me", []PhaseSpec{{Name: "build", Role: "builder", Instruction: "build it"}}, false)
	if err != nil {
		t.Fatal(err)
	}

	mi, err := PauseMission(m, q, mid)
	if err != nil {
		t.Fatalf("PauseMission: %v", err)
	}
	if mi.Status != "paused" {
		t.Fatalf("status after pause = %q, want paused", mi.Status)
	}
	if reason, err := q.MissionHaltReason(mid); err != nil || reason != "paused" {
		t.Fatalf("MissionHaltReason after pause = %q, %v; want paused, nil", reason, err)
	}
	// A paused mission is not in the running set — Tick skips it entirely.
	running, err := m.RunningMissions()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range running {
		if r.ID == mid {
			t.Fatalf("paused mission %d still appears in RunningMissions", mid)
		}
	}

	// Pausing again (already paused) is refused, not silently accepted.
	if _, err := PauseMission(m, q, mid); err == nil {
		t.Fatal("PauseMission on an already-paused mission: want error, got nil")
	}

	mi, err = ResumeMission(m, q, mid)
	if err != nil {
		t.Fatalf("ResumeMission: %v", err)
	}
	if mi.Status != "running" {
		t.Fatalf("status after resume = %q, want running", mi.Status)
	}
	if reason, err := q.MissionHaltReason(mid); err != nil || reason != "" {
		t.Fatalf("MissionHaltReason after resume = %q, %v; want empty, nil", reason, err)
	}
	running, err = m.RunningMissions()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range running {
		if r.ID == mid {
			found = true
		}
	}
	if !found {
		t.Fatalf("resumed mission %d not back in RunningMissions", mid)
	}

	// Resuming a running (not paused) mission is refused.
	if _, err := ResumeMission(m, q, mid); err == nil {
		t.Fatal("ResumeMission on a running mission: want error, got nil")
	}
}

// TestCancelMissionStopsItAndLeavesActiveSet proves cancel halts the claim
// path (same enforcement as pause) and moves the mission permanently out of
// RunningMissions — with no resume back from cancelled.
func TestCancelMissionStopsItAndLeavesActiveSet(t *testing.T) {
	dir := t.TempDir()
	q := openQueue(t, dir)
	m := openMissionStore(t, dir)

	mid, err := CreateMission(m, q, "cancel me", []PhaseSpec{{Name: "build", Role: "builder", Instruction: "build it"}}, false)
	if err != nil {
		t.Fatal(err)
	}

	mi, err := CancelMission(m, q, mid)
	if err != nil {
		t.Fatalf("CancelMission: %v", err)
	}
	if mi.Status != "cancelled" {
		t.Fatalf("status after cancel = %q, want cancelled", mi.Status)
	}
	if reason, err := q.MissionHaltReason(mid); err != nil || reason != "cancelled" {
		t.Fatalf("MissionHaltReason after cancel = %q, %v; want cancelled, nil", reason, err)
	}
	running, err := m.RunningMissions()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range running {
		if r.ID == mid {
			t.Fatalf("cancelled mission %d still appears in RunningMissions", mid)
		}
	}

	// No resume from cancelled, and cancelling twice is refused.
	if _, err := ResumeMission(m, q, mid); err == nil {
		t.Fatal("ResumeMission on a cancelled mission: want error, got nil")
	}
	if _, err := CancelMission(m, q, mid); err == nil {
		t.Fatal("CancelMission on an already-cancelled mission: want error, got nil")
	}
}

// TestCancelPausedMissionSupersedesTheHalt proves cancelling a paused
// mission works (cancel is allowed from any non-terminal state) and the halt
// reason moves from "paused" to "cancelled".
func TestCancelPausedMissionSupersedesTheHalt(t *testing.T) {
	dir := t.TempDir()
	q := openQueue(t, dir)
	m := openMissionStore(t, dir)

	mid, err := CreateMission(m, q, "pause then cancel", []PhaseSpec{{Name: "build", Role: "builder", Instruction: "build it"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PauseMission(m, q, mid); err != nil {
		t.Fatal(err)
	}
	mi, err := CancelMission(m, q, mid)
	if err != nil {
		t.Fatalf("CancelMission on a paused mission: %v", err)
	}
	if mi.Status != "cancelled" {
		t.Fatalf("status = %q, want cancelled", mi.Status)
	}
	if reason, err := q.MissionHaltReason(mid); err != nil || reason != "cancelled" {
		t.Fatalf("MissionHaltReason = %q, %v; want cancelled, nil", reason, err)
	}
}
