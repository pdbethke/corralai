// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/memory"
	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/principals"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/sandbox"
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
func (stubValidator) ParseTest(raw string) string                         { return raw }

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

	assign := advPoolAssign(staffing, nil)
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
	assign := advPoolAssign(nil, nil)
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
	assign2 := advPoolAssign(staffing, nil)
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
	assign3 := advPoolAssign(staffing3, nil)
	if assign3[advpool.RoleTestWriter] != "writer-model" || assign3[advpool.RoleTestCritic] != "critic-model" {
		t.Fatalf("expected leaderboard-earned writer/critic models, got %+v", assign3)
	}
}

// TestAdvPoolAssign_SkipsUnknownModel proves an UNATTRIBUTED leaderboard entry
// (the "(unknown model)" sentinel) never becomes a routing target — a worker
// cannot run a model called "(unknown model)". The env/default model stands
// instead. Regression: a hung run left mutant-generator/test-writer completions
// attributed to unknownModel and advPoolAssign routed to it, stamping
// "(unknown model)" onto the next run's tasks.
func TestAdvPoolAssign_SkipsUnknownModel(t *testing.T) {
	base := advpool.RoleAssignment{
		advpool.RoleMutantGenerator: "gemini-flash-latest",
		advpool.RoleTestWriter:      "gemini-flash-latest",
		advpool.RoleTestCritic:      "gemini-pro-latest",
	}
	staffing := &mission.StaffingManager{Perf: fakePerf{stats: []mission.ModelStats{
		{Model: unknownModel, Role: advpool.RoleMutantGenerator, TasksCompleted: 99, ExecPassRatePct: 100},
		{Model: unknownModel, Role: advpool.RoleTestWriter, TasksCompleted: 99, ExecPassRatePct: 100},
	}}}
	got := advPoolAssign(staffing, base)
	if got[advpool.RoleMutantGenerator] != "gemini-flash-latest" || got[advpool.RoleTestWriter] != "gemini-flash-latest" {
		t.Fatalf("unknown-model entries must fall back to the env model, got %+v", got)
	}
	if err := advpool.CheckDecorrelation(got); err != nil {
		t.Fatalf("must stay decorrelated: %v (%+v)", err, got)
	}
	// Guard against over-filtering: a genuinely routable leaderboard model still wins.
	staffing2 := &mission.StaffingManager{Perf: fakePerf{stats: []mission.ModelStats{
		{Model: "real-model", Role: advpool.RoleMutantGenerator, TasksCompleted: 99, ExecPassRatePct: 100},
	}}}
	if got2 := advPoolAssign(staffing2, base); got2[advpool.RoleMutantGenerator] != "real-model" {
		t.Fatalf("a routable leaderboard model must still win: %+v", got2)
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

// TestAdvPoolRuntimeRunStatusDelegates proves AdvPoolRuntime.RunStatus is a
// thin delegate to the driver's own RunStatus: a known, started (but not yet
// converged) run reports found=true/converged=false, and an unknown id
// reports found=false. Full-convergence behavior is already covered by
// advpool's own TestRunStatusUnknownRunningConverged; this only needs to
// prove the runtime forwards to the SAME driver instance StartRun used.
func TestAdvPoolRuntimeRunStatusDelegates(t *testing.T) {
	rt, _ := newTestAdvPoolRuntime(t, nil)

	runID, err := rt.StartRun(testRunSpecIn())
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	st, found := rt.RunStatus(runID)
	if !found {
		t.Fatalf("RunStatus(%d): found=false, want true for a just-started run", runID)
	}
	if st.Converged {
		t.Fatalf("RunStatus(%d): Converged=true, want false before any Tick", runID)
	}

	if _, found := rt.RunStatus(999); found {
		t.Fatal("RunStatus(999): found=true, want false for an unknown id")
	}
}

// TestGetAdversarialRunToolRequiresAdmin proves get_adversarial_run is
// isHumanAdmin-gated exactly like start_adversarial_run (mirrors
// TestStartAdversarialRunTool_RequiresAdmin's setup verbatim).
func TestGetAdversarialRunToolRequiresAdmin(t *testing.T) {
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

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "get_adversarial_run", Arguments: map[string]any{
		"run_id": int64(1),
	}})
	if err != nil {
		t.Fatalf("get_adversarial_run non-admin call: %v", err)
	}
	if !res.IsError {
		t.Fatal("want tool error for non-admin get_adversarial_run, got success")
	}
}

// advPoolJailSkipUnlessGoWorks resolves a real bwrap backend and skips the
// caller's test unless the `go` toolchain is actually reachable INSIDE the
// jail — bwrap only binds /usr into the sandbox (see
// internal/sandbox/isolator_linux.go), so a host where `go` is installed
// outside /usr (e.g. a snap-packaged go, common on Ubuntu dev boxes: `go` at
// /snap/bin -> /usr/bin/snap) has a working bwrap backend that can still
// never actually run `go` — a plain "no backend, skip" check would silently
// pass over that case instead of skipping it. Returns the resolved backend,
// or skips via t.Skip.
func advPoolJailSkipUnlessGoWorks(t *testing.T) sandbox.Isolator {
	t.Helper()
	backend, err := sandbox.Resolve(sandbox.Config{})
	if err != nil || backend == nil {
		t.Skip("no sandbox backend available (bwrap) — needs the real jail to exercise go vet")
	}
	jail := adequacy.NewJail(backend, 30*time.Second)
	pass, rerr := jail.RunTest(context.Background(), nil, []string{"go", "version"})
	if rerr != nil || !pass {
		t.Skipf("go toolchain not reachable inside the bwrap jail on this host (rerr=%v pass=%v) — likely a snap-packaged go outside /usr", rerr, pass)
	}
	return backend
}

// TestAdvPoolValidatorCompileTest_SubdirectoryCodePath is I-1's regression
// test: a SUBDIRECTORY code_path (e.g. internal/auth/login.go, the common
// case — control-plane targets are rarely at the module root) must not have
// its compiling test wrongly rejected. Before the fix, CompileTest ran
// `go vet ./` (module root, non-recursive) while the candidate files landed
// under internal/auth/ — the root package has no .go files, so vet always
// errored and every authored test was rejected regardless of whether it
// actually compiled, wedging the run forever. This exercises the REAL
// advpoolValidator over a real jail (never the stub — the stub is exactly
// what hid this bug in the other tests in this file).
func TestAdvPoolValidatorCompileTest_SubdirectoryCodePath(t *testing.T) {
	backend := advPoolJailSkipUnlessGoWorks(t)
	jail := adequacy.NewJail(backend, 30*time.Second)
	v := advpoolValidator{jail: jail}

	code := "package auth\n\nfunc ValidatePassword(pw string) error { return nil }\n"
	test := "package auth\n\nimport \"testing\"\n\nfunc TestValidatePassword(t *testing.T) {\n\tif err := ValidatePassword(\"x\"); err != nil {\n\t\tt.Fatal(err)\n\t}\n}\n"

	if err := v.CompileTest(context.Background(), "internal/auth/login.go", code, test); err != nil {
		t.Fatalf("CompileTest rejected a compiling test under a subdirectory code_path: %v", err)
	}
}

// TestAdvPoolValidatorCompileTest_SubdirectoryNonCompilingTest proves the
// fix didn't just widen the check into a rubber stamp: a genuinely
// non-compiling test under the SAME subdirectory code_path must still be
// rejected.
func TestAdvPoolValidatorCompileTest_SubdirectoryNonCompilingTest(t *testing.T) {
	backend := advPoolJailSkipUnlessGoWorks(t)
	jail := adequacy.NewJail(backend, 30*time.Second)
	v := advpoolValidator{jail: jail}

	code := "package auth\n\nfunc ValidatePassword(pw string) error { return nil }\n"
	badTest := "package auth\n\nimport \"testing\"\n\nfunc TestValidatePassword(t *testing.T) {\n\tValidatePassword(123)\n}\n" // wrong arg type

	if err := v.CompileTest(context.Background(), "internal/auth/login.go", code, badTest); err == nil {
		t.Fatal("expected CompileTest to reject a non-compiling test, got nil error")
	}
}

func TestParseAdvPoolModels(t *testing.T) {
	// Happy path: all three roles, decorrelated.
	got, err := parseAdvPoolModels("mutant-generator=anthropic/claude-sonnet-4-6,test-writer=anthropic/claude-sonnet-4-6,test-critic=google/gemini-2.5-flash")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got[advpool.RoleMutantGenerator] != "anthropic/claude-sonnet-4-6" ||
		got[advpool.RoleTestWriter] != "anthropic/claude-sonnet-4-6" ||
		got[advpool.RoleTestCritic] != "google/gemini-2.5-flash" {
		t.Fatalf("wrong assignment: %+v", got)
	}

	// Whitespace tolerated.
	if _, err := parseAdvPoolModels(" mutant-generator = a , test-writer = b , test-critic = c "); err != nil {
		t.Fatalf("whitespace form should parse: %v", err)
	}

	// Decorrelation violation (critic == writer) → error.
	if _, err := parseAdvPoolModels("mutant-generator=a,test-writer=b,test-critic=b"); err == nil {
		t.Fatalf("critic==writer must be rejected")
	}

	// Missing a role → error.
	if _, err := parseAdvPoolModels("mutant-generator=a,test-writer=b"); err == nil {
		t.Fatalf("missing test-critic must be rejected")
	}

	// Unknown role key → error.
	if _, err := parseAdvPoolModels("mutant-generator=a,test-writer=b,test-critic=c,pentester=d"); err == nil {
		t.Fatalf("unknown role must be rejected")
	}

	// Empty value → error.
	if _, err := parseAdvPoolModels("mutant-generator=,test-writer=b,test-critic=c"); err == nil {
		t.Fatalf("empty model must be rejected")
	}

	// Empty string → (nil, nil): "unset", caller uses hardcoded defaults.
	got, err = parseAdvPoolModels("")
	if err != nil || got != nil {
		t.Fatalf("empty string should be (nil,nil), got (%v,%v)", got, err)
	}
}

func TestAdvPoolAssignUsesDefaults_UnsetIdenticalToToday(t *testing.T) {
	// nil defaults → the hardcoded qwen/llama assignment (no behavior change).
	got := advPoolAssign(nil, nil)
	if got[advpool.RoleMutantGenerator] != defaultAdvPoolModel ||
		got[advpool.RoleTestWriter] != defaultAdvPoolModel ||
		got[advpool.RoleTestCritic] != defaultAdvPoolCriticModel {
		t.Fatalf("nil defaults must reproduce today's assignment, got %+v", got)
	}

	// Provided defaults (no leaderboard staffing) → those models, decorrelation intact.
	base := advpool.RoleAssignment{
		advpool.RoleMutantGenerator: "anthropic/claude-sonnet-4-6",
		advpool.RoleTestWriter:      "anthropic/claude-sonnet-4-6",
		advpool.RoleTestCritic:      "google/gemini-2.5-flash",
	}
	got = advPoolAssign(nil, base)
	if got[advpool.RoleTestWriter] != "anthropic/claude-sonnet-4-6" || got[advpool.RoleTestCritic] != "google/gemini-2.5-flash" {
		t.Fatalf("provided defaults not used: %+v", got)
	}
	if err := advpool.CheckDecorrelation(got); err != nil {
		t.Fatalf("assignment must stay decorrelated: %v", err)
	}
}
