<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Per-mission herd composer — design

**Date:** 2026-07-09
**Status:** approved (brainstorm) → ready for implementation plan
**Scope:** v1 = compose a mission's herd at launch and persist it per-mission; concurrency deferred to v2.

## Problem

When you define a mission you should be able to compose its **herd**: assign an
agent to each role, choose which MCP endpoints the herd may consume, attach
lookbook design directives, then launch. Most of the drag-to-role flow already
exists in the cockpit's **Mission Composer & Role Assignment** tab; two pieces of
the ask do not, and the existing composer has correctness gaps (it was authored
by a herd agent — a "Gemini bit" — and needs a review pass).

## What exists vs. the delta

**Exists** (`internal/ui/web/index.html`, `renderComposer`/`submitMission` ~2138–2245):
- Directive box, Thoroughness/Footprint sliders, "requires approval" checkbox.
- A draggable **agent pool** → **role dropzones** (builder/tester/reviewer/pentester).
- "Let Corral Choose" → `/api/mission/propose_staffing`; "Launch Mission" →
  `POST /api/mission/create` with a `role_models` map.

**Delta (this spec):**
1. **MCP-endpoints section** — the gateway is per-*principal* today
   (`internal/gateway/store.go`), with no per-mission binding.
2. **Lookbook section** — lookbook is a *global* gallery
   (`internal/taskartifacts/store.go` `lookbook_items`, no `mission_id`); the only
   mission link is a manual "copy guidelines to clipboard."
3. **Per-mission herd persistence** — `role_models` is written into the single
   **global** `rolemodel.Policy`, not stored against the mission
   (`internal/mission/engine.go`, `internal/rolemodel/`).
4. **Backend-tagging fix** — `submitMission` hardcodes
   `{backend:'ollama', model:agent}` (index.html:2233), so dragging a cloud agent
   like `claude` onto a role mis-tags it as `ollama:claude`.
5. **Review-and-harden** the Gemini-written composer/create path.

## v1 / v2 boundary

The task queue is **global across missions** (`internal/queue/store.go`:
`ClaimNextAs` claims the oldest ready task by role regardless of mission), and the
UI already refuses to launch a second mission while one runs. Per-mission herds
therefore split cleanly:

- **v1 (this spec):** a mission *owns* its herd config (role→agent, endpoints,
  lookbook), persisted at creation and **applied to the run at launch**. Still one
  active mission at a time. No queue changes.
- **v2 (out of scope):** concurrent, differently-composed missions — requires
  mission-scoped claiming and the engine reading each mission's policy instead of
  the shared global one, plus per-mission endpoint-access *enforcement*. The v1
  data model is shaped so v2 is additive.

## Design

### 1. Data model — `mission_herds`

A side table keyed to the mission (mirrors how `task_artifacts` hangs off a
mission; not columns on `missions`, to keep the herd config cohesive and optional):

```
mission_herds(
  mission_id  INTEGER PRIMARY KEY,   -- FK to missions
  role_models TEXT NOT NULL DEFAULT '{}',  -- JSON map role -> {backend, model}
  endpoints   TEXT NOT NULL DEFAULT '[]',  -- JSON []string of gateway endpoint names
  lookbook_ids TEXT NOT NULL DEFAULT '[]', -- JSON []int64 of lookbook_items ids
  created_ts  REAL NOT NULL
)
```

Store methods (in the mission store, or a small `herd` store): `SaveHerd(missionID,
Herd)`, `Herd(missionID) (*Herd, bool)`. `CreateMission` writes it when a herd
config is supplied. Idempotent, degrade-never-block: a missing herd row means "no
per-mission overrides" and the run behaves exactly as today.

### 2. API — `create_mission` / `/api/mission/create`

Both gain two optional fields alongside the existing `role_models`:
- `mcp_endpoints []string` — gateway endpoint names to bind to this herd.
- `lookbook_ids []int64` — lookbook items to attach as design directives.

`CreateMission(...)` grows a `herd *Herd` parameter (nil = no overrides). It
persists the herd row, then, for **v1**, applies `role_models` to the run at launch
(the existing behaviour, but now also recorded per-mission). Validation: endpoint
names must be `Usable` by the caller; lookbook ids must exist — unknown entries are
rejected with a loud error (a bad reference must not silently no-op).

### 3. Runtime injection — at `planToTasks` / instruction assembly

Injection happens where a phase becomes a task instruction (`internal/mission/
store.go` `CreateMission` → `planToTasks`), so the herd's context rides the pull
model with no agent-side change:

- **Lookbook** — for each attached item, prepend its guidelines
  (`name` + `description`, and an image reference/URL) to the instructions of the
  **build / design / review** roles (not pentester/perf). Wrapped in a
  `fence.Untrusted` block (operator-authored free text → treat as data, matching
  the reflex-instruction pattern).
- **MCP endpoints** — prepend an "Available MCP capabilities: X, Y (call via
  call_capability)" note to every role's instructions so the herd knows the
  endpoints exist. **v1 advertises**; actual access still resolves by principal in
  the gateway (`call_capability`). Per-mission *enforcement* is v2.

### 4. Composer UI

In the existing `renderComposer`/`submitMission` (harden as we touch it):
- **Backend fix** — the agent pool carries each chip's real backend (from
  `topology` / the model catalog), so an assignment posts the correct
  `{backend, model}`; drop the hardcoded `ollama`.
- **MCP Endpoints** — a multi-select listing the operator's usable gateway
  endpoints (`list_capabilities`); selections post as `mcp_endpoints[]`.
- **Lookbook** — a picker of lookbook items (`/api/lookbook` meta list); selections
  post as `lookbook_ids[]`.

### 5. Review-and-harden the Gemini composer/create path

Part of the work, not a follow-up. Known finding: the hardcoded backend. The plan
includes a correctness pass over `renderComposer`, `submitMission`, the
`/api/mission/create` handler, and `CreateMission`/`planToTasks`, filing/fixing
what it finds (escape/XSS in injected strings, empty-assignment handling, the
active-mission guard).

## Testing

TDD throughout:
- `mission_herds` store: save/read round-trip, mission-scoped, absent = no override.
- `CreateMission` with a herd: persists the row; `planToTasks` injects lookbook
  guidelines into build/design/review instructions and the endpoints note into all;
  no herd = byte-identical to today.
- API: `create_mission`/`/api/mission/create` accept + validate the new fields
  (unknown endpoint/lookbook id → error).
- Live: extend `scratch/guardrail-probe` (or a new compose probe) with a
  compose → launch → assert-herd-persisted-and-injected check against a local brain.

## Out of scope (v2)

- Concurrent missions + mission-scoped claiming.
- The engine reading per-mission policy instead of the global one.
- Per-mission endpoint-access *enforcement* (v1 advertises).
- Saved/reusable agent profiles, drag-reordering, load-order UI.

## Decisions (defaulted; revisit if wrong)

- Lookbook injects into **build/design/review** roles only.
- Endpoints in v1 are **advertised in instructions, not access-enforced** per mission.
- Herd config lives in a **side table**, not columns on `missions`.
