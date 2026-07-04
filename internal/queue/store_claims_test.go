// SPDX-License-Identifier: Elastic-2.0

package queue

import (
	"path/filepath"
	"testing"
)

// HoldsClaimedTask is the staleness oracle for the bug-#40 escalation: a
// path-claim holder with no claimed task is by definition done (or dead)
// with the work that justified the lease.
func TestHoldsClaimedTask(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Enqueue(1, []TaskSpec{{Key: "build", Role: "builder", Title: "b", Instruction: "b"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PromoteReady(1); err != nil {
		t.Fatal(err)
	}

	if held, err := s.HoldsClaimedTask("Bob"); err != nil || held {
		t.Fatalf("before claiming: held=%v err=%v, want false,nil", held, err)
	}
	task, err := s.ClaimNext("Bob", []string{"builder"}, 300)
	if err != nil || task == nil {
		t.Fatalf("claim: %v %v", task, err)
	}
	if held, err := s.HoldsClaimedTask("Bob"); err != nil || !held {
		t.Fatalf("while claimed: held=%v err=%v, want true,nil", held, err)
	}
	if ok, err := s.Complete(task.ID, "Bob", "done"); err != nil || !ok {
		t.Fatalf("complete: %v %v", ok, err)
	}
	if held, err := s.HoldsClaimedTask("Bob"); err != nil || held {
		t.Fatalf("after completing: held=%v err=%v, want false,nil", held, err)
	}
}
