// SPDX-License-Identifier: Elastic-2.0

package queue

import (
	"path/filepath"
	"sync"
	"testing"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "q.sqlite3"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestEnqueueAndPromoteReady(t *testing.T) {
	s := open(t)
	// build has no deps; test depends on build.
	if err := s.Enqueue(1, []TaskSpec{
		{Key: "build", Role: "builder", Title: "build", Instruction: "do build"},
		{Key: "test", Role: "tester", Title: "test", Instruction: "do test", DependsOn: []string{"build"}},
	}); err != nil {
		t.Fatal(err)
	}

	// First promotion: only build (no deps) becomes ready; test stays pending.
	if n, _ := s.PromoteReady(1); n != 1 {
		t.Fatalf("first PromoteReady = %d, want 1 (build only)", n)
	}
	bee := "Ada"
	got, _ := s.ClaimNext(bee, []string{"tester"}, 300)
	if got != nil {
		t.Fatalf("tester claimed %q, but test depends on build — nothing should be ready for tester", got.Key)
	}
	build, _ := s.ClaimNext(bee, []string{"builder"}, 300)
	if build == nil || build.Key != "build" {
		t.Fatalf("builder should claim build, got %v", build)
	}

	// test still pending until build is done.
	if n, _ := s.PromoteReady(1); n != 0 {
		t.Fatalf("PromoteReady before build done = %d, want 0", n)
	}
	if _, err := s.Complete(build.ID, bee, "built"); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.PromoteReady(1); n != 1 {
		t.Fatalf("PromoteReady after build done = %d, want 1 (test unlocks)", n)
	}
}

func TestMissionTaskKeys(t *testing.T) {
	s := open(t)
	if err := s.Enqueue(1, []TaskSpec{
		{Key: "build", Role: "builder", Title: "build", Instruction: "b"},
		{Key: "test", Role: "tester", Title: "test", Instruction: "t", DependsOn: []string{"build"}},
	}); err != nil {
		t.Fatal(err)
	}
	// A different mission's keys must not leak in.
	if err := s.Enqueue(2, []TaskSpec{{Key: "other", Role: "builder", Title: "o", Instruction: "o"}}); err != nil {
		t.Fatal(err)
	}
	keys, err := s.MissionTaskKeys(1)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, k := range keys {
		got[k] = true
	}
	if !got["build"] || !got["test"] || got["other"] || len(keys) != 2 {
		t.Fatalf("MissionTaskKeys(1) = %v, want exactly [build test]", keys)
	}
}

func TestClaimNextConcurrentNoDoubleClaim(t *testing.T) {
	s := open(t)
	const N = 50
	specs := make([]TaskSpec, N)
	for i := range specs {
		specs[i] = TaskSpec{Key: key(i), Role: "builder", Title: "t", Instruction: "x"}
	}
	if err := s.Enqueue(1, specs); err != nil {
		t.Fatal(err)
	}
	s.PromoteReady(1)

	var mu sync.Mutex
	seen := map[int64]int{}
	var wg sync.WaitGroup
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for {
				task, err := s.ClaimNext("bee", []string{"builder"}, 300)
				if err != nil || task == nil {
					return
				}
				mu.Lock()
				seen[task.ID]++
				mu.Unlock()
			}
		}(g)
	}
	wg.Wait()

	if len(seen) != N {
		t.Fatalf("claimed %d distinct tasks, want %d", len(seen), N)
	}
	for id, c := range seen {
		if c != 1 {
			t.Fatalf("task %d claimed %d times — double-claim", id, c)
		}
	}
}

func TestCompleteOwnershipAndIdempotent(t *testing.T) {
	s := open(t)
	s.Enqueue(1, []TaskSpec{{Key: "a", Role: "builder", Title: "a", Instruction: "x"}})
	s.PromoteReady(1)
	task, _ := s.ClaimNext("Ada", []string{"builder"}, 300)
	if task == nil {
		t.Fatal("expected a claimable task")
	}
	// A non-claimer can't complete it.
	if ok, _ := s.Complete(task.ID, "Eve", "sneaky"); ok {
		t.Fatal("non-claimer completed the task")
	}
	// The claimer completes it.
	if ok, _ := s.Complete(task.ID, "Ada", "done"); !ok {
		t.Fatal("claimer could not complete its task")
	}
	// Completing again is a no-op (idempotent).
	if ok, _ := s.Complete(task.ID, "Ada", "again"); ok {
		t.Fatal("second Complete should be a no-op")
	}
}

func TestReapPresenceAuthoritativeWithLeaseFallback(t *testing.T) {
	defer restoreNow()
	clock := 1000.0
	now = func() float64 { return clock }

	s := open(t)
	s.Enqueue(1, []TaskSpec{
		{Key: "a", Role: "builder", Title: "a", Instruction: "x"},
		{Key: "b", Role: "builder", Title: "b", Instruction: "x"},
	})
	s.PromoteReady(1)
	s.ClaimNext("Ada", []string{"builder"}, 300) // lease until 1300
	s.ClaimNext("Bob", []string{"builder"}, 300)

	// Everyone present → nothing reaped.
	if n, _ := s.Reap(map[string]bool{"Ada": true, "Bob": true}); n != 0 {
		t.Fatalf("reap with everyone present = %d, want 0", n)
	}

	// Bob is gone → his task is requeued (presence authoritative).
	if n, _ := s.Reap(map[string]bool{"Ada": true}); n != 1 {
		t.Fatalf("reap with Bob absent = %d, want 1", n)
	}

	// Ada's lease has expired, but she is STILL PRESENT (a long task with
	// regular heartbeats) → her task is NOT reaped. This is the no-false-reap
	// guarantee for a busy bee.
	clock = 1400
	if n, _ := s.Reap(map[string]bool{"Ada": true}); n != 0 {
		t.Fatalf("present bee with expired lease reaped = %d, want 0 (presence wins)", n)
	}

	// Presence unknown (nil) → the lease is the fallback, so Ada's expired task
	// is reaped to keep the hive from stranding work.
	if n, _ := s.Reap(nil); n != 1 {
		t.Fatalf("nil-presence fallback with expired lease = %d, want 1", n)
	}
}

func TestMissionDoneOnlyWhenAllDone(t *testing.T) {
	s := open(t)
	s.Enqueue(1, []TaskSpec{
		{Key: "a", Role: "builder", Title: "a", Instruction: "x"},
		{Key: "b", Role: "builder", Title: "b", Instruction: "x"},
	})
	if done, _ := s.MissionDone(1); done {
		t.Fatal("mission can't be done before any task finishes")
	}
	s.PromoteReady(1)
	a, _ := s.ClaimNext("Ada", []string{"builder"}, 300)
	s.Complete(a.ID, "Ada", "ok")
	if done, _ := s.MissionDone(1); done {
		t.Fatal("mission done with one task still open")
	}
	b, _ := s.ClaimNext("Ada", []string{"builder"}, 300)
	s.Complete(b.ID, "Ada", "ok")
	if done, _ := s.MissionDone(1); !done {
		t.Fatal("mission should be done once every task is done")
	}
}

func TestStarvationDoesNotCompleteMission(t *testing.T) {
	s := open(t)
	// A task no running bee can serve (role 'pentester'); no pentester ever claims it.
	s.Enqueue(1, []TaskSpec{
		{Key: "build", Role: "builder", Title: "build", Instruction: "x"},
		{Key: "scan", Role: "pentester", Title: "scan", Instruction: "x"},
	})
	s.PromoteReady(1)
	b, _ := s.ClaimNext("Ada", []string{"builder"}, 300)
	s.Complete(b.ID, "Ada", "ok")
	s.PromoteReady(1)
	// builder is done, scan sits ready unserved → mission is NOT done.
	if done, _ := s.MissionDone(1); done {
		t.Fatal("mission must not complete while a ready task is unserved (starvation)")
	}
	// A builder must not be able to steal a pentester-only task.
	if got, _ := s.ClaimNext("Ada", []string{"builder"}, 300); got != nil {
		t.Fatalf("builder claimed a pentester task %q", got.Key)
	}
}

func key(i int) string { return "t" + string(rune('A'+i%26)) + string(rune('0'+i/26)) }

func restoreNow() { now = realNow }

func TestSelfHealRespectsRoleScope(t *testing.T) {
	defer restoreNow()
	clock := 1000.0
	now = func() float64 { return clock }

	s := open(t)
	mid := int64(1)
	// Two tasks: one test-critic, one mutant-generator.
	if err := s.Enqueue(mid, []TaskSpec{
		{Key: "test-critic", Role: "test-critic", Title: "critique", Instruction: "x"},
		{Key: "mutant-generator", Role: "mutant-generator", Title: "mutate", Instruction: "x"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PromoteReady(mid); err != nil {
		t.Fatal(err)
	}

	// bee "shared" claims the test-critic task with roles=[test-critic].
	tc, err := s.ClaimNextAs("shared", "inst-A", []string{"test-critic"}, 60)
	if err != nil || tc == nil || tc.Role != "test-critic" {
		t.Fatalf("setup claim of test-critic failed: %v %+v", err, tc)
	}

	// Let the lease expire.
	clock = 1100

	// Same bee "shared" now polls claiming for roles=[mutant-generator] (a
	// different worker, same principal). It must NOT self-heal the orphaned
	// test-critic task — that's outside the roles it's claiming for. It should
	// instead get the mutant-generator task (fresh) or nothing test-critic.
	got, err := s.ClaimNextAs("shared", "inst-B", []string{"mutant-generator"}, 60)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil && got.Role == "test-critic" {
		t.Fatalf("self-heal handed a test-critic task to a mutant-generator claim: %+v", got)
	}
	// And a test-critic-scoped claim by the same bee SHOULD recover it.
	rec, err := s.ClaimNextAs("shared", "inst-A", []string{"test-critic"}, 60)
	if err != nil {
		t.Fatal(err)
	}
	if rec == nil || rec.Role != "test-critic" {
		t.Fatalf("role-matching self-heal should recover the orphaned test-critic task, got %+v", rec)
	}
}

func TestVerifyRoundTripAndClaimedMission(t *testing.T) {
	s := openTestStore(t)
	if err := s.Enqueue(7, []TaskSpec{
		{Key: "test#1", Role: "tester", Title: "test", Instruction: "verify it", Verify: "go test"},
		{Key: "design#1", Role: "designer", Title: "design", Instruction: "design it"}, // ungated
	}); err != nil {
		t.Fatal(err)
	}
	// promote + claim the test task so it's loadable by id and claimed by a bee
	if _, err := s.PromoteReady(7); err != nil {
		t.Fatal(err)
	}
	ct, err := s.ClaimNext("Tess", []string{"tester"}, 300)
	if err != nil || ct == nil {
		t.Fatalf("claim: %v %v", ct, err)
	}
	got, err := s.TaskByID(ct.ID)
	if err != nil || got == nil {
		t.Fatalf("TaskByID: %v %v", got, err)
	}
	if got.Verify != "go test" {
		t.Fatalf("Verify not persisted: %q", got.Verify)
	}
	mid, err := s.ClaimedMission("Tess")
	if err != nil {
		t.Fatal(err)
	}
	if mid != 7 {
		t.Fatalf("ClaimedMission(Tess) = %d, want 7", mid)
	}
	if m, _ := s.ClaimedMission("Nobody"); m != 0 {
		t.Fatalf("ClaimedMission of an idle bee should be 0, got %d", m)
	}
}

func TestEnqueueCarriesAssignedModel(t *testing.T) {
	s := openTestStore(t)
	if err := s.Enqueue(1, []TaskSpec{{Key: "w", Role: "test-writer", Title: "t", Instruction: "i", Model: "qwen2.5-coder:7b"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PromoteReady(1); err != nil {
		t.Fatal(err)
	}
	task, err := s.ClaimNextAs("bee", "inst", []string{"test-writer"}, 60)
	if err != nil || task == nil {
		t.Fatalf("claim: %v", err)
	}
	if task.Model != "qwen2.5-coder:7b" {
		t.Errorf("Model = %q, want the assigned model", task.Model)
	}
}
