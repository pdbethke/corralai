# Mission Telemetry & DuckDB Analytics — Plan (sub-project #9)

> Design: docs/superpowers/specs/2026-06-29-telemetry-analytics-design.md

## Status: COMPLETE (2026-06-29)

- T1 telemetry store (`0a6c543`) — DuckDB events + named reports + read-only Query.
- T2 emit events (`a5625c0`) — brain tools record mission_created, task_claimed,
  task_completed, finding_reported, cancel/reopen/supersede/enqueue, review_*.
- T3 analytics surface — mission_analytics MCP tool + `corral-admin analyze
  [report] | --sql`.
- T4 live e2e — a mission run produced 12 task_claimed/completed, the finding,
  and mission_created; `analyze agents/kinds/findings` and ad-hoc SQL all work.

The execution timeline is recorded in DuckDB and analyzable (named reports, ad-hoc
SQL, or any DuckDB client against CORRALAI_TELEMETRY_DB). Engine-internal events
(mission_done, task_reaped) and MotherDuck sync of telemetry are follow-ups.
