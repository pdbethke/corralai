# Cross-Model Attribution + Role→Model Policy — Design

**Status:** design · **Date:** 2026-07-02 · **Sub-project:** cross-model review (iteration one)

## Problem / Goal

corralai's agents are model-agnostic (each `corral-agent` picks its model at startup from
`AGENT_MODEL`/`MODEL_BACKEND`), so you can already run a swarm with different models per role.
But the system doesn't **record or surface which model did what** — "coordinated multi-agent,
multi-model" is true in practice but invisible. This makes it a bullet point, not a capability.

**Iteration one delivers the visible, attributable layer, plus a light central policy:**
- **(A) Attribution:** tag every finding with the model that filed it, and surface which model
  each agent runs — visible in the UI, queryable via analytics/oracle ("every finding the Gemini
  reviewer raised").
- **(C-light) Role→Model policy:** a brain-side `CORRALAI_ROLE_MODELS` map that **declares** the
  cross-model plan, **applies it on brain-spawn** (best-effort, pool-aware), and **reconciles** it
  against what agents actually report — all degrading gracefully, never blocking.

**Design constraint that shapes v1:** thread `model` all the way to the **fleet/telemetry layer**,
not just the local findings table, so the downstream **MotherDuck model-vs-model reports**
(empirical model evaluation on the adopter's own codebase — count/severity/type + finding
confirmation rate per model) are a query away. The report layer itself is iteration two; v1's job
is to make the data reach the fleet.

## What already exists (reuse, don't rebuild)

- **`report_host`** already captures `Model` + `Backend` per agent → an in-memory `HostBook`
  (`internal/brain/host.go`, `map[agent]Host`, latest-wins, with a last-announce `TS`).
- **`queue.Finding`** already has `Reporter` (the agent name); findings already emit a
  `finding_reported` **telemetry** event (`internal/telemetry`), which flows to the fleet sync.
- Agents self-select model at startup; the brain controls child env **only** for brain-spawned
  agents (`cmd/corral-agent/launcher.go:79` already injects `AGENT_ROLE` into `childEnv`).
- **Presence/heartbeat + claims + lease/reaper** already track liveness and reassign tasks whose
  claimer goes away — the runtime-fallback path rides these, no new router.

## Design

### 1. Attribution — model on the finding
- Add `ReporterModel` and `ReporterBackend` (both `string`, empty allowed) to `queue.Finding` and
  the `findings` table (migration: `ALTER TABLE findings ADD COLUMN ...`, back-compat: old rows =
  empty).
- Populate **at `report_finding` time, brain-side**: look up the reporter agent's `HostBook` entry
  → its `Model`/`Backend`, store on the finding (denormalized → durable/forensic; survives brain
  restart and later model changes). **No agent-side change.** Missing HostBook entry (agent never
  announced) → `""` ("unknown"); the finding still files.
- **Stamp `model` on the `finding_reported` telemetry event** — the load-bearing step that threads
  attribution to the fleet layer / MotherDuck.

### 2. Surface it
- `list_findings` returns `ReporterModel`/`ReporterBackend`, plus an optional `by_model` filter.
- **UI:** a model badge on each finding; the topology/agents view surfaces each agent's model
  (already available from `HostBook.List()`).
- **Analytics/oracle:** because the model is on the telemetry event, `mission_analytics`
  (ad-hoc SQL) and — once synced — `ask_fleet` can group findings by model.

### 3. Role→Model policy — `CORRALAI_ROLE_MODELS`
- Brain env, e.g. `CORRALAI_ROLE_MODELS="reviewer=anthropic:claude-opus,builder=ollama:qwen2.5-coder"`
  → parsed at startup to `map[role]→{Backend, Model}` (`role=backend:model` or `role=model`;
  malformed entries skipped + logged). Unset → the whole policy is inert (attribution still works).
- **Pool view — the brain knows its pool.** Derive `AvailableModels()` from `HostBook`: the
  distinct `{backend, model}` among agents heartbeating **within the presence window**. This is the
  brain's live picture of its pool and answers "is model X available?".
- **Declare.** Expose the parsed policy (a read-only field/tool) as the intended cross-model plan;
  surfaced in the topology/config view.
- **Apply-on-spawn (best-effort, pool-aware).** When the brain spawns an agent for role R
  (`spawn_subagent` → `launcher` `childEnv`): if the policy has R **and** that model is available
  (in the live pool **or** the backend is configured so it can be brought up), inject
  `AGENT_MODEL`/`MODEL_BACKEND` into `childEnv`. Otherwise **fall back** to the default/parent
  model. Never spawn a broken agent; never block.
- **Reconcile.** For each live agent, compare its *reported* model (`HostBook`) against
  `policy[role]`; expose **expected-vs-actual + a drift flag** in the topology view.

### 4. Runtime 404 fallback
- When an agent's backend call returns **404 / model-unreachable** (bad model name, endpoint
  pulled, quota exhausted): the agent **releases its task claim** (does not `complete_task`) and
  **reports the model failure** (a health signal / note), instead of spinning. The existing
  **lease/reaper + re-claim** hands the task to another available agent — possibly a *different*
  model = the fallback. Surfaced as agent-health / policy-drift.
- No circuit-breaker in v1: a persistently-404ing agent either keeps failing visibly or drops from
  presence; release+reclaim + presence is the v1 mechanism.

### 5. Data flow (unlocks the iteration-two reports)
```
report_finding ─► finding row (reporter_model) ─┐
                └► finding_reported telemetry event (model) ─► fleet sync ─► MotherDuck
                                                                              └► ask_fleet / analytics:
                                                                                 model-vs-model (count,
                                                                                 severity, type, and —
                                                                                 via finding status /
                                                                                 verification outcome —
                                                                                 CONFIRMATION rate)
```
v1 guarantees `model` reaches the fleet event. The report/dashboard is iteration two.

## Error handling / edge cases
- Missing HostBook entry at file-time → `model=""`; finding still files (never blocks).
- Unknown role in the policy → ignored. Malformed `CORRALAI_ROLE_MODELS` entry → skipped + logged.
- Model unavailable on spawn → graceful fallback to default.
- Runtime 404 → release + reclaim; never blocks the mission.
- Reconcile is advisory only (surfacing, never enforcement).
- `CORRALAI_ROLE_MODELS` unset → attribution works with whatever models agents self-select.

## Testing
- Finding carries the reporter's model, populated from `HostBook` at file-time; missing host →
  `"unknown"`, finding still files.
- `finding_reported` telemetry event carries `model` (the thread-to-fleet guarantee).
- `list_findings` returns and filters by model.
- `CORRALAI_ROLE_MODELS` parse: valid `backend:model` + bare `model` + malformed (skipped).
- `AvailableModels()` reflects only agents heartbeating within the presence window.
- Apply-on-spawn: injects the policy model when available; falls back to default when not.
- Reconcile flags drift when an agent's reported model ≠ `policy[role]`.
- 404 fallback: at the agent backend-call boundary, a 404/unreachable response releases the claim
  and reports (unit); the reclaim path is exercised.

## Out of scope (follow-ups)
- **MotherDuck model-vs-model report/dashboard layer** (iteration two — v1 threads the data).
- Circuit-breaker for a repeatedly-404ing model (mark "degraded", stop assigning).
- Per-task model override.
- Forcing a model on an already-running **externally-launched** agent (it's a live process — can't).
- The brain auto-spawning agents to satisfy the policy (pool-filling).
