// SPDX-License-Identifier: Elastic-2.0

package mission

import (
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/queue"
)

func reviewSetup(t *testing.T) (*Engine, *queue.Store, *Store) {
	t.Helper()
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })
	m, err := Open(filepath.Join(dir, "m.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return NewEngine(m, q), q, m
}

// A trivial single-task plan keeps the lifecycle test focused on the gate.
func oneTask() []PhaseSpec {
	return []PhaseSpec{{Name: "build", Role: "builder", Instruction: "build it"}}
}

func TestNonReviewMissionAutoCompletes(t *testing.T) {
	e, q, m := reviewSetup(t)
	mid, _ := CreateMission(m, q, "thing", oneTask(), false) // no review
	e.Tick()
	b, _ := q.ClaimNext("Bee", nil, 300)
	q.Complete(b.ID, "Bee", "done")
	e.Tick()
	if mv, _ := m.Mission(mid); mv.Status != "done" {
		t.Fatalf("non-review mission should auto-complete, got %q (no regression)", mv.Status)
	}
}

// TestResolveNeedsReview: the human-gate resolution path for a mission the
// findings gate parked at needs-review. It must refuse to certify while a
// blocking finding is still open, and converge to done once the human has
// cleared (dismissed/addressed) every blocker.
func TestResolveNeedsReview(t *testing.T) {
	_, q, m := reviewSetup(t)
	mid, _ := CreateMission(m, q, "thing", oneTask(), false)
	fid, err := q.AddFinding(queue.Finding{
		MissionID: mid, Reporter: "reviewer", Type: "design-flaw", Severity: "critical",
		Target: "arch", Evidence: "unsound",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetMissionStatus(mid, "needs-review"); err != nil {
		t.Fatal(err)
	}

	// Refused while the blocker stands — and the mission stays parked.
	if _, err := ResolveNeedsReview(m, q, mid, "high"); err == nil {
		t.Fatal("ResolveNeedsReview must refuse while a blocking finding is open")
	}
	if mv, _ := m.Mission(mid); mv == nil || mv.Status != "needs-review" {
		t.Fatalf("mission must stay needs-review while a blocker is open")
	}

	// Human dismisses the finding → the mission may now certify done.
	if _, err := q.SetFindingStatus(fid, queue.FindingDismissed); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveNeedsReview(m, q, mid, "high"); err != nil {
		t.Fatalf("ResolveNeedsReview after clearing blockers: %v", err)
	}
	if mv, _ := m.Mission(mid); mv == nil || mv.Status != "done" {
		t.Fatalf("mission should converge to done once blockers are cleared, got %v", mv)
	}
}

// ResolveNeedsReview only applies to a parked mission; calling it on a mission
// in any other state is a stale assumption and must be refused (mirrors the
// Pause/Resume/Cancel state guards).
func TestResolveNeedsReviewRefusesWrongState(t *testing.T) {
	_, q, m := reviewSetup(t)
	mid, _ := CreateMission(m, q, "thing", oneTask(), false)
	// still "running"
	if _, err := ResolveNeedsReview(m, q, mid, "high"); err == nil {
		t.Fatal("ResolveNeedsReview on a running mission must be refused")
	}
}
