# Cross-Model Attribution + Role→Model Policy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make "coordinated multi-agent, multi-model" visible and attributable — tag findings with the model that filed them, add a brain-side role→model policy (declare + pool-aware apply-on-spawn + reconcile), and thread `model` to the fleet layer so MotherDuck model-vs-model reports become a query.

**Architecture:** Reuse what exists — `report_host`→`HostBook` (per-agent model/backend), `queue.Finding.Reporter`, the `finding_reported` telemetry event, presence/heartbeat, and the lease/reaper. Add attribution at `report_finding` time (brain-side HostBook lookup, denormalized onto the finding + stamped on the telemetry event), a small `internal/rolemodel` package for the policy (parse + pool-availability + reconcile), pool derivation on `HostBook`, apply-on-spawn env injection in the launcher, and an agent-side 404→release+report fallback.

**Tech Stack:** Go 1.26; SQLite (`modernc.org/sqlite`) for queue; DuckDB for telemetry; MCP (`go-sdk/mcp`).

## Global Constraints
- Everything **degrades gracefully, never blocks**: missing model → `""`/"unknown"; unavailable model → fallback to default; runtime 404 → release+reclaim; reconcile is advisory only.
- **No agent-side change is required for attribution** — the brain sources the model from `HostBook` at `report_finding` time.
- **Thread `model` to the fleet layer** — the `finding_reported` telemetry event MUST carry the model (this is what unlocks the iteration-two MotherDuck reports; do not leave model only in the local findings table).
- Back-compat: new finding columns are nullable/empty; old rows read as empty model.
- `CORRALAI_ROLE_MODELS` unset ⇒ policy is inert; attribution still works with whatever models agents self-select.
- `go build ./...`, `go vet ./...`, `go test ./...`, and `bash scripts/check-security.sh` stay green each task. New `.go` files carry the `// SPDX-License-Identifier: Elastic-2.0` header (the licensing gate enforces it).

---

## Task 1: Finding model attribution (schema + populate + telemetry)

**Files:**
- Modify: `internal/queue/findings.go` (Finding struct + AddFinding INSERT + row scans), `internal/queue/store.go` (findings DDL + a migration `ALTER TABLE`)
- Modify: `internal/brain/missions.go` (report_finding handler: HostBook lookup + set model; list_findings returns model), `internal/brain/identity.go` if the server needs the HostBook handle in the missions registration
- Modify: `internal/telemetry/store.go` (the `finding_reported` event carries model) and its brain emit site
- Test: `internal/queue/findings_test.go`, `internal/brain/missions_test.go` (or the existing findings test file)

**Interfaces produced:**
- `queue.Finding` gains `ReporterModel string` + `ReporterBackend string` (json `reporter_model`,`reporter_backend`, omitempty).
- `AddFinding(Finding)` persists the two new columns; `ListFindings`/`FindingsByStatus` scan them.

- [ ] **Step 1 — failing test (queue):** in `findings_test.go`, add `TestAddFindingCarriesModel`: `AddFinding(Finding{MissionID:1, Reporter:"Hawk", Type:"vuln", Severity:"high", ReporterModel:"claude-opus", ReporterBackend:"anthropic"})`, read it back via the list/get API, assert `ReporterModel=="claude-opus"` && `ReporterBackend=="anthropic"`. Also assert a Finding added WITHOUT model reads back `""` (back-compat).
- [ ] **Step 2 — run red.**
- [ ] **Step 3 — implement (queue):** add the two fields to `Finding`; add columns to the `CREATE TABLE findings` DDL AND an idempotent migration (`ALTER TABLE findings ADD COLUMN reporter_model TEXT DEFAULT ''` / `reporter_backend TEXT DEFAULT ''`, ignoring "duplicate column" errors — match the store's existing migration pattern); include them in the `AddFinding` INSERT column list + values and in every `SELECT`/scan of findings.
- [ ] **Step 4 — run green (queue).**
- [ ] **Step 5 — failing test (brain):** `TestReportFindingStampsModelFromHostBook` — with a HostBook where agent "Hawk" reported `Model:"gemini-3", Backend:"gemini"`, invoking the `report_finding` handler as "Hawk" stores a finding whose `ReporterModel=="gemini-3"`. A reporter with NO HostBook entry → `ReporterModel==""` and the finding still files (no error).
- [ ] **Step 6 — run red.**
- [ ] **Step 7 — implement (brain):** the `report_finding` handler resolves the caller/reporter agent name (already used to set `Reporter`), looks it up in the `HostBook` (`book.Get(reporter)` — add a `Get` accessor if only `List()` exists), sets `ReporterModel`/`ReporterBackend` on the `Finding` before `AddFinding`. Then emit/extend the `finding_reported` telemetry event to include the model (add a `Model` field to the telemetry event struct/insert; empty allowed). `list_findings` response includes the two fields.
- [ ] **Step 8 — run green + build/vet:** `go test ./internal/queue/ ./internal/brain/ ./internal/telemetry/`; `go build ./...`.
- [ ] **Step 9 — commit:** `feat(findings): attribute each finding to the model that filed it (HostBook lookup + telemetry)`

---

## Task 2: `internal/rolemodel` — policy parse + pool availability + reconcile

**Files:**
- Create: `internal/rolemodel/rolemodel.go`, `internal/rolemodel/rolemodel_test.go`
- Modify: `internal/brain/host.go` (add `HostBook.Get` if missing, and `AvailableModels(window)` deriving the live pool)

**Interfaces produced:**
- `type ModelRef struct { Backend, Model string }`
- `type Policy map[string]ModelRef` // role → ModelRef
- `func Parse(env string) (Policy, []string)` — parses `role=backend:model,role=model`; returns the policy + a slice of skipped/malformed entries (for logging). Bare `model` ⇒ `Backend:""`.
- `func (p Policy) Lookup(role string) (ModelRef, bool)`
- `func (p Policy) Available(role string, pool []ModelRef) (ModelRef, bool)` — the role's ModelRef IF it's present in `pool` (model match; backend match when the policy specifies one), else `false`.
- `func Reconcile(role, reportedModel string, p Policy) (expected string, drift bool)` — `drift` true when policy has the role and `reportedModel != expected`.
- `HostBook.AvailableModels(windowSecs int64, now int64) []rolemodel.ModelRef` — distinct `{backend,model}` among hosts whose `TS` is within `now-window`.

- [ ] **Step 1 — failing tests (rolemodel):**
  - `TestParse`: `"reviewer=anthropic:claude-opus,builder=qwen2.5-coder, bad-entry ,x="` → policy has `reviewer={anthropic,claude-opus}`, `builder={"",qwen2.5-coder}`; malformed `["bad-entry","x="]` returned. Empty string → empty policy, no malformed.
  - `TestAvailable`: pool `[{anthropic,claude-opus},{ollama,qwen}]`; `Available("reviewer",pool)` for policy `reviewer=anthropic:claude-opus` → `({anthropic,claude-opus},true)`; for `reviewer=gemini:gemini-3` (not in pool) → `false`.
  - `TestReconcile`: policy `reviewer=claude-opus`; `Reconcile("reviewer","gemini-3",p)` → `("claude-opus",true)`; `Reconcile("reviewer","claude-opus",p)` → `(...,false)`; role not in policy → `("",false)`.
- [ ] **Step 2 — run red.**
- [ ] **Step 3 — implement `rolemodel.go`** per the interfaces (SPDX header). Model match in `Available`: compare `Model`; if the policy entry's `Backend != ""`, require backend match too.
- [ ] **Step 4 — failing test (host pool):** `TestAvailableModelsRespectsPresenceWindow` — a `HostBook` with agent A (`Model:"claude-opus"`, `TS:now`) and agent B (`Model:"qwen"`, `TS:now-10000`); `AvailableModels(300, now)` returns only `{...,"claude-opus"}` (B is stale). Distinct-dedupes two agents on the same model.
- [ ] **Step 5 — run red.**
- [ ] **Step 6 — implement `HostBook.Get` + `AvailableModels`** (lock, iterate items, filter by `TS >= now-window`, dedupe by `{Backend,Model}`).
- [ ] **Step 7 — run green + build:** `go test ./internal/rolemodel/ ./internal/brain/`; `go build ./...`.
- [ ] **Step 8 — commit:** `feat(rolemodel): policy parse + pool-availability + reconcile; HostBook.AvailableModels`

---

## Task 3: Surface — `list_findings` by-model filter + policy/reconcile exposure + UI badge

**Files:**
- Modify: `internal/brain/missions.go` (`list_findings` gains an optional `by_model` filter), `internal/brain/host.go`/the topology tool (`coordination_status` or the host-list tool: include each agent's `expected` model + `drift` from `Reconcile`, and the declared policy)
- Modify: `internal/ui/ui.go` (findings render: a model badge; topology: show expected-vs-actual + drift) — minimal, additive
- Test: `internal/brain/missions_test.go`, `internal/brain/host_test.go` (or the topology tool's test)

**Interfaces consumed:** Task 1's finding model fields; Task 2's `Policy`, `Reconcile`, `AvailableModels`.

- [ ] **Step 1 — failing test:** `TestListFindingsByModel` — three findings with `ReporterModel` `claude-opus`,`gemini-3`,`claude-opus`; `list_findings(by_model:"gemini-3")` returns exactly the one. Empty `by_model` → all.
- [ ] **Step 2 — run red / Step 3 — implement:** add `by_model` (optional) to the `list_findings` input + a `WHERE reporter_model = ?` clause when set (via the queue layer — add a `ByModel` field to the findings query filter).
- [ ] **Step 4 — failing test:** `TestTopologyShowsExpectedAndDrift` — HostBook agent "Quill" role `reviewer` reported `Model:"ollama-x"`; policy `reviewer=claude-opus`; the topology/status response for Quill carries `expected:"claude-opus"`, `drift:true`; and the declared policy is present in the response.
- [ ] **Step 5 — run red / Step 6 — implement:** the topology/status handler, given the `Policy`, annotates each host with `Reconcile(host.Role, host.Model, policy)` → `expected`+`drift`, and includes the declared policy map. (Wire the `Policy` into the server — it arrives via Task 4's config; for THIS task's test, construct the server/handler with a policy directly.)
- [ ] **Step 7 — UI (minimal):** in `internal/ui/ui.go`, render a small model badge next to each finding's reporter, and in the topology view show `model (expected X ⚠ drift)` when drift. Keep additive; no layout rework. (Per [[feedback_library_ui_is_load_bearing]]-style caution: additive only, never remove existing indicators.)
- [ ] **Step 8 — run green + build/vet + commit:** `feat(brain,ui): list_findings by-model + topology expected-vs-actual model drift + finding model badge`

---

## Task 4: Apply-on-spawn (pool-aware, best-effort) + `CORRALAI_ROLE_MODELS` wiring

**Files:**
- Modify: `cmd/corral/main.go` (parse `CORRALAI_ROLE_MODELS` via `rolemodel.Parse`, log malformed; pass the `Policy` into `brain.Options`/server + the spawn path)
- Modify: `internal/brain/subagents.go` (spawn_subagent: when spawning for role R, resolve the model via policy + pool), `cmd/corral-agent/launcher.go` (inject `AGENT_MODEL`/`MODEL_BACKEND` into `childEnv` when provided by the spawn spec)
- Modify: `internal/brain/identity.go` (`Options` gains `RoleModels rolemodel.Policy`)
- Test: `internal/brain/subagents_test.go`

**Interfaces consumed:** Task 2 `Policy.Available`, `HostBook.AvailableModels`.

- [ ] **Step 1 — failing test:** `TestSpawnAppliesPolicyModelWhenAvailable` — server with policy `deployer=anthropic:claude-opus` and a HostBook pool containing `{anthropic,claude-opus}` (live); spawning a subagent with role `deployer` produces a spawn spec whose env carries `AGENT_MODEL=claude-opus`,`MODEL_BACKEND=anthropic`. `TestSpawnFallsBackWhenModelUnavailable` — same policy but pool WITHOUT that model → the spawn spec does NOT force the model (falls back to inherited/default; assert no `AGENT_MODEL` override for that role or it equals the parent default).
- [ ] **Step 2 — run red / Step 3 — implement:** in the spawn path, compute `ref, ok := policy.Available(role, hostBook.AvailableModels(window, now))`; if `ok`, set the spec's model/backend (threaded into `launcher.childEnv` as `AGENT_MODEL=`/`MODEL_BACKEND=`); else leave unset (inherit). Availability = in the live pool OR (optional, if easily known) the backend is configured — for v1, pool membership is sufficient; document that a not-yet-connected model won't be force-spawned (acceptable: attribution + reconcile still cover it).
- [ ] **Step 4 — wire config:** in `cmd/corral/main.go`, `pol, bad := rolemodel.Parse(os.Getenv("CORRALAI_ROLE_MODELS"))`; log `bad`; set `opts.RoleModels = pol`; pass into the topology handler (Task 3) and the spawn path.
- [ ] **Step 5 — run green + build/vet + commit:** `feat(spawn): pool-aware best-effort role->model apply-on-spawn + CORRALAI_ROLE_MODELS`

---

## Task 5: Runtime 404 fallback (agent releases + reports; reclaim does the rest)

**Files:**
- Modify: `cmd/corral-agent/backend.go` (surface a distinguishable model-unreachable/404 error from the backend call), `cmd/corral-agent/main.go` (on a model-unreachable error during a claimed task: `release_claims` + report the model failure, then continue the loop instead of spinning)
- Test: `cmd/corral-agent/backend_test.go` (or a focused test on the error classification + the release path)

**Interfaces consumed:** the existing `release_claims`/report tools; the brain closure.

- [ ] **Step 1 — failing test:** `TestBackendReports404AsUnreachable` — a backend pointed at an httptest server returning HTTP 404 for the model returns an error classified as model-unreachable (e.g. `errors.Is(err, ErrModelUnreachable)` or a typed `ModelUnreachableError`). Non-404 errors are NOT classified as unreachable.
- [ ] **Step 2 — run red / Step 3 — implement:** in `backend.go`, when the provider responds 404 (or connection refused / model-not-found body), wrap/return a sentinel `ErrModelUnreachable`.
- [ ] **Step 4 — failing test:** `TestTaskLoopReleasesOnModelUnreachable` — drive the task loop (or the smallest extractable helper) so that a `runTask` returning `ErrModelUnreachable` triggers a `release_claims` call for the claimed paths and a model-failure report, and does NOT call `complete_task`. Assert via a fake brain closure recording the calls.
- [ ] **Step 5 — run red / Step 6 — implement:** in the task loop, if `runTask` fails with `ErrModelUnreachable`, call `release_claims` (all of this agent's claims for the task) + report the model failure (a `report_finding` note or a health signal), then `continue` — so the queue's lease/reaper reassigns the task to another live agent (possibly a different model). Never `complete_task` on unreachable.
- [ ] **Step 7 — run green + build/vet + commit:** `feat(agent): release+report on model-unreachable (404) so reclaim falls back to a healthy agent`

---

## Final verification
- [ ] `go build ./...`, `go vet ./...`, `go test ./...` all green; `bash scripts/check-security.sh` exits 0 (SPDX on the new `rolemodel` files; no new gosec medium+).
- [ ] **Attribution end-to-end:** a finding filed by an agent carries that agent's reported model, on the finding row AND the `finding_reported` telemetry event (the fleet-thread guarantee); `list_findings by_model` filters; UI shows the badge.
- [ ] **Policy:** `CORRALAI_ROLE_MODELS` parses (malformed logged, not fatal); topology shows declared policy + per-agent expected-vs-actual + drift; apply-on-spawn injects the model when it's in the live pool and falls back when not; all non-blocking.
- [ ] **404:** an agent whose model 404s releases its claim + reports and does not complete; the task remains claimable by another agent.
- [ ] Everything degrades, nothing blocks; `CORRALAI_ROLE_MODELS` unset ⇒ attribution still works.
