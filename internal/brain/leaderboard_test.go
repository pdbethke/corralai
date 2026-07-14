// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/telemetry"
)

// setupLeaderboardFixture seeds a queue store with two missions spanning two
// models (gemini, claude) across two roles (builder, tester): each agent
// completes a task, runs an execution, and files a finding, so the matrix has
// a real cross product to compute over. It also seeds a HostBook with each
// agent's model and a telemetry store with one task_reissued rework event.
func setupLeaderboardFixture(t *testing.T) (*queue.Store, *HostBook, *telemetry.Store) {
	t.Helper()
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })

	book := NewHostBook()
	book.Set(Host{Agent: "Nova", Role: "builder", Model: "gemini-3.1-pro", Backend: "gemini", TS: time.Now().Unix()})
	book.Set(Host{Agent: "Comet", Role: "tester", Model: "gemini-3.1-pro", Backend: "gemini", TS: time.Now().Unix()})
	book.Set(Host{Agent: "Rune", Role: "builder", Model: "claude-opus-4", Backend: "anthropic", TS: time.Now().Unix()})
	book.Set(Host{Agent: "Wisp", Role: "tester", Model: "claude-opus-4", Backend: "anthropic", TS: time.Now().Unix()})

	tel, err := telemetry.Open(filepath.Join(dir, "t.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tel.Close() })

	// Mission 1: Nova (gemini/builder) + Comet (gemini/tester).
	if err := q.Enqueue(1, []queue.TaskSpec{
		{Key: "build#1", Role: "builder", Title: "build"},
		{Key: "test#1", Role: "tester", Title: "test"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.PromoteReady(1); err != nil {
		t.Fatal(err)
	}
	completeAs(t, q, "Nova", []string{"builder"})
	cometTask := completeAs(t, q, "Comet", []string{"tester"})

	// Mission 2: Rune (claude/builder) + Wisp (claude/tester).
	if err := q.Enqueue(2, []queue.TaskSpec{
		{Key: "build#2", Role: "builder", Title: "build"},
		{Key: "test#2", Role: "tester", Title: "test"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.PromoteReady(2); err != nil {
		t.Fatal(err)
	}
	completeAs(t, q, "Rune", []string{"builder"})
	wispTask := completeAs(t, q, "Wisp", []string{"tester"})

	// Executions: Nova passes twice, Rune fails once.
	must(t, q.RecordExecution(queue.Execution{MissionID: 1, Agent: "Nova", Role: "builder", Command: "go test ./...", ExitCode: 0, OK: true, TS: time.Now().Unix()}))
	must(t, q.RecordExecution(queue.Execution{MissionID: 1, Agent: "Nova", Role: "builder", Command: "go build ./...", ExitCode: 0, OK: true, TS: time.Now().Unix()}))
	must(t, q.RecordExecution(queue.Execution{MissionID: 2, Agent: "Rune", Role: "builder", Command: "go test ./...", ExitCode: 1, OK: false, TS: time.Now().Unix()}))

	// Findings: Comet (gemini/tester) raises one on her own task, resolved;
	// Wisp (claude/tester) raises one on her own task, still open. TaskID
	// threads the finding to its task so the leaderboard can resolve role.
	if _, err := q.AddFinding(queue.Finding{MissionID: 1, TaskID: cometTask.ID, Reporter: "Comet", ReporterModel: "gemini-3.1-pro", ReporterBackend: "gemini", Type: "bug", Severity: "medium", Target: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.AddFinding(queue.Finding{MissionID: 2, TaskID: wispTask.ID, Reporter: "Wisp", ReporterModel: "claude-opus-4", ReporterBackend: "anthropic", Type: "bug", Severity: "low", Target: "y"}); err != nil {
		t.Fatal(err)
	}
	// Resolve Comet's finding (the only one AddFinding just made for mission 1).
	fs, err := q.Findings(1, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 1 {
		t.Fatalf("expected 1 finding for mission 1, got %d", len(fs))
	}
	if ok, err := q.SetFindingStatus(fs[0].ID, queue.FindingAddressed); err != nil || !ok {
		t.Fatalf("SetFindingStatus: ok=%v err=%v", ok, err)
	}

	// Rework: Rune's build task gets reissued once (lost-reply self-heal).
	if err := tel.Record(telemetry.Event{MissionID: 2, Kind: "task_reissued", Actor: "Rune", Subject: "build#2", Detail: map[string]any{"role": "builder"}}); err != nil {
		t.Fatal(err)
	}

	return q, book, tel
}

func completeAs(t *testing.T, q *queue.Store, bee string, roles []string) *queue.Task {
	t.Helper()
	task, err := q.ClaimNext(bee, roles, 300)
	if err != nil {
		t.Fatal(err)
	}
	if task == nil {
		t.Fatalf("no claimable task for %s", bee)
	}
	time.Sleep(2 * time.Millisecond)
	if ok, err := q.Complete(task.ID, bee, "done"); err != nil || !ok {
		t.Fatalf("complete: ok=%v err=%v", ok, err)
	}
	return task
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func cellFor(t *testing.T, lb Leaderboard, model, role string) LeaderboardCell {
	t.Helper()
	for _, c := range lb.Cells {
		if c.Model == model && c.Role == role {
			return c
		}
	}
	t.Fatalf("no cell for model=%s role=%s (have %d cells)", model, role, len(lb.Cells))
	return LeaderboardCell{}
}

func TestBuildLeaderboardComputesPerCellMetrics(t *testing.T) {
	q, book, tel := setupLeaderboardFixture(t)

	lb, err := BuildLeaderboard(q, book, tel)
	if err != nil {
		t.Fatalf("BuildLeaderboard: %v", err)
	}
	if len(lb.Cells) == 0 {
		t.Fatal("expected a non-empty matrix")
	}

	geminiBuilder := cellFor(t, lb, "gemini-3.1-pro", "builder")
	if geminiBuilder.TasksCompleted != 1 {
		t.Errorf("gemini/builder tasks_completed = %d, want 1", geminiBuilder.TasksCompleted)
	}
	if geminiBuilder.Samples != 1 {
		t.Errorf("gemini/builder samples = %d, want 1", geminiBuilder.Samples)
	}
	if geminiBuilder.Executions != 2 || geminiBuilder.ExecutionsOK != 2 {
		t.Errorf("gemini/builder executions = %d/%d, want 2/2", geminiBuilder.ExecutionsOK, geminiBuilder.Executions)
	}
	if geminiBuilder.ExecPassRatePct != 100 {
		t.Errorf("gemini/builder exec_pass_rate_pct = %v, want 100", geminiBuilder.ExecPassRatePct)
	}
	if geminiBuilder.DurationSamples != 1 || geminiBuilder.AvgTaskDuration <= 0 {
		t.Errorf("gemini/builder duration_samples=%d avg=%v, want 1 and >0", geminiBuilder.DurationSamples, geminiBuilder.AvgTaskDuration)
	}

	claudeBuilder := cellFor(t, lb, "claude-opus-4", "builder")
	if claudeBuilder.TasksCompleted != 1 {
		t.Errorf("claude/builder tasks_completed = %d, want 1", claudeBuilder.TasksCompleted)
	}
	if claudeBuilder.Executions != 1 || claudeBuilder.ExecutionsOK != 0 {
		t.Errorf("claude/builder executions = %d/%d, want 1/0", claudeBuilder.ExecutionsOK, claudeBuilder.Executions)
	}
	if claudeBuilder.ExecPassRatePct != 0 {
		t.Errorf("claude/builder exec_pass_rate_pct = %v, want 0", claudeBuilder.ExecPassRatePct)
	}
	if claudeBuilder.ReworkCount != 1 {
		t.Errorf("claude/builder rework_count = %d, want 1 (the reissued build#2)", claudeBuilder.ReworkCount)
	}

	geminiTester := cellFor(t, lb, "gemini-3.1-pro", "tester")
	if geminiTester.FindingsRaised != 1 || geminiTester.FindingsResolved != 1 {
		t.Errorf("gemini/tester findings = raised %d resolved %d, want 1/1", geminiTester.FindingsRaised, geminiTester.FindingsResolved)
	}

	claudeTester := cellFor(t, lb, "claude-opus-4", "tester")
	if claudeTester.FindingsRaised != 1 || claudeTester.FindingsResolved != 0 {
		t.Errorf("claude/tester findings = raised %d resolved %d, want 1/0 (still open)", claudeTester.FindingsRaised, claudeTester.FindingsResolved)
	}
}

// TestBuildLeaderboardUnattributedAgent covers an agent that completes work
// without ever announcing itself via report_host (no HostBook entry) — model
// attribution must degrade to "(unknown model)" rather than erroring or
// silently dropping the observation.
func TestBuildLeaderboardUnattributedAgent(t *testing.T) {
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })
	if err := q.Enqueue(1, []queue.TaskSpec{{Key: "build#1", Role: "builder", Title: "build"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.PromoteReady(1); err != nil {
		t.Fatal(err)
	}
	completeAs(t, q, "Ghost", []string{"builder"})

	lb, err := BuildLeaderboard(q, NewHostBook(), nil)
	if err != nil {
		t.Fatalf("BuildLeaderboard: %v", err)
	}
	c := cellFor(t, lb, unknownModel, "builder")
	if c.TasksCompleted != 1 {
		t.Errorf("tasks_completed = %d, want 1", c.TasksCompleted)
	}
}

// TestBuildLeaderboardSparseAndEmpty asserts a freshly-opened store (no
// missions run yet) computes cleanly to an empty matrix — corral has few real
// runs yet, and the endpoint must never error on thin data — and that a nil
// queue store (dev mode, no queue configured) degrades the same way.
func TestBuildLeaderboardSparseAndEmpty(t *testing.T) {
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })

	lb, err := BuildLeaderboard(q, nil, nil)
	if err != nil {
		t.Fatalf("BuildLeaderboard on empty store: %v", err)
	}
	if len(lb.Cells) != 0 {
		t.Errorf("expected an empty matrix from an empty store, got %d cells", len(lb.Cells))
	}

	lb2, err := BuildLeaderboard(nil, nil, nil)
	if err != nil {
		t.Fatalf("BuildLeaderboard(nil queue): %v", err)
	}
	if lb2.Cells == nil || len(lb2.Cells) != 0 {
		t.Errorf("expected a non-nil empty Cells slice from a nil queue store, got %#v", lb2.Cells)
	}
}

// TestBuildLeaderboardFoldsAdvPoolOutcomes proves the compounding-routing
// loop is actually closed (M-1): advpoolLeaderboardSink.Record's
// advpool_leaderboard telemetry events (the ONLY thing a certified
// adversarial-pool run leaves behind for a role's fitness) are folded into
// the leaderboard's per-role cell exec pass rate, so a model that keeps
// passing the pool's gate for a role ranks higher on the very next
// mission.StaffingManager.Perf.GetRoleModelStats() call — not just logged
// and forgotten.
func TestBuildLeaderboardFoldsAdvPoolOutcomes(t *testing.T) {
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })
	tel, err := telemetry.Open(filepath.Join(dir, "t.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tel.Close() })

	// Mirrors advpoolLeaderboardSink.Record's rec() call shape: actor=model,
	// subject=role, detail={"outcome": ...}. qwen passes test-writer twice
	// and fails once; llama has no evidence at all for test-writer.
	record := func(model, role, outcome string) {
		must(t, tel.Record(telemetry.Event{Kind: "advpool_leaderboard", Actor: model, Subject: role, Detail: map[string]any{"outcome": outcome}}))
	}
	record("qwen2.5-coder:7b", "test-writer", "pass")
	record("qwen2.5-coder:7b", "test-writer", "pass")
	record("qwen2.5-coder:7b", "test-writer", "fail")
	record("llama3.2:3b", "test-critic", "pass")

	lb, err := BuildLeaderboard(q, nil, tel)
	if err != nil {
		t.Fatalf("BuildLeaderboard: %v", err)
	}

	qwenWriter := cellFor(t, lb, "qwen2.5-coder:7b", "test-writer")
	if qwenWriter.Executions != 3 || qwenWriter.ExecutionsOK != 2 {
		t.Errorf("qwen/test-writer executions = %d/%d, want 3/2", qwenWriter.ExecutionsOK, qwenWriter.Executions)
	}
	wantPct := 200.0 / 3.0
	if diff := qwenWriter.ExecPassRatePct - wantPct; diff < -0.001 || diff > 0.001 {
		t.Errorf("qwen/test-writer exec_pass_rate_pct = %v, want ~%v", qwenWriter.ExecPassRatePct, wantPct)
	}

	llamaCritic := cellFor(t, lb, "llama3.2:3b", "test-critic")
	if llamaCritic.Executions != 1 || llamaCritic.ExecutionsOK != 1 || llamaCritic.ExecPassRatePct != 100 {
		t.Errorf("llama/test-critic = %+v, want 1/1 executions, 100%% pass rate", llamaCritic)
	}

	// The GetRoleModelStats() read path (mirrors cmd/corral/main.go's
	// perfTracker) must expose the SAME signal: this is the thing
	// advPoolAssign actually queries when staffing the next run.
	var stats []mission.ModelStats
	for _, cell := range lb.Cells {
		stats = append(stats, mission.ModelStats{Model: cell.Model, Role: cell.Role, TasksCompleted: cell.TasksCompleted, ExecPassRatePct: cell.ExecPassRatePct})
	}
	best := advPoolBestByRole(stats)
	if best["test-writer"] != "qwen2.5-coder:7b" {
		t.Errorf("best test-writer by role = %q, want qwen2.5-coder:7b (from folded advpool outcomes)", best["test-writer"])
	}
}
