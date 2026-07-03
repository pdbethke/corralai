# LLM Lead + Supersession — Implementation Plan (sub-project #4)

> TDD, commit per task on `feat/lead-supersession`. Design:
> `docs/superpowers/specs/2026-06-29-lead-supersession-design.md`.

**Goal:** an LLM lead re-plans with judgment — superseding/cancelling/reopening/
enqueuing tasks via brain tools — for findings the reflex tier can't resolve;
lineage is recorded; the mission still converges; the UI shows the re-architecture.

## Global Constraints

- Mechanism deterministic + idempotent + the only mutator; lead only decides.
- `cancelled`/`superseded` terminal & non-open; `MissionDone = hasTasks && open==0`.
- Per-mission lead-action cap; lead marks acted findings addressed.

---

## Task 1: supersession + lineage (`internal/queue`)

`supersedes` column; `CancelTask`, `ReopenTask`, `SupersedeTask`; `MissionDone`
→ `hasTasks && open==0`. Lineage in `Task`/queries.

- [ ] Failing tests: CancelTask (non-terminal→cancelled; done unchanged);
  ReopenTask (done→ready); SupersedeTask (old→superseded, new.supersedes=old);
  MissionDone converges with cancelled/superseded; existing queue tests still green.
- [ ] Implement; `go test ./internal/queue/`. Commit.

## Task 2: brain mutation tools (`internal/brain/tasks.go`)

`cancel_task`, `reopen_task`, `supersede_task`, `enqueue_task` (queue-gated).
supersede_task optionally rewrites dependents' depends_on to the new key.

- [ ] Failing test (over MCP): each mutation; supersede lineage; enqueue_task adds
  to a live mission; tool-count assertions if a Queue-set test exists (it doesn't —
  Options{} tests unaffected).
- [ ] Implement; `go test ./internal/brain/`. Commit.

## Task 3: the LLM lead bee (`cmd/corral-agent`)

`AGENT_MODE=lead`: per running mission, read open findings + tasks; for findings
reflex left (design-flaw, or still-open), drive the LLM with the mutation tools to
re-plan; mark acted findings addressed; bounded by a cap; idle-poll otherwise.

- [ ] Implement (lead loop + tools in agentTools + dispatch). `go build`. Commit.

## Task 4: UI — superseded/cancelled lineage

Task panel renders `cancelled` (dimmed) and `superseded` (struck, "→ #id").

- [ ] Test: `/api/state` tasks include `supersedes`; render handles the statuses.
- [ ] Implement; `go test ./internal/ui/`. Commit.

## Task 5: live e2e + verification

- [ ] Real brain: a design-flaw finding → lead supersedes dependent tasks +
  enqueues rework → mission converges; lineage visible.
- [ ] Full `go test ./...` + `go vet` green. Commit. Merge.

---

## Status: COMPLETE (2026-06-29)

T1 mechanism (`b9406ee`) · T2 tools + dependent-rewrite (`6304d97`) · T3 LLM lead
bee (`a33fecc`) · T4 UI lineage (`6d0320d`). `go test ./...` + `go vet` green.

**Live e2e (real brain) — both tiers in one run:** a design-flaw finding → the
lead superseded `build#1` → `build-v2` (lineage `supersedes=1`) and the pending
dependents (test/secops) were rewritten to wait on build-v2 and completed after
it; meanwhile a secops bee's HIGH vuln drove the reflex tier (`fix-f2` +
`verify-f2`, not in the plan); the mission **converged to done** with the
superseded task not blocking, and both findings `addressed`. The full adaptive
loop — directive → build → findings → reflex fix + lead re-architecture →
converge — is live end to end. (The lead's decision layer is the LLM in prod; the
e2e drove the identical mutation tools deterministically.)
