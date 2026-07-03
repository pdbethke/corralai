# The Learning Loop — Plan (sub-project #8)

> TDD where it fits, commit per task on `feat/learning-loop`. Design:
> `docs/superpowers/specs/2026-06-29-learning-loop-design.md`.

## Global Constraints
- Lessons persist in the existing memory engine (type=lesson); active injection
  over passive search; recurrence surfaced, never silent.

## Task 1: connect the swarm to memory (foundation)
- agent: search_memory + add_memory in agentTools + dispatch routing; brain()
  must NOT clobber add_memory's `name` (slug) with the agent name.
- [ ] build + manual; commit.

## Task 2: lessons as a memory type + recall
- memory.RecallLessons(query, k) over type=lesson; retro/lead prompt records
  lessons via add_memory(type=lesson, shared).
- [ ] test RecallLessons; commit.

## Task 3: active injection into missions
- mission gains a read dep on memory; CreateMission prepends relevant lessons to
  the seed plan's phase instructions.
- [ ] test: seeded lessons appear in phase instructions; commit.

## Task 4: recurrence detection
- on finding record, flag a finding that matches a prior addressed finding /
  lesson (type+target) as recurring; surface + escalate.
- [ ] test; e2e; full suite + vet; commit; merge.

---

## Status: COMPLETE (2026-06-29)

T1 swarm↔memory wiring (`0ad5d1b`) · T2/T3 recall + active injection (`ddd28b3`) ·
T4 recurrence detection. `go test ./...` + `go vet` green.

The loop, persisted in the existing memory engine: retro records `type=lesson`
entries → create_mission recalls lessons relevant to the directive and INJECTS
them into every phase's instructions → a finding that repeats a prior
(type,target) is flagged `recurring` (shown ↻ in the UI). Proven end to end by an
integration test: a seeded lesson is injected into a new mission's task
instructions. A recorded mistake now changes the next mission's plan — that earns
the word "learns." (Outcome-weighted promotion / lesson→reflex-rule escalation
remain deeper follow-ups.)
