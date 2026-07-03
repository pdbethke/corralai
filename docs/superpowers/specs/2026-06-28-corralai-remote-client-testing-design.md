# Testing & Demonstrating CorralAI with Real Remote Clients

- **Date:** 2026-06-28
- **Status:** Approved (design) → ready for implementation planning
- **Component:** corralai (the coordination + memory MCP brain)

## Context & Problem

corralai is a model-agnostic coordination substrate: any MCP-speaking agent,
driven by any model, coordinates through the brain (file/path leases — advisory
by default, exclusive ones enforced; see Coordination-Core below — presence,
instruction inbox, mission orchestration). Today's tests
(`internal/brain/*_test.go`) run **in-process over in-memory MCP transports with
a single client**. They verify handler logic but never exercise the real
streamable-HTTP transport, independent concurrent sessions, lease-TTL expiry, or
the actual remote-client experience. And there is no way for a developer to
*see* the system work — no demo, no reproducible environment.

We need two things that serve two different audiences:

1. A **deterministic correctness layer** CI can bet on (machines).
2. A **real, credible, easy-to-run demonstration** of heterogeneous AI agents
   coordinating through the brain (humans / contributors / evaluators).

A simulated/fake client was explicitly rejected: it would read as a toy and
undermine the core claim. The demo must use **real LLM-driven agents**.

## Goals

- Prove coordination **correctness** under genuine concurrency, deterministically,
  in CI (`go test ./...`), with no LLM and no docker.
- Let any developer **spin up the whole system with one command**, watch real AI
  agents self-organize through the brain, with **zero API keys and zero cost** on
  the default path.
- Demonstrate **provider-agnosticism**: the *same* reference agent runs against a
  local model (Ollama) or a hosted model (Anthropic / Gemini / OpenAI-compatible)
  via one env var.
- Demonstrate **the value of the substrate** directly: the same agents, *without*
  the brain, visibly clobber each other.
- Be honest about **capability tiers**: peer coordination works with any
  tool-capable model; subagent orchestration needs a frontier model.

## Non-Goals

- The docker/agent environment is **not** a CI merge gate (it is nondeterministic
  and slow by nature). It may later run as an opt-in / nightly integration smoke.
- Not building a general-purpose agent framework — `corral-agent` is a minimal
  *reference* client, intentionally small.
- Not load/scale testing (a separate future effort).

## Coordination-Core Changes (Permission-Aware Leasing)

These are substrate changes the demo's credibility depends on — they answer the
first objection every developer raises: **"this will trample my stuff."** All are
deterministic functions of the clock seam, so the harness tests them cold.

### 1. Atomic exclusive conflict detection
Today `ClaimPaths` (`internal/coord/store.go`) snapshots active claims, computes
conflicts, then inserts — a non-transactional read-then-insert. Two clients racing
on the same path can both read "clear" before either inserts, so both report zero
conflicts. **Fix:** for **exclusive** claims, perform the conflict-check + insert
in a single transaction (or a guarded `INSERT … WHERE NOT EXISTS`). **Guarantee:**
under a simultaneous race on the same path, **exactly one** exclusive claimant sees
zero conflicts; every other sees a conflict naming the holder. Non-exclusive claims
stay advisory.

### 2. Permission-aware presence (`status`)
Add `status` (`working` | `awaiting_approval` | `idle`) and `status_since`
(timestamp) to the `Agent` record, reported on `heartbeat`/`bootstrap` (agents
already heartbeat — no new chatty protocol). corralai does **not** own any client's
approval UX; it only records and displays what the agent reports.
- **Reference agent** reports `status` natively at its decision points.
- **BYO Claude Code** reports via hooks: a `Notification` hook (fires when a
  permission prompt is waiting) POSTs `status=awaiting_approval`; a
  `PostToolUse`/`Stop` hook flips back to `working`. Snippet ships in the BYO docs.

### 3. Derived parked-lease policy (non-destructive)
An exclusive claim is treated as **non-enforcing (advisory)** when its holder is
`awaiting_approval` and `now() - status_since > CORRALAI_PARKED_GRACE_SECONDS`
(default **300**; the demo sets **~20** so the lifecycle is watchable). This is
**derived at conflict-check time inside the same transaction as #1 — nothing is
mutated, deleted, or stolen.** A peer claiming the same path is **granted with a
surfaced conflict** ("owner parked; proceed at your discretion"), never
hard-blocked or silently overridden. The instant the owner un-parks, its lease
enforces again. (Treat-as-advisory, not hard-steal: a hard-steal would itself read
as "trampling" — solving the fear by demonstrating it.)

### 4. Resume re-validation (the lost-update guard)
When an agent transitions `awaiting_approval` → `working`, its `bootstrap`/
`heartbeat` response includes any of its claims that **became contested while it
was parked**, so it re-checks before writing instead of clobbering a peer who
proceeded. This closes the window #3 opens.

### 5. Clock seam
`internal/coord/store.go` already funnels time through one `func now() float64`;
change it to `var now = func() float64 {…}`. The #1 race window, #3 grace expiry,
and lease TTL then become deterministically testable by overriding `now` in
`package coord` tests. Behavior-preserving one-liner.

## Guiding Principle: They Must *See It* To *Get It* — And It Cannot Be Clumsy

corralai is not self-evident from a README. Developers understand it the moment
they **watch real agents coordinate through it** — and they only extend or adopt
something they understood. So the demo's job is the **"aha": one command, and
within minutes the contributor is watching real AI agents claim, conflict, and
hand off in the swarm UI, and *gets it*.**

Equally load-bearing: **if the first run is clumsy, they won't give it the
attention it deserves.** A demo that needs manual steps, throws confusing errors,
takes too long before anything moves, or leaves the viewer unsure what they're
looking at will get dismissed *regardless of how good the underlying system is*.
Polish of the first-run experience is therefore a **functional requirement**, not
a nicety.

Concretely:

- `make demo` (or `docker compose up`) is the entire happy path — **no manual
  steps between the command and visible motion.**
- The default profile pulls the model automatically (Ollama), needs **no
  credentials**, and runs fully offline.
- The swarm UI is reachable at `http://localhost:9019/` the moment it's up, and
  the terminal output tells the contributor **exactly where to look and what
  they're seeing** (a one-line "open this URL; watch the agents claim files").
- **Time-to-first-motion is short** — agents should start visibly coordinating
  quickly (model pull is the only long pole, and it's cached after first run).
- Failure modes are legible: if Docker/GPU/model isn't available, the output says
  what to do, it doesn't dump a stack trace.
- Everything beyond the default (hosted models, auth, orchestration) is an opt-in
  profile or env var, never required for first-run.

This principle outranks feature breadth. Extensibility by other developers is
expected and welcome (documented seams below) — but it is **secondary** to the
first-run "aha." When a choice trades ease-or-clarity for realism or breadth, the
default favors the contributor's first three minutes; the rest becomes opt-in.

---

## Deliverable 1 — CI Backbone: Deterministic Coordination Harness

A Go test layer that boots the **real** brain and drives **multiple real MCP
clients** against it to assert the coordination guarantees — deterministically,
in-process, no LLM, no docker, in seconds. This is what gates merges.

### How it works

- Boot the real brain on an `httptest.Server` → real streamable-HTTP transport,
  real auth-middleware code path (auth disabled for the harness), real SQLite
  coordination store (temp file or `:memory:` equivalent).
- Launch **N real `mcp.NewClient`s over loopback**, each a fully independent MCP
  session — behaviorally identical to N separate machines, driven from one test
  process.
- Drive tool calls and assert coordination semantics.

### Scenarios asserted

- **Atomic exclusive race** (depends on Coordination-Core #1): N clients behind a
  barrier call `claim_paths` (exclusive) on the same path at the same instant →
  assert **exactly one sees zero conflicts**, every other sees a conflict naming
  the holder. Without #1 this would flake; with it, it's a real guarantee.
- **Sequential conflict reporting:** A claims P, then B claims P → B's
  `conflicts` contains P with `held_by = A`. (Deterministic regardless of #1.)
- **Overlap:** A claims `src/`, B claims `src/app.go` → B gets a conflict
  (`pathsOverlap` glob/dir matching).
- **Release frees it:** A claims P, A `release_claims` P, then B claims P → zero
  conflicts.
- **TTL expiry** (clock seam): claim P with a TTL, advance `now` past expiry,
  another client claims P → zero conflicts (expired claim is not active).
- **Parked-lease downgrade** (Coordination-Core #3): A (exclusive) holds P and
  goes `awaiting_approval`; advance `now` past `CORRALAI_PARKED_GRACE_SECONDS`;
  B claims P → **granted with a surfaced conflict**, not hard-blocked. Before the
  grace window, B is blocked; after, advisory.
- **Resume re-validation** (Coordination-Core #4): after the downgrade above, A
  transitions back to `working` → A's `bootstrap`/`heartbeat` response flags P as
  contested.
- **Presence + status:** `bootstrap`/`list_active` reflects N agents and their
  `status`; heartbeat keeps a session alive and carries status; absence expires it.
- **Instruction inbox:** `send_instruction` → target `check_instructions` →
  `ack_instruction` → status `pending`→`done`.
- **Mission advancement:** seed a mission, tick the engine, assert phases advance
  as their instructions complete (drives the engine directly — no LLM needed).

The atomic-race, parked-downgrade, TTL, and resume scenarios are the parts that
require the clock seam and the Coordination-Core changes; the rest hold today.

### Time seam (the one required refactor)

`internal/coord/store.go` already funnels all time through a single package-level
`func now() float64`. Change it to an overridable seam:

```go
var now = func() float64 { return float64(time.Now().UnixNano()) / 1e9 }
```

Tests set `now` to a controllable clock to make lease/heartbeat expiry **instant
and deterministic** instead of sleeping real seconds. This is the only production
change Deliverable 1 requires; it is a behavior-preserving one-liner.

### Determinism rules

- No `time.Sleep` for correctness; advance the clock seam instead.
- No reliance on goroutine scheduling order; use explicit barriers and
  result-set assertions (counts/membership), not ordering.
- Runs under `go test -race`.

### Suggested layout

- `internal/brain/remote_harness_test.go` (or a small `internal/cohort` test
  helper package) housing the httptest boot + N-client fixture, reused across
  scenario tests.

---

## Deliverable 2 — The Docker Demo/Dev Environment ("the formula")

A `docker compose` stack a developer runs to *see and feel* the system: the
brain, the swarm UI, a local model, and several real AI-agent containers
coordinating on a shared task.

### Spin-up UX

- `make demo` → `docker compose up` with the default `coordination` profile.
- Brings up: `brain`, `ollama` (auto-pulls the default model), `N × corral-agent`.
- No keys, offline, swarm UI at `localhost:9019`.
- `make demo-clobber` runs the control (same agents, no brain) so the contributor
  sees chaos-vs-order in two commands.

### Topology

```
docker compose
├── brain            corral binary; MCP /mcp/ + swarm UI + /healthz on :9019
├── ollama           serves the default open model; volume-cached
├── agent-1..N       cmd/corral-agent, each an independent MCP session
│                    driving itself with the configured backend, working the
│                    seeded backlog on a shared mounted workspace
├── (auth profile)   oidc-stub: minimal local OIDC issuer for bearer-token path
└── workspace vol    throwaway repo mounted RW into all agents
```

### Profiles

| Profile | Backend | Auth | Demonstrates |
|---|---|---|---|
| `coordination` (default) | Ollama 7B (local) | off | Tier-1: real agents coordinating; seeded overlap → visible conflicts & hand-offs. The model-agnostic floor. |
| `clobber` (control) | Ollama 7B | off | The same agents **without** the brain → overlapping edits, lost work. **The value proof.** |
| `orchestration` (opt-in) | hosted (Anthropic/Gemini) | off/on | Tier-2: an orchestrator spawns subagents, drives the mission engine. **Documented as frontier-model-gated.** |
| `auth` | any | OIDC stub | The real bearer-token / OIDC relying-party path. |
| `gpu` / `cpu` | model/runtime select | — | Good experience (GPU, 7B) vs runs-anywhere (CPU, smaller model). |

Profiles compose (e.g. `coordination` + `gpu` + `auth`).

### Reference agent — `cmd/corral-agent` (in-repo)

A small, real agent loop — **not** a simulator. It connects as an MCP client,
discovers the brain's tools, and runs a perceive→decide→act loop driven by an LLM.

- **`Backend` interface** with adapters: `ollama`, `openai` (OpenAI-compatible),
  `anthropic`, `gemini`. Selected by `MODEL_BACKEND` + `MODEL` env. The *same*
  agent against three backends is the provider-agnosticism proof.
- **System prompt encodes the corralai discipline:** check active sessions, claim
  before editing, heartbeat while working, release on completion, respect
  reported conflicts (back off / pick another ticket).
- Works the seeded ticket backlog (below).

### Scenario — seeded backlog with engineered overlap

- A small throwaway repo is mounted RW into every agent container.
- The brain (or a seed file) is loaded with a backlog of tickets; **some tickets
  deliberately touch the same files**, so contention — and therefore visible
  claims, conflicts, and hand-offs — is **guaranteed**, not left to chance.
- Disciplined agents (with the brain) serialize on the contended files and stay
  in their lanes. Undisciplined agents (`clobber` profile) overwrite each other.

### Narrative spine: answering "will this trample my stuff?"

That sentence is the first objection every developer raises, so the demo is built
to answer it — and the objection *after* it — in three beats:

1. **The fear, made real (`clobber` control):** identical agents and workload,
   **no brain** → overlapping writes, lost edits, garbled files. Yes, this is
   exactly what you're afraid of.
2. **The fix (`coordination`):** same agents *with* corralai → atomic exclusive
   claims (Coordination-Core #1) serialize the contended files; nobody trampled.
3. **The hard case (parked lease):** *"but what if an agent goes AFK behind my
   approval prompt and just sits on my files?"* An agent goes
   `awaiting_approval` mid-edit; the swarm UI shows its lease parked with a
   countdown; after the (demo-shortened) grace window the lease downgrades to
   advisory; a peer proceeds **with a surfaced warning**; the parked agent
   returns and **re-validates instead of clobbering**. The messy real-world case,
   handled without trampling.

A short captured artifact (clobbered-hunk count, or a before/after of one
contended file) makes beats 1 vs 2 legible at a glance.

### Status & parked-lease in the swarm UI

The UI renders the permission posture so the hazard is *visible* (Coordination-Core
#2–#4):

- `working` node = normal pulse; `awaiting_approval` node = amber with a
  "⏸ waiting on human" badge.
- Exclusive claims held by a parked agent render as **amber/dashed "parked"
  edges** with a **countdown to downgrade** ("lease opens to peers in 2:14"),
  then flip to a "downgraded — peers may proceed" state, then "re-validating" when
  the owner returns.

This makes the whole park → degrade → open → re-validate lifecycle watchable —
the visual proof that corralai handles stalls without either deadlocking the
fleet or trampling work.

### Model & hardware

- **Default: a 7B tool-capable model** (e.g. Qwen2.5-Coder-7B) — best-in-tier at
  tool calls, which is the agent's entire job. `OLLAMA_MODEL` overrides it.
- `gpu` profile uses the NVIDIA runtime (fast); `cpu` profile swaps to a smaller
  model so it runs anywhere (slower, weaker tool calls — acceptable fallback).
- Rationale: a demo where agents fumble tool calls reads as "broken." Competence
  by default beats lowest-common-denominator; CPU remains the escape hatch.

### Auth

- **Default: dev mode (auth off)** for zero-friction first-run.
- `auth` profile stands up a **minimal local OIDC stub** that mints bearer tokens
  the agents present, exercising the real relying-party path
  (`CORRALAI_OIDC_ISSUER`/`_AUDIENCE`) without any external IdP.

### Bring-your-own agent harness

Docs (`docs/demo.md` or similar) showing how to point **any existing MCP agent**
(Claude Code, Gemini CLI, Cursor, …) at the brain via its `.mcp.json`, so the
ecosystem isn't locked to our reference agent. The reference agent guarantees a
turnkey first-run; the BYO docs prove the substrate is genuinely open.

---

## Capability Tiers (honesty as credibility)

| Tier | Primitives | Model requirement | Demo home |
|---|---|---|---|
| **1 — Coordination** | claim / release / heartbeat / presence / instructions | any tool-capable model, incl. local 7B | `coordination` (default) |
| **2 — Orchestration** | subagent spawn, in/out-of-process delegation, mission drive | frontier-class (hosted) | `orchestration` (opt-in) |

The docs state plainly: the local 7B does **coordination**, not **orchestration**.
Not overselling the small model is itself a credibility signal. (Deliverable 1
still tests tier-2 *mechanics* deterministically by driving the engine/tools
directly — no LLM competence required.)

## What Proves What

| Claim | Proven by |
|---|---|
| Coordination logic is correct under concurrency | Deliverable 1 (CI harness) |
| **Two agents are never both told "clear" on the same file** | Deliverable 1: atomic exclusive race (Core #1) |
| **"This will trample my stuff" — it won't** | `clobber` vs `coordination` contrast (beats 1–2) |
| **A parked agent neither deadlocks nor tramples the fleet** | parked-lease scenario (harness Core #3–#4 + demo beat 3) |
| Real agents (any model) coordinate safely | `coordination` profile |
| Without the substrate, agents clobber each other | `clobber` profile |
| Provider-agnostic (same agent, many models) | `corral-agent` backend swap |
| Orchestration works with frontier models | `orchestration` profile |
| Not locked to our agent | BYO-agent docs |

## Acceptance Criteria (ease-of-spin-up is a requirement, not a nicety)

- `make demo` on a clean clone, with Docker installed and **no credentials**,
  brings up the brain + Ollama + agents and produces visible coordination in the
  swarm UI within a few minutes (model pull included).
- `go test ./...` runs the coordination harness deterministically (green under
  `-race`), no docker, no network egress, no LLM.
- Switching to a hosted backend is a single env var; switching on auth is a single
  profile flag.
- A first-time contributor needs to read **one** short doc section to run the demo.
- **No-clumsiness bar:** zero manual steps between `make demo` and visible agent
  motion; terminal output names the URL to open and what to watch for; missing
  Docker/GPU/model degrades to a clear instruction, never a raw stack trace.
- **Extensibility seam:** adding a new model backend is a single `Backend` adapter;
  adding a new scenario is a new backlog seed — both documented, so other
  developers can expand the demo without touching the core.

## Open Questions (resolve in planning)

- **Backlog seeding mechanism:** seed tickets via a brain tool/endpoint at startup,
  or a mounted seed file the agents read? (Leaning: a small seed step the compose
  runs against the brain.)
- **Clobber-evidence artifact:** what exactly we capture to make "lost work"
  legible (hunk count, contended-file diff, or a short summary line).
- **CPU-profile model pick:** which smaller model balances "runs anywhere" against
  tool-call reliability.
- **Optional nightly CI integration job** running the `coordination` profile
  headless as a smoke test (separate from the deterministic suite).

## Out of Scope

- Load/scale/perf benchmarking.
- A production multi-tenant deployment story (covered by existing Hetzner deploy).
- Restoring/altering the `branch-guard` hook (unrelated session change).

## Artifact Inventory

- `internal/coord/store.go` — Coordination-Core changes: `now` time-seam
  one-liner (#5); atomic transactional exclusive `ClaimPaths` (#1); `Agent.status`
  + `status_since` (#2); derived parked-lease non-enforcement at conflict-check
  time (#3); contested-claims flag on `Bootstrap`/heartbeat response (#4).
- `internal/brain/{server,inbox}.go` — carry `status` on `heartbeat`/`bootstrap`
  tool I/O; surface contested-on-resume.
- `internal/ui/` — `awaiting_approval` node state + parked/countdown/downgraded/
  re-validating claim-edge rendering.
- `internal/brain/remote_harness_test.go` (+ optional `internal/cohort` helper) —
  Deliverable 1.
- `cmd/corral-agent/` — reference agent (backend interface + adapters + loop).
- `deploy/demo/docker-compose.yml` (+ profile overlays), `Dockerfile`s for brain
  and agent, `Makefile` targets (`demo`, `demo-clobber`).
- `deploy/demo/oidc-stub/` — minimal local OIDC issuer (auth profile).
- `deploy/demo/workspace/` + backlog seed — the shared scenario.
- `docs/demo.md` — one-command quickstart + BYO-agent harness instructions.
