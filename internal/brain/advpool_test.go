// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/memory"
	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/principals"
	"github.com/pdbethke/corralai/internal/queue"
)

// stubScorer/stubValidator satisfy advpool.Scorer/advpool.Validator with
// no-op bodies: these tests exercise start_adversarial_run's admin gate and
// DAG-enqueue/decorrelation behavior, never Tick, so the scoring/validation
// path itself is irrelevant here (the pure driver's own tests, in
// internal/advpool, cover Tick's state machine against these interfaces).
type stubScorer struct{}

func (stubScorer) Score(_ context.Context, _, _, _ string, _ []adequacy.Mutant, _ string) (float64, []adequacy.Mutant, error) {
	return 0, nil, nil
}

type stubValidator struct{}

func (stubValidator) CompileTest(_ context.Context, _, _, _ string) error { return nil }
func (stubValidator) ParseMutants(_ string) ([]adequacy.Mutant, error)    { return nil, nil }

// newTestAdvPoolRuntime wires an AdvPoolRuntime over fresh queue/mission
// stores with stub Scorer/Validator — enough to exercise StartRun's
// admin-gate/enqueue/decorrelation behavior without a real sandbox jail.
func newTestAdvPoolRuntime(t *testing.T, staffing *mission.StaffingManager) (*AdvPoolRuntime, *queue.Store) {
	t.Helper()
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = q.Close() })
	ms, err := mission.Open(filepath.Join(dir, "m.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ms.Close() })

	assign := advPoolAssign(staffing)
	driver, err := advpool.NewDriver(q, stubScorer{}, stubValidator{}, assign, 0.8)
	if err != nil {
		t.Fatal(err)
	}
	rt := &AdvPoolRuntime{driver: driver, missions: ms, staffing: staffing, tickErrors: map[int64]int{}}
	return rt, q
}

func testRunSpecIn() AdvPoolRunSpec {
	return AdvPoolRunSpec{
		Repo:        "example/repo",
		Commit:      "deadbeef",
		Goal:        "passwords >= 12 chars",
		CodePath:    "target.go",
		Code:        "package target\nfunc ValidatePassword(pw string) error { return nil }",
		DevTestPath: "target_test.go",
		DevTestCode: "package target\nfunc TestAlwaysPasses(t *testing.T) {}",
		TestCmd:     "go test ./...",
		NMutants:    3,
	}
}

// TestAdvPoolAssign_AlwaysDecorrelated proves advPoolAssign can never
// produce an assignment advpool.CheckDecorrelation rejects, with or without
// leaderboard evidence — this is the guarantee StartRun leans on instead of
// re-deriving decorrelation logic itself.
func TestAdvPoolAssign_AlwaysDecorrelated(t *testing.T) {
	// Cold start: no staffing at all.
	assign := advPoolAssign(nil)
	if err := advpool.CheckDecorrelation(assign); err != nil {
		t.Fatalf("cold-start assignment must be decorrelated: %v (%+v)", err, assign)
	}
	if assign[advpool.RoleTestCritic] == assign[advpool.RoleTestWriter] {
		t.Fatalf("test-critic must differ from test-writer, got %+v", assign)
	}

	// A leaderboard where every role's best-earned model is the SAME model
	// (a real scenario: one model dominates every cell) must still force
	// test-critic onto something else.
	staffing := &mission.StaffingManager{Perf: fakePerf{stats: []mission.ModelStats{
		{Model: "same-model", Role: advpool.RoleMutantGenerator, TasksCompleted: 10, ExecPassRatePct: 99},
		{Model: "same-model", Role: advpool.RoleTestWriter, TasksCompleted: 10, ExecPassRatePct: 99},
		{Model: "same-model", Role: advpool.RoleTestCritic, TasksCompleted: 10, ExecPassRatePct: 99},
	}}}
	assign2 := advPoolAssign(staffing)
	if err := advpool.CheckDecorrelation(assign2); err != nil {
		t.Fatalf("single-dominant-model leaderboard must still decorrelate: %v (%+v)", err, assign2)
	}
	if assign2[advpool.RoleTestWriter] != "same-model" {
		t.Fatalf("test-writer should take the dominant model, got %+v", assign2)
	}
	if assign2[advpool.RoleTestCritic] == "same-model" {
		t.Fatalf("test-critic must NOT take the writer's dominant model, got %+v", assign2)
	}

	// A leaderboard with a genuinely BETTER, distinct critic candidate must
	// be preferred over the static fallback.
	staffing3 := &mission.StaffingManager{Perf: fakePerf{stats: []mission.ModelStats{
		{Model: "writer-model", Role: advpool.RoleTestWriter, TasksCompleted: 10, ExecPassRatePct: 99},
		{Model: "critic-model", Role: advpool.RoleTestCritic, TasksCompleted: 10, ExecPassRatePct: 95},
	}}}
	assign3 := advPoolAssign(staffing3)
	if assign3[advpool.RoleTestWriter] != "writer-model" || assign3[advpool.RoleTestCritic] != "critic-model" {
		t.Fatalf("expected leaderboard-earned writer/critic models, got %+v", assign3)
	}
}

type fakePerf struct{ stats []mission.ModelStats }

func (f fakePerf) GetRoleModelStats() []mission.ModelStats { return f.stats }

// TestAdvPoolStartRun_EnqueuesDecorrelatedDAG proves StartRun enqueues the
// run's three-role DAG (mutant-generator + test-writer + test-critic),
// stamped with models, and that test-critic's stamped model differs from
// test-writer's — the decorrelation guarantee at the actual enqueue seam,
// not just at advPoolAssign in isolation.
func TestAdvPoolStartRun_EnqueuesDecorrelatedDAG(t *testing.T) {
	rt, q := newTestAdvPoolRuntime(t, nil)

	runID, err := rt.StartRun(testRunSpecIn())
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if runID == 0 {
		t.Fatal("expected a non-zero run/mission id")
	}

	tasks, err := q.List(runID)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]queue.Task{}
	for _, tk := range tasks {
		byKey[tk.Key] = tk
	}
	mg, ok := byKey[advpool.RoleMutantGenerator]
	if !ok || mg.Model == "" {
		t.Fatalf("expected a stamped mutant-generator task, got %+v", byKey)
	}
	tw, ok := byKey[advpool.RoleTestWriter]
	if !ok || tw.Model == "" {
		t.Fatalf("expected a stamped test-writer task, got %+v", byKey)
	}
	tc, ok := byKey[advpool.RoleTestCritic]
	if !ok || tc.Model == "" {
		t.Fatalf("expected a stamped test-critic task, got %+v", byKey)
	}
	if tc.Model == tw.Model {
		t.Fatalf("decorrelation violated: test-critic model %q == test-writer model %q", tc.Model, tw.Model)
	}

	// A second run while one is active must be refused (single active run).
	if _, err := rt.StartRun(testRunSpecIn()); err == nil {
		t.Fatal("expected StartRun to refuse a second concurrent run")
	}
}

// TestStartAdversarialRunTool_RequiresAdmin proves start_adversarial_run is
// isHumanAdmin-gated exactly like promote_control/stage_control: a caller
// authenticated as a non-superuser (here, simply unauthenticated against a
// Principals store that HAS a real superuser configured, mirroring
// TestPromoteControlRequiresAdmin) is refused a tool error, never a run.
func TestStartAdversarialRunTool_RequiresAdmin(t *testing.T) {
	dir := t.TempDir()
	cstore, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cstore.Close() })
	mstore, err := memory.Open(filepath.Join(dir, "m.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mstore.Close() })
	pstore, err := principals.Open(filepath.Join(dir, "p.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pstore.Close() })
	if err := pstore.CreateSuperuser("real-admin@example.com", "test"); err != nil {
		t.Fatal(err)
	}

	rt, _ := newTestAdvPoolRuntime(t, nil)

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() {
		_ = NewServer(cstore, mstore, Options{Principals: pstore, AdvPool: rt}).Run(ctx, serverT)
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "t1", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect non-admin: %v", err)
	}
	defer sess.Close()

	in := testRunSpecIn()
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "start_adversarial_run", Arguments: map[string]any{
		"repo": in.Repo, "commit": in.Commit, "goal": in.Goal,
		"code_path": in.CodePath, "code": in.Code,
		"dev_test_path": in.DevTestPath, "dev_test_code": in.DevTestCode,
		"test_cmd": in.TestCmd, "n_mutants": in.NMutants,
	}})
	if err != nil {
		t.Fatalf("start_adversarial_run non-admin call: %v", err)
	}
	if !res.IsError {
		t.Fatal("want tool error for non-admin start_adversarial_run, got success")
	}
}
