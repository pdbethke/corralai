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

- **P8 — the learning loop (DONE 2026-07-03).** The herd gets smarter between
  missions, with a human as the only promotion gate: a periodic sweep
  (`internal/learn`, `CORRALAI_LEARN_SWEEP_SECONDS`, default 60s / demo 10s)
  clusters recurring finding signatures (≥3 same Type+Target) and similar
  lessons into **proposals**; an LLM drafts corrective guidance + a reusable
  skill; Shep announces pending proposals at standup (even with an empty
  queue); a dedicated **Proposals tab** (live count badge, off the Progress
  tab so a busy sweep doesn't crowd the mission view) grows a *"the herd
  proposes"* card with approve/reject (also
  `corral-admin proposals list|show|approve|reject`).
  Approval fans out to vetted memory (`shared=true`) + a versioned skills
  artifact; `create_mission` injects the top ≤3 vetted lessons into phase
  instructions, fence-wrapped under `LESSONS FROM THE HERD (vetted)`. If a
  promoted signature recurs (≥2 reports post-approval), a revision proposal
  reopens against it. Repo missions ingest `CORRAL.md` + `docs/corral/*.md` as
  *advisory* (`shared=false`) memory — code review is the trust gate for
  repo-shipped knowledge; the operator click is the gate for herd-proposed
  knowledge. **Verified live** (demo stack, qwen2.5-coder:7b, no keys): run 1's
  empty-workspace `go build` failures produced `regression|build-core#1`
  findings → sweep opened proposal #1 at 3 occurrences → model drafted the
  `go-build-diagnosis` skill → Shep's standup announced `1 skill proposal(s)
  awaiting the operator` → approved via the UI card → guidance + skill landed
  in vetted memory and the artifact store (rev 1) → the same signature kept
  recurring mid-fix-cycle, and the efficacy watchdog opened revision proposal
  #2 against the approved one → a second mission's instructions carried the
  fenced `LESSONS FROM THE HERD (vetted)` block (cap 3, lessons-first
  priority; in the dev-mode demo the herd's own auto-vetted lessons filled
  all three slots ahead of the promoted guidance — with auth on, only
  human-promoted entries are vetted, so the mix skews to the operator's
  picks). Known caveat, same class as P7's: the 7B builder needed operator
  shepherding (workspace fix + task cancels) to converge run 1 — the
  learning-loop beats themselves ran unassisted.

- **P9 — the human gate (DONE 2026-07-03).** Closes a gap P8 opened: a
  delegation token still rolls `UserID` up to its principal, so an agent
  spawned under a superuser could `approve_proposal` on itself, and dev
  mode's open-until-first-superuser default meant every unauthenticated
  caller — including the herd's own agents — passed the admin gate too. One
  rule now guards all six admin writes (`approve_proposal`, `reject_proposal`,
  `add_memory(shared=true)`, `promote_memory`, `promote_reference`, and the
  UI's `/api/proposal/approve|reject`): `isHumanAdmin` = `isAdmin` AND no
  `subagent` claim on the token (`internal/brain/identity.go`); the UI gets
  the same rule via `auth.Subagent(ctx)` beside the existing `auth.ReadOnly`.
  Dev mode has no cryptographic identity to check, so it's a **truthfulness
  guardrail, not a security boundary**: a session that names itself
  `corral-agent` at the MCP handshake, or that calls `bootstrap`/
  `report_host` (every shipped worker does; `corral-admin` never does), is
  marked a worker for the life of that session and refused at the same six
  gates — "the human gate: workers propose, the operator disposes." Accepted
  limitation: in-process subagents share their parent's session/token, so
  they're indistinguishable from the parent — the boundary is per-session,
  and out-of-process delegation is the spawn mode that matters for autonomous
  workers. `cmd/corral/main.go`'s UI approve closure also stopped hardcoding
  actor `"operator"` — it passes the real verified principal when auth is on,
  falling back to `"operator"` only in dev. Honesty note: the dev-mode UI's
  `/api/proposal/approve|reject` endpoints are plain HTTP with no MCP
  session, so they sit OUTSIDE the per-session worker-mark boundary
  entirely — a script that wanted to pose as the operator there would need
  to skip announcing itself over the UI's own auth (`isSuperuser`'s
  permissive-dev-mode rule), the same class of deliberate evasion dev mode
  already concedes by design elsewhere (see the `WorkerSessions` doc
  comment): this gate cannot stop a caller who lies, only one who doesn't
  bother to.

- **P10 — mission history + replay (DONE 2026-07-03).** Every finished mission
  gets a **Completed tab** (`mission_history` read surface, mirroring
  `mission_analytics`'s shape) — directive, status, duration (task-timestamp
  derived until a mission speaks `mission_completed`, then event-based),
  task/finding counts, best-effort learned-linkage (promoted proposals whose
  signature matches the mission's findings), and a detail drill-down
  (phases/tasks/findings/executions). A **▶ replay** button reconstructs the
  whole build from durable rows only — `internal/brain/replay.go` merges task
  lifecycle, findings, executions, and (when present) the telemetry event log
  into one time-ordered stream — and plays it back on the same corral canvas
  through the existing render path at 1×–16×, scrub bar included; live SSE
  pauses while replaying; positions are recomputed, never recorded; a mission
  with no ambience telemetry still replays from its durable rows alone
  (graceful degradation). Recording got richer alongside it: `mission_completed`
  (the engine finally speaks telemetry, on both its auto-complete and
  review-accept paths), `findings.resolved_ts` (the row is no longer
  timeline-blind), `agent_activity` (capped at 2,000/mission with a loud log
  at cap), `claim_made`/`claim_released`, `host_seen` (first sighting +
  material change only), and `memory_written` (metadata only — slug/type/shared,
  never the body). **Verified live** (demo stack, qwen2.5-coder:7b, no keys,
  no pre-seeded mission — recorded fresh for this check): mission "Build a Go
  package 'stack' with a LIFO stack of ints: New, Push, Pop, Peek, Len;
  Pop/Peek return an error on empty. Include table-driven unit tests..."
  ran end to end through real MCP flows (39 tasks, 34 done; 40 findings
  raised and resolved over repeated fix/verify cycles — the queue's `-r2`
  rework generation superseded five original tasks; 44 recorded executions) and
  finished `done` in 19m 39s. It appeared in the Completed tab with correct
  duration/counts; **details** rendered phases, findings (type/target/
  severity/outcome), executions (6/44 passed), and an honest "nothing
  promoted from this mission (yet)" for the learned-linkage slot; **▶ replay**
  reconstructed all 423 durable events, the scrub bar and 16× speed both
  worked (16× rendered the whole build in a few seconds), scrubbing back to
  an earlier point re-rendered the correct in-progress node/link state, and
  **Exit replay** reopened the live `/events` SSE (confirmed in the network
  panel). Honesty correction made from this run: `agent_activity` never
  actually reached this mission's replay stream, because `cmd/corral-agent`'s
  automatic `report_activity` call (`main.go`, both tool-loop sites) never
  passes the `mission_id` it already holds — `recordActivity` treats
  `missionID==0` as "outside a mission" and no-ops before it ever calls
  `tel.Record`. `claim_made`/`claim_released`/`host_seen`/`memory_written`
  are recorded with a hardcoded `mission_id=0` by design (`internal/brain/
  coordination_activity.go`'s own doc comments call them "global ambience,
  not part of the Part B replay merge") — they were never meant to appear in
  a mission-scoped replay. Net effect: today, every mission's replay is built
  from task/finding/execution/mission-event rows only; the "richer ambience"
  originally drafted for the README was walked back to match what actually
  showed up on screen. Wiring `mission_id` through `report_activity`'s call
  sites is a small, well-scoped follow-up, not a design flaw — the brain-side
  plumbing and the cap are already correct and tested.

- **P11 — corralai.dev (build complete 2026-07-03; live cutover pending
  human credentials).** A static one-pager (`site/`, Astro, Cloudflare
  Pages, custom domain) whose hero is not a mockup: it's
  `internal/ui/web/replay-player.js` — extracted verbatim from the product
  UI, documented with a DOM contract — embedding a real, privacy-scrubbed
  recorded mission (`scripts/export-golden-run.sh`, gated by an automated
  deny-list scan plus a human-reviewed manifest before anything is written
  or committed). `site/public/replay-player.js` is a hash-checked copy of
  the product's file (`scripts/sync-site-assets.sh --check`, wired into the
  site build) — no silent drift between what plays on the product and what
  plays on the marketing page. Every other section's copy traces verbatim to
  README.md. Deployed via an independent GitHub Actions workflow
  (`.github/workflows/deploy-site.yml`, ubuntu-latest, gated on the same Go
  test suite the main Deploy workflow runs) publishing to Cloudflare Pages.
  Verified locally (`npm run test:e2e` against the built `dist/`: hero
  canvas renders and autoplays the golden run, scrub bar reflects the real
  event count, zero non-local network requests across the whole page
  session including mid-session scrubbing, the committed golden-run.json
  passes a belt-and-suspenders JS re-implementation of
  `scripts/scrub-golden-run.py`'s deny-list, the GitHub link resolves and
  appears in both the hero and the footer, a quick accessibility pass).
  The Cloudflare Pages deploy and the `corralai.dev` custom-domain attach
  are blocked on human credentials (`wrangler login`, a dashboard-minted
  API token, `gh secret set`) — see task-6-report.md's human handoff and
  task-7-report.md's cutover runbook for the exact commands to run once
  those secrets exist. Nothing in this entry is claimed live; it records
  what's built and verified pre-cutover.

### Open threads (next)

- **`report_activity` never carries `mission_id`.** Found during P10's live
  verification: `cmd/corral-agent/main.go`'s automatic `report_activity` call
  (fired after every agent tool-call) omits `mission_id`, even though `runTask`
  has it in scope — so `agent_activity` never actually reaches a mission's
  replay stream despite the brain-side recording/cap logic being correct and
  tested. Small, well-scoped fix: thread `missionID` into that call at both
  sites in `main.go`.
- **Lead re-planning can mint an unclaimable role — silent mission deadlock.**
  Also found during P10's live verification: the lead's rework generation
  created `secops#1-r2` with role `security` instead of `pentester` — a
  free-typed role name no running agent registers as. `ClaimNextAs` filters
  by exact role match, so the task sat `ready` forever; the queue never
  drains, the mission never leaves `running`, and (because
  `MissionHistoryList` skips `running` missions) it never even surfaces in
  the Completed tab — a silent stall with no finding, no log, no visible
  anomaly. The live run was unblocked by hand-editing the task's `role`
  column in `corralai_queue.sqlite3`. Fix belongs brain-side (per the
  standing enforcement directive): validate the role of every enqueued/
  superseding task against the known role set (registered agents + the
  phase-template roles) and refuse or coerce loudly, rather than trusting
  the lead model's free text. Tracked as a follow-up.
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
