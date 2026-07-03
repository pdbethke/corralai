# Client + Feedback + Sprints — Design (sub-project #6)

**Status:** design · **Date:** 2026-06-29

## Where this fits

#1–#5 model a dev *team* (research→design→build→verify→integrate→docs→retro) with
an LLM lead orchestrating and an adaptive find→re-plan loop. #6 adds the layer
*above* the team — the **client/stakeholder** — and the **cadence** (sprints):

- A mission converges when the **client accepts**, not merely when the queue
  drains.
- **Feedback** (from a human or a modeled client agent) is a first-class input
  that drives the next sprint's rework.

This closes the outermost loop of real delivery: stakeholder ↔ team.

## Goal

A review-enabled mission, once its queue drains, enters **awaiting_review**
instead of completing; a **client review** either **accepts** it (→ done) or
submits **feedback** that becomes change-request findings + bumps the **sprint**
and sends it back to running for another round. The review can come from the
**human operator** (UI / corral-admin) or a modeled **client agent**
(AGENT_MODE=client) that reviews the deliverable against the directive.

## Global constraints

- **Opt-in:** the review gate is per-mission (`requires_review`). Existing
  one-shot missions/demos (no review) keep auto-completing — no behavior change
  for anything that doesn't ask for the loop.
- Reuse the adaptive machinery: feedback → **change-request findings** → the LLM
  lead re-plans (judgment tier; reflex ignores change-request). No new re-planner.
- Convergence preserved + bounded: a sprint cap stops an endless feedback loop;
  hitting it is logged.

## Mission lifecycle (review-enabled)

```
running ──(queue drained, no open change-requests)──▶ awaiting_review
awaiting_review ──client accepts──▶ done
awaiting_review ──client feedback──▶ running   (sprint++, change-request findings opened)
                                       │
                                       └─ lead re-plans the feedback into rework → drains → awaiting_review …
```

`missions.status` gains `awaiting_review`; `missions.sprint` (int, default 1)
tracks the round.

## Components

### 1. Mechanism (mission + queue + engine) — this checkpoint (T1)

- `mission`: `sprint` column; `Sprint()`/`BumpSprint()`; `requires_review` column;
  `CreateMission` takes `requiresReview bool`.
- `engine.Tick`: on a review-enabled mission, queue-drained **and no open
  change-request findings** → `awaiting_review` (else the existing `done`). The
  "no open change-request" guard keeps it from re-gating before the lead has
  turned fresh feedback into rework.
- `SubmitReview(missionID, accept bool, feedback string)` orchestration (brain
  tool `review_mission`): accept → `done`; else → add a `change-request` finding
  (reporter `client`, the feedback as evidence) + `BumpSprint` + status `running`.
- `change-request` added to the finding types.

### 2. Human-as-client (T2)

- `review_mission` MCP tool (above) + `corral-admin review <id> --accept` /
  `--changes "..."`.
- UI: a mission in `awaiting_review` shows an **"awaiting your review"** banner on
  the Progress tab with **Accept** / **Request changes** controls (posts to a
  new `/api/review` endpoint, like `instruct`).

### 3. Client agent (T3)

- `AGENT_MODE=client`: per `awaiting_review` mission, read the directive + the
  deliverable/notes from shared memory, and via the LLM decide **accept** or
  **feedback**, calling `review_mission`. Bounded by the sprint cap.

### 4. Demo + e2e (T4)

- A `client` bee in the mission profile (or human review); the Progress tab shows
  sprint number, the awaiting-review gate, feedback → next sprint → re-converge.

## Testing strategy

- **mechanism (T1):** review-enabled mission drains → `awaiting_review` (not
  done); accept → `done`; feedback → `running` + sprint=2 + a `change-request`
  finding; the engine does NOT re-gate to awaiting_review while a change-request
  is open; a non-review mission still auto-completes (no regression); sprint cap.
- **tools (T2):** `review_mission` accept/feedback over MCP.
- **client agent (T3):** plumbing (drives review_mission); full LLM run in demo.
- **live e2e (T4):** seed a review mission → drain → awaiting_review → feedback →
  sprint 2 rework → accept → done.

## Decisions deferred to the plan

- Whether feedback also reopens specific phases (design/build) vs. only opens a
  change-request the lead routes (start with the latter — the lead decides).
- Sprint cap default (e.g. 5).
- Discussions (synchronous agent-to-agent threads) are explicitly OUT — a
  separate philosophical shift from the current stigmergic coordination.
