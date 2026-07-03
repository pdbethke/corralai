# LLM Lead + Supersession — Design (sub-project #4)

**Status:** design · **Date:** 2026-06-29

## Where this fits

#3 gave the reflex tier: a HIGH vuln/bug deterministically spawns fix +
re-verify. #4 is the **judgment tier**: an LLM "lead" reads the open findings and
the live task state and decides what reflex rules can't — *should we re-architect?
which in-flight work is now invalid? what should be reworked?* — and acts by
**superseding** stale tasks, **cancelling** abandoned work, **reopening** done
work, and **enqueuing** rework. This is the "rethink this" half of the loop.

It has two layers:
- **The mechanism** (deterministic, fully testable): task **supersession +
  lineage + cancel + reopen** in the queue, plus the MCP tools that expose them.
- **The lead** (LLM-driven): a `lead`-role bee that reads findings + tasks and
  drives those tools with judgment.

## Goal

A finding the reflex tier can't resolve (a design-flaw, or a root cause spanning
work) is handled by an LLM lead that supersedes/cancels/reopens/enqueues tasks via
brain tools; task lineage records what replaced what; the mission still converges
(cancelled/superseded tasks don't block completion); and the UI shows the
re-architecture (struck-through superseded tasks, lineage).

## Global constraints

- The mechanism is deterministic and idempotent and is the only thing that
  mutates task state; the lead only *decides* (so the lead is testable as
  plumbing, the mechanism as logic).
- Convergence preserved: `cancelled`/`superseded` are terminal, non-open;
  `MissionDone` = has tasks AND no open (pending/ready/claimed) tasks.
- Bounded: the lead acts at most once per finding (marks it `addressed`), and a
  per-mission lead-action cap mirrors the reflex cap — no runaway re-architecting.
- Reuse the model-agnostic backend (the lead is a bee; no LLM in the brain).

## Layer 1 — the mechanism (queue + tools)

Activate the reserved statuses from #1: `cancelled` (work abandoned),
`superseded` (replaced by a newer task). Add a lineage column:

```sql
ALTER TABLE tasks ADD COLUMN supersedes INTEGER NOT NULL DEFAULT 0;  -- the task id this one replaces
```

Queue methods:
- `CancelTask(id) (bool, error)` — any non-terminal task → `cancelled`.
- `ReopenTask(id) (bool, error)` — a `done` task → `ready` (re-do finished work,
  e.g. rebuild on a corrected layer).
- `SupersedeTask(oldID int64, spec TaskSpec) (newID int64, err error)` — mark
  `oldID` `superseded` and enqueue `spec` with `supersedes=oldID` (lineage).
- `MissionDone` changed to `hasTasks && openCount==0` (open = pending|ready|
  claimed) so a mission whose remaining work was cancelled/superseded still
  completes.

Brain tools (queue-gated, superuser not required — the lead is an agent):
- `cancel_task {id}` · `reopen_task {id}` · `supersede_task {old_id, key, role,
  title, instruction, depends_on?}` · `enqueue_task {mission_id, key, role,
  title, instruction, depends_on?}` (add rework to an existing mission).
- (reads already exist: `list_tasks`, `list_findings`.)

## Layer 2 — the LLM lead (a bee)

A `lead`-role bee (`AGENT_MODE=lead` in `corral-agent`) loops per running mission:
1. Read `list_findings(open)` + `list_tasks(mission)`.
2. If there are open findings the reflex tier left (design-flaw, or anything
   still open after reflex), feed the findings + the current task list to the LLM
   with the mutation tools and a prompt: *"You are the lead. Given these findings
   and the plan, decide what to rework. Supersede/cancel stale tasks, reopen work
   built on a broken foundation, enqueue rework. Be surgical."*
3. Apply the model's tool calls; mark the findings it acted on `addressed`.
4. Idle-poll otherwise.

The brain still owns truth; the lead is a thin client driving the mechanism, so
its decisions are auditable (every mutation is an MCP call) and the mechanism is
unit-tested independently of the model.

## Convergence & safety

- A lead-action cap per mission (like reflex) bounds re-architecting.
- The lead marks findings `addressed` when it acts, so it can't loop on the same
  finding; new findings (from reworked + re-verified work) drive new rounds until
  the swarm goes quiet (loop-until-dry).
- `cancelled`/`superseded` never block `MissionDone`.

## UI

The task panel shows `cancelled` (dimmed/struck) and `superseded` (struck, with
"→ #newid") so the re-architecture is visible — the dramatic "the lead tore up
the plan and rebuilt it" moment. (Builds on the existing task panel.)

## Testing strategy

- **mechanism:** CancelTask (non-terminal→cancelled; done stays); ReopenTask
  (done→ready); SupersedeTask (old→superseded, new has supersedes=old);
  MissionDone with cancelled/superseded (converges).
- **tools:** each mutation over MCP; supersede creates lineage; enqueue_task adds
  to a live mission.
- **lead plumbing:** the lead loop reads findings/tasks and issues mutations
  (verified with a deterministic/stub decision in test; full LLM run in the demo).
- **live e2e:** a design-flaw finding → the lead supersedes dependent tasks +
  enqueues rework → the mission converges; the UI shows superseded lineage.

## Decisions deferred to the plan

- How the lead is scheduled (a dedicated `lead` loop vs. folding into the `retro`
  phase). Likely a dedicated `AGENT_MODE=lead` loop, one per mission.
- Whether `supersede_task` auto-rewrites dependents' `depends_on` to the new task
  (probably yes, so the DAG stays valid) — settle in T1/T2.
