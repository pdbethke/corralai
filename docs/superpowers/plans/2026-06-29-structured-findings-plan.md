# Structured Findings — Implementation Plan (sub-project #2)

> Execute task-by-task, TDD, commit per task on `feat/findings`. Design +
> schema + rationale: `docs/superpowers/specs/2026-06-29-structured-findings-design.md`.

**Goal:** bees emit structured findings (`{type,severity,target,evidence,
suggested_action}`); findings are stored in the queue DB, queryable, surfaced
live in the UI (high-severity prominent), and listable via `corral-admin
findings`. No re-planning (that's #3).

## Global Constraints

- Findings live in `internal/queue` (same DB/package as tasks).
- Validate type + severity on write; reject junk, never silently drop.
- UI-visible from day one; `status` tracked but only manually transitioned.

---

## Task 1: findings store (`internal/queue/findings.go`)

`Finding` struct; `AddFinding` (validates type ∈ {vuln,bug,design-flaw,
missing-req,regression,note}, severity ∈ {low,medium,high,critical});
`Findings(missionID, status)`, `AllFindings()`, `SetFindingStatus`;
`SeverityRank`. Add the `findings` table to the schema.

- [ ] Failing tests: AddFinding rejects bad type/severity; stores `open`;
  Findings filters by status; SeverityRank orders low<…<critical;
  SetFindingStatus transitions + idempotent on unknown id.
- [ ] Implement. `go test ./internal/queue/`. Commit.

## Task 2: brain tools (`internal/brain/tasks.go`)

`report_finding`; extend `complete_task` with optional `findings []findingIn`
(reuse one input shape); `list_findings`. Validate via the store (tool error on
junk). Queue-gated, so tool-count tests unchanged.

- [ ] Failing test (over MCP): report_finding returns an id and stores it;
  complete_task with a finding marks the task done AND records the finding;
  list_findings filters by mission/status; invalid severity → IsError.
- [ ] Implement. `go test ./internal/brain/`. Commit.

## Task 3: `corral-agent` emission (`cmd/corral-agent`)

`report_finding` in `agentTools()` + `dispatch` (inject `name` + `mission_id`);
pass the mission id into `runTask`; nudge investigative roles to report.

- [ ] Implement; `go build`. Commit. (LLM loop verified in T6 / the #5 demo.)

## Task 4: UI findings panel (`internal/ui`)

`/api/state` gains `findings` (from `AllFindings`, capped); render a findings
panel, severity color-coded, high/critical prominent.

- [ ] Test: `/api/state` includes findings with severity + reporter.
- [ ] Implement (store→Server→snapshot→index.html). `go test ./internal/ui/`. Commit.

## Task 5: `corral-admin findings` (`cmd/corral-admin`)

`corral-admin findings [--mission N] [--status open]` → `list_findings`,
severity-sorted table; `--json`.

- [ ] Implement; `go build`. Commit.

## Task 6: live e2e + verification

- [ ] Real brain: report a finding (standalone + via complete_task); see it in
  `/api/state` and `corral-admin findings`; filter by status; persists.
- [ ] Full `go test ./...` + `go vet` green. Commit.

---

## Status: COMPLETE (2026-06-29)

All tasks landed on `feat/findings`; `go test ./...` + `go vet` green throughout.

- T1 findings store (`d3faa1d`) · T2 brain tools (`681cfd9`) · T3 agent emission
  (`ef985bf`) · T4 UI findings panel (`0ee16d8`) · T5 corral-admin findings
  (`680d094`).
- **Live e2e through a real brain:** a secops bee reported a HIGH vuln via
  `complete_task`; the finding surfaced in `corral-admin findings` (severity-
  sorted) and `/api/state` (`high/vuln/score-API/open`); `--status` filtering
  worked (open=1, addressed=0). The feedback channel is live end to end. The
  re-planner that consumes open findings is sub-project #3.
