# Client + Feedback + Sprints — Plan (sub-project #6)

> TDD, commit per task on `feat/client-sprints`. Design:
> `docs/superpowers/specs/2026-06-29-client-feedback-sprints-design.md`.

**Goal:** review-enabled missions converge on client ACCEPTANCE; feedback (human
or client agent) → change-request findings + sprint++ → the lead re-plans the
next round. Opt-in; reuses the adaptive loop; bounded by a sprint cap.

## Global Constraints

- Per-mission opt-in (`requires_review`); non-review missions unchanged.
- Feedback → `change-request` findings → LLM lead routes rework (reflex ignores).
- Sprint cap; logged when hit.

---

## Task 1: lifecycle mechanism (mission + queue + engine + review tool)

- queue: add `change-request` finding type.
- mission: `sprint` + `requires_review` columns (+ migrations); `Mission.Sprint`/
  `.RequiresReview`; `Sprint()`/`BumpSprint()`; `CreateMission(…, requiresReview)`.
- engine: review-enabled + queue-drained + no open change-request → `awaiting_review`
  (else existing `done`).
- brain `review_mission {id, accept, feedback?}`: accept → done; else →
  change-request finding (reporter client) + BumpSprint + running; sprint cap.
- update CreateMission callers + create_mission tool (`requires_review`).

- [ ] Tests: review mission drains → awaiting_review (not done); accept → done;
  feedback → running + sprint 2 + change-request finding; no re-gate while a
  change-request is open; non-review mission still auto-completes; sprint cap.
- [ ] Implement. `go test ./...`. Commit.

## Task 2: human-as-client (tool + UI)

- `corral-admin review <id> --accept | --changes "..."`.
- UI: awaiting_review banner + Accept / Request-changes on the Progress tab →
  `/api/review`.

- [ ] Test `/api/review`; implement; commit.

## Task 3: client agent (`AGENT_MODE=client`)

- Per awaiting_review mission: read directive + deliverable notes; LLM decides
  accept/feedback; calls review_mission. Bounded by sprint cap.

- [ ] Implement; build; commit.

## Task 4: demo + live e2e

- client bee in the mission profile; Progress tab shows sprint + the gate.
- [ ] e2e: review mission → awaiting_review → feedback → sprint 2 → accept → done.
- [ ] Full `go test ./...` + `go vet`. Commit. Merge.

---

## Status: COMPLETE (2026-06-29)

T1 mechanism (`c2d9386`) · T2 human review (`552ab20`) · T3 client agent
(`113c63e`) · T4 demo + e2e. `go test ./...` + `go vet` green.

**Live e2e (real binaries):** a `--review` mission's build drained → parked at
`awaiting_review` (not done); `corral-admin review 1 --changes "..."` → running,
**sprint 2**, change-request finding opened; `corral-admin review 1 --accept` →
done. The full client loop — gate → feedback → next sprint → accept — works
end to end. The autonomous version (client + lead bees) ships in `make
demo-mission` (seeded `--review`, client bee `AGENT_MODE=client`).

This completes sub-project #6 — the swarm now models the whole org: client ↔
team, with sprints.
