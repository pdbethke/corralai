# Reflex Re-planner — Design (sub-project #3)

**Status:** design · **Date:** 2026-06-29

## Where this fits

#1 gave the pull model; #2 made findings structured + stored + visible. #3 closes
the first half of the adaptive loop: **a finding deterministically spawns
remediation work.** A HIGH/CRITICAL vuln/bug doesn't just sit in the log — the
re-planner enqueues a *fix* task and a *re-verify* task, the hive picks them up,
and the re-verify can surface a fresh finding → loop. This is the reflex tier
(fast, deterministic). The LLM "lead" tier (judgment, re-architecting,
supersession) is #4.

## Goal

Each engine tick, for every running mission, open findings at or above a severity
threshold are turned by deterministic rules into remediation tasks (fix +
dependent re-verify), the finding is marked `addressed`, and the mission keeps
running until the queue drains with no further actionable findings — a converging
feedback loop. No LLM in this tier.

## Global constraints

- Deterministic + idempotent: a finding is remediated exactly once (marked
  `addressed` when its tasks are enqueued; never reprocessed).
- No silent runaway: a per-mission reflex-task cap bounds the loop; hitting it is
  logged, never silent (so a pathological "every verify finds a new vuln" can't
  spawn unbounded work).
- Reuses existing primitives only: `queue.Findings(open)`, `queue.Enqueue`,
  `queue.SetFindingStatus`, `queue.SeverityRank`. No schema change.
- Free UI: remediation tasks are ordinary queue tasks and findings flip
  open→addressed, so the existing task + findings panels visualize re-planning
  with zero UI work.

## The reflex rules

`reflexRules(f Finding) (specs []queue.TaskSpec, actionable bool)`:

| Finding type | Actionable | Remediation tasks |
|---|---|---|
| `vuln` | yes | `fix-f<id>` (role builder) → `verify-f<id>` (role **pentester**, depends on fix) |
| `bug`, `regression`, `missing-req` | yes | `fix-f<id>` (builder) → `verify-f<id>` (role **tester**, depends on fix) |
| `design-flaw`, `note` | no | none — deferred to the #4 LLM lead (judgment) |

- Task keys are finding-id-scoped (`fix-f<id>`, `verify-f<id>`) so they're unique
  and idempotent.
- Instructions embed the finding's `target`, `evidence`, and `suggested_action`
  so the bee has full context; the verify instruction says "if not resolved,
  `report_finding` again" — that's the loop.
- Below-threshold findings (default threshold `high`) are left `open` (recorded,
  not acted on) — the #4 lead may act on them with judgment.

## Engine integration

`Engine` gains config (defaults, overridable from env in main):
`ReflexMinSeverity` (default `high`, `CORRALAI_REFLEX_MIN_SEVERITY`),
`ReflexMaxTasks` (default 50, `CORRALAI_REFLEX_MAX_TASKS`).

`Tick` per running mission becomes: **`replan` → `PromoteReady` → `MissionDone`**.
`replan(missionID)`:
1. `findings = q.Findings(mid, open)`; count existing reflex tasks (`fix-f*` /
   `verify-f*`) for the cap.
2. For each finding: skip if not actionable or below threshold; if the cap would
   be exceeded, log and stop; else `q.Enqueue` its specs and
   `q.SetFindingStatus(addressed)`.

Putting `replan` before `MissionDone` ensures a finding reported on the last task
revives the mission with remediation work rather than completing prematurely.

## Convergence

The loop terminates when: every reflex finding has been addressed (so no new
tasks are enqueued) and the queue drains → `MissionDone` → mission `done`. The
cap is the backstop against a non-converging verify→finding→fix cycle.

## Testing strategy

- **rules:** vuln → fix(builder)+verify(pentester, dep); bug → verify(tester);
  design-flaw/note → not actionable.
- **replan:** a HIGH vuln enqueues fix+verify and marks the finding addressed;
  a LOW finding does nothing and stays open; running replan twice is idempotent
  (no duplicate tasks); the cap stops runaway and logs.
- **loop via Tick:** report a HIGH vuln → tick enqueues fix (ready) + verify
  (pending); complete fix → tick promotes verify; complete verify → mission done.
- **live e2e:** a secops bee reports a HIGH vuln; the reflex re-planner enqueues
  fix + re-verify (visible in the task panel); a builder + pentester complete
  them; the finding shows addressed; the mission converges to done.

## Decisions deferred to the plan

- Exact role for the `fix` task (builder vs the finding's reporter role) — builder
  by default (fixes are implementation; keeps reporter independent of fixer).
- Whether to also surface an "addressed N" count in the UI findings header (nice,
  optional).
