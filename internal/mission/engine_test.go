// SPDX-License-Identifier: Elastic-2.0

package mission

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/queue"
)

// fullDAGTestPlan is a fixture standing in for the retired DefaultPlan: the
// same research->design->build-core->build->(test,secops,perf)->integrate->
// docs->retro DAG shape (dependency-gating, multiple downstream fan-out, and a
// couple of Verify-gated phases) that these engine tests were written against.
// The build-plan sizer itself is gone; this is test-only scaffolding to keep
// exercising the engine's dependency/verify-gate machinery.
func fullDAGTestPlan(directive string) []PhaseSpec {
	return []PhaseSpec{
		{Name: "research", Role: "researcher", Count: 1, Instruction: "research: " + directive},
		{Name: "design", Role: "designer", Count: 1, DependsOn: []string{"research"}, Instruction: "design: " + directive},
		{Name: "build-core", Role: "builder", Count: 1, DependsOn: []string{"design"}, Verify: "go build", Instruction: "build-core: " + directive},
		{Name: "build", Role: "builder", Count: 1, DependsOn: []string{"build-core"}, Verify: "go build", Instruction: "build: " + directive},
		{Name: "test", Role: "tester", Count: 2, DependsOn: []string{"build"}, Verify: "go test", Instruction: "test: " + directive},
		{Name: "secops", Role: "pentester", Count: 1, DependsOn: []string{"build"}, Instruction: "secops: " + directive},
		{Name: "perf", Role: "perf", Count: 1, DependsOn: []string{"build"}, Instruction: "perf: " + directive},
		{Name: "integrate", Role: "integrator", Count: 1, DependsOn: []string{"test", "secops", "perf"}, Verify: "go build", Instruction: "integrate: " + directive},
		{Name: "docs", Role: "writer", Count: 1, DependsOn: []string{"integrate"}, Instruction: "docs: " + directive},
		{Name: "retro", Role: "reviewer", Count: 1, DependsOn: []string{"docs"}, Instruction: "retro: " + directive},
	}
}

func status(t *testing.T, m *Store, q *queue.Store, mid int64, name string) string {
	t.Helper()
	mv, err := m.View(mid, q)
	if err != nil || mv == nil {
		t.Fatalf("view: %v", err)
	}
	for _, p := range mv.Phases {
		if p.Name == name {
			return p.Status
		}
	}
	t.Fatalf("no phase %q", name)
	return ""
}

// drain claims and completes every currently-ready task (as a generic bee), then
// returns how many it cleared — one "layer" of the DAG per call.
func drain(t *testing.T, q *queue.Store) int {
	t.Helper()
	n := 0
	for {
		task, err := q.ClaimNext("Bee", nil, 300)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if task == nil {
			return n
		}
		if ok, err := q.Complete(task.ID, "Bee", "done"); err != nil || !ok {
			t.Fatalf("complete %d: ok=%v err=%v", task.ID, ok, err)
		}
		n++
	}
}

func TestMissionPipelinePull(t *testing.T) {
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

	mid, err := CreateMission(m, q, "add a wishlist feature", fullDAGTestPlan("add a wishlist feature"), false) // default pipeline
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(m, q)

	// research has no deps and gates everything else.
	_ = e.Tick()
	if s := status(t, m, q, mid, "research"); s != "running" {
		t.Fatalf("research should be first (ready), got %q", s)
	}
	for _, n := range []string{"design", "build", "test", "secops", "perf", "integrate", "docs", "retro"} {
		if s := status(t, m, q, mid, n); s != "pending" {
			t.Fatalf("%s should be pending behind research, got %q", n, s)
		}
	}

	// Drain the DAG layer by layer (generic bee clears each ready set, then tick
	// promotes the next layer) until the mission converges.
	done := false
	for i := 0; i < 60; i++ {
		drain(t, q)
		_ = e.Tick()
		if mv, _ := m.Mission(mid); mv != nil && mv.Status == "done" {
			done = true
			break
		}
	}
	if !done {
		t.Fatal("mission did not converge through the full pipeline")
	}
	// retro is the terminal phase — done only after the whole chain.
	if s := status(t, m, q, mid, "retro"); s != "done" {
		t.Fatalf("retro should be done at convergence, got %q", s)
	}
}

// TestEngineFiresOnMissionCompleted verifies that the engine's auto-complete
// path (no review required) fires OnMissionCompleted exactly once, with the
// mission's id, "done" status, and its ReviewRounds count.
func TestEngineFiresOnMissionCompleted(t *testing.T) {
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

	mid, err := CreateMission(m, q, "add a wishlist feature", fullDAGTestPlan("add a wishlist feature"), false) // default pipeline, no review
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(m, q)
	var calls int
	var gotID int64
	var gotStatus string
	var gotRounds int
	e.OnMissionCompleted = func(missionID int64, status string, reviewRounds int) {
		calls++
		gotID, gotStatus, gotRounds = missionID, status, reviewRounds
	}

	done := false
	for i := 0; i < 60 && !done; i++ {
		drain(t, q)
		_ = e.Tick()
		if mv, _ := m.Mission(mid); mv != nil && mv.Status == "done" {
			done = true
		}
	}
	if !done {
		t.Fatal("mission did not converge")
	}
	if calls != 1 {
		t.Fatalf("OnMissionCompleted should fire exactly once, got %d calls", calls)
	}
	if gotID != mid || gotStatus != "done" {
		t.Fatalf("got id=%d status=%q, want id=%d status=done", gotID, gotStatus, mid)
	}
	if gotRounds != 0 {
		t.Fatalf("non-review mission should report review_rounds=0, got %d", gotRounds)
	}
}

// TestEngineRoutesToNeedsReviewOnOpenCriticalFinding: a drained queue is not a
// clean bill of health. If an open finding at/above `high` remains — e.g. a
// critical `design-flaw`, which is never auto-actioned and so never becomes a
// task and never blocks MissionDone — the brain must NOT certify the mission
// "done". It routes to the human-gate `needs-review` terminal state instead: a
// judge may not certify a result it knows still holds a critical defect.
func TestEngineRoutesToNeedsReviewOnOpenCriticalFinding(t *testing.T) {
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

	mid, err := CreateMission(m, q, "add a wishlist feature", fullDAGTestPlan("add a wishlist feature"), false)
	if err != nil {
		t.Fatal(err)
	}
	// A non-actionable but critical finding: design-flaw is never auto-remediated
	// into a task or marked addressed — it stays open right through convergence.
	if _, err := q.AddFinding(queue.Finding{
		MissionID: mid, Reporter: "reviewer", Type: "design-flaw", Severity: "critical",
		Target: "architecture", Evidence: "the auth model is fundamentally unsound",
	}); err != nil {
		t.Fatal(err)
	}

	e := NewEngine(m, q)
	var gotStatus string
	var completedCalls int
	e.OnMissionCompleted = func(_ int64, status string, _ int) {
		completedCalls++
		gotStatus = status
	}

	settled := false
	for i := 0; i < 60 && !settled; i++ {
		drain(t, q)
		_ = e.Tick()
		if mv, _ := m.Mission(mid); mv != nil && mv.Status != "running" {
			settled = true
		}
	}
	mv, _ := m.Mission(mid)
	if mv == nil {
		t.Fatal("mission missing")
	}
	if mv.Status == "done" {
		t.Fatal("mission certified done despite an open critical finding — the gate must withhold certification")
	}
	if mv.Status != "needs-review" {
		t.Fatalf("mission status = %q, want needs-review", mv.Status)
	}
	if gotStatus != "needs-review" || completedCalls != 1 {
		t.Fatalf("OnMissionCompleted got status=%q calls=%d, want needs-review/1", gotStatus, completedCalls)
	}
}

// TestEngineConvergesDoneWhenOnlyLowFindingsOpen: the needs-review gate must not
// over-fire — an open finding BELOW the blocking threshold (e.g. a low-severity
// note) does not withhold certification; the mission still converges to done.
func TestEngineConvergesDoneWhenOnlyLowFindingsOpen(t *testing.T) {
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

	mid, err := CreateMission(m, q, "add a wishlist feature", fullDAGTestPlan("add a wishlist feature"), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.AddFinding(queue.Finding{
		MissionID: mid, Reporter: "reviewer", Type: "note", Severity: "low",
		Target: "style", Evidence: "consider a shorter variable name",
	}); err != nil {
		t.Fatal(err)
	}

	e := NewEngine(m, q)
	done := false
	for i := 0; i < 60 && !done; i++ {
		drain(t, q)
		_ = e.Tick()
		if mv, _ := m.Mission(mid); mv != nil && mv.Status == "done" {
			done = true
		}
	}
	if !done {
		mv, _ := m.Mission(mid)
		got := "<nil>"
		if mv != nil {
			got = mv.Status
		}
		t.Fatalf("a low-severity finding must not block convergence; status = %q", got)
	}
}

// TestEngineSweepsBlockedDependencies is the graceful-degradation guarantee for a
// DAG deadlock: a pending task whose dependency can never be satisfied (cancelled/
// superseded/missing) is invisibly stuck forever (PromoteReady never promotes it,
// MissionDone never converges, DetectRoleStalls can't see non-ready tasks). The
// engine must sweep it — cancel it and file a loud finding — so the hang becomes a
// visible failure.
func TestEngineSweepsBlockedDependencies(t *testing.T) {
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

	mid, err := CreateMission(m, q, "build a thing", fullDAGTestPlan("build a thing"), false)
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(m, q)

	// Find a pending task that depends on another, and cancel that dependency so it
	// can never become done — orphaning the dependent.
	tasks, err := q.List(mid)
	if err != nil {
		t.Fatal(err)
	}
	var dependent queue.Task
	for _, tk := range tasks {
		if len(tk.DependsOn) > 0 {
			dependent = tk
			break
		}
	}
	if dependent.ID == 0 {
		t.Fatal("expected the default plan to contain a task with a dependency")
	}
	depKey := dependent.DependsOn[0]
	var depID int64
	for _, tk := range tasks {
		if tk.Key == depKey {
			depID = tk.ID
		}
	}
	if depID == 0 {
		t.Fatalf("dependency task %q not found", depKey)
	}
	if _, err := q.CancelTask(depID); err != nil {
		t.Fatal(err)
	}

	// One tick: the sweep must cancel the now-orphaned dependent and record it.
	if err := e.Tick(); err != nil {
		t.Fatalf("tick: %v", err)
	}

	tk, err := q.TaskByID(dependent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tk.Status != queue.StatusCancelled {
		t.Fatalf("orphaned task %q should be swept to cancelled, got %q", dependent.Key, tk.Status)
	}
	fs, _ := q.Findings(mid, queue.FindingOpen)
	found := false
	for _, f := range fs {
		if f.Reporter == "dep-sweep" && strings.HasPrefix(f.Target, "blocked-dep") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected a dep-sweep blocked-dependency finding, got %+v", fs)
	}
}

// TestBlockedDepChainRoutesToNeedsReviewNotPR guards against a false convergence:
// a dep-sweep blocker (cancelled dependency) files a high-severity finding that is
// never auto-remediated/addressed, so blockingFindingOpen keeps holding the
// mission at needs-review instead of falling through to done.
func TestBlockedDepChainRoutesToNeedsReviewNotPR(t *testing.T) {
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

	mid, err := CreateMission(m, q, "build a thing", fullDAGTestPlan("build a thing"), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetRepo(mid, "https://github.com/o/r", "main", "corralai/m1"); err != nil {
		t.Fatal(err)
	}

	e := NewEngine(m, q)

	// Find a pending task that depends on another, and cancel that dependency so it
	// can never become done — orphaning the dependent (same setup as
	// TestEngineSweepsBlockedDependencies).
	tasks, err := q.List(mid)
	if err != nil {
		t.Fatal(err)
	}
	var dependent queue.Task
	for _, tk := range tasks {
		if len(tk.DependsOn) > 0 {
			dependent = tk
			break
		}
	}
	if dependent.ID == 0 {
		t.Fatal("expected the default plan to contain a task with a dependency")
	}
	depKey := dependent.DependsOn[0]
	var depID int64
	for _, tk := range tasks {
		if tk.Key == depKey {
			depID = tk.ID
		}
	}
	if depID == 0 {
		t.Fatalf("dependency task %q not found", depKey)
	}
	if _, err := q.CancelTask(depID); err != nil {
		t.Fatal(err)
	}

	// Drive to convergence: sweep cancels the orphan and files the dep-sweep
	// finding, then subsequent ticks/replans must not auto-address it away.
	var mi *Mission
	for i := 0; i < 60; i++ {
		if err := e.Tick(); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
		drain(t, q)
		mi, err = m.Mission(mid)
		if err != nil {
			t.Fatal(err)
		}
		if mi.Status == "done" || mi.Status == "needs-review" || mi.Status == "failed" {
			break
		}
	}
	if mi == nil {
		t.Fatal("mission never converged")
	}
	if mi.Status == "done" {
		t.Fatalf("mission converged to done despite a cancelled dependency chain")
	}
	if mi.Status != "needs-review" {
		t.Fatalf("status = %q, want needs-review", mi.Status)
	}
}

// TestEngineFailsMissionWithNoProgress is the universal give-up backstop: a
// running mission that makes no forward progress for NoProgressTicks consecutive
// ticks while nothing is claimed (no agent actively holding work) must reach the
// terminal `failed` state — not hang in "running" forever — and say so.
func TestEngineFailsMissionWithNoProgress(t *testing.T) {
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

	mid, err := CreateMission(m, q, "build a thing", fullDAGTestPlan("build a thing"), false)
	if err != nil {
		t.Fatal(err)
	}

	e := NewEngine(m, q)
	e.NoProgressTicks = 3
	var terminalStatus string
	e.OnMissionCompleted = func(_ int64, status string, _ int) {
		if status != "done" {
			terminalStatus = status
		}
	}

	// No agents ever claim anything; nothing progresses. The backstop must give up.
	for i := 0; i < 6; i++ {
		if err := e.Tick(); err != nil {
			t.Fatalf("tick: %v", err)
		}
	}

	mv, err := m.Mission(mid)
	if err != nil {
		t.Fatal(err)
	}
	if mv.Status != "failed" {
		t.Fatalf("a mission with no progress and nothing claimable must reach the terminal failed state, got %q", mv.Status)
	}
	if terminalStatus != "failed" {
		t.Fatalf("a failed mission must fire OnMissionCompleted with 'failed', got %q", terminalStatus)
	}
}
