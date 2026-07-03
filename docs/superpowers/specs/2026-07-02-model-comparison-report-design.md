# Model-vs-Model Comparison Report — Design

**Status:** design · **Date:** 2026-07-02 · **Sub-project:** cross-model attribution — iteration two

## Problem / Goal

Iteration one tagged every finding with the model that filed it. The payoff — the reason
attribution matters — is **empirical model evaluation on the adopter's own codebase**: on the same
repos, which model's findings are more valuable? This iteration builds that: a **model-vs-model
comparison** — per model, the volume/severity/type of findings and a **confirmation rate** (how
often that model's findings were confirmed real vs dismissed as false positives) — running both on
a single swarm (local) and across the fleet (MotherDuck), plus a demo scenario that populates it.

**The confirmation metric uses the existing finding lifecycle:** a finding is `open` →
`addressed` (a fix acted on it — *confirmed real*) or `dismissed` (rejected — *false positive*),
set by a human/lead via `resolve_finding`. So **confirmation rate = addressed / (addressed +
dismissed)** per model, with `open` excluded as undecided, reported alongside raw volume and
breakdowns by severity and type (a high rate on 2 trivial findings ≠ a high rate on 40 serious
ones).

## What already exists (and the gaps this closes)

- **Iteration one** put `reporter_model`/`reporter_backend` on findings and stamped `model` on the
  local `finding_reported` telemetry event; `finding_reported`'s `detail` already carries
  `{type, severity}`.
- **Gap 1:** `resolve_finding` (`internal/brain/tasks.go`) emits **no telemetry event** — so the
  resolution outcome (addressed/dismissed) exists only in the local `findings` table, not in the
  telemetry timeline that feeds the fleet.
- **Gap 2:** the fleet sync's `fleet_telemetry` table (`internal/fleet/sync.go`) is a minimal
  projection — `(brain, id, ts, mission_id, kind, actor, subject)` — carrying **no `model` and no
  `detail`**. So even iteration one's model stamp does **not** reach MotherDuck yet.
- Existing surfaces to reuse: `mission_analytics` (named canned reports over `telem.events`, plus
  ad-hoc SQL for superusers), the `ask_fleet` NL oracle over `fleet_telemetry`, the reports map in
  `internal/telemetry/store.go`.

## Design

### Part A — Plumb the data (telemetry as the single source of truth)
1. **`finding_resolved` telemetry event.** When `resolve_finding` → `SetFindingStatus` transitions
   a finding to `addressed`/`dismissed`, emit `finding_resolved` carrying the finding's `model`
   (look it up from the finding row's `reporter_model`) and `detail = {outcome: addressed|dismissed}`.
   Missing model → `""` (grouped as `unknown`; never blocks the resolve).
2. **Carry `model` + `detail` through the fleet sync.** Add `model VARCHAR` and `detail VARCHAR`
   columns to the `fleet_telemetry` remote DDL and the sync `SELECT` projection in
   `internal/fleet/sync.go` (idempotent — `ADD COLUMN IF NOT EXISTS` / the existing add-column
   pattern; old rows null). This is the load-bearing plumbing that gets model + severity/type +
   outcome to MotherDuck.

Result: both `telem.events` (local) and `fleet_telemetry` (fleet) expose `model` + a `detail` JSON
holding `severity`/`type` (on `finding_reported`) and `outcome` (on `finding_resolved`) — so **one
query shape runs on both**.

### Part B — The `model_comparison` report
A canned query producing, **per model**: total findings; counts by severity; counts by type;
addressed / dismissed / open counts; and **confirmation rate = addressed / (addressed + dismissed)**
(open excluded; when `addressed + dismissed == 0` → `—`, no divide-by-zero). Computed from the
telemetry events: `finding_reported` (one per finding: model + severity + type) joined/aggregated
with `finding_resolved` (model + outcome). Surfaces:
- **Local:** a named `model_comparison` report added to the `mission_analytics` reports map
  (over `telem.events`, using DuckDB JSON extraction on `detail`).
- **Fleet:** the same query shape over `fleet_telemetry`; **`ask_fleet` NL works ad-hoc** over the
  now-threaded data (free — no new code beyond the plumbing).
- **UI:** a minimal, additive model-comparison table in the swarm UI (screenshot-able); no layout
  rework, no removed indicators.

### Part C — Multi-model demo scenario
A `deploy/demo` addition: a `CORRALAI_ROLE_MODELS` preset + a `make demo-models` target that runs
the mission with **different models per role** (e.g. `reviewer`, `pentester`, `builder` on
different backends) so ≥2 models file findings, and drives a few resolutions (the review/lead loop,
or a seeded `resolve_finding`), so `model_comparison` renders a real side-by-side. Documented that
the *striking* comparison wants 2+ frontier backends (the demo `.env` points at Gemini; add a
Claude/OpenAI key for the second); with the bundled local model the *mechanism* still demonstrates.

## Error handling / edge cases
- Resolve of a finding with no/blank model → `finding_resolved` with `model=""` → grouped `unknown`;
  never blocks the resolve.
- No resolutions yet for a model → confirmation rate `—` (open-only), never a divide-by-zero.
- Fleet not configured (no MotherDuck) → the **local** report still works; the fleet report /
  `ask_fleet` are simply unavailable (as today).
- `fleet_telemetry` schema change is idempotent + back-compat (old rows null `model`/`detail`).
- A finding resolved twice / re-opened → the report reflects the latest `finding_resolved`
  outcome per finding (dedupe by finding id, latest wins) to avoid double-counting.

## Testing
- `finding_resolved` is emitted on `resolve_finding` with the finding's `model` + `outcome`;
  missing-model resolve degrades to `""` and still resolves.
- The fleet sync carries `model` + `detail` into `fleet_telemetry` (projection test / schema test).
- The `model_comparison` report SQL: per-model volume + severity + type breakdown; confirmation
  rate = addressed/(addressed+dismissed) with open excluded; `addressed+dismissed==0` → `—`;
  a finding resolved twice counts once (latest outcome).
- `mission_analytics model_comparison` returns the structured rows; a no-data run returns empty
  cleanly.
- Demo: the `CORRALAI_ROLE_MODELS` preset parses and the `make demo-models` target is valid.

## Out of scope (follow-ups)
- **Same-mission *pairwise* head-to-head** (both models scored on the exact same task/finding) —
  this iteration is aggregate-per-model.
- Statistical significance / confidence intervals on the rates.
- A single severity-weighted confirmation score (raw counts + by-severity breakdown cover it).
- The brain auto-spawning the demo's models (the preset assumes the operator brings the backends).
