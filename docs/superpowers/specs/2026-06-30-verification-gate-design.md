# Verification Gate — Design (sub-project #12)

**Status:** design · **Date:** 2026-06-30

## Where this fits

A live mission (Gemini pro, 2026-06-29) shipped a "done" mission whose code did
not compile. Forensics: the **tester** (`Tess`) `cat`'d the test file and marked
the task `completed` — she never ran `go test`. **Zero findings** were raised by
any verification role across the whole mission, and the mission engine accepted
every bee's `"completed"` at face value. The role existed; nothing forced the
role to do its job.

This is a controls problem, not a model problem, so the fix is a **deterministic
engine gate**, not another (probabilistically-lazy) agent: a verify task cannot
close unless a recorded execution of its declared verify command exited 0. It
composes with the existing findings + reflex re-planner — a refused completion
becomes a `regression` finding, which `reflexRules` already turns into a
`fix-f<id>` remediation task.

(The fuzzy layer — vacuous tests, thrash, drift — is a separate, advisory
"accountant" auditor over the telemetry; explicitly **out of scope** here.)

## First principle: done means verified

A deterministic invariant, enforced where it cannot be lazy: **a task that
declares a `Verify` command is `done` only if some execution recorded for its
mission ran that command and exited 0.** Otherwise the close is refused and a
`regression` finding is raised. The check is encodable, so it lives in the
engine, never in a prompt.

## Components

### 1. Durable per-mission execution record (`internal/queue`)

Today executions hit only the brain's `ExecRing` (cap-40, in-memory, UI-only) —
not durable, not queryable per mission, so nothing can ask "did a `go test`
pass?". Add an `executions` table to the queue SQLite store (the same store as
tasks + findings, so the gate is one in-store read, not a cross-store query;
DuckDB telemetry stays the analytics surface and the accountant's future data
source):

```
executions(id INTEGER PK, mission_id INTEGER, agent TEXT, role TEXT,
           command TEXT, exit_code INTEGER, ok INTEGER, ts INTEGER)
```

- `func (s *Store) RecordExecution(e Execution) error` — insert one row.
- `func (s *Store) MissionPassedVerify(missionID int64, verify string) (bool, error)`
  — true iff ∃ a row for `missionID` whose `command` **contains** `verify` and
  `exit_code == 0`. Substring match (a bee runs `cd calc && go test ./...`, not
  the bare string); `verify` is the minimal stable command core the plan sets.

`type Execution struct { MissionID int64; Agent, Role, Command string; ExitCode int; OK bool; TS int64 }`.

### 2. `Verify` field on the task (`internal/queue` Task + TaskSpec)

Add `Verify string` to `TaskSpec` and `Task` (persisted in the existing task JSON
blob — no schema column needed). It is the command that must pass; **empty =
ungated** (research/design/docs/retro/secops are unchanged). `DefaultPlan` sets
it on the gated phases:

- `build` → `go build`
- `test` → `go test`
- `integrate` → `go build`

A non-Go directive overrides these via the plan (the field is language-agnostic).

### 3. The brain tool writes the durable record (`internal/brain/executions.go`)

`report_execution` already feeds the `ExecRing` for the UI. It now ALSO calls
`queue.RecordExecution` (mission_id derived from the reporting agent's current
claimed task, or carried in the report). The `ExecRing` (UI) and the durable
table (gate) are both written; the table is the source of truth for the gate.

### 4. The gate (`internal/brain/tasks.go`, `complete_task`)

Before calling `queue.Complete`, load the task; if `task.Verify != ""`:

- `ok, _ := q.MissionPassedVerify(task.MissionID, task.Verify)`
- **ok** → complete normally (done means verified).
- **!ok** → **do NOT complete.** Raise:
  ```
  queue.Finding{ MissionID: task.MissionID, TaskID: task.ID, Type: "regression",
    Severity: "high", Target: task.Key, Reporter: "verify-gate",
    Title: "verify never passed for this mission: " + task.Verify }
  ```
  Return a `complete_task` result that tells the bee its close was **refused**
  and why (`"refused: no successful '<verify>' run recorded — run it and fix the
  failures, then complete"`), so the model actually runs + fixes the build.

The `regression` finding flows through the existing engine: `reflexRules`
(`case "regression"`) → a `fix-f<id>` builder task + a verify task. Bounded by
the existing `ReflexMaxTasks` and sprint caps, so a non-converging
verify→finding→fix cycle can't run away.

### 5. No new role

The gate is engine-side and deterministic. The "accountant" auditor (judgment
over telemetry — vacuous tests, thrash, drift) is a separate later sub-project,
advisory only.

## Data flow

```
bee runs `go test ./...` (real exec in the jail)
  → report_execution → ExecRing (UI)  AND  queue.executions table (durable)
bee calls complete_task(test#1)
  → gate: task.Verify = "go test"
     → MissionPassedVerify(mission, "go test")
        → exit-0 `go test` recorded?  yes → Complete (done = verified)
                                      no  → REFUSE + regression finding
                                            → reflexRules → fix-f<id> builder task → loop
```

Tess can no longer `cat` the test file and call it tested: with `Verify="go
test"` and no exit-0 `go test` on record, her `complete_task(test#1)` is refused
and a `regression` finding is raised instead.

## Error handling / edge cases

- **Ungated tasks** (`Verify == ""`): unchanged, no query, no gate.
- **Runaway protection**: the auto-raised finding is bounded by the existing
  `ReflexMaxTasks` (per-mission reflex cap) + `SprintCap`.
- **Substring matching**: case-sensitive `contains`. The plan sets a stable core
  (`go test`, not `go test ./... -run X`). Documented on the `Verify` field.
- **Vacuous pass**: a bee could exit-0 `go test ./emptydir`. The gate guarantees
  "a matching command passed," not "the test was meaningful" — that fuzzy class
  is the accountant's, acknowledged and deferred.
- **mission_id on executions**: derived from the reporting agent's claimed task
  at report time; if the agent has no claimed task (e.g. lead/client), the
  execution is recorded with `mission_id = 0` and never matches a gated task —
  harmless.
- **Concurrency**: `MissionPassedVerify` is a read on the single-conn
  (`MaxOpenConns=1`) SQLite store; `Complete` is transactional. No new races.

## Testing

- **queue** (`store_test`/`executions_test`): `RecordExecution` insert;
  `MissionPassedVerify` — true on a matching exit-0 row, false when the only
  matching row is non-zero, false when no row contains the verify substring,
  true on a `cd … && go test …` substring row.
- **brain** (`tasks_test`): `complete_task` on a gated task with NO passing exec
  → task NOT done + exactly one `regression` finding (reporter `verify-gate`) +
  refused message; with a passing exec → completed, no finding; an ungated task
  (`Verify==""`) → completes regardless of executions.
- **engine integration**: the auto-raised `regression` finding drives
  `reflexRules` → a `fix-f<id>` task appears (reuse the existing reflex test
  harness).
- **plan**: `DefaultPlan`'s build/test/integrate tasks carry the expected
  `Verify` values; research/design/docs carry `""`.

## Out of scope (follow-ups)

- The **accountant** auditor (telemetry-mining, advisory meta-findings).
- Per-execution → per-task linkage (the gate works at mission granularity; a
  task-level link is a finer-grained future refinement).
- Auto-promoting recurring accountant findings into new deterministic gates.
