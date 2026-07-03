# Model-vs-Model Comparison Report Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Empirical model-vs-model evaluation on the adopter's own codebase — per model, finding volume/severity/type + a confirmation rate (addressed vs dismissed) — running local (mission_analytics) and cross-swarm (MotherDuck via ask_fleet), plus a demo scenario that populates it.

**Architecture:** Telemetry is the single source. Iteration one stamped `model` on `finding_reported` (with `{type,severity}` in `detail`). This adds a `finding_resolved` event (model + outcome) and carries `model`+`detail` through the fleet sync into `fleet_telemetry` (which today carries neither), so one query shape works on both `telem.events` (local) and `fleet_telemetry` (fleet). A canned `model_comparison` report reads it; `ask_fleet` NL covers the fleet ad-hoc for free.

**Tech Stack:** Go 1.26; DuckDB (telemetry + fleet, MotherDuck); MCP; `deploy/demo` docker-compose.

## Global Constraints
- Degrade-never-block: resolve of a finding with no model → `finding_resolved` with `model=""` (grouped `unknown`), resolve still succeeds; no MotherDuck → local report still works.
- Telemetry is the source of truth for the report (local + fleet use the SAME query shape).
- Confirmation rate = `addressed / (addressed + dismissed)`, open excluded; `addressed+dismissed==0` → NULL/`—` (no divide-by-zero). Latest-wins dedupe per finding on double-resolve.
- Fleet schema changes idempotent + back-compat (old rows null `model`/`detail`).
- `go build ./...`, `go vet ./...`, `go test ./...`, `bash scripts/check-security.sh`, `bash scripts/check-licensing.sh` green each task. New `.go` files carry the `// SPDX-License-Identifier: Elastic-2.0` header.
- Repo: Go 1.26, branch `feat/model-comparison` (MAIN checkout — commit there). Scope to `git ls-files`; never touch `.claude/`.

---

## Task 1: `finding_resolved` telemetry event

**Files:** Modify `internal/queue/findings.go` (a getter for a finding by id), `internal/brain/tasks.go` (`resolve_finding` handler emits the event). Test: `internal/queue/findings_test.go`, `internal/brain/findings_test.go`.

**Interfaces produced:**
- `func (s *Store) FindingByID(id int64) (Finding, bool, error)` — returns the full finding (incl `MissionID`, `Target`, `ReporterModel`, `ReporterBackend`) or `ok=false`.

- [ ] **Step 1 — failing test (queue):** `TestFindingByID` — `AddFinding(Finding{MissionID:7, Reporter:"Hawk", Type:"vuln", Severity:"high", Target:"auth", ReporterModel:"gemini-3", ReporterBackend:"gemini"})`; `FindingByID(id)` returns that finding with all fields; a missing id → `ok=false, err=nil`.
- [ ] **Step 2 — run red.**
- [ ] **Step 3 — implement:** `FindingByID` selects via the existing `findingsSelect` constant + a `WHERE id = ?`, scanning through the same `queryFindings` scan path (reuse — don't hand-roll a second scan).
- [ ] **Step 4 — run green (queue).**
- [ ] **Step 5 — failing test (brain):** `TestResolveFindingEmitsFindingResolved` — with a queue holding a finding (id known, `ReporterModel:"gemini-3"`, `MissionID:7`, `Target:"auth"`), invoking `resolve_finding{id, status:"dismissed"}` records a `finding_resolved` telemetry event with `Model=="gemini-3"`, `MissionID==7`, and `detail` containing `outcome:"dismissed"` + `finding_id:<id>`. A finding with `ReporterModel==""` → event emitted with `Model==""` and the resolve still succeeds.
- [ ] **Step 6 — run red.**
- [ ] **Step 7 — implement:** in the `resolve_finding` handler (`internal/brain/tasks.go`), after `SetFindingStatus` succeeds, `f, ok, _ := q.FindingByID(in.ID)`; if `ok`, `recModel(tel, f.MissionID, "finding_resolved", actorOf(req), f.Target, f.ReporterModel, map[string]any{"outcome": in.Status, "finding_id": in.ID, "backend": f.ReporterBackend})`. (`recModel` is the iteration-one helper that stamps the model column + writes detail.) Missing finding → skip the event (resolve still returns success). Never error the handler on the telemetry emit.
- [ ] **Step 8 — run green + build/vet:** `go test ./internal/queue/ ./internal/brain/ ./...`; `go build ./...`.
- [ ] **Step 9 — commit:** `feat(telemetry): emit finding_resolved (model + outcome) on resolve_finding`

---

## Task 2: carry `model` + `detail` through the fleet sync

**Files:** Modify `internal/fleet/sync.go` (the `fleet_telemetry` tableSpec: DDL columns + the `src`/projection). Test: `internal/fleet/sync_test.go`.

**Interfaces consumed:** none new. **Produces:** `fleet_telemetry` rows now include `model VARCHAR, detail VARCHAR`.

- [ ] **Step 1 — failing test:** `TestFleetTelemetryCarriesModelAndDetail` — drive the telemetry→fleet sync for the `fleet_telemetry` spec against a temp DuckDB (follow the existing sync_test pattern); assert the remote `fleet_telemetry` table HAS columns `model` and `detail`, and that a synced `finding_reported`/`finding_resolved` row carries the source event's `model` and `detail` values (not null/empty when the source had them).
- [ ] **Step 2 — run red.**
- [ ] **Step 3 — implement:** in the `fleet_telemetry` tableSpec (≈`internal/fleet/sync.go:86`): add `model VARCHAR, detail VARCHAR` to the `createDDL`; add an idempotent `ADD COLUMN IF NOT EXISTS model VARCHAR` / `... detail VARCHAR` for pre-existing remote tables (match how other specs migrate, if any; else the create-if-not-exists + a guarded ALTER); and extend the `src` projection (`telem.events`) to SELECT `model, detail` in the column order matching the DDL. Keep `id`-based incremental cursor semantics unchanged (the retention/cursor-safety invariant from #19 still holds).
- [ ] **Step 4 — run green + build:** `go test ./internal/fleet/ ./...`; `go build ./...`.
- [ ] **Step 5 — commit:** `feat(fleet): carry model + detail into fleet_telemetry (unlocks MotherDuck model-vs-model)`

---

## Task 3: the `model_comparison` report + surfaces

**Files:** Modify `internal/telemetry/store.go` (add `model_comparison` to the `reports` map), `internal/ui/ui.go` (a minimal additive model-comparison table). Test: `internal/telemetry/store_test.go`, a UI render sanity test if the UI package has one.

**Interfaces consumed:** Task 1's `finding_resolved` events; the `finding_reported` events (model + `detail.{severity,type}`).

- [ ] **Step 1 — failing test (report SQL):** `TestModelComparisonReport` — seed `telem.events` with: model `A` → 3 `finding_reported` (severity high/high/low), 2 `finding_resolved` (outcome addressed, addressed); model `B` → 2 `finding_reported` (critical/medium), 2 `finding_resolved` (addressed, dismissed); and a double-resolve on one `B` finding (finding_id X: dismissed then addressed at a later ts → counts once as addressed). Run the `model_comparison` report; assert: A → findings=3, high=2, low=1, addressed=2, dismissed=0, open=1, confirm=100.0; B → findings=2, addressed=1, dismissed=1, open=0, confirm=50.0 (latest-wins dedupe applied). A model with 0 resolved → confirm NULL.
- [ ] **Step 2 — run red.**
- [ ] **Step 3 — implement the report SQL** (add to the `reports` map in `internal/telemetry/store.go`; DuckDB dialect; column names match the `events` table: `ts, mission_id, kind, actor, subject, model, detail, id`):
```sql
WITH rep AS (
  SELECT COALESCE(NULLIF(model,''),'unknown') AS model,
    count(*) AS findings,
    count(*) FILTER (WHERE json_extract_string(detail,'$.severity')='critical') AS critical,
    count(*) FILTER (WHERE json_extract_string(detail,'$.severity')='high')     AS high,
    count(*) FILTER (WHERE json_extract_string(detail,'$.severity')='medium')   AS medium,
    count(*) FILTER (WHERE json_extract_string(detail,'$.severity')='low')      AS low
  FROM events WHERE kind='finding_reported' GROUP BY 1
),
res AS (
  SELECT model, fid, arg_max(outcome, ts) AS outcome FROM (
    SELECT COALESCE(NULLIF(model,''),'unknown') AS model,
      COALESCE(NULLIF(json_extract_string(detail,'$.finding_id'),''), CAST(id AS VARCHAR)) AS fid,
      json_extract_string(detail,'$.outcome') AS outcome, ts
    FROM events WHERE kind='finding_resolved'
  ) GROUP BY model, fid
),
resagg AS (
  SELECT model,
    count(*) FILTER (WHERE outcome='addressed') AS addressed,
    count(*) FILTER (WHERE outcome='dismissed') AS dismissed
  FROM res GROUP BY model
)
SELECT rep.model, rep.findings, rep.critical, rep.high, rep.medium, rep.low,
  COALESCE(resagg.addressed,0) AS addressed,
  COALESCE(resagg.dismissed,0) AS dismissed,
  rep.findings - COALESCE(resagg.addressed,0) - COALESCE(resagg.dismissed,0) AS open,
  CASE WHEN COALESCE(resagg.addressed,0)+COALESCE(resagg.dismissed,0)=0 THEN NULL
    ELSE round(100.0*resagg.addressed/(resagg.addressed+resagg.dismissed),1) END AS confirm_pct
FROM rep LEFT JOIN resagg USING (model)
ORDER BY rep.findings DESC
```
   Confirm this report is reachable via `mission_analytics{report:"model_comparison"}` (it reads the same `reports` map — verify the analytics handler needs no extra wiring; if it whitelists report names, add `model_comparison`).
- [ ] **Step 4 — run green (report).**
- [ ] **Step 5 — UI (minimal, additive):** in `internal/ui/ui.go`, render a small **Model comparison** table (model · findings · crit/high/med/low · addressed/dismissed/open · confirm%) fed by the report; additive only — no layout rework, no removed indicators. If the UI reads a snapshot, wire the report rows into it; keep it behind the existing analytics/topology area.
- [ ] **Step 6 — run green + build/vet + commit:** `feat(analytics,ui): model_comparison report (per-model volume/severity + confirmation rate) + UI table`

---

## Task 4: multi-model demo scenario

**Files:** Create `deploy/demo/docker-compose.yml` additions (a `models` profile or a documented `CORRALAI_ROLE_MODELS` env), `deploy/demo/Makefile` (`demo-models` target), `deploy/demo/README.md` (document it). No Go changes expected.

- [ ] **Step 1 — implement the scenario:** add a `make demo-models` target (mirroring `demo-mission`) that runs the mission profile with **`CORRALAI_ROLE_MODELS`** set so ≥2 roles run different backends — e.g. `CORRALAI_ROLE_MODELS="reviewer=<backendA>:<modelA>,pentester=<backendB>:<modelB>,builder=ollama:qwen2.5-coder"` — passed to the brain, and the per-role agent services configured to match (or rely on apply-on-spawn where the brain spawns). Ensure the seed directive is one that reliably produces findings (reuse the calc directive or a small buggy-on-purpose target so pentester/reviewer file findings), and that the client/lead loop resolves some (addressed/dismissed) so the confirmation column populates. If resolutions don't occur naturally in a short demo, add a documented `corral-admin` step (or a seed) that resolves a couple of findings so the report renders non-empty.
- [ ] **Step 2 — document** in `deploy/demo/README.md`: what `demo-models` shows (the `model_comparison` table with a real A-vs-B side-by-side), that the striking comparison wants 2+ frontier backends (Gemini in `.env` + a second key), and that the bundled local model still demonstrates the mechanism. Note where to view the report (mission_analytics / the UI table / `ask_fleet`).
- [ ] **Step 3 — verify:** `docker compose -f deploy/demo/docker-compose.yml config` (or the compose lint the repo uses) validates; `CORRALAI_ROLE_MODELS` parses (a quick `rolemodel.Parse` unit check if you added a preset constant). No Go test regressions.
- [ ] **Step 4 — commit:** `feat(demo): make demo-models — multi-model run that populates model_comparison`

---

## Final verification
- [ ] `go build ./...`, `go vet ./...`, `go test ./...`, `bash scripts/check-security.sh`, `bash scripts/check-licensing.sh` all green.
- [ ] **End-to-end:** `resolve_finding` emits `finding_resolved` (model+outcome); the fleet sync carries `model`+`detail` into `fleet_telemetry`; `mission_analytics{report:"model_comparison"}` returns per-model volume/severity + confirmation rate (open excluded, div-by-zero→NULL, double-resolve deduped latest-wins); the UI shows the table; `demo-models` populates a real side-by-side.
- [ ] Degrade paths: no-model resolve → `unknown` group, no block; no MotherDuck → local report works; fleet schema change idempotent + back-compat.
- [ ] `ask_fleet` can now answer model-vs-model questions over `fleet_telemetry` (the data reaches MotherDuck) — spot-verify the columns exist in the synced table.
