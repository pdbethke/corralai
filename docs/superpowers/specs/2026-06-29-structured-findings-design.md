# Structured Findings — Design (sub-project #2)

**Status:** design · **Date:** 2026-06-29

## Where this fits

Sub-project #2 of the adaptive swarm. #1 gave us the pull model (directive →
task queue → hive). #2 adds the **feedback channel**: when a bee finishes a
task it can emit a *structured finding* — not a prose summary, but an actionable
record (`{type, severity, target, evidence, suggested_action}`) the re-planner
will act on mechanically in #3/#4.

This sub-project makes findings a first-class, stored, queryable, **UI-visible**
artifact. It deliberately stops short of acting on them: turning a finding into
new tasks (reflex rules) is #3; the LLM lead + supersession is #4.

The payoff even without re-planning: the dramatic "pentester finds a HIGH vuln"
moment becomes *visible* in the swarm UI — a red finding lights up — setting the
stage for the swarm to re-think itself in #3/#4.

## Goal

A bee can report a structured finding (standalone, or attached as it completes a
task); findings are stored alongside the mission's task state, queryable by
mission/status/severity, surfaced live in the swarm UI (high-severity ones
prominent), and listable by an operator via `corral-admin findings`.

## Global constraints

- Engine: SQLite, the `coord`/`queue` recipe. Findings live in the **queue
  store** (`internal/queue`) — the same DB and package that owns mission
  execution state — so the #3 re-planner reads open findings and the task queue
  from one place. (Files that change together live together.)
- Resiliency: emission is append-only and idempotent-friendly; a malformed
  finding is rejected, never silently dropped.
- UI-visible from day one: findings (with severity) are in `/api/state`; the
  page renders them, high/critical prominently.
- Scope fence: NO re-planning here. `status` is tracked (`open` default) but
  nothing transitions it to `addressed` automatically — that's #3. Reserved for
  later, exercised only by the manual `corral-admin findings resolve`.

## Data model

Add to the queue store's SQLite DB:

```sql
CREATE TABLE findings (
  id               INTEGER PRIMARY KEY,
  mission_id       INTEGER NOT NULL,
  task_id          INTEGER NOT NULL DEFAULT 0,   -- the task that surfaced it; 0 = standalone
  reporter         TEXT    NOT NULL,             -- the bee that reported it
  type             TEXT    NOT NULL,             -- vuln|bug|design-flaw|missing-req|regression|note
  severity         TEXT    NOT NULL,             -- low|medium|high|critical
  target           TEXT    NOT NULL DEFAULT '',  -- file / area
  evidence         TEXT    NOT NULL DEFAULT '',  -- what was observed
  suggested_action TEXT    NOT NULL DEFAULT '',  -- proposed fix
  status           TEXT    NOT NULL,             -- open|addressed|dismissed
  created_ts       REAL    NOT NULL
);
CREATE INDEX ix_findings_mission ON findings(mission_id);
CREATE INDEX ix_findings_status  ON findings(status);
```

`Finding` Go struct mirrors it. `SeverityRank(sev) int` (low=0…critical=3) for
threshold logic (#3) and UI ordering. Unknown type/severity is rejected by
`AddFinding` (validated against the allowed sets).

## Components

### 1. `internal/queue/findings.go` (the store)

- `AddFinding(f Finding) (int64, error)` — validate type+severity, insert `open`,
  return id.
- `Findings(missionID int64, status string) ([]Finding, error)` — list,
  newest first; empty `status` = all. (`OpenFindings` = `Findings(m,"open")`.)
- `AllFindings() ([]Finding, error)` — fleet-wide, recent, for the live UI.
- `SetFindingStatus(id int64, status string) (bool, error)` — manual resolve/
  dismiss (the only status transition in #2).

### 2. Brain MCP tools (`internal/brain/tasks.go`, queue-gated)

- `report_finding { name, mission_id, task_id?, type, severity, target?, evidence?, suggested_action? }`
  → `{ id }`. The emission path a bee calls when it finds something.
- `complete_task` gains an optional `findings: []` — report findings *as* you
  finish a task, in one call (the common case for tester/pentester/reviewer).
- `list_findings { mission_id?, status? }` → `{ findings: [] }` — observability.

These are gated on `Options.Queue`, so existing tool counts are unchanged.

### 3. `corral-agent` (`cmd/corral-agent`)

- `report_finding` added to `agentTools()` and routed in `dispatch` to the brain
  (carrying the bee's `name` + `mission_id`).
- The role prompts nudge investigative roles to report: a pentester/tester/
  reviewer is told to call `report_finding` with a severity when it finds a vuln/
  bug/issue. The mission id is passed into `runTask` (the task carries it).

### 4. UI (`internal/ui`)

- `/api/state` gains a `findings` array (from `AllFindings`, capped).
- The page renders a **findings panel**: each finding shows severity (color-
  coded — critical/high red, medium amber, low muted), type, target, reporter.
  High/critical findings are visually prominent (the "vuln found" alert).

### 5. `corral-admin` (`cmd/corral-admin`)

- `corral-admin findings [--mission N] [--status open]` → `list_findings`,
  printed as a severity-sorted table. (`--json` for machines.)

## Data flow

1. A pentester bee, finishing its `pentest` task, calls `complete_task` with a
   finding `{vuln, high, "score-API", "SQLi in query param", "parameterize"}` —
   or `report_finding` mid-task.
2. The brain validates + stores it `open`.
3. `/api/state` carries it; the UI lights up a red HIGH finding.
4. An operator runs `corral-admin findings` and sees it.
5. (In #3) the reflex re-planner reads `OpenFindings` and enqueues a fix +
   re-verify — out of scope here.

## Testing strategy

- **store:** AddFinding validates type/severity (rejects junk); Findings filters
  by status; SeverityRank ordering; SetFindingStatus transitions + idempotency.
- **brain:** report_finding stores and returns an id; complete_task with findings
  stores them and marks the task done atomically-enough (task done + findings
  present); list_findings filters; invalid severity → tool error.
- **UI:** `/api/state` includes findings with severity + reporter.
- **live e2e:** report a finding through a real brain; see it in `/api/state` and
  `corral-admin findings`; confirm it persists and is filterable by status.

## Decisions deferred to the plan

- Whether `complete_task`'s findings reuse the `report_finding` input shape
  (one struct) — almost certainly yes (DRY).
- UI panel placement (likely top-right, mirroring the task panel top-left).
