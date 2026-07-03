# Mission Telemetry & DuckDB Analytics — Design (sub-project #9)

**Status:** design · **Date:** 2026-06-29

## Where this fits

The operational stores (queue/mission, SQLite) hold each mission's *final state*
with timing + agents. #9 adds the *timeline*: an append-only **event log in
DuckDB** the brain emits to at every lifecycle point, plus an analytics surface —
so you can ask "where do missions stall, who's the bottleneck, how often do we
re-plan, what's the accept rate" with real columnar queries.

## Goal

Every significant mission event (create, enqueue, claim, complete, reap, finding,
supersede/cancel/reopen, sprint, review, done) is recorded as a timestamped event
in a DuckDB store; the data is queryable both via named reports (a brain tool /
`corral-admin analyze`) and directly (the `.duckdb` file, any DuckDB client); and
it can sync to MotherDuck alongside the coord fleet stream.

## Components

### 1. `internal/telemetry` store (DuckDB)

```sql
CREATE TABLE events (
  id         BIGINT PRIMARY KEY,
  ts         DOUBLE  NOT NULL,
  mission_id BIGINT  NOT NULL DEFAULT 0,
  kind       VARCHAR NOT NULL,   -- mission_created | task_enqueued | task_claimed | task_completed |
                                 -- task_reaped | task_cancelled | task_superseded | task_reopened |
                                 -- finding_reported | finding_recurring | sprint_started |
                                 -- review_accepted | review_changes | mission_done | lesson_recorded
  actor      VARCHAR,            -- agent / principal / 'engine'
  subject    VARCHAR,            -- task key / finding target / etc.
  detail     VARCHAR             -- JSON
);
```

API: `Record(Event)` (append, non-blocking-friendly); `RunReport(name) (Report,
error)` with a fixed set of named analytic queries; `Query(sql) (Report, error)`
read-only ad-hoc (operator). `Report = {Columns []string, Rows [][]any}`. `var
now` seam.

Named reports (MVP): `missions` (per-mission event counts + first→last duration),
`agents` (task_completed throughput by actor), `kinds` (event counts), `findings`
(by type/severity + recurring count), `replans` (supersede/cancel/reopen counts),
`sprints` (review_changes vs review_accepted).

### 2. Emit events (brain + engine + reaper)

A `telemetry.Store` on `Options`; emit at each point:
- create_mission → `mission_created` (+ `lesson_recorded` when lessons injected)
- claim_task → `task_claimed`; complete_task → `task_completed` (+
  `finding_reported`/`finding_recurring` per finding)
- report_finding → `finding_reported` (+ `finding_recurring` if flagged)
- cancel/supersede/reopen/enqueue → matching events
- review_mission → `review_accepted` / `review_changes` (+ `sprint_started`)
- engine: `task_enqueued` (at create), `mission_done`; reaper: `task_reaped`

Recording is best-effort and must never block or fail a tool (log + continue).

### 3. Analytics surface

- Brain tool `mission_analytics { report?, sql? }` → runs a named report (or
  read-only ad-hoc SQL) and returns columns+rows. Gated to superusers for `sql`.
- `corral-admin analyze [report] [--sql "..."]` renders it as a table.
- Direct: the `.duckdb` file (`CORRALAI_TELEMETRY_DB`) is queryable by `duckdb`
  CLI / any client for unrestricted ad-hoc analysis.

## Testing strategy

- **store:** Record + RunReport("agents"/"kinds"/"findings") returns expected
  aggregates over seeded events; Query read-only.
- **emission:** a create→claim→complete→finding→done sequence over MCP produces
  the expected events (count by kind).
- **e2e:** drive a mission; `corral-admin analyze agents` shows throughput.

## Decisions deferred to the plan

- MotherDuck sync of telemetry (reuse `internal/fleet`) — note now, wire later.
- Whether `task_claimed` is recorded on every claim incl. re-claims after reap
  (yes — that's the point of the timeline).
- Read-only enforcement for ad-hoc `sql` (superuser + a SELECT-only guard).
