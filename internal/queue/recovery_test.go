// SPDX-License-Identifier: Elastic-2.0

package queue

import (
	"strings"
	"testing"
)

// The 2026-07-03 mixed-fleet run, distilled: the lead cancelled a claimed task
// (stranding its dependents), then tried to recover by superseding — and
// supersede hard-failed on a UNIQUE(mission_id,key) collision because the
// replacement reused the old key. These tests pin the recovery behaviors.

func seedPipeline(t *testing.T, s *Store) {
	t.Helper()
	if err := s.Enqueue(1, []TaskSpec{
		{Key: "build-core", Role: "builder", Title: "build-core", Instruction: "core", Verify: "go build"},
		{Key: "build", Role: "builder", Title: "build", Instruction: "rest", Verify: "go build", DependsOn: []string{"build-core"}},
		{Key: "test", Role: "tester", Title: "test", Instruction: "verify", Verify: "go test", DependsOn: []string{"build"}},
		{Key: "docs", Role: "writer", Title: "docs", Instruction: "write", DependsOn: []string{"build"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PromoteReady(1); err != nil {
		t.Fatal(err)
	}
}

func taskByKey(t *testing.T, s *Store, key string) Task {
	t.Helper()
	ts, _ := s.List(1)
	for _, x := range ts {
		if x.Key == key {
			return x
		}
	}
	t.Fatalf("task %q not found", key)
	return Task{}
}

// Superseding with a colliding (or empty) key must derive a unique one, not
// blow up mid-recovery.
func TestSupersedeAutoUniquifiesKey(t *testing.T) {
	s := open(t)
	seedPipeline(t, s)
	old := taskByKey(t, s, "build")

	newID, err := s.SupersedeTask(old.ID, TaskSpec{
		Key: "build", Role: "builder", Title: "build (rework)", Instruction: "again",
	})
	if err != nil {
		t.Fatalf("supersede with colliding key must not fail: %v", err)
	}
	nt, _ := s.TaskByID(newID)
	if nt.Key == "build" || nt.Key == "" {
		t.Fatalf("replacement key must be uniquified, got %q", nt.Key)
	}
	if !strings.HasPrefix(nt.Key, "build") {
		t.Fatalf("uniquified key should derive from the base, got %q", nt.Key)
	}
	// Dependents must now wait on the NEW key.
	if deps := taskByKey(t, s, "test").DependsOn; len(deps) != 1 || deps[0] != nt.Key {
		t.Fatalf("dependent should be rewritten to %q, got %v", nt.Key, deps)
	}
}

// A replacement of a gated task inherits the verify gate unless the spec sets
// its own — re-planning must not silently drop the mission's guarantees.
func TestSupersedeInheritsVerifyGate(t *testing.T) {
	s := open(t)
	seedPipeline(t, s)
	old := taskByKey(t, s, "build")

	newID, _ := s.SupersedeTask(old.ID, TaskSpec{
		Key: "build-v2", Role: "builder", Title: "rework", Instruction: "again",
	})
	nt, _ := s.TaskByID(newID)
	if nt.Verify != "go build" {
		t.Fatalf("replacement must inherit the verify gate, got %q", nt.Verify)
	}

	// An explicit spec verify wins.
	old2 := taskByKey(t, s, "test")
	newID2, _ := s.SupersedeTask(old2.ID, TaskSpec{
		Key: "test-v2", Role: "tester", Title: "rework", Instruction: "again", Verify: "go vet ./...",
	})
	nt2, _ := s.TaskByID(newID2)
	if nt2.Verify != "go vet ./..." {
		t.Fatalf("explicit verify must win, got %q", nt2.Verify)
	}
}

// Cancelling a task with live dependents strands them (promotion only fires
// off DONE deps). Guarded cancel refuses and names the dependents; cascade
// takes the whole subtree down deliberately.
func TestCancelGuardedRefusesWithLiveDependents(t *testing.T) {
	s := open(t)
	seedPipeline(t, s)
	build := taskByKey(t, s, "build")

	cancelled, blocked, err := s.CancelTaskGuarded(build.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(cancelled) != 0 {
		t.Fatalf("guarded cancel must refuse, cancelled %v", cancelled)
	}
	if len(blocked) != 2 { // test + docs both wait on build
		t.Fatalf("refusal should name the live dependents, got %v", blocked)
	}
	if got := taskByKey(t, s, "build").Status; got == StatusCancelled {
		t.Fatal("refused cancel must not change state")
	}

	// Cascade: the subtree goes down together (build, test, docs).
	cancelled, blocked, err = s.CancelTaskGuarded(build.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked) != 0 || len(cancelled) != 3 {
		t.Fatalf("cascade should cancel the subtree, got cancelled=%v blocked=%v", cancelled, blocked)
	}
	// A leaf with no dependents cancels without cascade.
	core := taskByKey(t, s, "build-core")
	cancelled, blocked, err = s.CancelTaskGuarded(core.ID, false)
	if err != nil || len(blocked) != 0 || len(cancelled) != 1 {
		t.Fatalf("leaf cancel: cancelled=%v blocked=%v err=%v", cancelled, blocked, err)
	}
}

// A deeper chain (build-core -> build -> test, build -> docs) exercises the
// in-memory BFS walk (not just a single hop) after the N+1 List-per-node
// refactor — the whole dependent subtree must still cascade.
func TestCancelGuardedCascadeDeepChain(t *testing.T) {
	s := open(t)
	seedPipeline(t, s)
	root := taskByKey(t, s, "build-core")
	cancelled, blocked, err := s.CancelTaskGuarded(root.ID, true /* cascade */)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked) != 0 {
		t.Fatalf("cascade should not report blocked: %v", blocked)
	}
	// build-core, build, test, docs all cancelled
	if len(cancelled) < 4 {
		t.Fatalf("cascade cancelled %d, want >=4 (whole chain)", len(cancelled))
	}
}

// The one-step recovery the lead reached for (and hallucinated tool names
// trying to find): re-point dependents of one key at another.
func TestRetargetDependents(t *testing.T) {
	s := open(t)
	seedPipeline(t, s)

	// Complete build-core so retargeted dependents can actually promote.
	core, _ := s.ClaimNext("Bob", []string{"builder"}, 300)
	if core == nil || core.Key != "build-core" {
		t.Fatalf("expected build-core, got %v", core)
	}
	if _, err := s.Complete(core.ID, "Bob", "done"); err != nil {
		t.Fatal(err)
	}

	n, err := s.RetargetDependents(1, "build", "build-core")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 { // test + docs
		t.Fatalf("expected 2 retargeted, got %d", n)
	}
	if deps := taskByKey(t, s, "test").DependsOn; deps[0] != "build-core" {
		t.Fatalf("test should now wait on build-core, got %v", deps)
	}
	// And they promote now that their new dependency is done.
	if promoted, _ := s.PromoteReady(1); promoted < 2 {
		t.Fatalf("retargeted dependents should promote, got %d", promoted)
	}

	// Cycle guard: build depends on build-core, so pointing build-core's
	// dependents at... rather: retargeting "build-core" dependents to "build"
	// would make build wait on itself transitively — refuse.
	if _, err := s.RetargetDependents(1, "build-core", "build"); err == nil {
		t.Fatal("retarget creating a cycle must be refused")
	}
	// Unknown target key: refuse.
	if _, err := s.RetargetDependents(1, "build", "no-such-key"); err == nil {
		t.Fatal("retarget onto a missing key must be refused")
	}
}
