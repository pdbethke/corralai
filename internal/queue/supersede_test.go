// SPDX-License-Identifier: Elastic-2.0

package queue

import "testing"

func TestCancelTask(t *testing.T) {
	s := open(t)
	s.Enqueue(1, []TaskSpec{{Key: "a", Role: "builder", Title: "a", Instruction: "x"}})
	s.PromoteReady(1)
	ts, _ := s.List(1)
	id := ts[0].ID

	if ok, _ := s.CancelTask(id); !ok {
		t.Fatal("cancel of a ready task should succeed")
	}
	got, _ := s.TaskByID(id)
	if got.Status != StatusCancelled {
		t.Fatalf("status = %q, want cancelled", got.Status)
	}
	// Cancelling again (now terminal) is a no-op.
	if ok, _ := s.CancelTask(id); ok {
		t.Fatal("cancel of a cancelled task should be a no-op")
	}
	// A done task can't be cancelled.
	s.Enqueue(1, []TaskSpec{{Key: "b", Role: "builder", Title: "b", Instruction: "x"}})
	s.PromoteReady(1)
	b, _ := s.ClaimNext("Ada", []string{"builder"}, 300)
	s.Complete(b.ID, "Ada", "done")
	if ok, _ := s.CancelTask(b.ID); ok {
		t.Fatal("cancel of a done task should fail")
	}
}

func TestReopenTask(t *testing.T) {
	s := open(t)
	s.Enqueue(1, []TaskSpec{{Key: "a", Role: "builder", Title: "a", Instruction: "x"}})
	s.PromoteReady(1)
	a, _ := s.ClaimNext("Ada", []string{"builder"}, 300)
	s.Complete(a.ID, "Ada", "built")

	if ok, _ := s.ReopenTask(a.ID); !ok {
		t.Fatal("reopen of a done task should succeed")
	}
	got, _ := s.TaskByID(a.ID)
	if got.Status != StatusReady || got.ClaimedBy != "" {
		t.Fatalf("reopened task = %+v, want ready + unclaimed", got)
	}
	// A non-done task can't be reopened.
	if ok, _ := s.ReopenTask(a.ID); ok {
		t.Fatal("reopen of a ready task should fail")
	}
}

func TestSupersedeTaskLineage(t *testing.T) {
	s := open(t)
	s.Enqueue(1, []TaskSpec{{Key: "build-ui", Role: "builder", Title: "ui", Instruction: "old way"}})
	s.PromoteReady(1)
	ts, _ := s.List(1)
	oldID := ts[0].ID

	newID, err := s.SupersedeTask(oldID, TaskSpec{Key: "build-ui-v2", Role: "builder", Title: "ui", Instruction: "rebuilt on new layer"})
	if err != nil {
		t.Fatal(err)
	}
	old, _ := s.TaskByID(oldID)
	if old.Status != StatusSuperseded {
		t.Fatalf("old task status = %q, want superseded", old.Status)
	}
	neu, _ := s.TaskByID(newID)
	if neu.Supersedes != oldID || neu.Status != StatusPending || neu.Key != "build-ui-v2" {
		t.Fatalf("new task = %+v, want supersedes=%d pending", neu, oldID)
	}
}

func TestSupersedeRewritesPendingDependents(t *testing.T) {
	s := open(t)
	// ui depends on build; the lead supersedes build → ui must now wait on the
	// replacement, not the (never-completing) superseded task.
	s.Enqueue(1, []TaskSpec{
		{Key: "build", Role: "builder", Title: "build", Instruction: "old"},
		{Key: "ui", Role: "builder", Title: "ui", Instruction: "x", DependsOn: []string{"build"}},
	})
	s.PromoteReady(1)
	bt, _ := s.List(1)
	var buildID int64
	for _, tk := range bt {
		if tk.Key == "build" {
			buildID = tk.ID
		}
	}
	if _, err := s.SupersedeTask(buildID, TaskSpec{Key: "build-v2", Role: "builder", Title: "build", Instruction: "rebuilt"}); err != nil {
		t.Fatal(err)
	}
	// ui should now depend on build-v2, and promote only once build-v2 is done.
	if n, _ := s.PromoteReady(1); n != 1 { // build-v2 (no deps) promotes; ui still waits
		t.Fatalf("expected only build-v2 to become ready, promoted %d", n)
	}
	v2, _ := s.ClaimNext("Ada", []string{"builder"}, 300)
	if v2.Key != "build-v2" {
		t.Fatalf("expected build-v2 ready, got %q", v2.Key)
	}
	s.Complete(v2.ID, "Ada", "done")
	if n, _ := s.PromoteReady(1); n != 1 { // now ui unlocks (its dep build-v2 is done)
		t.Fatalf("ui should unlock after build-v2 done, promoted %d", n)
	}
	ui, _ := s.ClaimNext("Ada", []string{"builder"}, 300)
	if ui == nil || ui.Key != "ui" {
		t.Fatalf("ui should be claimable after its rewritten dep completed, got %+v", ui)
	}
}

func TestMissionDoneConvergesWithTerminalStatuses(t *testing.T) {
	s := open(t)
	s.Enqueue(1, []TaskSpec{
		{Key: "a", Role: "builder", Title: "a", Instruction: "x"},
		{Key: "b", Role: "builder", Title: "b", Instruction: "x"},
	})
	s.PromoteReady(1)
	a, _ := s.ClaimNext("Ada", []string{"builder"}, 300)
	s.Complete(a.ID, "Ada", "done")
	// b is still ready → not done.
	if done, _ := s.MissionDone(1); done {
		t.Fatal("mission not done with an open task")
	}
	// The lead cancels b → no open tasks remain → mission converges.
	bt, _ := s.List(1)
	var bID int64
	for _, tk := range bt {
		if tk.Key == "b" {
			bID = tk.ID
		}
	}
	s.CancelTask(bID)
	if done, _ := s.MissionDone(1); !done {
		t.Fatal("mission should converge once the only open task is cancelled")
	}
}
