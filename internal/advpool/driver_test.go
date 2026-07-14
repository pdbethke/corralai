// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/queue"
)

func newTestQueue(t *testing.T) *queue.Store {
	t.Helper()
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = q.Close() })
	return q
}

// scoreCall records one Scorer.Score invocation for assertions.
type scoreCall struct {
	code, test string
	mutants    []adequacy.Mutant
}

// fakeScorer never runs a real jail: the first call is assumed to be the dev
// score (mutant-generator's mutants against the dev's own tests), the second
// the pool score (survivors against the pool's authored test) — matching the
// order Tick's state machine actually calls Score in.
type fakeScorer struct {
	calls []scoreCall
	err   error

	devKillRate  float64
	devSurvivors []adequacy.Mutant

	poolSurvivors []adequacy.Mutant
}

func (f *fakeScorer) Score(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (float64, []adequacy.Mutant, error) {
	f.calls = append(f.calls, scoreCall{code, test, mutants})
	if f.err != nil {
		return 0, nil, f.err
	}
	if len(f.calls) == 1 {
		return f.devKillRate, f.devSurvivors, nil
	}
	return 1.0, f.poolSurvivors, nil
}

type fakeValidator struct {
	mutants    []adequacy.Mutant
	parseErr   error
	compileErr error
}

func (f *fakeValidator) ParseMutants(raw string) ([]adequacy.Mutant, error) {
	return f.mutants, f.parseErr
}

func (f *fakeValidator) CompileTest(ctx context.Context, codePath, code, test string) error {
	return f.compileErr
}

func decorrelatedAssign() RoleAssignment {
	return RoleAssignment{
		RoleMutantGenerator: "model-gen",
		RoleTestWriter:      "model-writer",
		RoleTestCritic:      "model-critic",
	}
}

// claimAllReady claims (as a generic bee) every currently-ready task and
// returns them indexed by key. Claiming one at a time via ClaimNext and
// checking for a specific key doesn't compose — a mismatched claim can't be
// released, so it would strand an unrelated task — so tests instead drain
// the whole ready set in one deterministic sweep and index by key.
func claimAllReady(t *testing.T, q *queue.Store) map[string]*queue.Task {
	t.Helper()
	out := map[string]*queue.Task{}
	for {
		task, err := q.ClaimNext("bee", nil, 300)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if task == nil {
			return out
		}
		out[task.Key] = task
	}
}

// claimByKey drains the ready set and returns the task with the given key,
// failing the test if it isn't among them.
func claimByKey(t *testing.T, q *queue.Store, key string) *queue.Task {
	t.Helper()
	ready := claimAllReady(t, q)
	task, ok := ready[key]
	if !ok {
		t.Fatalf("no claimable task found for key %q (ready were: %v)", key, keysOf(ready))
	}
	return task
}

func keysOf(m map[string]*queue.Task) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// claimTaskByID claims tasks until it wins the one identified by id — used
// for test-writer, whose live key drifts across SupersedeTask calls
// ("test-writer" -> "test-writer-r2" -> ...; see the note in
// tickDevAdequacy), so claiming by key is unreliable for it.
func claimTaskByID(t *testing.T, q *queue.Store, id int64) *queue.Task {
	t.Helper()
	for i := 0; i < 5; i++ {
		task, err := q.ClaimNext("bee", nil, 300)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if task == nil {
			t.Fatalf("no claimable task found for id %d", id)
		}
		if task.ID == id {
			return task
		}
	}
	t.Fatalf("could not claim task %d within attempts", id)
	return nil
}

func mustComplete(t *testing.T, q *queue.Store, id int64, result string) {
	t.Helper()
	ok, err := q.Complete(id, "bee", result)
	if err != nil || !ok {
		t.Fatalf("complete %d: ok=%v err=%v", id, ok, err)
	}
}

// newTestDriver wires a Driver over a fresh queue + the given fakes/threshold
// and starts a run for missionID, returning the driver and its RunSpec.
func newTestDriver(t *testing.T, missionID int64, scorer *fakeScorer, validator *fakeValidator, threshold float64) (*Driver, RunSpec) {
	t.Helper()
	q := newTestQueue(t)
	d, err := NewDriver(q, scorer, validator, decorrelatedAssign(), threshold)
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}
	rs := testRunSpec()
	if err := d.StartRun(missionID, rs, nil); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	// Enqueue leaves every task pending; flip the no-deps roles
	// (mutant-generator, test-critic) to ready before a test claims them.
	if _, err := q.PromoteReady(missionID); err != nil {
		t.Fatalf("PromoteReady: %v", err)
	}
	return d, rs
}

// (a) mutant-generator done -> Tick scores the dev's own tests and promotes
// test-writer, re-rendered with the real survivors.
func TestTick_DevAdequacy_PromotesTestWriterWithSurvivors(t *testing.T) {
	survivor := adequacy.Mutant{ID: "m1", Code: "SURVIVOR-MARKER-m1"}
	scorer := &fakeScorer{devKillRate: 0.9, devSurvivors: []adequacy.Mutant{survivor}}
	validator := &fakeValidator{mutants: []adequacy.Mutant{survivor, {ID: "m2", Code: "killed-m2"}}}
	d, _ := newTestDriver(t, 1, scorer, validator, 0.5)

	mg := claimByKey(t, d.Q, RoleMutantGenerator)
	mustComplete(t, d.Q, mg.ID, "raw mutant-generator output")

	v, err := d.Tick(context.Background(), 1)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if v != nil {
		t.Fatalf("expected no verdict yet (test-critic/test-writer not done), got %+v", v)
	}
	if len(scorer.calls) != 1 {
		t.Fatalf("expected Scorer.Score called once (dev tests), got %d calls", len(scorer.calls))
	}
	if scorer.calls[0].test != testRunSpec().DevTestCode {
		t.Fatalf("dev score call used test %q, want the dev's own test code", scorer.calls[0].test)
	}

	live, err := d.Q.TaskByID(d.runs[1].testWriterTaskID)
	if err != nil {
		t.Fatal(err)
	}
	if live == nil {
		t.Fatal("no live test-writer task after dev-adequacy scoring")
	}
	if live.Status != queue.StatusReady {
		t.Fatalf("test-writer status = %s, want ready (promoted)", live.Status)
	}
	if !strings.Contains(live.Instruction, "SURVIVOR-MARKER-m1") {
		t.Fatalf("test-writer instruction does not reference the real survivor:\n%s", live.Instruction)
	}
}

// (b) test-writer done -> Tick validates it compiles and scores it against
// the survivors, producing ProvenMissed.
func TestTick_PoolAdequacy_ScoresProvenMissed(t *testing.T) {
	survivors := []adequacy.Mutant{{ID: "m1", Code: "c1"}, {ID: "m2", Code: "c2"}}
	scorer := &fakeScorer{
		devKillRate:   0.5,
		devSurvivors:  survivors,
		poolSurvivors: []adequacy.Mutant{{ID: "m2", Code: "c2"}}, // pool test killed m1 but not m2
	}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m0", Code: "c0"}, survivors[0], survivors[1]}}
	d, _ := newTestDriver(t, 2, scorer, validator, 0.1)

	mg := claimByKey(t, d.Q, RoleMutantGenerator)
	mustComplete(t, d.Q, mg.ID, "raw mutants")
	if _, err := d.Tick(context.Background(), 2); err != nil {
		t.Fatalf("Tick (dev-adequacy): %v", err)
	}

	tw := claimTaskByID(t, d.Q, d.runs[2].testWriterTaskID)
	mustComplete(t, d.Q, tw.ID, "pool test source")

	v, err := d.Tick(context.Background(), 2)
	if err != nil {
		t.Fatalf("Tick (pool-adequacy): %v", err)
	}
	if v != nil {
		t.Fatalf("expected no verdict yet (test-critic not done), got %+v", v)
	}
	if len(scorer.calls) != 2 {
		t.Fatalf("expected 2 Scorer.Score calls (dev, pool), got %d", len(scorer.calls))
	}
	if scorer.calls[1].test != "pool test source" {
		t.Fatalf("pool score call used test %q, want the test-writer's result", scorer.calls[1].test)
	}
	run := d.runs[2]
	if !run.poolScored {
		t.Fatal("expected poolScored=true")
	}
	if run.provenMissed != 1 { // 2 dev-survivors, pool killed 1 (only m2 still survives)
		t.Fatalf("ProvenMissed = %d, want 1", run.provenMissed)
	}
}

// completeFullRun drives test-critic, mutant-generator, and test-writer to
// done and returns the final Verdict. mutant-generator and test-critic are
// both ready from the start, so they're claimed in one sweep; test-critic is
// completed first so the aggregate step's second precondition is already met
// by the time test-writer's completion satisfies the first.
func completeFullRun(t *testing.T, d *Driver, missionID int64, criticResult string) *Verdict {
	t.Helper()
	ctx := context.Background()

	ready := claimAllReady(t, d.Q)
	tc, mg := ready[RoleTestCritic], ready[RoleMutantGenerator]
	if tc == nil || mg == nil {
		t.Fatalf("expected test-critic and mutant-generator both ready, got: %v", keysOf(ready))
	}
	mustComplete(t, d.Q, tc.ID, criticResult)
	mustComplete(t, d.Q, mg.ID, "raw mutants")
	if _, err := d.Tick(ctx, missionID); err != nil {
		t.Fatalf("Tick (dev-adequacy): %v", err)
	}

	tw := claimTaskByID(t, d.Q, d.runs[missionID].testWriterTaskID)
	mustComplete(t, d.Q, tw.ID, "pool test source")
	v, err := d.Tick(ctx, missionID)
	if err != nil {
		t.Fatalf("Tick (pool-adequacy + aggregate): %v", err)
	}
	if v == nil {
		t.Fatal("expected a verdict once test-critic + pool-adequacy are both done")
	}
	return v
}

// (c) test-critic + pool-adequacy done, no blocking finding, DevKillRate
// above threshold -> certified.
func TestTick_Aggregate_Certified(t *testing.T) {
	survivors := []adequacy.Mutant{{ID: "m1", Code: "c1"}}
	scorer := &fakeScorer{devKillRate: 0.9, devSurvivors: survivors, poolSurvivors: nil}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m0", Code: "c0"}, survivors[0]}}
	d, _ := newTestDriver(t, 3, scorer, validator, 0.5)

	v := completeFullRun(t, d, 3, "no vacuous tests found")

	if v.Status != StatusCertified {
		t.Fatalf("Status = %q, want %q", v.Status, StatusCertified)
	}
	if v.DevKillRate != 0.9 {
		t.Fatalf("DevKillRate = %v, want 0.9", v.DevKillRate)
	}
	if v.ProvenMissed != 1 { // pool killed the one dev-survivor
		t.Fatalf("ProvenMissed = %d, want 1", v.ProvenMissed)
	}
}

// (d) a blocking finding open -> needs-review, even with a high DevKillRate.
func TestTick_Aggregate_BlockingFinding_NeedsReview(t *testing.T) {
	survivors := []adequacy.Mutant{{ID: "m1", Code: "c1"}}
	scorer := &fakeScorer{devKillRate: 0.95, devSurvivors: survivors, poolSurvivors: nil}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m0", Code: "c0"}, survivors[0]}}
	d, _ := newTestDriver(t, 4, scorer, validator, 0.5)

	ctx := context.Background()

	// mutant-generator and test-critic are both ready from the start; claim
	// both in one sweep (claiming one at a time by key can't compose here —
	// see claimAllReady), file the blocking finding against test-critic, then
	// complete both.
	ready := claimAllReady(t, d.Q)
	tc, mg := ready[RoleTestCritic], ready[RoleMutantGenerator]
	if tc == nil || mg == nil {
		t.Fatalf("expected test-critic and mutant-generator both ready, got: %v", keysOf(ready))
	}
	if _, err := d.Q.AddFinding(queue.Finding{
		MissionID: 4, TaskID: tc.ID, Reporter: "test-critic", Type: "bug",
		Severity: "high", Target: "TestAlwaysPasses", Evidence: "vacuous — no assertions",
	}); err != nil {
		t.Fatalf("AddFinding: %v", err)
	}
	mustComplete(t, d.Q, tc.ID, "flagged TestAlwaysPasses as vacuous")
	mustComplete(t, d.Q, mg.ID, "raw mutants")
	if _, err := d.Tick(ctx, 4); err != nil {
		t.Fatalf("Tick (dev-adequacy): %v", err)
	}
	tw := claimTaskByID(t, d.Q, d.runs[4].testWriterTaskID)
	mustComplete(t, d.Q, tw.ID, "pool test source")

	v, err := d.Tick(ctx, 4)
	if err != nil {
		t.Fatalf("Tick (pool-adequacy + aggregate): %v", err)
	}
	if v == nil {
		t.Fatal("expected a verdict")
	}
	if v.Status != StatusNeedsReview {
		t.Fatalf("Status = %q, want %q (blocking finding open)", v.Status, StatusNeedsReview)
	}
	if len(v.VacuousFindings) != 1 {
		t.Fatalf("VacuousFindings = %d, want 1", len(v.VacuousFindings))
	}
}

// (e) DevKillRate below threshold -> needs-review, even with no findings.
func TestTick_Aggregate_BelowThreshold_NeedsReview(t *testing.T) {
	survivors := []adequacy.Mutant{{ID: "m1", Code: "c1"}, {ID: "m2", Code: "c2"}}
	scorer := &fakeScorer{devKillRate: 0.2, devSurvivors: survivors, poolSurvivors: nil}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m0", Code: "c0"}, survivors[0], survivors[1]}}
	d, _ := newTestDriver(t, 5, scorer, validator, 0.8)

	v := completeFullRun(t, d, 5, "no vacuous tests found")

	if v.Status != StatusNeedsReview {
		t.Fatalf("Status = %q, want %q (DevKillRate 0.2 < threshold 0.8)", v.Status, StatusNeedsReview)
	}
}

// (f) decorrelation: an assignment where test-critic and test-writer share a
// model must be rejected at driver construction.
func TestNewDriver_RejectsDecorrelatedAssignment(t *testing.T) {
	q := newTestQueue(t)
	scorer := &fakeScorer{}
	validator := &fakeValidator{}
	assign := RoleAssignment{
		RoleMutantGenerator: "model-gen",
		RoleTestWriter:      "same-model",
		RoleTestCritic:      "same-model",
	}
	d, err := NewDriver(q, scorer, validator, assign, 0.5)
	if err == nil {
		t.Fatal("expected an error for test-critic == test-writer model, got nil")
	}
	if d != nil {
		t.Fatal("expected a nil Driver on decorrelation rejection")
	}
}

func TestCheckDecorrelation(t *testing.T) {
	if err := CheckDecorrelation(decorrelatedAssign()); err != nil {
		t.Fatalf("expected a properly decorrelated assignment to pass, got %v", err)
	}
	same := RoleAssignment{RoleTestWriter: "x", RoleTestCritic: "x"}
	if err := CheckDecorrelation(same); err == nil {
		t.Fatal("expected an error when test-writer and test-critic share a model")
	}
	// Both empty (no assignment yet) must not false-positive.
	if err := CheckDecorrelation(RoleAssignment{}); err != nil {
		t.Fatalf("expected an empty assignment to pass, got %v", err)
	}
}
