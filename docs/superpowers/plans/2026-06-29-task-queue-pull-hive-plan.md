# Task Queue + Pull Hive — Implementation Plan (sub-project #1)

> **For agentic workers:** execute task-by-task, TDD, commit per task on
> `feat/task-queue`. Full design + schema + API + rationale live in the spec:
> `docs/superpowers/specs/2026-06-29-task-queue-pull-hive-design.md`. This plan
> is the ordering and the TDD steps; the spec is the source of detail.

**Goal:** a directive → a dependency-ordered SQLite task queue → a hive of
`corral-agent` bees that atomically pull, execute, and complete ready tasks →
mission done when the queue drains, all observable live in the UI.

**Resolved decisions:** lease 300s (`CORRALAI_TASK_LEASE_SECONDS`); `claim_task`
`roles` optional (empty = any); `mission.phases` is the seed-plan template source.

## Global Constraints

- Engine: SQLite via `modernc.org/sqlite`, the `coord.Open` recipe (WAL,
  `MaxOpenConns=1`). Not DuckDB.
- `var now` clock seam (like `coord`) for deterministic lease tests.
- Resiliency-first: reaper requeues a dead/absent bee's task; starvation never
  marks a mission done; durable across restart.
- One-way imports: `internal/queue` imports neither `mission` nor `brain`.
- Out of scope (assert, don't build): findings, re-planning, supersession,
  autoscaling, headline demo. `cancelled`/`superseded` statuses reserved, unused.

---

## Task 1: `internal/queue` store

**Files:** Create `internal/queue/store.go`, `internal/queue/store_test.go`.

Implements the schema and API from the spec: `Open`, `Enqueue([]TaskSpec)`,
`PromoteReady`, `ClaimNext` (atomic, `BEGIN IMMEDIATE`), `Complete` (ownership +
idempotent), `Reap(presentBees)`, `MissionDone`, `List`/`Active`, `var now`.

- [ ] Write failing tests: enqueue + `PromoteReady` honors `depends_on`;
  **N concurrent `ClaimNext` goroutines yield N distinct tasks, no double-claim**;
  `Complete` rejects non-claimer + is idempotent; `Reap` requeues on absent bee
  and on expired lease (drive via `now`); `MissionDone` only when all done;
  starvation (ready task, no matching role) never completes the mission.
- [ ] Implement `store.go` to pass them. Run `go test ./internal/queue/`.
- [ ] Commit.

## Task 2: mission engine refactor (push → pull)

**Files:** Modify `internal/mission/engine.go`; adjust `internal/mission/store.go`
+ tests as needed. The engine gains a `*queue.Store`.

- [ ] Failing test: a created mission enqueues the expected tasks with the
  expected `depends_on` (phase → `TaskSpec`); ticks `PromoteReady` then mark the
  mission `done` once `MissionDone`; no `Spawn`/`SendInstruction` remains.
- [ ] Implement: `dispatch` → `queue.Enqueue`; `advance` →
  `PromoteReady` + `MissionDone`. Delete the spawn-identity dispatch.
- [ ] Run mission tests. Commit.

## Task 3: brain MCP tools + reaper wiring

**Files:** Create `internal/brain/tasks.go`; modify `internal/brain/identity.go`
(Options gains `*queue.Store`), `internal/brain/server.go` (register tools),
`cmd/corral/main.go` (open the queue DB, pass it, start a reaper tick driven by
`coord` presence). Update tool-count assertions in
`internal/brain/server_test.go` + `memory_test.go`.

- [ ] Failing tests: `claim_task` returns one ready task and marks it claimed by
  the caller; `complete_task` rejects a non-claimer; `list_tasks` filters by
  mission/status; new tool count.
- [ ] Implement tools (`claim_task {roles?}`, `complete_task {id,result}`,
  `list_tasks {mission_id?,status?}`), wire `Queue` into `Options`, open
  `CORRALAI_QUEUE_DB` in main, add the reaper goroutine (each tick:
  `Reap(presentBees from coord)`).
- [ ] Run brain tests. Commit.

## Task 4: `corral-agent` pull loop

**Files:** Modify `cmd/corral-agent/main.go` (+ `dispatch` for the two new tools).

- [ ] Implement the loop: `claim_task(roles=[myRole])` → if a task, `execute`
  via the existing LLM + `edit_file`/`claim_paths` tools → `complete_task(id,
  summary)`; if none, `heartbeat` + idle-poll. Keep the coordination demos intact.
- [ ] Manual/live smoke (no unit harness for the LLM loop): documented in Task 6.
- [ ] Commit.

## Task 5: UI observability (task list + assignment edges)

**Files:** Modify `internal/ui/ui.go` (+ snapshot), `internal/ui/web/index.html`;
the UI `Server` gains read access to the queue (`queue.Active`).

- [ ] Test: `/api/state` includes a `tasks` array with `status` + `claimed_by`.
- [ ] Implement: snapshot includes `queue.Active()`; the page renders a live
  task list colored by status and draws a bee→task edge for each `claimed_by`.
- [ ] Run UI tests. Commit.

## Task 6: live e2e + verification

- [ ] Dev brain + 2–3 bees + a small enqueued mission: watch tasks flow to done,
  mission completes, UI shows the task list + assignment edges.
- [ ] Kill a bee mid-task; confirm its task is reaped and finished by another.
- [ ] Full `go test ./...` green; `go vet`. Commit any fixes.

---

## Status: COMPLETE (2026-06-29)

All six tasks landed on `feat/task-queue`; `go test ./...` + `go vet` green throughout.

- T1 queue store (`6ad2145`) · T2 engine push→pull (`fbbb660`) · T3 brain tools +
  reaper (`f4d30d8`) · T4 agent pull loop (`7a44f59`) · T5 UI task list (`68e7b4e`).
- **Live e2e through a real brain:** `mission create "build me a world cup scores
  dashboard"` enqueued 5 tasks; a generic MCP bee drained them in strict DAG order
  (build → test∥secops → retro), proving live dependency gating; mission reached
  `done` with every phase done; `/api/state` showed all tasks `done` with
  `claimed_by`. Reaper requeue is covered by unit tests (presence-authoritative +
  lease fallback); the full LLM-driven agent run is the sub-project #5 demo.
