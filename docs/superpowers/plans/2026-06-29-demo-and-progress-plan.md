# Progress Tab + Headline Demo — Implementation Plan (sub-project #5)

> TDD where it fits, commit per task on `feat/progress-demo`. Design:
> `docs/superpowers/specs/2026-06-29-demo-and-progress-design.md`.
> **The Progress tab is the centerpiece ("where the true wow is") — invest there.**

## Global Constraints

- Additive UI (swarm/memory tabs untouched); reuse the SSE /api/state stream.
- Demo reuses bundled-Ollama compose; queue bees + a lead + auto-seed.

---

## Task 1: Progress tab (the wow)

- Backend: `ui.Handler` gains the mission store; `snapshot()` adds
  `missions:[{id,directive,status,created_ts}]`. Update main + ui_test call sites.
- Frontend: a third tab `progress`; per mission (newest first): directive +
  status pill; the plan as ordered steps (title, role, status dot, `← assignee`,
  `superseded → replacement` lineage); findings (severity-colored, status); a
  `N/M done · k superseded · j findings` summary. Live via the existing SSE path.

- [ ] Test: `/api/state` includes `missions` with id/directive/status.
- [ ] Implement backend + tab + render + styling. `go test ./internal/ui/`. Commit.

## Task 2: headline demo (`deploy/demo`)

`mission` profile + `make demo-mission`: brain + ollama(+pull) + scaled queue
bees (builder/tester/pentester/reviewer) + a lead bee + a one-shot `seed` that
runs `corral-admin mission create "$DEMO_DIRECTIVE"` once the brain is healthy.
README + `DEMO_DIRECTIVE`/scale knobs.

- [ ] Implement compose service(s) + Makefile target + README. `docker compose
  --profile mission config` validates. Commit.

## Task 3: live e2e + verification

- [ ] Real brain + queue bee(s) + seed (drive with the e2e bee, no GPU needed):
  the mission's plan appears in `/api/state.missions` + tasks and progresses;
  Progress tab data is correct.
- [ ] Full `go test ./...` + `go vet` green. Commit. Merge.

---

## Status: COMPLETE (2026-06-29)

T1 Progress tab (`c723d92`) · T2 mission demo profile (`df9a8a2`). `go test ./...`
+ `go vet` green.

**Live e2e:** a full run (lead supersedes build#1 → build-v2 with dependents
following; reflex spawns fix-f2 + verify-f2 from a vuln; mission converges) put
everything the Progress tab needs into one `/api/state` payload — the mission
(directive/status), all 8 tasks with status + assignee + lineage (`build-v2
supersedes #1`, `build#1 superseded`), and both findings (addressed). The demo
profile (`make demo-mission`: brain + ollama + queue hive + lead + seed)
validates via `docker compose --profile mission config`.

This completes sub-project #5 — and the full adaptive-swarm vision (#1–#5).
