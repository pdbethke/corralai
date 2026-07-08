// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/sandbox"
)

func TestTaskToolsOverMCP(t *testing.T) {
	dir := t.TempDir()
	cstore, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cstore.Close() })
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })

	// A mission's worth of ready work: one builder task.
	if err := q.Enqueue(1, []queue.TaskSpec{{Key: "build#1", Role: "builder", Title: "build", Instruction: "do it"}}); err != nil {
		t.Fatal(err)
	}
	q.PromoteReady(1)

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() { _ = NewServer(cstore, nil, Options{Queue: q}).Run(ctx, serverT) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "bee", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()

	// claim_task → returns the ready task, marked claimed by the bee.
	var claimed claimTaskOut
	callTask(t, sess, "claim_task", map[string]any{"name": "Ada", "roles": []string{"builder"}}, &claimed)
	if claimed.Task == nil || claimed.Task.Key != "build#1" {
		t.Fatalf("claim_task returned %+v, want build#1", claimed.Task)
	}
	if claimed.Task.ClaimedBy != "Ada" || claimed.Task.Status != queue.StatusClaimed {
		t.Fatalf("claimed task = %+v, want claimed by Ada", claimed.Task)
	}
	id := claimed.Task.ID

	// A second claim finds nothing ready (the only task is taken).
	var empty claimTaskOut
	callTask(t, sess, "claim_task", map[string]any{"name": "Bob", "roles": []string{"builder"}}, &empty)
	if empty.Task != nil {
		t.Fatalf("second claim_task got %+v, want null (nothing ready)", empty.Task)
	}

	// A non-claimer cannot complete it.
	var bad completeTaskOut
	callTask(t, sess, "complete_task", map[string]any{"id": id, "name": "Bob", "result": "x"}, &bad)
	if bad.OK {
		t.Fatal("non-claimer completed the task")
	}

	// The claimer completes it.
	var ok completeTaskOut
	callTask(t, sess, "complete_task", map[string]any{"id": id, "name": "Ada", "result": "built"}, &ok)
	if !ok.OK {
		t.Fatal("claimer could not complete its task")
	}

	// list_tasks reflects the done task; status filter works.
	var listed listTasksOut
	callTask(t, sess, "list_tasks", map[string]any{"mission_id": 1, "status": "done"}, &listed)
	if len(listed.Tasks) != 1 || listed.Tasks[0].ID != id {
		t.Fatalf("list_tasks(done) = %+v, want the one done task", listed.Tasks)
	}
}

func TestVerificationGate(t *testing.T) {
	dir := t.TempDir()
	cstore, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cstore.Close() })
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })

	if err := q.Enqueue(9, []queue.TaskSpec{{Key: "test#1", Role: "tester", Title: "test", Instruction: "verify", Verify: "go test"}}); err != nil {
		t.Fatal(err)
	}
	// A historical pass before this task's claim must NOT satisfy the gate.
	if err := q.RecordExecution(queue.Execution{MissionID: 9, Agent: "Tess", Command: "go test ./...", ExitCode: 0, OK: true, TS: 1}); err != nil {
		t.Fatalf("RecordExecution(pre-claim): %v", err)
	}
	q.PromoteReady(9)
	ct, err := q.ClaimNext("Tess", []string{"tester"}, 300)
	if err != nil || ct == nil {
		t.Fatalf("ClaimNext: %v %v", ct, err)
	}
	// After claim, latest verify fails — gate must refuse.
	if err := q.RecordExecution(queue.Execution{MissionID: 9, Agent: "Tess", Command: "go test ./...", ExitCode: 1, OK: false, TS: int64(ct.ClaimedTS) + 1}); err != nil {
		t.Fatalf("RecordExecution(post-claim fail): %v", err)
	}

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() { _ = NewServer(cstore, nil, Options{Queue: q}).Run(ctx, serverT) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "bee", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()

	// (a) No passing `go test` in current claim window → completion REFUSED + a regression finding.
	var out completeTaskOut
	callTask(t, sess, "complete_task", map[string]any{"name": "Tess", "id": ct.ID, "result": "looks fine"}, &out)
	if out.OK {
		t.Fatal("gate must refuse: no passing verify on record")
	}
	if out.Message == "" {
		t.Fatal("refusal must explain why")
	}
	if tk, _ := q.TaskByID(ct.ID); tk.Status == queue.StatusDone {
		t.Fatal("task must NOT be done")
	}
	fs, _ := q.Findings(9, queue.FindingOpen)
	if len(fs) != 1 || fs[0].Type != "regression" || fs[0].Reporter != "verify-gate" {
		t.Fatalf("expected one verify-gate regression finding, got %+v", fs)
	}

	// (b) Record a passing `go test` after the claim, then completion SUCCEEDS.
	if err := q.RecordExecution(queue.Execution{MissionID: 9, Agent: "Tess", Command: "go test ./...", ExitCode: 0, OK: true, TS: int64(ct.ClaimedTS) + 2}); err != nil {
		t.Fatalf("RecordExecution: %v", err)
	}
	callTask(t, sess, "complete_task", map[string]any{"name": "Tess", "id": ct.ID, "result": "tested, green"}, &out)
	if !out.OK {
		t.Fatalf("with a passing verify the task must complete: %+v", out)
	}
}

// TestVerifyGateRunsCommandNotSelfReport is the Workstream-A guarantee: the gate
// must certify on the brain's OWN independent run of the verify command, not on
// an exit code the worker self-reports. A builder that claims "it passed" while
// the command actually fails must be refused — a judge may not certify herself.
func TestVerifyGateRunsCommandNotSelfReport(t *testing.T) {
	dir := t.TempDir()
	cstore, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cstore.Close() })
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })

	const mid = 42
	// The brain owns the working copy; the verify command runs with it as cwd.
	ws := t.TempDir()
	if err := os.MkdirAll(mission.MissionDir(ws, mid), 0o755); err != nil {
		t.Fatal(err)
	}

	// A task whose verify command REALLY fails when actually executed.
	if err := q.Enqueue(mid, []queue.TaskSpec{{Key: "build#1", Role: "builder", Title: "build", Instruction: "do it", Verify: "exit 1"}}); err != nil {
		t.Fatal(err)
	}
	q.PromoteReady(mid)
	ct, err := q.ClaimNext("Ada", []string{"builder"}, 300)
	if err != nil || ct == nil {
		t.Fatalf("ClaimNext: %v %v", ct, err)
	}

	// The worker LIES: it records the verify command as a clean pass, post-claim.
	if err := q.RecordExecution(queue.Execution{MissionID: mid, Agent: "Ada", Command: "exit 1", ExitCode: 0, OK: true, TS: int64(ct.ClaimedTS) + 1}); err != nil {
		t.Fatal(err)
	}

	// The independent verifier: the brain runs the command in a jail (none backend
	// = raw sh; no bwrap needed on the test host) and reads the TRUE exit code.
	iso, err := sandbox.Resolve(sandbox.Config{Backend: "none", UnsafeHost: true})
	if err != nil {
		t.Fatal(err)
	}
	verify := func(ctx context.Context, wd, command string) (bool, string) {
		r := sandbox.Run(ctx, command, sandbox.Options{Workspace: wd, Backend: iso})
		return r.ExitCode == 0, r.Output
	}

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() {
		_ = NewServer(cstore, nil, Options{Queue: q, Workspace: ws, Verify: verify}).Run(ctx, serverT)
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "bee", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()

	var out completeTaskOut
	callTask(t, sess, "complete_task", map[string]any{"name": "Ada", "id": ct.ID, "result": "trust me, it's green"}, &out)
	if out.OK {
		t.Fatal("gate certified on the worker's self-reported exit code; the brain must run verify itself and refuse")
	}
	if tk, _ := q.TaskByID(ct.ID); tk.Status == queue.StatusDone {
		t.Fatal("task must NOT be done when the brain's own verify run fails")
	}
}

func callTask(t *testing.T, sess *mcp.ClientSession, name string, args map[string]any, out any) {
	t.Helper()
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("%s: tool error: %v", name, res.Content)
	}
	decode(t, res, out)
}
