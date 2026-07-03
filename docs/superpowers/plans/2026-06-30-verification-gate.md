# Verification Gate Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A verify task (one carrying a `Verify` command) can't be marked done unless a recorded execution of that command exited 0; otherwise the completion is refused and a `regression` finding is auto-raised, feeding the existing reflex re-planner.

**Architecture:** A durable per-mission executions table in the queue SQLite store records every `report_execution`; the brain's `complete_task` consults it before completing a gated task (deterministic gate). The refusal raises a `regression` finding, which `reflexRules` already turns into a `fix-f<id>` task.

**Tech Stack:** Go 1.26, `modernc.org/sqlite` (pure-Go, the queue store, `MaxOpenConns=1`). No new deps.

## Global Constraints

- The gate is **deterministic and engine-side** — never a prompt or an agent.
- Findings raised by the gate use type `regression` (already valid in `findingTypes`), severity `high`, reporter `verify-gate`.
- Verify matching is **case-sensitive substring**: a recorded execution's `command` must *contain* the task's `Verify` string AND have `exit_code == 0`.
- A task with `Verify == ""` is **ungated** — unchanged behavior.
- The auto-finding is bounded by the existing `ReflexMaxTasks` + `SprintCap` (no new runaway protection needed).
- The queue store is pure-Go; run focused tests per package (`go test ./internal/queue/`, `./internal/brain/`, `./internal/mission/`). Do not run `-race` or the whole repo per task.
- Open a test store with `queue.Open(filepath.Join(t.TempDir(), "q.db"))`.

---

## File Structure

- `internal/queue/executions.go` (new): `Execution`, `RecordExecution`, `MissionPassedVerify`.
- `internal/queue/executions_test.go` (new): tests for the above.
- `internal/queue/store.go` (modify): `executions` table in `schema`; `Verify` on `TaskSpec`/`Task`; `verify` column + migration; `Enqueue` writes it; `taskSelect` + `query` scan it; `ClaimedMission`.
- `internal/queue/store_test.go` (modify): Verify round-trip + `ClaimedMission`.
- `internal/brain/executions.go` (modify): `registerExecutions` persists to the queue store.
- `internal/brain/server.go` (modify): pass `opts.Queue` to `registerExecutions`.
- `internal/brain/tasks.go` (modify): the gate in `complete_task`; `Message` on `completeTaskOut`.
- `internal/brain/tasks_test.go` (modify): gate behavior.
- `internal/mission/store.go` (modify): `Verify` on `PhaseSpec`; `DefaultPlan` sets it; spec-build carries it.
- `internal/mission/store_test.go` (modify): `DefaultPlan` Verify values.

---

## Task 1: Durable executions table + RecordExecution + MissionPassedVerify

**Files:**
- Modify: `internal/queue/store.go` (the `schema` const)
- Create: `internal/queue/executions.go`
- Create: `internal/queue/executions_test.go`

**Interfaces:**
- Produces: `type Execution struct { MissionID int64; Agent, Role, Command string; ExitCode int; OK bool; TS int64 }`; `func (s *Store) RecordExecution(e Execution) error`; `func (s *Store) MissionPassedVerify(missionID int64, verify string) (bool, error)`.

- [ ] **Step 1: Write the failing test** — `internal/queue/executions_test.go`:

```go
package queue

import (
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMissionPassedVerify(t *testing.T) {
	s := openTestStore(t)
	rec := func(mid int64, cmd string, code int) {
		if err := s.RecordExecution(Execution{MissionID: mid, Agent: "Bob", Role: "builder", Command: cmd, ExitCode: code, OK: code == 0, TS: 1}); err != nil {
			t.Fatal(err)
		}
	}
	// mission 1: a failing then a passing `go test`
	rec(1, "cd calc && go test ./...", 1)
	rec(1, "cd calc && go test ./...", 0) // substring "go test" + exit 0
	// mission 2: only a failing build
	rec(2, "go build ./...", 1)

	pass := func(mid int64, v string) bool {
		ok, err := s.MissionPassedVerify(mid, v)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !pass(1, "go test") {
		t.Fatal("mission 1 should have a passing 'go test'")
	}
	if pass(2, "go build") {
		t.Fatal("mission 2's go build only ever failed — must not pass")
	}
	if pass(1, "go vet") {
		t.Fatal("no 'go vet' was ever run — must not pass")
	}
	if pass(2, "go test") {
		t.Fatal("mission 2 never ran 'go test' — must not pass")
	}
}
```

- [ ] **Step 2: Run it — verify it fails to compile**

Run: `go test ./internal/queue/ -run TestMissionPassedVerify`
Expected: FAIL — `undefined: Execution`, `RecordExecution`, `MissionPassedVerify`.

- [ ] **Step 3a: Add the executions table to `schema`** in `internal/queue/store.go`. Immediately before the closing backtick of the `schema` const (after the `ix_findings_status` index line), insert:

```sql
CREATE TABLE IF NOT EXISTS executions (
  id         INTEGER PRIMARY KEY,
  mission_id INTEGER NOT NULL,
  agent      TEXT    NOT NULL DEFAULT '',
  role       TEXT    NOT NULL DEFAULT '',
  command    TEXT    NOT NULL DEFAULT '',
  exit_code  INTEGER NOT NULL DEFAULT 0,
  ok         INTEGER NOT NULL DEFAULT 0,
  ts         INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS ix_executions_mission ON executions(mission_id);
```

- [ ] **Step 3b: Create `internal/queue/executions.go`:**

```go
package queue

// Execution is one shell command a swarm agent ran, recorded durably so the
// verification gate can ask "did <verify> ever pass for this mission?".
type Execution struct {
	MissionID int64
	Agent     string
	Role      string
	Command   string
	ExitCode  int
	OK        bool
	TS        int64 // Unix seconds
}

// RecordExecution durably stores one execution.
func (s *Store) RecordExecution(e Execution) error {
	ok := 0
	if e.OK {
		ok = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO executions (mission_id,agent,role,command,exit_code,ok,ts) VALUES (?,?,?,?,?,?,?)`,
		e.MissionID, e.Agent, e.Role, e.Command, e.ExitCode, ok, e.TS,
	)
	return err
}

// MissionPassedVerify reports whether some execution for missionID ran a command
// CONTAINING verify and exited 0 — the deterministic basis for the gate. An empty
// verify is treated as "ungated" and returns true.
func (s *Store) MissionPassedVerify(missionID int64, verify string) (bool, error) {
	if verify == "" {
		return true, nil
	}
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM executions WHERE mission_id=? AND ok=1 AND instr(command, ?)>0`,
		missionID, verify,
	).Scan(&n)
	return n > 0, err
}
```

- [ ] **Step 4: Run — verify pass**

Run: `go test ./internal/queue/ -run TestMissionPassedVerify -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/queue/executions.go internal/queue/executions_test.go internal/queue/store.go
git commit -m "feat(queue): durable executions table + MissionPassedVerify"
```

---

## Task 2: Verify field on Task/TaskSpec + ClaimedMission

**Files:**
- Modify: `internal/queue/store.go` (`TaskSpec`, `Task`, `schema`, migration, `Enqueue`, `taskSelect`, `query`)
- Create/Modify: `internal/queue/store_test.go`

**Interfaces:**
- Consumes: `Execution` types from Task 1.
- Produces: `TaskSpec.Verify string`, `Task.Verify string`; `func (s *Store) ClaimedMission(bee string) (int64, error)`. `TaskByID(id)` now returns a `Task` whose `Verify` is populated.

- [ ] **Step 1: Write the failing test** — append to `internal/queue/store_test.go` (create the file with `package queue` + imports if it doesn't exist; reuse `openTestStore` from Task 1):

```go
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
```

- [ ] **Step 2: Run — verify it fails**

Run: `go test ./internal/queue/ -run TestVerifyRoundTripAndClaimedMission`
Expected: FAIL — `unknown field Verify in struct literal` / `undefined: ClaimedMission`.

- [ ] **Step 3a: Add `Verify` to the structs** in `internal/queue/store.go`. In `TaskSpec` add after `DependsOn`:

```go
	Verify string // command that MUST pass (exit 0) before this task can complete; "" = ungated
```

In `Task` add after `Supersedes`:

```go
	Verify string `json:"verify,omitempty"`
```

- [ ] **Step 3b: Add the `verify` column.** In the `schema` const's `tasks` table, add a line after `supersedes ...`:

```sql
  verify           TEXT    NOT NULL DEFAULT '',
```

And in `Open`'s idempotent-migration loop, add to the `[]string{...}`:

```go
		`ALTER TABLE tasks ADD COLUMN verify TEXT NOT NULL DEFAULT ''`,
```

- [ ] **Step 3c: Write + read the column.** In `Enqueue`, change the INSERT to include `verify` (it currently inserts `mission_id,key,role,title,instruction,status,depends_on,created_ts`):

```go
		if _, err := tx.Exec(
			`INSERT INTO tasks (mission_id,key,role,title,instruction,status,depends_on,verify,created_ts)
			 VALUES (?,?,?,?,?,?,?,?,?)`,
			missionID, sp.Key, sp.Role, sp.Title, sp.Instruction, StatusPending, string(b), sp.Verify, now(),
		); err != nil {
```

In the `taskSelect` const, add `verify` as the LAST column:

```go
const taskSelect = `SELECT id,mission_id,key,role,title,instruction,status,depends_on,claimed_by,result,created_ts,claimed_ts,done_ts,claim_expires_ts,supersedes,verify FROM tasks`
```

In the `query` helper's `rows.Scan(...)`, add `&t.Verify` as the LAST scan target (after `&t.Supersedes`):

```go
		if err := rows.Scan(&t.ID, &t.MissionID, &t.Key, &t.Role, &t.Title, &t.Instruction,
			&t.Status, &depJSON, &claimedBy, &result, &t.CreatedTS, &claimedTS, &doneTS, &exp, &t.Supersedes, &t.Verify); err != nil {
```

- [ ] **Step 3d: Add `ClaimedMission`** — add to `internal/queue/store.go`:

```go
// ClaimedMission returns the mission id of the task the bee currently holds
// (claimed), or 0 if it holds none. Used to attribute an agent's executions to
// its mission for the verification gate.
func (s *Store) ClaimedMission(bee string) (int64, error) {
	var mid int64
	err := s.db.QueryRow(
		`SELECT mission_id FROM tasks WHERE claimed_by=? AND status=? LIMIT 1`,
		bee, StatusClaimed,
	).Scan(&mid)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return mid, err
}
```

- [ ] **Step 4: Run — verify pass**

Run: `go test ./internal/queue/ -run TestVerifyRoundTripAndClaimedMission -v && go test ./internal/queue/ -count=1`
Expected: PASS (new test + the whole queue suite still green).

- [ ] **Step 5: Commit**

```bash
git add internal/queue/store.go internal/queue/store_test.go
git commit -m "feat(queue): Verify field on tasks + ClaimedMission for exec attribution"
```

---

## Task 3: report_execution persists to the durable table

**Files:**
- Modify: `internal/brain/executions.go` (`registerExecutions` signature + body)
- Modify: `internal/brain/server.go` (call site)
- Modify: `internal/brain/executions_test.go`

**Interfaces:**
- Consumes: `queue.Store.RecordExecution`, `queue.Store.ClaimedMission` (Tasks 1-2).
- Produces: `registerExecutions(s *mcp.Server, ring *ExecRing, q *queue.Store)` — now also records executions durably (mission attributed via the agent's claimed task).

- [ ] **Step 1: Write the failing test** — add to `internal/brain/executions_test.go` (follow the file's existing in-memory MCP test pattern; if the file lacks one, model it on `tasks_test.go`'s server+session setup). The test: enqueue+claim a task for "Bob", call `report_execution`, assert a row landed in the queue with the claimed mission:

```go
func TestReportExecutionPersists(t *testing.T) {
	q, _ := queue.Open(filepath.Join(t.TempDir(), "q.db"))
	q.Enqueue(5, []queue.TaskSpec{{Key: "build#1", Role: "builder", Title: "build", Instruction: "build", Verify: "go build"}})
	q.PromoteReady(5)
	q.ClaimNext("Bob", []string{"builder"}, 300)

	ring := NewExecRing()
	sess := newTestSession(t, Options{Queue: q, ExecRing: ring}) // helper: starts a brain + connected client session
	callTool(t, sess, "report_execution", map[string]any{
		"name": "Bob", "role": "builder", "command": "go build ./...", "exit_code": 0, "ok": true, "summary": "ok",
	})

	pass, err := q.MissionPassedVerify(5, "go build")
	if err != nil {
		t.Fatal(err)
	}
	if !pass {
		t.Fatal("report_execution did not persist Bob's passing build to the durable table")
	}
}
```

(If `newTestSession`/`callTool` helpers don't already exist in the brain test package, add minimal ones mirroring the existing `tasks_test.go` setup — an in-memory `mcp` server from `NewServer(Options{...})` + an in-memory client transport + `sess.CallTool`. Reuse whatever the package already has; do not invent a new harness if one exists.)

- [ ] **Step 2: Run — verify it fails**

Run: `go test ./internal/brain/ -run TestReportExecutionPersists`
Expected: FAIL — either compile error (signature) or `pass == false` (not persisted).

- [ ] **Step 3a: Persist in `registerExecutions`** — change its signature and body in `internal/brain/executions.go`:

```go
func registerExecutions(s *mcp.Server, ring *ExecRing, q *queue.Store) {
	if ring == nil {
		return
	}
	// ... reportExecIn unchanged ...
	mcp.AddTool(s, &mcp.Tool{ /* unchanged */ }, func(_ context.Context, req *mcp.CallToolRequest, in reportExecIn) (*mcp.CallToolResult, okOut, error) {
		agent := identity(req, in.Name)
		ring.Add(Execution{
			Agent: agent, Role: in.Role, Command: in.Command, ExitCode: in.ExitCode,
			Ok: in.Ok, TimedOut: in.TimedOut, Summary: in.Summary, TS: time.Now().Unix(),
		})
		// Durable record for the verification gate: attribute to the agent's mission.
		if q != nil {
			mid, _ := q.ClaimedMission(agent)
			_ = q.RecordExecution(queue.Execution{
				MissionID: mid, Agent: agent, Role: in.Role, Command: in.Command,
				ExitCode: in.ExitCode, OK: in.Ok, TS: time.Now().Unix(),
			})
		}
		return nil, okOut{OK: true}, nil
	})
}
```

Add `"github.com/pdbethke/corralai/internal/queue"` to the file's imports if not present.

- [ ] **Step 3b: Update the call site** in `internal/brain/server.go` (currently `registerExecutions(s, opts.ExecRing)`):

```go
	registerExecutions(s, opts.ExecRing, opts.Queue)
```

- [ ] **Step 4: Run — verify pass + CGO-free agent untouched**

Run: `go test ./internal/brain/ -run TestReportExecutionPersists -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/brain/executions.go internal/brain/server.go internal/brain/executions_test.go
git commit -m "feat(brain): report_execution persists to the durable executions table"
```

---

## Task 4: The verification gate in complete_task

**Files:**
- Modify: `internal/brain/tasks.go` (`completeTaskOut` + the gate in `complete_task`)
- Modify: `internal/brain/tasks_test.go`

**Interfaces:**
- Consumes: `queue.TaskByID`, `queue.MissionPassedVerify`, `queue.AddFinding` (Tasks 1-2).
- Produces: `completeTaskOut.Message string`; `complete_task` refuses a gated task lacking a passing verify and raises a `regression` finding.

- [ ] **Step 1: Write the failing test** — add to `internal/brain/tasks_test.go`:

```go
func TestVerificationGate(t *testing.T) {
	q, _ := queue.Open(filepath.Join(t.TempDir(), "q.db"))
	q.Enqueue(9, []queue.TaskSpec{{Key: "test#1", Role: "tester", Title: "test", Instruction: "verify", Verify: "go test"}})
	q.PromoteReady(9)
	ct, _ := q.ClaimNext("Tess", []string{"tester"}, 300)

	sess := newTestSession(t, Options{Queue: q})

	// (a) No passing `go test` recorded → completion REFUSED + a regression finding.
	var out completeTaskOut
	callToolInto(t, sess, "complete_task", map[string]any{"name": "Tess", "id": ct.ID, "result": "looks fine"}, &out)
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

	// (b) Record a passing `go test`, then completion SUCCEEDS.
	q.RecordExecution(queue.Execution{MissionID: 9, Agent: "Tess", Command: "go test ./...", ExitCode: 0, OK: true, TS: 1})
	callToolInto(t, sess, "complete_task", map[string]any{"name": "Tess", "id": ct.ID, "result": "tested, green"}, &out)
	if !out.OK {
		t.Fatalf("with a passing verify the task must complete: %+v", out)
	}
}
```

(`callToolInto` decodes the structured result into `&out`; reuse the package's existing decode helper if present, else add a thin one over `sess.CallTool` + `json.Unmarshal(res.StructuredContent, out)`.)

- [ ] **Step 2: Run — verify it fails**

Run: `go test ./internal/brain/ -run TestVerificationGate`
Expected: FAIL — `out.OK` is true (no gate yet) / no `Message` field.

- [ ] **Step 3a: Add `Message` to `completeTaskOut`** in `internal/brain/tasks.go`:

```go
type completeTaskOut struct {
	OK      bool   `json:"ok"`                // false if you weren't the claimer, it was already done, OR the verify gate refused it
	Message string `json:"message,omitempty"` // why a gated completion was refused
}
```

- [ ] **Step 3b: Insert the gate** at the TOP of the `complete_task` handler body, BEFORE `q.Complete`:

```go
		func(_ context.Context, req *mcp.CallToolRequest, in completeTaskIn) (*mcp.CallToolResult, completeTaskOut, error) {
			bee := identity(req, in.Name)
			// Verification gate: a gated task (one with a Verify command) cannot close
			// unless a recorded execution of that command exited 0. Otherwise refuse and
			// raise a regression finding for the reflex re-planner.
			if t, _ := q.TaskByID(in.ID); t != nil && t.Verify != "" {
				passed, err := q.MissionPassedVerify(t.MissionID, t.Verify)
				if err != nil {
					return nil, completeTaskOut{}, err
				}
				if !passed {
					if _, err := q.AddFinding(queue.Finding{
						MissionID: t.MissionID, TaskID: in.ID, Reporter: "verify-gate",
						Type: "regression", Severity: "high", Target: t.Key,
						Evidence:        "no successful '" + t.Verify + "' execution recorded for this mission",
						SuggestedAction: "run '" + t.Verify + "' and fix the failures, then complete",
					}); err != nil {
						return nil, completeTaskOut{}, fmt.Errorf("gate finding: %w", err)
					}
					rec(tel, t.MissionID, "finding_reported", "verify-gate", t.Key, map[string]any{"type": "regression", "severity": "high"})
					return nil, completeTaskOut{OK: false,
						Message: "refused: no successful '" + t.Verify + "' run is on record for this mission — run it, fix the failures, then complete"}, nil
				}
			}
			ok, err := q.Complete(in.ID, bee, in.Result)
			// ... the rest of the existing handler is unchanged ...
```

(Leave everything from `ok, err := q.Complete(...)` onward exactly as it is.)

- [ ] **Step 4: Run — verify pass + the brain suite stays green**

Run: `go test ./internal/brain/ -run TestVerificationGate -v && go test ./internal/brain/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/brain/tasks.go internal/brain/tasks_test.go
git commit -m "feat(brain): verification gate — gated tasks need a passing verify or refuse + regression finding"
```

---

## Task 5: DefaultPlan declares Verify on the gated phases

**Files:**
- Modify: `internal/mission/store.go` (`PhaseSpec`, `DefaultPlan`, spec-build loop)
- Modify: `internal/mission/store_test.go`

**Interfaces:**
- Consumes: `queue.TaskSpec.Verify` (Task 2).
- Produces: `PhaseSpec.Verify string`; `DefaultPlan` sets `Verify` on build/test/integrate; the regression finding raised by Task 4 flows into the EXISTING `reflexRules` (`case "regression"`) — no engine change needed.

- [ ] **Step 1: Write the failing test** — add to `internal/mission/store_test.go`:

```go
func TestDefaultPlanVerify(t *testing.T) {
	want := map[string]string{"build": "go build", "test": "go test", "integrate": "go build"}
	for _, p := range DefaultPlan("a calc package") {
		if v, gated := want[p.Name]; gated {
			if p.Verify != v {
				t.Fatalf("phase %s Verify = %q, want %q", p.Name, p.Verify, v)
			}
		} else if p.Verify != "" {
			t.Fatalf("phase %s should be ungated, got Verify=%q", p.Name, p.Verify)
		}
	}
}
```

- [ ] **Step 2: Run — verify it fails**

Run: `go test ./internal/mission/ -run TestDefaultPlanVerify`
Expected: FAIL — `unknown field Verify in struct literal` / Verify empty.

- [ ] **Step 3a: Add `Verify` to `PhaseSpec`** in `internal/mission/store.go` (after `Count int`):

```go
	Verify string // command that must pass (exit 0) before a task of this phase can complete; "" = ungated
```

- [ ] **Step 3b: Set it in `DefaultPlan`** — add `Verify:` to the build, test, and integrate phases:

```go
		{Name: "build", Role: "builder", Count: 1, DependsOn: []string{"design"}, Verify: "go build",
			Instruction: "Build this: " + d + ". FIRST read the designer's design from SHARED memory and implement against it (don't redesign). Record what you built and any deviations in SHARED memory so the verifiers have full context."},
		{Name: "test", Role: "tester", Count: 2, DependsOn: []string{"build"}, Verify: "go test",
			Instruction: "Independently verify the feature built for: " + d + ". Read the build notes for intent — but you did NOT build it, so test adversarially: edge cases, error paths, broken assumptions. Record every failure in SHARED memory."},
		{Name: "integrate", Role: "integrator", Count: 1, DependsOn: []string{"test", "secops", "perf"}, Verify: "go build",
			Instruction: "Assemble the work for: " + d + " into one coherent, working whole. Read the build/test/secops/perf notes from SHARED memory, wire the pieces together, resolve cross-file integration, and confirm it runs end to end. Record integration status and any remaining gaps in SHARED memory."},
```

(Leave research/design/secops/perf/docs/retro unchanged — they stay ungated.)

- [ ] **Step 3c: Carry it into the TaskSpec** — in the spec-build loop (`CreateMission`/`enqueueSpecs`), add `Verify: p.Verify` to the `queue.TaskSpec{...}` literal:

```go
			specs = append(specs, queue.TaskSpec{
				Key:         taskKey(p.Name, i),
				Role:        p.Role,
				Title:       p.Name,
				Instruction: p.Instruction,
				DependsOn:   deps,
				Verify:      p.Verify,
			})
```

- [ ] **Step 4: Run — verify pass + the mission suite stays green**

Run: `go test ./internal/mission/ -run TestDefaultPlanVerify -v && go test ./internal/mission/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mission/store.go internal/mission/store_test.go
git commit -m "feat(mission): DefaultPlan declares Verify on build/test/integrate (the gate's first controls)"
```

---

## Final verification (after all tasks)

- [ ] Full suite + vet + CGO-free agent:

```bash
go build ./... && go vet ./... && go test ./... && CGO_ENABLED=0 go build -o /dev/null ./cmd/corral-agent
```
Expected: all packages PASS, vet clean, agent CGO-free.

- [ ] Sanity that the loop closes end-to-end (the gate's refusal feeds the reflex re-planner): the `regression` finding raised by Task 4 is the same shape `reflexRules` (`internal/mission/replan.go`, `case "regression"`) already turns into a `fix-f<id>` builder task — so a refused `test#1` revives the mission with a fix task. No new engine code; confirm the existing reflex test still passes (`go test ./internal/mission/ -run Reflex`).
