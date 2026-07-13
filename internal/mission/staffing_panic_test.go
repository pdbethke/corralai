// SPDX-License-Identifier: Elastic-2.0

package mission

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/rolemodel"
)

// panicLLM always panics from Generate, simulating a broken Judge backend (a
// bad response shape, a nil-pointer deref deep in a vendor SDK, etc). The
// off-tick staffing goroutine must not let this take the whole process down.
type panicLLM struct{}

func (p *panicLLM) Generate(ctx context.Context, system, prompt string) (string, error) {
	panic("boom: judge backend exploded")
}

func (p *panicLLM) Available() bool { return true }

// TestStaffingPanicIsRecoveredAndLatchesGiveUp asserts that a panic inside the
// async staffing goroutine (e.g. a Judge backend that panics instead of
// returning an error) is recovered rather than crashing the process, and that
// the mission is latched give-up so it is never re-dispatched on a later tick
// — mirroring the failure-latch behavior of a plain Judge error.
func TestStaffingPanicIsRecoveredAndLatchesGiveUp(t *testing.T) {
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	m, err := Open(filepath.Join(dir, "m.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	missionID, err := CreateMission(m, q, "add a wishlist feature", fullDAGTestPlan("add a wishlist feature"), false)
	if err != nil {
		t.Fatal(err)
	}

	e := NewEngine(m, q)
	e.Staffing = &StaffingManager{
		LLM:        &panicLLM{},
		Perf:       &fakePerf{},
		RoleModels: rolemodel.New(),
	}

	// First tick dispatches the staffing goroutine, which panics. If Tick
	// itself returns without error and the process is still alive to run the
	// rest of this test, the panic was contained.
	if err := e.Tick(); err != nil {
		t.Fatalf("Tick returned error: %v", err)
	}
	e.waitStaffingIdle()

	e.staffMu.Lock()
	gaveUp := e.staffGaveUp[missionID]
	e.staffMu.Unlock()
	if !gaveUp {
		t.Fatalf("mission %d: expected staffGaveUp latched true after a staffing panic", missionID)
	}

	// A second tick must not re-dispatch: staffInflight should never be set
	// again for this mission (the latch takes effect before Tick would loop
	// back around to it).
	if err := e.Tick(); err != nil {
		t.Fatalf("second Tick returned error: %v", err)
	}
	e.waitStaffingIdle()

	e.staffMu.Lock()
	inflight := e.staffInflight[missionID]
	e.staffMu.Unlock()
	if inflight {
		t.Fatalf("mission %d: staffing was re-dispatched after give-up latch", missionID)
	}
}

// TestFailMissionEvictsStaffBookkeeping asserts that failMission — the give-up
// backstop that transitions a mission to the terminal `failed` state — drops
// the staffed/staffAttempts/staffGaveUp bookkeeping for that mission, mirroring
// the noProgress/lastFingerprint cleanup it already does. Without this, a
// long-running brain accumulates one entry per failed mission forever.
func TestFailMissionEvictsStaffBookkeeping(t *testing.T) {
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	m, err := Open(filepath.Join(dir, "m.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	missionID, err := CreateMission(m, q, "add a wishlist feature", fullDAGTestPlan("add a wishlist feature"), false)
	if err != nil {
		t.Fatal(err)
	}

	e := NewEngine(m, q)
	mi, err := m.Mission(missionID)
	if err != nil {
		t.Fatal(err)
	}

	// Seed the staff bookkeeping as if a staffing pass had run for this mission.
	e.staffMu.Lock()
	e.staffed[missionID] = true
	e.staffAttempts[missionID] = 2
	e.staffGaveUp[missionID] = true
	e.staffMu.Unlock()

	e.failMission(mi, "test-induced failure")

	e.staffMu.Lock()
	_, stillStaffed := e.staffed[missionID]
	_, stillAttempts := e.staffAttempts[missionID]
	_, stillGaveUp := e.staffGaveUp[missionID]
	e.staffMu.Unlock()
	if stillStaffed || stillAttempts || stillGaveUp {
		t.Fatalf("mission %d: staff bookkeeping not evicted after failMission (staffed=%v attempts=%v gaveUp=%v)",
			missionID, stillStaffed, stillAttempts, stillGaveUp)
	}
}
