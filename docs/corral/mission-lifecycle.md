# mission-lifecycle

> **RETIRED FLOW.** This document describes the build-from-directive mission
> loop (directive -> plan -> queue -> build -> reflex/lead re-planning ->
> client review), which is being retired as corral re-focuses to a reactive
> audit/certification gate (the repo gate + control gate are the current live
> surface — see `README.md`'s "What runs today"). Some symbols referenced below
> (e.g. `internal/mission/replan.go`) have already been deleted. Kept for
> reference pending a rewrite; see
> `docs/superpowers/specs/2026-07-13-corral-refocus-audit-not-builder-design.md`.

## Directive -> plan -> queue

`create_mission` (`internal/brain/missions.go`) takes a directive and either a
custom `plan` or, when omitted, `mission.DefaultPlan(directive)`
(`internal/mission/store.go`): research -> design -> build-core -> build ->
test ∥ secops ∥ perf -> integrate -> docs -> retro. `CreateMission` persists the
phases and calls `q.Enqueue` to turn the plan into executable `queue.TaskSpec`s
the herd pulls via `claim_task`.

## Findings -> reflex re-planning

Any phase can `report_finding` (`internal/brain/tasks.go`) — a vuln, bug, or
design flaw with a severity. The mission engine's `replan` step
(`internal/mission/replan.go`) turns open findings at or above
`ReflexMinSeverity` into deterministic fix + re-verify tasks via
`reflexRules`, deduplicating recurring findings and capping total reflex tasks
at `ReflexMaxTasks` so the loop can't run away. This is the automatic,
rule-based half of re-planning.

## Lead re-planning

Beyond reflex, an LLM acting as lead (`actorOf` defaults to `"lead"` when no
verified principal, `internal/brain/telemetry.go`) can actively reshape a
running mission with `cancel_task`, `retarget_dependencies`, `reopen_task`, and
`supersede_task` (`internal/brain/tasks.go`) — e.g. replacing a stale task
whose premise changed, with dependents automatically rewritten to the
replacement.

## Review gate -> sprints

A mission created with `requires_review=true` waits for a client verdict
instead of auto-completing. `SubmitReview` (`internal/mission/store.go`) either
accepts (mission done) or, on rejection, calls `BumpSprint` and
`ReopenForReview` to append a new round of phases addressing the feedback.
Sprints are capped at `SprintCap` (5) so a never-satisfied client can't loop
forever — the mission must be accepted or the directive revised.
