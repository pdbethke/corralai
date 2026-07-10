<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Per-task complexity router — design

**Date:** 2026-07-09
**Status:** approved (brainstorm) → spec for review
**One line:** The engine scores each task's complexity and routes it to the model
that its own gate-verified metrics say is the **best fit** — high success first,
then fastest, weighted harder for complex work — re-evaluated continuously from
live telemetry.

## Why (the differentiator)

Today a model is assigned **per role** (`CORRALAI_ROLE_MODELS` / the Mission
Composer) and **per mission** (the staffing Judge). Within one mission, an easy
"fix a typo" builder task and a hard "design the auth model" builder task run on
the *same* model — wasteful on the easy one, under-powered on the hard one. And
the choice is a human/config decision, not the herd's own results.

This feature makes selection **metrics-driven and continuous**: the engine
constantly re-evaluates agents and models on **success and efficiency** (the
gate-earned leaderboard) and routes each task to the best fit for its difficulty.
No static tier ladder, no manual tuning — a model that proves itself gets the next
hard task automatically. It's the corralai thesis (a deterministic correctness
gate feeding metrics-fed cognition) applied at task granularity.

## What exists to build on

- `classifyComplexity(directive) Tier` (`internal/mission/complexity.go`) — a
  deterministic keyword+length heuristic → lean/standard/full. Sizes the *plan*.
- `BuildLeaderboard(q, hosts, tel) (Leaderboard, error)` (`internal/brain/
  leaderboard.go`) — the model×role matrix from verify-gate telemetry.
  `LeaderboardCell` has exactly the signals we need: **`ExecPassRatePct`** (success),
  **`AvgTaskDuration`** (speed, claim→done), **`ReworkCount`** (friction),
  **`Samples`** (confidence). Computed on-demand today (only `ui.go:359`).
- `resolveSpawnModel(pol, book, role, now) (ModelRef, bool)`
  (`internal/brain/subagents.go`) — where a role's model is resolved at spawn.
- The staffing Judge (Sense→Judge→Clamp, `internal/mission/routing.go`) already
  ranks models per-mission over the leaderboard — we **extend** that idea to
  per-task, sharing the ranking, not duplicating it.

## Design

### 1. Per-task complexity — `classifyTaskComplexity(role, instruction) Tier`

A sibling to `classifyComplexity` in `complexity.go`, tuned for a *task* rather
than a directive: run the same `fullSignals`/`leanSignals` scan on the task's
instruction, then apply a **role floor** so structurally-hard roles never
under-score:

- Floor to at least Standard: `designer`, `researcher`, `pentester`, `reviewer`,
  `integrator` (reasoning/judgment-heavy).
- May reach Lean: `tester`, `writer`/`docs`, and `builder` on a short, signal-free
  instruction.
Deterministic, no LLM. Absent/empty instruction → the mission's own tier.

### 2. Store the tier on the task

Add `Tier string` (`"lean"|"standard"|"full"`, default `""`) to `queue.TaskSpec`
and `queue.Task` (+ a `tier` column, idempotent migration). Set it at enqueue:
`planToTasks` scores each phase→task, and `enqueue_task` scores its task. `""` =
untiered → the router treats it as the mission default (backward-compatible: old
rows and untiered callers behave exactly as today).

### 3. Continuously re-evaluated leaderboard — `LeaderboardCache`

Recomputing `BuildLeaderboard` per spawn is too costly. Add a small
`LeaderboardCache` (`internal/brain`): holds the latest `Leaderboard` behind an
RWMutex, refreshed by a goroutine every `refreshInterval` (default **30s**) via
`BuildLeaderboard`. The router reads the cached snapshot (cheap, lock-read). This
IS the "constantly re-evaluates" engine — as verify-gate telemetry lands, the next
refresh reshapes routing. Nil cache / not started → router degrades to today's
per-role behavior.

### 4. The metrics pick — `pickTaskModel(lb, role, tier, pool) (ModelRef, bool)`

`internal/brain` (co-located with the leaderboard so it can share ranking with the
Judge). Given the cached leaderboard, the role, the task tier, and the available
model `pool` (live-connected models, as `resolveSpawnModel` already computes):

1. **Eligibility — success is the gate (the "important").** Keep only cells where
   `Role == role`, `Model ∈ pool`, `Samples ≥ sampleFloor` (default **5** — cold
   start), `ExecPassRatePct ≥ successGate` (default **70**), and
   `ReworkCount/max(TasksCompleted,1) ≤ reworkCap` (default **0.34**). A flaky
   model is never chosen no matter how fast.
2. **No eligible cell → return `(_, false)`** — the caller falls back to the role's
   configured model (cold-start honesty; never route on a guess).
3. **Rank the eligible by a tier-weighted score; argmax wins.** Normalize each
   candidate across the eligible set: `s = ExecPassRatePct/100`,
   `d = 1 − minmax(AvgTaskDuration)` (faster → higher), `r = 1 − rework_ratio`.
   `score = wS(tier)·s + wD(tier)·d + wR·r`. Weights encode the rule *"faster at
   more complex decisions + good success → choose it"*:
   - **lean:** `wD` dominant (cheapest/fastest qualifier); `wS` modest.
   - **full:** `wS` and `wD` both high (reward the agent that is fast **and**
     reliable on hard work); ties break toward higher success.
   - **standard:** balanced (≈ the Judge's per-mission best-fit).
   Defaults in a `routerWeights` table, tunable.

### 5. Apply at resolution

Extend `resolveSpawnModel(pol, lb, book, role, tier, now)`: if routing is enabled
and `pickTaskModel` returns a confident pick, use it; else the existing
`pol.Available(role, pool)`. Thread the task's tier from the claim/spawn path to
this call. Off by default (`CORRALAI_TASK_ROUTING=1` to enable); disabled or thin
data → byte-identical to today.

## Honest limits

- **Applies where the brain resolves the model per spawn** (apply-on-spawn /
  spawned subagents). A long-lived fixed-model `corral-agent` keeps its process
  model — it can't switch per task. Routing benefits the brain-driven execution
  path; this is stated, not hidden.
- **Efficiency = speed + rework, not $ cost** — the leaderboard has no cost signal
  yet. `AvgTaskDuration` and `ReworkCount` are the efficiency proxies; a true
  cost/token dimension is a follow-on.
- **Confidence-gated** — below `sampleFloor` the router stays out of the way. Thin
  data must not masquerade as a ranking.

## Testing (TDD)

- `classifyTaskComplexity`: role floor (a `designer` short task ≥ Standard; a
  `tester` short task = Lean); `fullSignals` in the instruction → Full; empty → "".
- `pickTaskModel`: two models both clearing the gate → the **faster** wins for
  full; a **flaky-but-fast** model (below `successGate`) is excluded even though
  it's fastest; below `sampleFloor` → `(_, false)`; lean tilts to speed, full
  rewards fast+reliable. Table-driven over synthetic `LeaderboardCell`s.
- `LeaderboardCache`: refresh replaces the snapshot; concurrent read is safe; nil
  cache → no pick.
- `resolveSpawnModel`: routing on + confident pick → tier model; routing off or
  thin → role default (unchanged).
- Enqueue sets `Tier`; a mission's plan tasks carry sensible tiers.

## Out of scope (follow-ons)

- Per-mission tier/weight config via the Mission Composer (v1 is global weights).
- Cost/token efficiency dimension in the leaderboard.
- Tier-aware *claiming* for long-lived fixed-model workers.
- Surfacing per-task routing decisions in the cockpit/replay (a nice observability
  add — the leaderboard tab could show "why this model won this task").

## Positioning (post-ship)

Surface this as the headline **differentiator** in README + ROADMAP + a field
note: *the engine continuously re-evaluates every agent and model on its own
gate-verified success and efficiency, and routes each task to the best fit* — no
static config, the herd's results decide. See [[corralai-continuous-reevaluation-differentiator]].

## Decisions (defaulted; revisit in review)

- Success is a hard **gate**, speed the **tiebreaker**, weighted harder for complex.
- Defaults: `sampleFloor=5`, `successGate=70%`, `reworkCap=0.34`, `refreshInterval=30s`.
- Role floor set: designer/researcher/pentester/reviewer/integrator ≥ Standard.
- Off by default (`CORRALAI_TASK_ROUTING`); degrade-to-role-default when unsure.
- Efficiency = duration + rework (no cost signal yet — honest).
