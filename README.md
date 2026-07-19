# Corralai

[![CI](https://github.com/pdbethke/corralai/actions/workflows/deploy.yml/badge.svg?branch=main)](https://github.com/pdbethke/corralai/actions/workflows/deploy.yml)
[![License: Elastic 2.0](https://img.shields.io/badge/license-Elastic--2.0-e8a838)](LICENSE)
[![docs](https://img.shields.io/badge/docs-corralai.dev-2f6f4e)](https://corralai.dev/docs/getting-started/)
[![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/pdbethke/corralai/badge)](https://securityscorecards.dev/viewer/?uri=github.com/pdbethke/corralai)

***Nemo iudex in causa sua*** — no one may be judge in their own cause. The one who
wrote the code doesn't get to certify it: the verdict is **measured by execution**, by
a **decorrelated** party, behind a **human gate**. That maxim isn't a slogan here — it's
the constraint everything below is built on. ([why it's the whole design](https://corralai.dev/field-notes/nemo-iudex/))

> **An audit for software change** — certify a change **by execution** (not
> opinion): run the check in a jail, sign a tamper-evident record, and gate the
> merge. Across any model (local 7B to frontier), behind real fences, human-gated,
> with every run recorded and replayable. *(A first slice of the adversarial,
> role-separated herd now exists — the **adversarial testing pool**, experimental
> and off by default; the broader staffed verification engine it's the seed of is
> still ahead. See the honest floor below.)*
>
> `corral certify <ref> -- <cmd>` checks out that commit into a jail, runs `<cmd>`,
> and writes a signed record you verify offline with `corral certify verify` — no
> server required; `--brain` optionally posts it to a brain. This certifies the
> change's **declared checks** — the control-owner tests are a later slice.
>
> `corral certify --local` runs the **adversarial testing pool** itself — the
> mutation-tested, decorrelated-critic audit described below — in one command,
> in-process, off your own `$ANTHROPIC_API_KEY`, no brain/daemon/MCP required.
> See the Quickstart for the exact invocation.

**Corral is re-focusing from a builder to a reactive audit / certification gate —
the CISO's tool, not another way to generate code.** The build-from-directive path
("give the brain a directive and a herd builds it") has been **retired**; see the
design spec
[`docs/superpowers/specs/2026-07-13-corral-refocus-audit-not-builder-design.md`](docs/superpowers/specs/2026-07-13-corral-refocus-audit-not-builder-design.md)
for the full reasoning. What's actually running today: a headless brain daemon
whose primary surface is its **gate** — a repo (merge) gate and a control gate that
poll open PRs, run checks **in a jail** (never trusting a self-report), and post a
signed `corral/gate` / `corral/control-gate` status that branch protection can
require. The mission engine that used to drive a build-and-iterate loop is still in
the codebase, but its Tick loop is **not started** — it's retained, dormant, as the
seed for a broader adversarial verification engine (staffed with
security/correctness/exploit-hunting roles instead of coder/builder roles) rather
than a code-generation one. A standalone `corral certify <ref> -- <cmd>` CLI now
exists (see above) and signs its records locally, no server required.

A first, narrower slice of that broader verification engine is now built and wired:
the **adversarial testing pool** — **experimental, off by default**
(`CORRALAI_ADVERSARIAL_POOL=1`). Given a code file plus the developer's own test
file, a brain-coordinated pool of 3 roles grades the *developer's own tests'*
adequacy: a **mutant-generator** seeds goal-violating mutants into the code, the
brain scores the **dev's own tests** against them in the jail (the kill-rate —
never a self-report), a **test-writer** authors a test targeting whatever survived
(proving the gap is real and catchable), and a **test-critic** (always a
*different*, decorrelation-enforced model than the test-writer) flags
vacuous/designed-to-pass tests in the dev's suite. Routing is dynamic and
gate-earned — the best-performing model per role, per the live leaderboard — and
the loop actually closes: a certified run's outcome feeds that same leaderboard,
so the next run routes better. Every converged run (certified or needs-review) is
signed via the same certify chain as `report_build`; a low kill-rate or an open
blocking finding always routes to **needs-review**, never auto-certified. **`corral certify --local` is the CLI trigger for this audit** — no server, no
MCP, just your own provider key (see Quickstart). The brain-hosted version of the
same pool (for a repo already wired to a running brain) is still started via an
admin-only `start_adversarial_run` MCP tool, and now runs the SAME sharded
mutant-generator + shadow-challenger machinery `--local` does (see Quickstart
for the mechanics; `CORRALAI_ADVPOOL_SHADOW_MODEL` is the daemon-wide on/off
switch, `max_shards`/`shadow_model` are the per-call `start_adversarial_run`
overrides). What it does **not** yet do, either way: no pentester role, no
concurrent runs (one active run at a time on a given brain), and its
certification threshold is a fixed constant today, not per-run configurable. The broader staffed verification engine and the
control-owner tests described in the spec remain **designed, not yet built** —
don't expect more than the above from this README; this is the honest floor.
Certify-by-execution now supports Go, Python (pytest), Ruby
(minitest/RSpec), JavaScript (node:test), and TypeScript (tsc + node:test),
with the language inferred from the code file's extension; C is next, each a
plugin in `internal/lang` (Python/Ruby runs need `pytest`/`ruby`+`rspec`
present on the brain host, TypeScript needs `tsc`+`@types/node` — missing it
fails closed, never a false certify).

What still stands from the original build:

1. **It's multi-model, not just multi-agent.** Most swarm frameworks run one LLM in
   N roles — parallelism with *correlated* blind spots, because the "reviewer"
   shares the "builder's" failure modes when it's the same model. Corralai lets each
   role run a *different* model and coordinates them through one brain — so
   cross-model checking is **decorrelated by construction**, no single model grading
   its own work. That's the foundation the staffed verification slice builds on. No
   lock-in: bring Claude, Gemini, GPT, anything OpenAI-compatible, or a local model.
2. **It's built to be contained.** Autonomous agents that write and run code are a
   security problem. Corralai starts from *"an agent can be hijacked"* and answers
   it structurally: every agent runs behind **fences** (jails, a credential
   boundary, sandboxed queries, trust-tiered knowledge), and because all traffic
   funnels through the brain, every agent action is **recorded and attributable**.
   Prevention *and* forensics — see **[SECURITY.md](SECURITY.md)**, which points at
   the adversarial tests you can run yourself to check the claims.
3. **It certifies by execution, not opinion.** The repo gate and control gate
   **run the actual check** (`go test`, the build, the control owner's vetted
   tests) themselves, in the jail, and read the exit code — never a worker's
   self-report. The correctness call is a deterministic bit, not a judgment —
   **a judge may not certify herself**. That's the line between "AI that *says*
   it's done" and "AI you can *check*."

The name is the metaphor: the **corral** is the enclosure agents work in, the
**fences** are the security boundaries, and the brain corrals a herd of (possibly
different) models — it coordinates and contains, it doesn't do the work itself.

> **Where it's at:** v0.1, solo-maintained, mid re-focus, and tested honestly —
> every claim in this README was run before it was written, and the
> [open threads](docs/DESIGN.md#open-threads-next) name what's still rough. Issues
> and verified-harness PRs are welcome.

## What runs today

**The gate is the primary surface, not a mission loop.** The build-from-directive
mission loop described in older write-ups of this project — a directive becomes a
mission, a dev-team of roles builds it phase by phase, the brain commits and opens
a PR, review feedback reopens a sprint — has been **retired**. The mission engine
(`internal/mission`) that used to drive that loop still exists in the codebase —
its dependency-ordered task queue, findings, and the human-gated needs-review path
are retained as the seed for the next slice — but its Tick loop is **not started**
by the daemon. What the daemon actually runs continuously:

- **The repo gate (merge gate).** A poller watches each covered repo's open PRs;
  on a new head commit it checks the PR out, runs the repo's declared check **in
  the bwrap jail** (never a self-report), signs the result, and posts a
  `corral/gate` status that branch protection can require.
- **The control gate.** The same poll-and-jail pattern, but it runs the **control
  owner's** independently-vetted tests against the PR head (not the repo's own
  check) and posts a distinct `corral/control-gate` status — the person
  accountable for code they didn't write sets the bar.
- **Multi-forge primitives** (`internal/repo`) back both gates: clone, checkout,
  commit/push, and PR/review calls against **GitHub, GitLab, and Gitea**, including
  self-hosted instances (`CORRALAI_FORGES` maps a host to its type, API base, and
  token) — each forge's token stays isolated to its own host.

The **staffing** system (role→agent/model assignment, dependency-aware dispatch)
is retained and re-pointed at verifier roles for the next slice, rather than
coder/builder roles — see the design spec linked above for where that's headed.

## Learn together — a shared memory

**A shared memory the whole herd reads and writes.**

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
- **Compose the herd — per mission.** A visual **Mission Composer** builds a
  mission's team — drag a model/agent onto each role, pick which **MCP
  endpoints** the herd may consume, and attach **lookbook** design directives —
  and persists the choice (role→agent map, endpoints, lookbook) for the mission.
  With mission creation retired as a build verb, this is retained wiring rather
  than a live "launch a build" flow; it's staged to re-point at composing a
  *verification* team for the next slice.
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
- **Egress control (the exit).** The mission engine's egress scanner (committed
  **secrets** are blocking; new/vulnerable deps and license conflicts are
  advisory) is retained in the codebase as part of the verification-engine seed,
  but with the mission Tick loop disabled it isn't wired into a running pipeline
  today. The gates cover the exit path that *is* live: the repo gate and control
  gate run their checks in the jail before signing a status, so nothing merges
  on an unverified report.
- **Isolated & Secure Artifacts Storage.** Rather than mixing task outputs (like agent-captured screenshots or files) into the primary queue database, Corralai decouples them into an isolated `corralai_task_artifacts.sqlite3` database. Uploads are strictly validated via multiple security gates: verifying that the uploading agent holds an active lease on the target task, running magic byte inspection to enforce a strict MIME allowlist (blocking malicious executable/HTML scripts), restricting size to 5MB, and sanitizing paths to prevent directory traversal.
- **Portable, secure key storage.** Provider API keys (OpenAI, Gemini, Anthropic, OpenRouter, …) and the worker token never sit in plaintext or leak into a process listing. `corral secret set NAME` reads the value from **stdin, never a CLI argument** (so it can't leak in `ps` or shell history), and the embedded keystore resolves each secret through **env var → OS keyring → an age-encrypted file** — your OS keychain on a desktop, an age-encrypted store on a headless server (the encryption identity fails closed and is protected by a systemd credential or a `0600` key, never plaintext beside the store). Every log and error redacts secret values to a fingerprint; nothing is embedded in the binary. It's the GCP-ADC pattern, shipped in the one binary.

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
| **`corral`** | the **brain** — MCP coordination, task queue, the retained (not-Tick-started) mission engine, memory, reference RAG, repo-work + multi-forge, fleet oracle, embedded swarm UI; owns the databases | yes | `deploy/demo/Dockerfile.brain` |
| **`corral-agent`** | the reference **agent** — model-agnostic worker; `queue` / `lead` / `scrum` modes | no | `deploy/demo/Dockerfile.agent` (distroless) |
| **`corral-observe`** | the **observer** — read-only credentialed window onto a brain's live UI | no | `deploy/observe/Dockerfile` (distroless) |
| **`corral-admin`** | the **operator** — privileged live console **plus** command verbs (instruct, mission, findings, resolve-findings, reference, member, proposals, analyze, mint-observer) over MCP | no | binary / `go install` |
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
Go-specific. The `DEMO_DIRECTIVE` + `make demo-mission` invocations below describe
that retired build-from-directive demo (see
[deploy/demo/README.md](deploy/demo/README.md) for the retirement note) and are
**not runnable today** — they're kept here to illustrate the language-agnostic
intent for whoever re-points the demo at the gate:

```bash
DEMO_DIRECTIVE="Build a FastAPI service exposing /healthz and /quote with pytest tests; 'pytest' must pass" make demo-mission
DEMO_DIRECTIVE="Build a Svelte 5 counter component with vitest tests; 'npm test' must pass" make demo-mission
```

The verification gate takes **any command** — `go test`, `pytest`, `npm test`,
`cargo test` — and refuses completion until a passing run is on record. A
Python-and-Svelte team never writes a line of Go to run, join, or benefit from
the swarm; Go is just what the corral fence is made of.

## Quickstart

The fastest way to see corral certify something is `--local`: one command, your
own key, no daemon.

```bash
go install github.com/pdbethke/corralai/cmd/corral@latest
export ANTHROPIC_API_KEY=sk-ant-...            # your own key

corral certify --local \
  --code path/to/your/file.go \
  --goal "what this code must guarantee" \
  --out verdict.json
```

That runs a full adversarial audit **in-process** — no brain daemon, no MCP, no
worker fleet: a mutant-generator seeds goal-violating bugs into your code, your
own test is scored against them **by execution, in a jail** (never a
self-report), a test-writer proves any gap is real by writing (and killing) the
test you were missing, and a decorrelated test-critic reads your suite cold. You
get a signed verdict — `certified` or `needs-review` — printed to stdout and
written to a local, tamper-evident ledger. `--out` also writes the signed record
as a self-contained file you can re-check anytime, offline, with no network call
(the run prints this exact line, filled in with your key):

```bash
corral certify verify verdict.json --pubkey "$(corral certify pubkey)" --allow-unanchored
```

`--allow-unanchored` is required because a `--local` record is signed by **your
own** key but never submitted to a public transparency log — an honest "signed
by you, not third-party witnessed" claim. The language and test command are
inferred from `--code` (a sibling `_test` file, `go test ./...`, `pytest`, …);
pass `--test`/`-- <cmd>` to override.

By default the audit runs two distinct Claude models off that one
`ANTHROPIC_API_KEY` (Sonnet writes/mutates, Haiku critiques) — decorrelation is
satisfied with a single key; add a second vendor's key and pass `--critic-model`
to cross a vendor boundary instead. It supports Go, Python (pytest), Ruby
(minitest/RSpec), JavaScript (node:test), and TypeScript (tsc + node:test) —
language inferred from `--code`'s extension.

**The mutant-generator is sharded, not one seat.** The file's top-level
functions are bin-packed into up to `--max-shards` (default 8) generator
seats, each attacking a different group of functions against the SAME dev
suite, so every function gets probed instead of whatever one generator
happened to pick. `--n-mutants` (default 5) is a **PER-SHARD** budget, not a
run total — the default 8 shards means up to ~40 mutants scored, not 5;
`--max-shards` is the width dial.

**A second, cheap "shadow" model attacks every shard again, by default, purely
for measurement.** `--shadow-model` (default `claude-haiku-4-5`, the critic's
model — no extra credential needed) fans a challenger seat across every
region for a region-controlled, execution-proven head-to-head between
generator models — same file, same goal, same commit. **It never affects the
verdict**: shadow mutants are scored in the jail and recorded to the
bug-catching scorecard (`corral scorecard`), but only the primary generator's
mutants ever feed dev-adequacy or the kill-rate. **This roughly doubles
generator API calls and jail-scoring wall-clock** — pass `--shadow-model off`
to disable it and run with only the primary generator.

Other flags worth knowing: `--swarm N` bounds how many audit tasks run
concurrently (0, the default, auto-sizes to this host's cores, capped);
`--repo-dir <path>` audits `--code` in the context of a whole cloned
repo/package (the tree is seeded into the jail and the project's own test
command, given after `--`, grades it — for real multi-file projects with
package imports); `--record <file>.json` writes a replayable tape of the run
(the pool's reasoning beats, task lifecycle, and findings, in the same
`{events:[…]}` shape the corralai.dev cockpit replays).

**Sharding and the shadow challenger are now wired for the hosted brain too**
(2026-07-19): `start_adversarial_run` sets `max_shards` (default 8, same as
`--local`, ceilinged at 20 for a hosted run — see `CORRALAI_ADVPOOL_*` below)
and defaults `shadow_model` on daemon-wide via `CORRALAI_ADVPOOL_SHADOW_MODEL`
(unset = on, `claude-haiku-4-5`; `off` disables it for the whole daemon; a
per-call `shadow_model` in `start_adversarial_run` overrides either way, same
override semantics as `--local`'s `--shadow-model`). This is a real cost
change on the hosted gate: for a file with 8+ named symbols at stock
defaults, a hosted run now costs roughly **16 generator LLM calls** (8
primary + 8 shadow, up from 1), **32 total mutants scored** (16 primary + 16
shadow, up from 5), and **~45 jail executions** (each `Score` call re-runs a
baseline plus one run per mutant, up from ~8) — bounded on the hosted side by
the `max_shards` ceiling and `CORRALAI_ADVPOOL_RUN_DEADLINE_S`, which the
daemon widens automatically when its shadow model is configured (the same
deadline-widening `--local`'s `--timeout` gets, so shadow work can never
force a run into a timeout `needs-review` verdict). Set
`CORRALAI_ADVPOOL_SHADOW_MODEL=off` to run the hosted pool primary-only.

**The audit always runs sandboxed** (`bwrap` on Linux by default; `--jail
container` for a docker/podman fallback; `sandbox-exec` on macOS) — there's no
unsandboxed option. On Ubuntu 24.04+, apparmor disables unprivileged user
namespaces by default and bwrap won't start (`bwrap: setting up uid map:
Permission denied`); the CLI's error message spells out the exact one-line fix.
The zero-sudo alternative is `--jail container` with a toolchain image —
`export CORRALAI_EXEC_IMAGE=golang:1.26` (or `python:3`, `ruby:3`, `node:22`,
matched to `--code`) — which runs the jailed test command inside that container
instead:

```bash
export CORRALAI_EXEC_IMAGE=python:3
corral certify --local --jail container --code app/passwd.py --goal "…" --out verdict.json
``` One gotcha that costs people
an hour: the language toolchain has to be **jail-visible** — installed
system-wide under `/usr` (e.g. your distro's `golang`/`python3` package), not a
`--user`/snap/pyenv install invisible to the sandboxed mount namespace; a snap
`go` or a `pip install --user pytest` won't be found once the command runs
inside the jail.

See **[the "first audit" walkthrough](https://corralai.dev/docs/first-audit/)**
for a real verdict end to end.

---

Running the full brain instead of `--local` (the coordination substrate the
gates below run inside of):

```bash
go test ./...
go run ./cmd/corral     # MCP /mcp/ · health /healthz · swarm UI / · on 127.0.0.1:9019
```

Open `http://127.0.0.1:9019/` for the live swarm + **Progress** tab (dev: auth
off).

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
