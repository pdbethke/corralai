# The Learning Loop — Design (sub-project #8)

**Status:** design · **Date:** 2026-06-29

## Where this fits

corralai began as a memory-sharing engine. The swarm *adapts* within a mission
(findings → re-planning) and *can* recall prior notes, but it doesn't **learn**:
recorded lessons are passive prose, nothing enforces them, and nothing checks a
mistake stops recurring. #8 closes that loop — and does it on the **existing
memory engine** rather than a parallel system.

It first fixes a foundation gap: **the agents can't currently use the memory
engine at all** (the brain has memory tools; the bees' toolset doesn't include
them — the "record in SHARED memory" phase instructions are aspirational).

## Goal

Lessons learned from a mission persist as first-class entries in the memory
corpus; future missions automatically carry the lessons relevant to their
directive (injected into phase instructions, not left to chance); and a finding
that recurs despite a lesson is detected and escalated — so a recorded mistake
provably changes the next mission's plan.

## Global constraints

- Reuse `internal/memory` (DuckDB) — lessons are a memory `type`, not a new store.
- Active over passive: lessons are *injected* into instructions, not merely
  searchable.
- Bounded + honest: injection is capped (top-N relevant lessons) and logged;
  recurrence detection is best-effort, surfaced, never silent.

## Components

### 1. Connect the swarm to memory (the foundation)

Add `search_memory` and `add_memory` to `corral-agent`'s `agentTools()` + route
them in `dispatch`. Now the bees can actually read/write the corpus the phase
instructions already reference. (get/list/promote stay operator-side.)

### 2. Lessons as a memory type

A lesson is a memory entry with `type=lesson`, body = *context/trigger → what
went wrong → corrective guidance*. Captured by:
- the **retro/lead** (instructed to record concrete lessons via `add_memory`,
  type lesson), and
- optionally a brain-side auto-distill when a HIGH finding is addressed (a
  deterministic lesson seed). Start with retro-recorded; auto-distill is a knob.

### 3. Active injection (the behavior-changer)

At `CreateMission`, the planner queries memory for lessons relevant to the
directive (`memory.RecallLessons(directive, k)` — FTS/semantic over `type=lesson`)
and **prepends** them to the seed plan's phase instructions:

> "Lessons from past missions — apply these: 1) … 2) …"

So every relevant phase carries the applicable lessons automatically. The mission
package gains a (one-way) read dependency on the memory store.

### 4. Recurrence detection (the measurement that earns "learning")

When a finding is recorded, check whether a prior **addressed** finding (or a
lesson) covers the same `type+target` for past missions. If so, mark it
`recurring` and surface it (the lesson didn't land) — and bump the lesson so it's
injected more prominently / escalated to a reflex check. This is the feedback
signal: lessons that fail to prevent recurrence are visible, not silent.

## Data flow

1. Mission runs; retro records `lesson` entries ("the score API needs
   parameterized queries — SQLi recurred in build").
2. Next mission for a related directive: planner injects that lesson into the
   build + secops phase instructions.
3. If a SQLi finding recurs anyway → flagged `recurring`, lesson escalated.

## Testing strategy

- **memory:** `RecallLessons` returns `type=lesson` entries ranked for a query;
  ignores non-lessons.
- **agent wiring:** `search_memory`/`add_memory` reachable + routed (build/manual;
  covered by the brain memory tests already).
- **injection:** `CreateMission` with seeded lessons prepends them to the
  relevant phase instructions (deterministic test over the plan text).
- **recurrence:** a finding matching a prior addressed finding for the mission
  lineage is flagged recurring.
- **e2e:** record a lesson, create a new mission, see the lesson in the phase
  instruction; reproduce a finding, see it flagged recurring.

## Decisions deferred to the plan

- Lesson relevance: FTS keyword vs. embedding similarity (start FTS — the memory
  engine already has it; embeddings are the RAG's job).
- Auto-distill lessons from findings (on by default vs. retro-only).
- Whether a recurring finding auto-creates a reflex rule (escalation depth).
