<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Corralai as an Operator System — vision synthesis (2026-07-05)

Captured from a working session. The individual work items are tickets; this is
the connective narrative that ties them together.

## The shift: demo → usable system
A demo proves it *can* work; a system lets you *operate* it — configure, observe,
tune, and trust it unattended. Corral's foundations are already system-grade (the
parts most demos fake): per-action attribution, the jail + human gate, the
learning loop, durable replayable history, embedded DuckDB analytics, MCP,
jail→repo. The next stretch is less "add features" and more "make it operable and
unbreakable."

## Core theses
- **Coordination infrastructure, not a model host.** The brain *conducts*; it
  doesn't compute. It is always API-based — point it at a LOCAL api (ollama on a
  GPU) or a REMOTE api (frontier). So it runs headless, on a low/no-GPU box, a
  VPS, a laptop. Models (local ↔ frontier) are pluggable back-ends. "Runs on a
  potato; the models do the heavy lifting." Sharpest differentiator vs "download a
  70B and pray your GPU holds."
- **The IDE moves up a level.** IDE-*shaped* but re-centered: the agents write
  code; the human directs/gates/observes. The developer's seat moves from AUTHOR
  to CONDUCTOR. Same primitives (workspace, code index, run/test, review, console,
  settings) organized around orchestrating a herd of models, not typing.
- **The moat is the data model, not the UIs.** Every high-value feature is a
  different query over ONE attributed DuckDB ledger. Competitors can clone a
  dashboard; they can't retroactively have recorded years of attributed
  multi-model runs.

## The operator surface ("model management") — all over the existing ledger
- **#33 Mission composer** — THE FRONT DOOR (keystone; see below)
- **#51 role→model assignment** (settings GUI; the `rolemodel` policy already exists)
- **#52 model registry + per-role performance leaderboard** (observational)
- **#53 cross-model eval/benchmark harness** (controlled; the verify gate is the
  ground-truth accuracy oracle — the thing agent-evals usually lack)
- **#37 role→GPU scheduling**
- **#54 VS Code cockpit** (thin MCP client; git/PR deep-link + read-only snapshot
  FS for live browse; NEVER reach the IDE into the jail — brain stays sole egress)

## The front door (#33) — keystone
The primary verb (tell the herd what to build) needs a first-class surface (today
it's only `corral-admin mission create`). A big prominent "what should the herd
build?" box + an OPTIONAL brain-clarify interview (rough ask → 2–4 clarifying
questions → crisp directive WITH explicit acceptance criteria → launch) + a
PRE-FLIGHT feasibility check (roles the plan needs vs workers available — catch
staffing gaps before launch, not as a stall).

This is prevention *and* signal-generation: the acceptance criteria feed the
`--review` client (a real "done" bar), verify gate v2 (#42), AND the eval harness
(#53). Specify once → drives convergence, review, and measurement.

## The reliability seam (both layers needed for demo→system)
- **Front door (composer)** = prevent bad missions from starting. Most
  Run-D-class stalls are *input/staffing* problems, not engine bugs.
- **Engine (harden #23 nonexistent-role deadlock, #39 role-capacity, #40
  verify-gate livelock)** = survive when a GOOD mission goes sideways anyway
  (agent stalls, quota drains, worker dies holding a claim).
- Prevention reduces *frequency*, not *possibility*. Both layers required.

## Forcing function
**#25 dogfood on real work.** Run D was a mini-dogfood and earned its keep by
exposing exactly what's not-yet-usable (9-role staffing deadlock, worker process
survival). The good kind of failure.

## Shared resources: consolidate on MCP, don't invent a type
Corral already shares KNOWLEDGE (memory), BEHAVIOR (skills+hooks), CONTEXT
(CORRAL.md/codebase). The fourth is REACH — external APIs. Do NOT add a bespoke
"api resource" type; abstract through the MCP gateway (`promote_endpoint`), which
already IS shared API resources: register any MCP endpoint (native like MotherDuck
#49, or a thin MCP shim over REST), superuser-gated, SSRF-guarded, credential held
in the brain, `allowed_principals` for fleet scope. MCP is the consolidating
abstraction: everything the herd touches is MCP through the brain — its own tools
(memory/skills/code) AND federated external endpoints. One interface, one trust
boundary.

Security invariant (why it's a *proven* shape): share the ACCESS (proxied), NEVER
the credential. The API key lives in the gateway exactly as the git/forge token
lives in the brain — same trust model, extended to arbitrary APIs; agents never
see it, brain is sole egress, every call attributed. Multi-brain: sync the
endpoint DEFINITION, keep each brain's secret local / in a secret manager — never
sync secrets via the artifacts store. Work = elevate the gateway to a first-class
Resources tab (sibling to Skills), not new plumbing.

## Architecture invariants — the boundary that must not be re-litigated
Crystallized 2026-07-05 from the "how do I use FastAPI without breaking 'one app'"
thread. **The boundary is the brain's API. Writes go through it; reads share the
analytical store read-only; foreign runtimes live on the client side of it.**

1. **The brain's API is the WRITE boundary.** Every mutation goes through the
   brain's API/MCP — never a second process writing the brain's databases
   directly. This preserves the single source of truth, the invariants, the human
   gate, and per-action attribution (the whole trust story depends on the brain
   being the sole writer). Share through the API, never the database — the
   "integration database" anti-pattern is banned.
2. **The analytical store is a READ-ONLY shared surface.** Reads share freely. A
   local DuckDB *file* is single-writer-per-process, but **MotherDuck is
   multi-client cloud** — so any consumer (a FastAPI analytics service, the "ask
   the fleet" oracle, the site's build-time `model_comparison` report, the
   leaderboard/eval APIs #52/#53) can connect READ-ONLY and query. No write
   contention, no authority bypass (reads can't corrupt truth). Read/write
   asymmetry: brain owns writes via API; MotherDuck is the shared read surface.
3. **Right DB for the job.** SQLite = transactional coordination (OLTP:
   tasks/claims/leases). DuckDB/MotherDuck = analytics (OLAP:
   memory/telemetry/fleet stream). DuckDB is the WRONG tool for a transactional
   app backend (columnar, single-writer) and the RIGHT tool for read/analytics
   services.
4. **The core is ONE Go binary; other runtimes stay on the CLIENT side.** Tools in
   other languages (FastAPI/FastMCP, REST→MCP converters) are a user's peripheral
   or an optional add-on the brain proxies to — never a dependency compiled into
   the core. No Python sidecar in core; if a native converter is ever needed,
   write a lean version in Go in the same binary. (The brain is already cgo via
   DuckDB — one contained dep; libpython is a whole ecosystem, a bad trade.)

## Known blind spots (surfaced 2026-07-05 — see tickets if promoted)
Cost governance / budget caps · concurrent multi-mission scheduling & resource
contention · mid-mission human steering (gate is terminal, not interactive) ·
vetting the herd's OUTPUT (secret-scan / dep-audit / license on produced code
before the PR — we fence the agents, do we vet what they ship?) · mission
resumability / checkpointing after a crash.
