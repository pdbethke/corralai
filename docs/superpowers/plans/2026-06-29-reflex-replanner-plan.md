# Reflex Re-planner — Implementation Plan (sub-project #3)

> TDD, commit per task on `feat/reflex-replanner`. Design:
> `docs/superpowers/specs/2026-06-29-reflex-replanner-design.md`.

**Goal:** open findings ≥ threshold deterministically spawn fix + re-verify tasks
each tick; findings flip open→addressed; the mission converges. No LLM (that's #4).

## Global Constraints

- Deterministic + idempotent (a finding remediated once); per-mission reflex-task
  cap, logged when hit. Reuse queue primitives only; no schema change.

---

## Task 1: reflex rules + engine.replan (`internal/mission/replan.go`)

`reflexRules(f) ([]queue.TaskSpec, bool)`; `Engine.ReflexMinSeverity`/`ReflexMaxTasks`
(defaults high/50 in NewEngine); `Engine.replan(missionID)`; wire `Tick` to
`replan → PromoteReady → MissionDone`.

- [ ] Failing tests: rules (vuln→fix+verify(pentester); bug→verify(tester);
  design-flaw/note→not actionable); replan enqueues + marks addressed; low-sev
  left open; idempotent re-run; cap stops runaway; full loop via Tick
  (report→fix→verify→done).
- [ ] Implement; `go test ./internal/mission/`. Commit.

## Task 2: main wiring + live e2e

- [ ] main: read `CORRALAI_REFLEX_MIN_SEVERITY` / `CORRALAI_REFLEX_MAX_TASKS`
  onto the engine.
- [ ] Live e2e through a real brain: report a HIGH vuln; confirm fix-f<id> +
  verify-f<id> appear in the queue, the finding shows addressed, and completing
  them converges the mission to done.
- [ ] Full `go test ./...` + `go vet` green. Commit. Merge.

---

## Status: COMPLETE (2026-06-29)

T1 reflex core (`c54f3c1`) + T2 main wiring. `go test ./...` + `go vet` green.

**Live e2e (real brain):** `mission create` → bee drained the seed plan → the
secops bee reported a HIGH vuln → the reflex re-planner enqueued `fix-f1`
(builder) + `verify-f1` (pentester, dep) — *not in the seed plan* → the bee
completed both in dependency order → the mission **converged to done** and the
finding shows **addressed**. The first half of the adaptive loop (finding →
deterministic remediation → converge) is live. The LLM lead + supersession is #4.
