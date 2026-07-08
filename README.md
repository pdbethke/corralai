# Corralai

[![CI](https://github.com/pdbethke/corralai/actions/workflows/deploy.yml/badge.svg?branch=main)](https://github.com/pdbethke/corralai/actions/workflows/deploy.yml)
[![License: Elastic 2.0](https://img.shields.io/badge/license-Elastic--2.0-e8a838)](LICENSE)
[![docs](https://img.shields.io/badge/docs-corralai.dev-2f6f4e)](https://corralai.dev/docs/getting-started/)

> **Status: early (v0.1).** Solo-maintained, moving fast, tested honestly — every
> demo claim in this README was run before it was written, and the
> [open threads](docs/DESIGN.md#open-threads-next) list what's known-rough.
> Expect sharp edges; issues and verified-harness PRs are very welcome.

**Coordinated multi-agent, multi-model.** Give a headless brain one directive —
*"build me a World Cup scores dashboard"* — and it turns it into a mission that a
team of AI agents plans, builds, verifies, **re-plans when they hit problems**, and
**iterates with the client** until it's accepted. The agents can be *different
models* — a Claude builder, a Gemini reviewer, a local model for the cheap passes —
all coordinating through one brain, behind real fences. All watchable live.

Three things make it different from the pile of agent-swarm demos:

1. **It's multi-model, not just multi-agent.** Most swarm frameworks run one LLM in
   N roles — parallelism with *correlated* blind spots, because the "reviewer"
   shares the "builder's" failure modes when it's the same model. Corralai lets each
   role run a *different* model and coordinates them through one brain, so review
   becomes genuinely **adversarial and decorrelated — cross-model review by
   construction**. No lock-in: bring Claude, Gemini, GPT, anything
   OpenAI-compatible, or a local model.
2. **It's adaptive, over a shared memory.** No central orchestrator drives the
   agents step-by-step. The brain holds the shared state — a task queue, path
   claims, findings, and a **persistent, searchable memory** — and the agents pull
   work, coordinate through it, and *reshape the plan as they learn*. What one agent
   learns it writes back to the shared memory (trust-tiered, so unvetted notes can't
   pose as authoritative), so knowledge **compounds across agents, models, and
   missions** instead of dying with each throwaway context. A high-severity finding
   rewrites the plan; a client rejection opens the next sprint.
3. **It's built to be contained.** Autonomous agents that write and run code are a
   security problem. Corralai starts from *"an agent can be hijacked"* and answers
   it structurally: every agent runs behind **fences** (jails, a credential
   boundary, sandboxed queries, trust-tiered knowledge), and because all traffic
   funnels through the brain, every agent action is **recorded and attributable**.
   Prevention *and* forensics — see **[SECURITY.md](SECURITY.md)**, which points at
   the adversarial tests you can run yourself to check the claims.

The name is the metaphor: the **corral** is the enclosure agents work in, the
**fences** are the security boundaries, and the brain corrals a herd of (possibly
different) models — it coordinates and contains, it doesn't do the work itself.

## The adaptive loop

A directive becomes a mission; the brain decomposes it into a dependency-ordered
task **queue**; the agents **pull** ready tasks and execute them; their
**structured findings** feed a two-tier re-planner; the mission **converges** when
the client accepts.

```
 CLIENT (you, or a modeled product-owner agent)
   │ directive ↓                 ↑ accept / feedback → next sprint
   ▼                             │
 LEAD ── research → design → build-core → build → test ∥ secops ∥ perf → integrate → docs → retro
 (orchestrates,        the dev team (one role per phase)
  re-plans, reworks)          SCRUM (standups · stall call-outs · nudges)
   └── findings → reflex fix+verify  ∥  lead supersede / re-architect → converge
```

- **A whole dev team, modeled as roles:** researcher · designer · builder ·
  tester · pentester · perf · integrator · writer · reviewer · lead · client ·
  **scrum master** (a deterministic standup tier — narrates progress in the live
  console, names stalled claims, nudges their holders; the brain's reclaim rules
  stay the enforcement floor).
- **Two-tier re-planning:** a *reflex* tier deterministically spawns fix +
  re-verify tasks for high-severity findings; an *LLM lead* tier handles judgment
  — superseding stale work, reopening done work, re-architecting — with full task
  lineage. Convergence is bounded (caps + loop-until-dry).
- **Sprints + client feedback:** review-enabled missions await client acceptance;
  feedback opens the next sprint (a human via the UI / `corral-admin`, or an
  autonomous client agent).
- **Live Progress tab** — watch the plan fill in, agents claim steps, findings
  appear, and the plan get rewritten, in real time (SSE).

## What it does

**Ship real code.** A repo-work mission clones a target repository, the agents
work in an isolated snapshot, the brain commits their changes to a branch and
**opens a pull request** — then watches for review. A `CHANGES_REQUESTED` review
(or, on GitLab, an unresolved discussion) automatically enqueues rework tasks and
the loop continues until the PR is approved.

- **Multi-forge:** the same engine targets **GitHub, GitLab, and Gitea**,
  including self-hosted instances (`CORRALAI_FORGES` maps a host to its type, API
  base, and token). The forge is selected by the repo's host; each forge's token
  stays isolated to its own host.
- **Semantic code index** — a per-mission index over the target repo (symbol-aware
  chunking via tree-sitter across 12 languages) gives the agents BM25 + vector
  code search so they ground changes in the real code, not guesses.

**Learn together — a shared memory.**

- **Shared memory** (DuckDB, full-text + optional HNSW vector) — a multi-tier,
  searchable corpus the *whole swarm* reads and writes; the source of truth is plain
  markdown. A lesson one agent learns becomes available to every agent — and every
  *model* — on later work, so knowledge compounds instead of dying with each
  context. Lessons are **trust-tiered**: searchable, but never auto-promoted into an
  authoritative instruction (see the security model).
- **The learning loop — the herd proposes, a human approves.** Recurring failure
  signatures (the same finding, again and again) and clusters of similar lessons
  are swept into **skill proposals**: an LLM drafts corrective guidance plus a
  reusable skill, Shep announces the pending proposal at standup, and the
  operator approves or rejects it — from its own **Proposals tab** (a live
  count badge) or `corral-admin proposals`. Approval promotes the guidance into vetted memory
  and a versioned skill artifact; every later mission's instructions carry the
  top vetted lessons (fence-wrapped, clearly labeled, capped at 3) so the herd
  starts each mission already warned. And the loop watches its own efficacy:
  if the same signature keeps recurring *after* promotion, a revision proposal
  reopens for the human to reconsider.
- **Shared skills + hooks — capability, not just knowledge.** Approved skills (and
  guardrail **hooks**) are versioned in the brain's **artifact store** and sync
  across the whole fleet: a `corral sync` pulls every changed skill/hook, so what
  one machine's herd learns, every machine's herd can *do*. It's team-shared
  capability for an agent fleet — **memory *and* skills**, both human-gated:
  publishing to the fleet is superuser-only (a worker proposes, it can't publish),
  and hooks are *staged for human review* rather than auto-applied, because
  executable guardrails should never silently activate. Corralai even ships a
  [`using-corralai`](skills/using-corralai/SKILL.md) skill that teaches any coding
  agent to drive the herd.
- **Reference RAG — upload your own grounding material** (text · URLs · **PDFs**); it's chunked and **vector-embedded** (any OpenAI-compatible embedding endpoint, so it's never tied to one machine) for agents to query. Runs on **embedded DuckDB — no Postgres, no separate vector database to operate**.
- **Swarm Design Lookbook:** A premium cockpit interface for uploading design screenshot mockups (PNG/JPEG) alongside visual layout guidelines. Built-in **one-click prompt emulation** makes it effortless to copy styling guidelines and instruct coding agents to match the exact look and feel of mockups.
- **Go-Native Headless Browser:** Built-in headless browser MCP tools powered by `github.com/go-rod/rod` compile directly into the Go binary. Swarm agents can statefully navigate, click, input text, and take screenshots of running web applications natively inside Docker or on host systems, with **zero Node.js or Playwright installation required**.

### The knowledge corpus (CORRAL.md)

A repo that runs with corralai can carry its working knowledge as a markdown
corpus in the repo itself: `CORRAL.md` at the root as the entry point,
`docs/corral/*.md` as the corpus. This is a team development pattern, not just
a feature — the same corpus serves four readers. Developers read it as
onboarding docs. Any developer's coding agent queries it conversationally: point
`.mcp.json` at the brain and ask about the codebase (`search_memory` finds the
corpus alongside everything else the herd knows). The herd itself searches it
before working and extends memory as it learns. And it grows the way code does —
through ordinary pull requests, where **code review is the trust gate for
knowledge exactly as it is for code**. On a repo mission the brain ingests the
corpus as *advisory* memory (searchable, never auto-injected), so a repo you
don't control can't smuggle authority in by shipping a file.

The learning loop closes the circle: skills the swarm proposes and a human
approves land in the same corpus — herd-discovered knowledge and
developer-written knowledge accumulate in one place, under one review gate,
readable by humans and queryable by every agent that joins.

### Watch it back (mission history + replay)

Nothing about a finished mission is thrown away: every task's claim and
completion, every finding and its resolution, every command an agent actually
ran, and the event log itself survive indefinitely. A **Completed tab** lists
past missions — directive, duration, task/finding counts, and (best-effort)
what got learned from them — with a detail view per mission and a **▶ replay**
button. Replay is read-only: it reconstructs the whole build from durable rows
and plays it back on the same corral canvas, at up to **16×**, with a scrub
bar — pause live traffic, watch history move. It works on missions that ran
before this shipped (positions are recomputed, not stored, so nothing needed
to be recorded in advance for the shapes to replay). The merged stream is
built entirely from durable task/finding/execution rows plus the event log
(`mission_completed`, review state) when the mission spoke it — the same
build, reconstructed, every time.

**And it's not just *what* they did — it's *why*.** With story capture on, the
replay streams each agent's own **reasoning**, verbatim, interleaved with its
commands (*"the retry test is flaky because the backoff refills too slowly"* →
`go test ✗`) — so you watch the herd *think*, not just move. Scrub to any moment
and **click an agent** to inspect its reconstructed state, or **click a task** to
read its causal chain — what triggered it, what it unblocked, and the commands
that ran under it. A **file-tree lens** reconstructs the paths the herd touched,
filling in as the tape plays, and **one scrub bar moves the whole cockpit** —
canvas, progress, and files — to the same instant. **Filter the console to one
agent** to follow a single thread. The captured reasoning is the agent's real
words, never synthesized — which is exactly what turns the replay into a
*debugger*: scrub back to the moment a model's reasoning went wrong and watch how
it cascaded to the others.

**See it live at [corralai.dev](https://corralai.dev).** The hero is a real
recorded mission replaying in your browser; the **recordings gallery** holds
more — different languages and model mixes, each labeled with the hardware it
ran on and honest per-run analytics — and every published run links to a
**result repo** so you can browse the actual code the herd wrote. Full docs at
[corralai.dev/docs](https://corralai.dev/docs).

**Coordinate — one swarm or many.**

- **Coordination substrate** (SQLite, transactional) — atomic exclusive
  path/branch claims with TTL, presence, a lease/presence reaper, a completed-work
  log, one-call `bootstrap`.
- **Fleet analytics** (optional, MotherDuck) — missions and telemetry from many
  brains roll up into one place, with retention/compaction built in.
- **Ask the fleet** — a natural-language oracle over that data ("what did agent X
  do across every mission? who ingested that document?"), which turns the audit
  trail into something you can actually query.
- **Cross-swarm coordination** — brains hold signed (Ed25519) identities and
  publish/read *advisory* claims through the fleet, so independent swarms can avoid
  colliding on the same work — observe, never coerce.

**Run anywhere.**

- **Model-agnostic** — agents drive themselves with Ollama or any OpenAI-compatible
  backend (Gemini, OpenRouter, Anthropic, local, …). Not wired to one LLM.
- **Harness-agnostic** — the herd "contract" is nothing but MCP calls against the
  brain (`bootstrap → claim_task → work → complete_task`), and `corral-agent` is
  merely its reference implementation. **`corral-harness`** loops any headless
  coding agent as a herd member — Claude Code, Gemini CLI, Codex, and GitHub
  Copilot CLI — each bringing its own tool loop, sandbox, and **its own auth**:
  they run on their own Pro/Max/Plus subscriptions, no API billing. Verified
  end-to-end: all four coordinated on one real mission — a Go recursive-descent
  parser built, tested, and gated with no API keys ([the all-frontier tape](https://corralai.dev/recordings))
  — and mixed in the *same* mission with a local 7B ([the mixtape](https://corralai.dev/recordings)).
  ```bash
  CORRAL_BRAIN=http://localhost:9019 AGENT_NAME=Cody AGENT_ROLE=builder \
  HARNESS_CMD='claude -p {prompt} --mcp-config {mcp_config} --allowedTools "mcp__corral,Read,Write,Edit,Bash" --permission-mode acceptEdits' \
  corral-harness
  ```
- **Auth from day 0** — identity was designed in, not bolted on:
  - **OIDC relying party, any provider** — point `CORRALAI_OIDC_ISSUER` at a
    discovery URL (Keycloak, Auth0, Okta, Dex, Authentik, …) and the brain
    validates bearer JWTs against its JWKS. Agents get tokens via the standard
    `client_credentials` grant; humans via their normal login. No bespoke auth.
  - **Principals & membership** — a member allowlist with superusers for the
    privileged surfaces (memory promotion, gateway registry, member management).
    The verified principal from the token is AUTHORITATIVE: it's stamped over
    whatever name a client claims, so no agent can register, claim, or complete
    work as anyone else.
  - **Signed delegation tokens** — an agent can spawn an out-of-process subagent
    with a scoped, TTL-bound token minted by the brain: the subagent acts under
    its own identity, accountability rolls up to the spawning principal, and the
    token dies on schedule (depth- and fan-out-capped).
  - **The human gate** — every admin write (approving/rejecting a learning-loop
    proposal, sharing memory, promoting a reference or memory entry) refuses a
    delegation token even when it rolls up to a superuser: workers propose, the
    operator disposes. In dev mode (no OIDC configured) the same rule holds by
    convention — a session that identifies itself as a worker (`corral-agent`,
    or its first `bootstrap`/`report_host` call) is refused at the same gates,
    so an agent can't accidentally vet its own knowledge just because dev mode
    has no cryptographic identity to check.
  - **Read-only observer tokens** — minted for dashboards and demo audiences:
    the holder can watch the live swarm but every mutating call is refused.
    Hand it to an ops screen without handing over the swarm.
  - Dev mode (no issuer configured) runs open with the same code paths, so
    "works on my machine" and "works with auth" don't drift apart.

## Security model

The headline feature, not a footnote. Full write-up in **[SECURITY.md](SECURITY.md)**;
the short version is three pillars:

- **Prevention (the fences).** Agents' shell commands run in a `bwrap` jail (no
  network by default, workspace-confined, secret-free env). The git/forge token
  lives only in the brain — scrubbed from the environment, never written to
  `.git/config`, never given to an agent, and never used against a forge other than
  its own. The "ask the fleet" query runs in a locked-down DuckDB connection that
  can't read files or reach secrets. Ingested knowledge is trust-tiered so a
  poisoned document can't become an instruction.

  This is what makes **full-auto safe**: an interactive harness gates risky
  commands on a human approval click — unworkable for a dozen autonomous agents
  working overnight. Corralai bounds *what a command can touch* instead of asking
  *whether it may run*: the jail replaces the permission prompt, so agents run at
  skip-permissions velocity with the blast radius confined to their own checkout.
  (Docker is only the demo's packaging — on bare-metal Linux the jail is one
  unprivileged `bubblewrap` package; see "[Do I need
  Docker?](deploy/demo/README.md#do-i-need-docker)".)
- **Detection (forensics).** Because every agent acts *through* the brain — the
  single trusted egress — the brain records every consequential action, attributed
  to a verified principal. Agents can't forge or erase their own trail; the subject
  of the record doesn't control the ledger.
- **Egress control (the exit).** The output is vetted before it ships. Right before
  the brain pushes the herd's branch and opens a PR, it scans the mission's
  *cumulative* changed files for committed **secrets** (API-key/token/private-key
  patterns) — a match is **blocking**: the push is withheld, a critical finding is
  filed, and the mission is parked. New or vulnerable dependencies and license
  conflicts are flagged (advisory). Crucially this runs on **every forge** — the
  floor GitHub's own secret-scanning doesn't cover on GitLab or self-hosted Gitea.
  So containment is complete: the agents are bounded on the way *in* (the jail),
  and their output is vetted on the way *out*.
- **Isolated & Secure Artifacts Storage.** Rather than mixing task outputs (like agent-captured screenshots or files) into the primary queue database, Corralai decouples them into an isolated `corralai_task_artifacts.sqlite3` database. Uploads are strictly validated via multiple security gates: verifying that the uploading agent holds an active lease on the target task, running magic byte inspection to enforce a strict MIME allowlist (blocking malicious executable/HTML scripts), restricting size to 5MB, and sanitizing paths to prevent directory traversal.

Every security core was adversarially red-teamed, and the tests ship with the repo.
The codebase also runs clean through static + supply-chain scanners: **`gosec`** (0
findings at medium severity or higher — every finding is either fixed or adjudicated
with an inline justification) and **`govulncheck`** (0 known dependency
vulnerabilities). Both are enforced in CI by
[`scripts/check-security.sh`](scripts/check-security.sh), so they stay green.

**Don't trust the claims — run them:** `go test ./...` and `bash scripts/check-security.sh`
(see `SECURITY.md` for the load-bearing suites by claim).

## The fleet — a daemon and its client apps

Corralai is a **headless server with thin client apps**, like a backup system:
`corral` holds the state and authority; everything else connects over MCP/HTTP.

| Binary | Role | CGO | Ships as |
|--------|------|-----|----------|
| **`corral`** | the **brain** — MCP coordination, task queue, missions + re-planner, memory, reference RAG, repo-work + multi-forge, fleet oracle, embedded swarm UI; owns the databases | yes | `deploy/demo/Dockerfile.brain` |
| **`corral-agent`** | the reference **agent** — model-agnostic worker; `queue` / `lead` / `client` / `scrum` modes | no | `deploy/demo/Dockerfile.agent` (distroless) |
| **`corral-observe`** | the **observer** — read-only credentialed window onto a brain's live UI | no | `deploy/observe/Dockerfile` (distroless) |
| **`corral-admin`** | the **operator** — privileged live console **plus** command verbs (instruct, missions, review, findings, reference, members, analytics, mint-observer) over MCP | no | binary / `go install` |
| **`corral-harness`** | the **harness-agent launcher** — loops any headless coding agent (Claude Code, Gemini CLI, Codex, …) as a swarm agent on ITS auth (e.g. a Claude Max subscription, no API billing) | no | binary / `go install` |

The observer and admin consoles share one reverse-proxy core (`internal/console`),
parameterized read-only vs read-write.

## Platforms

The design premise keeps your OS mostly out of the picture: **the brain lives on
a Linux server; everything else joins it over MCP/HTTP.** A Mac or Windows
developer participates fully without installing anything beyond a config stanza.

| | Linux | macOS | Windows |
|---|---|---|---|
| **Thin client** (your coding agent + `.mcp.json`) | ✅ | ✅ | ✅ |
| **`corral-admin`** (operator CLI) | ✅ | ✅ compiles | ✅ compiles |
| **`corral-observe`** (read-only window) | ✅ | ✅ | ✅ |
| **`corral-agent`** — narrate mode | ✅ | ✅ compiles | ✅ compiles ¹ |
| **`corral-agent`** — real exec (bwrap jail) | ✅ | via Docker ² | via Docker/WSL2 ² |
| **`corral` (the brain)** | ✅ first-class | ⚠️ untested ³ | via Docker/WSL2 ³ |
| **The demo** (`deploy/demo`) | ✅ | ✅ Docker Desktop ⁴ | ✅ Docker Desktop ⁴ |

**Thin clients — any OS, zero install.** Anything that speaks MCP over
streamable-HTTP (Claude Code, Cursor, Gemini CLI, …) joins the swarm by pointing
its `.mcp.json` at the brain's URL. This is the primary way humans' machines
participate, and it is completely OS-agnostic.

**The brain is Linux-first by design.** Its two CGO dependencies (DuckDB memory,
tree-sitter code index) make it the one binary that cares about its platform.
Deploy it once, on a Linux host (systemd + your tunnel/proxy) — that's the
tested, supported shape.

**The jail is a Linux capability — and that's the point.** `bwrap` (bubblewrap)
is Linux namespaces; on a bare-metal Linux host it runs **unprivileged** (one
package from your distro, no root, no daemon). macOS and Windows have no
equivalent primitive, so exec-capable agents on those hosts run inside a Linux
environment — Docker Desktop or WSL2 — which is exactly what the demo packages.
Production alternatives for the outer boundary: a VM, gVisor, or rootless Podman
(see the demo README's security note).

Footnotes:

1. Narrate-mode `corral-agent` cross-compiles clean for macOS and Windows
   (Unix process-group handling is behind build tags in `internal/sandbox`;
   on Windows a timeout kills the direct process only — moot in narrate mode,
   which never execs).
2. Exec = bwrap = Linux. On a Mac, run exec agents in Docker Desktop and — the
   trick worth knowing — keep **Ollama native on the host for Apple-silicon
   (Metal) speed**: set `OLLAMA_URL=http://host.docker.internal:11434` (knob in
   `deploy/demo/.env.example`) so containers coordinate while inference runs at
   full GPU speed. On Windows, WSL2 with the NVIDIA CUDA driver gives containers
   real GPU access.
3. The brain's CGO deps both ship macOS libraries, so a native `go build` on a
   Mac is expected to work — it just isn't in CI. Windows-native would need a
   MinGW toolchain and is not supported; use a container or WSL2.
4. No NVIDIA container runtime on macOS — use `make demo-cpu` or the host-Ollama
   trick in footnote 2. Verified compile targets above were checked with
   `GOOS=darwin|windows go build` at the time of writing; Linux paths are what
   CI runs and the demo exercises end-to-end.

## Why Go — and why your stack doesn't have to be

Two different questions people conflate:

**The substrate is Go** because a coordination brain has infrastructure-shaped
requirements, and Go is the boring, correct answer to them:

- **One static binary per component.** The brain, the agent, the observer, the
  admin CLI each ship as a single file — no runtime to install, no virtualenv to
  activate, no `node_modules` to reconcile on the server. `scp` + systemd is a
  deployment. The demo's agent containers are distroless *because they can be*.
- **A brain is mostly concurrent I/O.** Dozens of agents heart-beating, claiming,
  and streaming over MCP/HTTP + SSE is exactly the workload goroutines were made
  for — no async ceremony, no worker-process tuning.
- **Embedded databases without an ops bill.** SQLite (single-writer, transactional
  claims) and DuckDB (FTS + vector memory) compile straight in. There is no
  Postgres to stand up before your first swarm.
- **Cross-compiles honestly.** The Platforms table above was produced with
  `GOOS=darwin|windows go build` — one toolchain, every client OS.

**What the swarm builds is a different axis entirely — any language the models
know.** The agents' tools are `write_file` and `run_command` (or, for harness agents
like Claude Code, their own editors and shells): nothing about the pipeline is
Go-specific. The demo directive happens to be a Go package; make it yours:

```bash
DEMO_DIRECTIVE="Build a FastAPI service exposing /healthz and /quote with pytest tests; 'pytest' must pass" make demo-mission
DEMO_DIRECTIVE="Build a Svelte 5 counter component with vitest tests; 'npm test' must pass" make demo-mission
```

The verification gate takes **any command** — `go test`, `pytest`, `npm test`,
`cargo test` — and refuses completion until a passing run is on record. A
Python-and-Svelte team never writes a line of Go to run, join, or benefit from
the swarm; Go is just what the corral fence is made of.

## Quickstart

```bash
go test ./...
go run ./cmd/corral     # MCP /mcp/ · health /healthz · swarm UI / · on 127.0.0.1:9019
```

Open `http://127.0.0.1:9019/` for the live swarm + **Progress** tab (dev: auth
off). To watch the whole loop end-to-end on one command (bundled GPU Ollama):

```bash
cd deploy/demo
make demo-mission       # directive → team builds it → re-plans → client review → converge
```

The mission demo brings up the brain plus the full role team (builder, tester,
pentester, reviewer, plus a lead and a client agent), seeds a real directive, and
you watch it converge in the Progress tab. See **[deploy/demo/README.md](deploy/demo/README.md)**.

Common knobs: `CORRALAI_OIDC_ISSUER`/`_AUDIENCE` (cross-machine auth) ·
`CORRALAI_GIT_TOKEN` + `CORRALAI_FORGES` (repo-work / multi-forge) ·
`CORRALAI_EMBED_URL` (reference RAG + vector search) · `CORRALAI_MOTHERDUCK`
(fleet analytics + oracle) · `MODEL_BACKEND`/`OPENAI_BASE_URL` (bring your own
model). See **[docs/DESIGN.md](docs/DESIGN.md)**.

### Real execution

By default agents produce artifacts as text. With execution enabled, an agent can
`write_file` the actual artifact and `run_command` to build, run, and test it, then
report the real exit code and output (a failing build becomes a finding instead of
an assumption).

**The jail wraps the command, not the agent.** The `corral-agent` process is never
sandboxed — it keeps full network, its MCP session to the brain, its token, and all
research/RAG tools. Only the subprocess `run_command` spawns is isolated:

- **Default-deny.** `AGENT_ALLOW_EXEC=1` turns on `run_command`, but it only runs
  once a backend has been resolved and preflighted. If the host can't isolate,
  execution stays disabled and `run_command` returns a loud, actionable error — it
  never silently degrades to running unprotected.
- **`bwrap` backend (default, Linux).** Each command runs in an unprivileged
  namespace jail: network off, read-only root except the workspace, no privileged
  caps, a secret-free env (the agent's `CORRAL_TOKEN` never reaches it). Needs
  `bubblewrap` present (the demo's `Dockerfile.agent-exec` installs it).
- **Network off by default.** Set `AGENT_EXEC_NET=1` for a build step that
  legitimately fetches deps (`go mod download`, `npm install`).
- **`none` backend** runs commands unisolated and is opt-in only via
  `AGENT_EXEC_BACKEND=none AGENT_EXEC_UNSAFE_HOST=1` — for a host you've already
  hardened yourself.

| Var | Default | Meaning |
|---|---|---|
| `AGENT_ALLOW_EXEC` | `0` | Master gate for `run_command`. |
| `AGENT_EXEC_BACKEND` | `bwrap` | `bwrap` \| `container` (future) \| `none`. |
| `AGENT_EXEC_NET` | `0` | Network access for executed commands. |
| `AGENT_EXEC_UNSAFE_HOST` | `0` | Required to select `none`. |

bwrap shares the host kernel — it stops casual damage, egress, and filesystem
escape, **not** a kernel-exploit escape. For adversarial code use a stronger
backend (container/microVM); the pluggable `Isolator` makes that a drop-in. See
**[the design note](docs/superpowers/specs/2026-06-29-exec-isolation-design.md)**.

Go (go1.26). The brain is one CGO-enabled binary (it embeds the databases); the
client apps are CGO-free and ship on distroless.

## Credits

Inspired by — not derived from — prior open work (`mcp_agent_mail`,
`agent-orchestration`, `Agent-MCP`). Design concepts only; no third-party code
incorporated.

## License

Corralai is **source-available** under the [Elastic License 2.0](LICENSE)
(`Elastic-2.0`). You're encouraged to read the whole codebase, modify it, and
self-host it. The one restriction that matters: you may **not** provide Corralai to
third parties as a hosted or managed service.

Want to run it as a service anyway? A **commercial license** is available — contact
pdbethke@gmail.com.

Contributions are welcome under a one-time [CLA](CLA.md); see
[CONTRIBUTING.md](CONTRIBUTING.md).

---
**[corralai.dev](https://corralai.dev)** — a live-replay one-pager (`site/`,
Astro, Cloudflare Pages) · github.com/pdbethke/corralai. Full docs —
concepts, a UI tour, and a generated CLI reference — live at
[corralai.dev/docs](https://corralai.dev/docs).
