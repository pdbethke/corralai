# CorralAI — Design

**Corral your coding agents.** An MCP-native **brain** that a swarm of coding
agents (local and across machines) coordinate and remember through. The agents are
the swarm; CorralAI is the **corral** — the shared environment they self-organize
within. No central orchestrator: each agent decides for itself and reads shared
state (claims, presence, completed work) to stay in its lane. That's stigmergy.

## Topology: central brain + thin clients

- **The brain** — one CorralAI instance (Go) on a server, the authoritative state.
- **Thin clients** — each dev machine's coding agent points its `.mcp.json` at the
  brain's URL over MCP/streamable-HTTP (via the Cloudflare tunnel). No local daemon,
  no sync logic — ask the brain, the brain answers. Strongly consistent.
- This is the standalone, Go, multi-machine successor to the existing Django
  `agent-coord` broker (which already proves the central-broker model in prod).

## The load-bearing rule: coordinate transactionally, observe analytically

Two jobs, opposite database needs — never conflate them:

| Layer | Job | Engine | Why |
|---|---|---|---|
| **Coordination** | live locks, presence — OLTP | **SQLite** (pure-Go, `modernc.org/sqlite`) | frequent small concurrent writes, read-your-writes, low-latency mutual exclusion |
| **Memory** | searchable knowledge corpus — OLAP | **DuckDB** (`go-duckdb`, CGO) | FTS now, vectors later (VSS); source of truth = markdown files |
| **Observe / analytics** | fleet action-stream + shared memory across machines | **MotherDuck** (DuckDB cloud) | hybrid local+cloud; Sigma dashboards on top |

**MotherDuck is for remembering and watching the swarm, NOT for locking it.** A
warehouse is the wrong tool for a live lock manager (cloud latency, OLAP semantics).
Coordinate through the brain; observe through MotherDuck.

## Auth — OIDC from day 0

The brain is a standard **OIDC relying party**: it validates incoming bearer JWTs
against the provider's JWKS (`github.com/coreos/go-oidc`, iss/aud/exp, cached keys).
Any OIDC-compliant provider works (Keycloak, Auth0, Okta, Dex, Authentik, …) — point
`CORRALAI_OIDC_ISSUER` at its discovery URL. Machines/agents obtain tokens via the
`client_credentials` grant. Being a plain OIDC RP leaves the door open for other OIDC
consumers without bespoke auth each time.
- Gotcha: the JWKS fetch sends a **custom User-Agent**, because some IdPs sit behind
  a WAF / bot-fight layer (e.g. Cloudflare) that 403s the default Go HTTP UA.

## UI — live swarm diagram

The brain serves a real-time topology view (force-directed graph: agents = nodes,
claims = agent→path edges, presence = pulse, actions = motion) fed by a WebSocket,
static assets embedded via `go:embed` (UI ships inside the single binary). Live
topology here; historical analytics via MotherDuck → Sigma.

## Stack

- **Go** (go1.26), single binary. CGO-enabled on Linux (typical dev/prod hosts are
  linux/amd64, so DuckDB's CGO requirement is a non-issue; cross-OS distribution is
  a later concern).
- Coordination: `modernc.org/sqlite` (pure-Go). Memory: `github.com/marcboeker/go-duckdb`.
- MCP: `github.com/modelcontextprotocol/go-sdk` (streamable-HTTP). Auth: `coreos/go-oidc`.
- Source-available under the **Elastic License 2.0** (no time-bomb; commercial
  dual-license). Clean-room — concepts only, no third-party code (notably NOT
  a fork of `mcp_agent_mail`, whose license carries an anti-OpenAI/Anthropic rider).
- Names: PyPI/npm/GitHub `corralai`; domain `corralai.dev`.

## Roadmap

- **P0 — coordination core (DONE).** SQLite-backed advisory TTL leases, presence,
  completed-work, audit; bootstrap one-call entry. `internal/coord`, 6/6 tests green.
- **P1 — the brain online (code DONE).** `cmd/corral`: MCP streamable-HTTP server
  wrapping the coord tools (`internal/brain`) + OIDC auth middleware
  (`internal/auth`; on when `CORRALAI_OIDC_ISSUER` set, dev pass-through otherwise) +
  `/healthz`. Verified: 8 tools over MCP, `initialize` 200 over HTTP. Remaining =
  *deploy*: register an OIDC client with your provider, route it behind your
  reverse proxy / tunnel, run as a service.
- **P2 — memory (DONE).** `internal/memory`: DuckDB FTS (BM25) over the markdown
  corpus, multi-tier; tools `search_memory`/`get_memory`/`list_memory`/`add_memory`
  folded into the brain (12 tools total). Verified: brain indexes 372 entries
  (FTS=true) on boot and `search_memory` returns ranked hits over HTTP. (Uses
  escaped-literal INSERTs — go-duckdb rejects very large *bound* string params.)
- **P3 — MotherDuck action stream (DONE).** `internal/fleet`: an in-memory DuckDB
  bridge ATTACHes the coordination SQLite (read-only) + the remote (`md:<db>` or a
  local `.duckdb`) and incrementally appends the audit/action stream to
  `fleet_actions`, tagged by `brain` (federation-ready across machines). The brain
  runs it on a ticker when `CORRALAI_MOTHERDUCK` is set. Verified: unit test
  (incremental) + live (HTTP action → audit → sync → remote `[brainA] BlueLake
  register`). **Sigma dashboard = connect Sigma to the MotherDuck `fleet_actions`
  table** (no code). Real MotherDuck differs only by DSN `md:` + token. (Syncing the
  memory *corpus* to MotherDuck too is a later add.)
- **P4 — swarm-diagram UI (DONE).** `internal/ui`: a dependency-free force-directed
  canvas view embedded via `go:embed`, pushed over **SSE** (`/events`; stdlib, no
  WebSocket dep) — agents as nodes, claims as edges, presence as pulse, conflicts in
  red. Plus `/api/state` (snapshot JSON) and the page at `/`. Verified live: page +
  state reflect real agents/claims including a conflict on a shared path.
  Observability surface — gate the host with CF Access in prod; the MCP endpoint
  stays the OIDC-authed surface.
- **P5 — missions & the hive (DONE).** The orchestration layer on top of the
  coordination core: a pull-model task queue (`internal/queue`: claim/complete with
  TTL leases, dependency promotion, presence-aware reaping), the mission engine
  (`internal/mission`: directive → phased plan, a reflex re-planner that turns
  findings into fix + re-verify tasks, a verification gate that refuses to close a
  gated task without a recorded passing run, and a client-review gate —
  `awaiting_review` → accept / request-changes → next sprint), modeled roles
  (lead = judgment tier, client = product owner), real execution in **bwrap**
  namespace jails for exec bees, per-model finding telemetry
  (`model_comparison`), and the one-command docker demo (`deploy/demo`) with a
  bundled GPU Ollama. This is what the P0–P4 substrate was for.
- **P6 — hardening from first-contact (DONE 2026-07-02).** A first-time-reviewer
  verification run found the flagship demo broken at first `make` and deadlocked
  mid-mission; the fixes hardened the core, not just the demo:
  - *Claim-orphan self-heal.* A lost `claim_task` reply left a task claimed by a
    bee that heartbeated "idle" forever — presence-authoritative reaping never
    frees a live bee's claim. Now: `ClaimNextAs` re-issues a bee's own claim on
    its next poll (instance = hostname disambiguates `--scale` replicas), and the
    brain's **slacker rule** requeues expired-lease claims on an idle heartbeat.
    Store tests + an MCP wire test reproduce the original incident.
  - *Loud failures.* Every failed agent→brain call logs with tool name; re-issues
    and reclaims are logged + telemetered. This immediately exposed that the
    agent's blanket `name`-stamping had been silently breaking every strict-schema
    tool call (status/list/search/resolve) — now the agent discovers name-aware
    tools from the brain's own `tools/list` schemas.
  - *Analytics integrity.* Reflex-replanner resolutions now emit
    `finding_resolved` telemetry (the `model_comparison` confirmation column was
    permanently zero without it), and recurring findings ride the in-flight
    remediation instead of spawning duplicate fix/re-verify pairs (9→1 observed).
  - *Demo/ops floor.* Fresh-checkout build fixed (`.dockerignore` negation),
    `demo-cpu` actually clears the GPU reservation (`!reset`), brain gets
    `restart: unless-stopped` + a `brain-state` volume (a brain recreation used to
    silently wipe the running mission), `init: true` reaps bwrap zombies, exec-on
    default documented truthfully with an `AGENT_ALLOW_EXEC=0` off switch, and
    `corral-admin` ships in the brain image so the documented analytics command
    works verbatim.
  - *Shep, the scrum master.* A deterministic standup-tier bee (`AGENT_MODE:
    scrum`, no model in its loop): narrates progress in the live console, names
    stalled claims, nudges holders via `send_instruction`. Enforcement stays in
    the brain; Shep is the visible layer.
- **P7 — the demo converges (DONE 2026-07-02).** The default mission directive is
  now small enough to close the whole loop on the default local 7B model (weights pulled on first run — only the Ollama runtime is bundled), and `build`
  is split into `build-core` → `build` so progress stays visible instead of one
  bee holding one giant task. Measured live (qwen2.5-coder:7b, RTX 5070, no API
  keys): choreography at ~2–3 min, all 23 tasks done and `awaiting_review` at
  **18m52s**, then request-changes → sprint 2 → accept → done, exercised through
  the UI. `make demo-mission-epic` keeps the ambitious parser directive (the old
  default, which never converged locally); the README pitches frontier models as
  the fast path for it. Known caveat: the 7B **lead** resolved a client
  change-request without enqueuing the rework — the judgment tier's quality
  tracks the model; the mechanism (sprint increment, re-gate, review buttons) is
  verified regardless.

### Open threads (next)

- **Change-request enforcement.** A client change-request that produces zero
  rework tasks should not silently re-gate to `awaiting_review` — the engine
  could require at least one enqueued task (or an explicit lead dismissal with
  reasons) before re-gating. Today that's left to lead-model judgment.
- **Lease renewal for long tasks.** A legitimately long exec task outlives its
  claim lease; nothing renews it mid-run. Fine single-instance (nothing reclaims
  a working bee), but under `--scale` an idle sibling can requeue it.
- ~~Supersede drops the verify gate / cancel_task strands dependents / no
  dependency-edit tool~~ **FIXED 2026-07-03** (same day observed): supersede
  auto-uniquifies replacement keys and inherits the original's verify gate
  unless the spec sets its own; cancel_task refuses while live dependents
  exist (naming them; `cascade:true` takes the subtree down deliberately); and
  `retarget_dependencies` is the one-step recovery the lead kept
  hallucinating. Pinned by `internal/queue/recovery_test.go`.
- **Frontier tool-calling reliability.** gemini-flash drops out of native tool
  calls under pressure (writes the call as prose) and needed three agent-side
  fixes to make progress (refusal feedback, transcript action echoes, textual
  call fallback — all landed 2026-07-03). The agent loop's native tool_calls
  threading (assistant tool_calls + role:"tool" results instead of text-woven
  transcripts) is the structural fix.
- **Memory corpus → MotherDuck** (deferred from P3) and cross-brain federation
  over the shared `fleet_actions` stream.
- **Console rehydration on brain restart.** The live console's activity and
  execution feeds are in-memory rings — a brain restart mid-mission leaves the
  UI mute until new activity arrives, even though the telemetry store has the
  history. Replay the last N events into the rings on boot.
- **CI hygiene.** GitHub Actions warns on Node 20 (`actions/checkout@v4`,
  `setup-go@v5`) — bump on next workflow touch.
