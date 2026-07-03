# Task Queue + Pull Hive — Design (sub-project #1)

**Status:** design, pending review · **Date:** 2026-06-29

## Where this fits

This is sub-project #1 of the **adaptive swarm** vision: a one-line directive
becomes a living plan that a scalable hive of agents executes, and which the
agents' own findings rewrite as they work.

The full vision decomposes into five sequential sub-projects, each its own
spec → plan → build → merge:

1. **Task queue + pull hive** ← *this spec*
2. Structured findings (the feedback channel)
3. Reflex re-planner (rules: finding → tasks)
4. LLM lead re-planner + supersession/lineage (judgment, cancel/reopen)
5. UI + bee supervisor + the headline demo

#1 is the load-bearing foundation: it replaces the engine's current
**push** model (the brain spawns named agent identities and instructs them, but
nothing binds compute to those identities — so missions never actually execute)
with a **pull** model (the brain enqueues tasks; a hive of generic worker bees
pulls ready tasks, executes, completes, pulls the next). Everything later rides
on this.

## Goal

A directive becomes a dependency-ordered task queue in the brain; a scalable
hive of `corral-agent` bees atomically claims and executes ready tasks; the
mission completes when every task is done; and the live task list — with which
bee owns which task — is observable in the swarm UI from the moment the queue
exists.

## Global constraints (bind every part of this sub-project)

- **Engine: SQLite (modernc, no-CGO), the `coord` pattern** — WAL,
  `MaxOpenConns=1`. The task queue is hot OLTP with claim contention; it reuses
  the exact transactional atomic-claim mechanic `coord.ClaimPaths` already uses.
  **DuckDB is not used here** (it is OLAP/columnar, single-writer — wrong for a
  hot transactional queue; it stays the analytics/observability engine).
- **Resiliency before happy path.** Failure modes — a bee dying mid-task, a
  task no bee can serve, brain restart — are designed in, not bolted on.
- **UI-observable from day one.** The task list and task↔bee assignment are a
  first-class requirement of #1, not deferred polish. The schema carries
  everything the UI needs (`status`, `claimed_by`) from the start.
- **One-way module hierarchy.** `internal/queue` is a peer store (no imports of
  `mission`/`brain`); the mission engine imports `queue`. The queue refers to
  missions and agents by id/name only (strings), never by import.
- **Scope discipline.** Findings, re-planning, supersession, autoscaling, and
  the headline demo are explicitly OUT (sub-projects #2–#5). The `cancelled` and
  `superseded` statuses are reserved in the enum but never transitioned here.

## Architecture

```
 directive ─▶ seed planner ─▶ ┌──────── TASK QUEUE (internal/queue, SQLite) ───────┐
 (mission engine)             │  pending ─(deps done)─▶ ready ─(atomic claim)─▶     │
                              │  claimed ─(complete)─▶ done                          │
                              │  claimed ─(claimer gone / lease expired)─▶ ready     │
                              └───────────────────────┬────────────────────────────┘
                                       claim_task / complete_task (MCP)
                ┌───────────────┬───────────────┬─────┴──────────┬───────────────┐
              bee₁            bee₂            bee₃      …        beeₙ    ← corral-agent pull loop
            (claim→run→       (idle-poll when queue empty; heartbeat keeps the claim alive)
             complete→repeat)
```

The mission engine is **refactored, not replaced**: its phase/dependency logic
survives, but `dispatch` (spawn-named-identity + instruct) becomes **enqueue**,
and completion detection reads task status instead of instruction acks.

## Components

### 1. `internal/queue` — the task store (new)

Own SQLite file (`CORRALAI_QUEUE_DB`, default under `~/.claude/`), opened with
the `coord` recipe (WAL, `MaxOpenConns=1`).

**Schema:**

```sql
CREATE TABLE tasks (
  id               INTEGER PRIMARY KEY,
  mission_id       INTEGER NOT NULL,
  key              TEXT    NOT NULL,   -- per-mission task key, e.g. "build-ui"
  role             TEXT    NOT NULL DEFAULT '', -- builder|tester|pentester|reviewer|'' (any)
  title            TEXT    NOT NULL,   -- short label for the UI
  instruction      TEXT    NOT NULL,   -- what the bee must do
  status           TEXT    NOT NULL,   -- pending|ready|claimed|done  (cancelled|superseded reserved)
  depends_on       TEXT    NOT NULL DEFAULT '[]', -- JSON array of task keys within the mission
  claimed_by       TEXT,               -- bee (agent) name while claimed, else NULL
  claim_expires_ts REAL,               -- lease while claimed
  result           TEXT,               -- completion summary (findings arrive in #2)
  created_ts       REAL NOT NULL,
  claimed_ts       REAL,
  done_ts          REAL,
  UNIQUE(mission_id, key)
);
CREATE INDEX ix_tasks_claimable ON tasks(status, role);
CREATE INDEX ix_tasks_mission   ON tasks(mission_id);
CREATE INDEX ix_tasks_claimed   ON tasks(claimed_by);
CREATE INDEX ix_tasks_lease     ON tasks(claim_expires_ts);
```

**Status lifecycle:**

```
pending ──(all deps done)──▶ ready ──(atomic claim)──▶ claimed ──(complete)──▶ done
                              ▲                            │
                              └──(claimer gone / lease)────┘   (reaper requeues)
```

**API (the load-bearing surface):**

- `Enqueue(missionID int64, specs []TaskSpec) error` — insert tasks
  (`key, role, title, instruction, depends_on`) for a mission, all `pending`.
  `TaskSpec` is the seed planner's output unit.
- `PromoteReady(missionID int64) (int, error)` — flip `pending → ready` for every
  task whose `depends_on` are all `done`. Idempotent; called each engine tick.
- `ClaimNext(bee string, roles []string, lease float64) (*Task, error)` — the
  atomic claim. In one `BEGIN IMMEDIATE … COMMIT`: select the oldest `ready`
  task matching one of `roles` (or any when `roles` empty), set
  `status=claimed, claimed_by=bee, claimed_ts=now, claim_expires_ts=now+lease`,
  return it. Returns `(nil, nil)` when nothing is claimable. **Exactly one bee
  can win a task** — guaranteed by the transaction + `MaxOpenConns=1`.
- `Complete(id int64, bee string, result string) (bool, error)` — `claimed → done`
  only if `claimed_by == bee` (idempotent; a second call is a no-op `false`).
- `Reap(presentBees map[string]bool) (int, error)` — requeue (`claimed → ready`,
  clear `claimed_by`) any task whose claimer is absent from `presentBees` OR
  whose `claim_expires_ts` has passed. The reaper is how the hive self-heals.
- `MissionDone(missionID int64) (bool, error)` — true iff the mission has tasks
  and all are `done`.
- `List(missionID int64) ([]Task, error)` and `Active() ([]Task, error)` — for the
  UI/admin (mission-scoped and fleet-wide live views).

`var now = func() float64 { … }` clock seam (matches `coord`) so tests drive
lease expiry deterministically.

### 2. Mission engine refactor (`internal/mission`)

- The seed plan (the default `build → test ∥ secops → retro`, or a custom plan)
  becomes a `[]TaskSpec` the engine **enqueues** at mission start, instead of
  per-phase `Spawn` + `SendInstruction`.
- `advance(mission)` per tick becomes: `queue.PromoteReady(missionID)`, then
  `if queue.MissionDone(missionID) { SetMissionStatus(done) }`. The
  dependency-gating and completion logic carry over; the dispatch-by-spawn is
  deleted.
- The engine no longer manufactures per-phase agent identities. The orchestrator
  swarm node (`mission-<id>`) is retained as a UI anchor.

### 3. Brain MCP tools (`internal/brain`)

- `claim_task { roles?: []string }` → the caller's verified identity is the bee;
  returns the claimed task or `{ "task": null }`. Lease defaults per config.
- `complete_task { id, result }` → marks done; errors if the caller isn't the
  claimer.
- `list_tasks { mission_id?, status? }` → observability (admin/UI/`corral-admin`).

Task-claim leases are tied to **presence**: a claimed task is held while its bee
keeps heart-beating (the existing `heartbeat` presence window). The reaper
requeues tasks whose bee has gone stale — reusing `coord` presence rather than a
parallel lease-renewal protocol. An explicit `claim_expires_ts` is the hard
backstop.

### 4. `corral-agent` pull loop (`cmd/corral-agent`)

A new primary loop for mission work, replacing self-assigned `roleWork` as the
default when a brain is reachable:

```
bootstrap(role)
loop:
  t := claim_task(roles=[myRole])
  if t == nil:            # queue empty
     heartbeat(); sleep(poll); continue
  execute(t.instruction)  # LLM + existing edit_file / claim_paths tools
  complete_task(t.id, summary)
```

Idle bees heart-beat and poll; a directive landing makes the whole hive swarm.
The existing coordination demos (clobber, self-organizing roleWork) are retained
for the coordination story; the queue is the mission-execution path.

### 5. UI observability (`internal/ui`, `internal/ui/web`)

First-class in #1:

- `/api/state` (the SSE snapshot) gains a `tasks` array:
  `{id, mission_id, key, title, role, status, claimed_by}` from `queue.Active()`.
- The swarm view renders a **live task list** (rows colored by status:
  pending/ready/claimed/done) and draws an **assignment edge** from each bee node
  to the task it currently holds (`claimed_by`). Ready-but-unclaimed tasks are
  visibly waiting; this is also how operators see a starved task (a ready task no
  bee serves).
- Richer visualization (queue throughput, the re-planning animation) is #5; #1
  delivers the data + a real, legible task list and assignment edges.

## Data flow (end to end)

1. `create_mission "build me a World Cup scores dashboard"` → engine builds a
   `[]TaskSpec` (seed plan) → `queue.Enqueue` (all `pending`).
2. Engine tick → `PromoteReady` flips dependency-free tasks to `ready`.
3. A bee calls `claim_task` → atomically gets one ready task → executes →
   `complete_task`.
4. Completing a task lets the next tick promote its dependents.
5. The UI shows tasks flowing pending→ready→claimed→done and bee→task edges live.
6. When all tasks are `done`, the engine marks the mission `done`.

## Error handling & resiliency

- **Bee dies mid-task:** its presence lapses (or the lease expires) → `Reap`
  returns the task to `ready` → another bee picks it up. No task is lost.
- **Double-claim race:** impossible — `ClaimNext` is a single transaction over a
  `MaxOpenConns=1` connection; the winner mutates the row before any other claim
  observes it.
- **Idempotent completion:** `Complete` is a no-op if the task is already `done`
  or the caller isn't the claimer; a retrying bee can't corrupt state.
- **Starvation (no bee for a role):** the task stays `ready` and the mission does
  **not** falsely complete (`MissionDone` requires all `done`). The UI shows the
  stranded ready task so an operator can scale a matching bee.
- **Brain restart:** the queue is durable (SQLite). `ready`/`done` survive;
  `claimed` tasks are reaped on the first tick if their bee didn't reconnect.
- **Throughput:** single-connection serialized writes do thousands of tx/sec —
  ample for a swarm. WAL + per-op `BEGIN IMMEDIATE` is the upgrade path if it ever
  saturates; no engine change.

## Testing strategy

- **queue unit tests:** enqueue; `PromoteReady` respects deps; **concurrent
  `ClaimNext` from N goroutines yields N distinct tasks, never a double-claim**;
  `Complete` ownership + idempotency; `Reap` requeues on absent bee and on expired
  lease (driven via the `now` seam); `MissionDone` only when all done; starvation
  (a ready task with no matching role never completes the mission).
- **engine tests:** a directive enqueues the expected tasks with the expected
  `depends_on`; ticks promote ready tasks and mark the mission done when the queue
  drains; no `Spawn`/`SendInstruction` dispatch remains.
- **brain MCP tests:** `claim_task` returns one task and marks it claimed;
  `complete_task` rejects a non-claimer; `list_tasks` filters; tool count
  assertions updated.
- **UI test:** `/api/state` includes `tasks` with `status` + `claimed_by`.
- **live e2e (dev brain):** enqueue a small mission, run 2–3 bees, watch tasks
  flow to done and the mission complete; kill a bee mid-task and confirm its task
  is reaped and finished by another.

## Decisions deferred to the implementation plan

- Exact home of the seed-plan templates (keep `mission.phases` as the template
  source vs. fold into `TaskSpec` literals).
- Default lease duration and idle-poll interval (config knobs).
- Whether `claim_task` with no `roles` (a fully generic bee) is exposed in #1 or
  held for the autoscaling work in #5.
