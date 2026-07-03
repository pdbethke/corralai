# Progress Tab + Headline Demo — Design (sub-project #5)

**Status:** design · **Date:** 2026-06-29

## Where this fits

#1–#4 built the adaptive swarm end to end (directive → queue → hive → findings →
reflex fix + LLM-lead re-architecture → converge). #5 makes it **watchable and
runnable in one command** — the payoff:

1. **Progress tab** — a real-time view of a mission as the plan it is: the steps
   the brain laid out, who's working each, completion, and rerouting/re-planning
   (cancelled/superseded/rework). (The user's explicit goal.)
2. **Headline demo** — `make demo-mission`: one command brings up the brain + a
   scaled hive of queue bees + a lead bee + auto-seeds a directive, so you watch
   it build and re-think itself.

## Goal

A "progress" tab in the swarm UI shows each running/recent mission's directive,
status, and its task plan (steps with status, assignee, and supersession
lineage) plus its findings, live over SSE; and `make demo-mission` spins the
whole loop up on one command against the bundled GPU Ollama.

## Global constraints

- Reuse the existing SSE `/api/state` stream (the page already re-renders per
  frame) — add `missions` to it; the tab is a DOM view, no new transport.
- The Progress tab is additive — the swarm/memory tabs are untouched
  ([[feedback_library_ui_is_load_bearing]]).
- The demo reuses the existing bundled-Ollama compose; queue-mode bees + a lead.

## Part 1 — Progress tab

### Data: missions in `/api/state`

`ui.Handler` gains the mission store; `snapshot()` adds:

```
missions: [ { id, directive, status, created_ts } ]
```

Tasks (already in state, with `mission_id`, `status`, `claimed_by`, `supersedes`,
`title`, `key`) and findings (with `mission_id`, `severity`, `type`, `status`)
are grouped per mission by the page.

### The tab

A third tab `progress` (beside `swarm`, `memory`). For each mission, newest
first:
- header: `directive` + a status pill (`running`/`done`).
- the **plan**: its tasks as ordered steps, each showing the title, role, a
  status dot (pending/ready/claimed/done/cancelled/superseded), the assignee
  (`← bee`), and lineage (`superseded → replacement-key`). Reflex/lead-added
  tasks (`fix-f*`, `verify-f*`, `build-v2`, …) appear inline as they're created —
  so re-planning is visible as it happens.
- the **findings**: severity-colored, with status (open/addressed).
- a one-line progress summary (`4/7 done · 1 superseded · 2 findings`).

Real-time: the existing `EventSource('/events')` handler already calls the
render path each frame; `renderProgress()` joins it.

## Part 2 — Headline demo (`deploy/demo`)

A `mission` profile + `make demo-mission`:
- `brain` + bundled `ollama` (+ pull) as today.
- a **scaled queue hive**: builder/tester/pentester/reviewer bees in
  `AGENT_MODE=queue` (the pull model). "More bees = better" via compose `--scale`
  / multiple replicas.
- a **lead** bee in `AGENT_MODE=lead`.
- a one-shot **seed** service that waits for the brain healthy then runs
  `corral-admin mission create "$DEMO_DIRECTIVE"` (default: "build me a World Cup
  scores dashboard").
- `make demo-mission` → open `http://localhost:9019` → the **Progress tab** shows
  the directive's plan filling in, bees claiming steps, findings appearing, and
  the lead/reflex re-planning — live.

README documents it and the `DEMO_DIRECTIVE` / scale knobs.

## Testing strategy

- **UI:** `/api/state` includes `missions` (id/directive/status); `renderProgress`
  groups tasks + findings by mission (covered by a state test for the missions
  field; render is exercised live).
- **compose:** `docker compose --profile mission config` validates; the seed
  one-shot creates a mission.
- **live e2e:** bring up brain + a couple queue bees + the seed (no GPU needed if
  bees use a stub/short model, or drive with the e2e bee); confirm the mission's
  plan appears in `/api/state.missions` + tasks and progresses.

## Decisions deferred to the plan

- Whether the Progress tab also renders a dependency arrow between steps (nice;
  start with ordered steps + lineage, add arrows only if cheap).
- Bee replica counts / whether to expose queue-depth autoscaling (start fixed via
  compose scale; note autoscaling as future).
