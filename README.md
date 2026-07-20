# Corralai

[![CI](https://github.com/pdbethke/corralai/actions/workflows/deploy.yml/badge.svg?branch=main)](https://github.com/pdbethke/corralai/actions/workflows/deploy.yml)
[![License: Elastic 2.0](https://img.shields.io/badge/license-Elastic--2.0-e8a838)](LICENSE)
[![docs](https://img.shields.io/badge/docs-corralai.dev-2f6f4e)](https://corralai.dev/docs/getting-started/)
[![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/pdbethke/corralai/badge)](https://securityscorecards.dev/viewer/?uri=github.com/pdbethke/corralai)

***Nemo iudex in causa sua*** — no one may be judge in their own cause. The one who
wrote the code doesn't get to certify it: the verdict is **measured by execution**, by
a **decorrelated** party, behind a **human gate**. That maxim isn't a slogan here — it's
the constraint everything below is built on. ([why it's the whole design](https://corralai.dev/field-notes/nemo-iudex/))

> **An audit for software change.** Certify a change **by execution, not opinion**:
> run the check in a jail, measure the result yourself, sign a tamper-evident
> record, and gate the merge. Across any model (local 7B to frontier), behind real
> fences, human-gated, every run recorded and replayable.

In the age of AI, the thing that wrote the code tends to be the thing that grades
it — the model writes the code and says it's good, or writes the tests for its own
code and reports that they pass. That's the author reading his own verdict into the
record, and authors are kind to themselves. Corral is built the other way: the party
that did the work never certifies the work, nothing is taken on a model's say-so, and
the one number that means anything is the one no model was allowed to author — it's
what happened when the check actually ran, in a sandbox.

## `corral certify --local` — the whole thing in one command

The fastest way to see it is `--local`: a complete adversarial audit, in-process, off
your own key, no server.

```bash
go install github.com/pdbethke/corralai/cmd/corral@latest
export ANTHROPIC_API_KEY=sk-ant-...

corral certify --local \
  --code path/to/your/file.py \
  --goal "what this code must guarantee" \
  --out verdict.json \
  -- python -m pytest
```

It asks the question you can never answer honestly about your own code — *do my tests
actually test anything, or do they just pass?* — and answers it by execution:

- A **mutant-generator** seeds real, goal-violating bugs into your code.
- Your **own test suite** is run against every one of them, **in a jail** — the
  fraction it catches (the *kill-rate*) is the adequacy score, measured, never a
  self-report.
- A **test-writer** authors a compiling test that kills whatever your suite missed,
  proving the gap is real and catchable — and hands it back to you.
- A **test-critic** — always a *different*, decorrelation-enforced model — reads your
  suite cold and flags vacuous, designed-to-pass tests. Its opinion is carried as
  **unverified advice; it never gates the verdict.**

You get a signed verdict — `certified` or `needs-review` — printed and written to a
local, tamper-evident ledger. `--out` also writes it as a self-contained file you
re-check anytime, offline:

```bash
corral certify verify verdict.json --pubkey "$(corral certify pubkey)" --allow-unanchored
```

Go, Python (pytest), Ruby (minitest/RSpec), JavaScript (node:test), and TypeScript
(tsc + node:test) — the language is inferred from `--code`'s extension; C is next,
each a plugin in `internal/lang`. By default the audit runs two distinct Claude
models off one key (Sonnet writes/mutates, Haiku critiques) — decorrelation satisfied
with a single key; on that same default path, `--critic-model gemini-3.5-flash` plus
`GEMINI_API_KEY` (or `GOOGLE_API_KEY`) routes the critic to Gemini via the
OpenAI-compatible Google endpoint, a real cross-vendor critic, while the writer and
mutant-generator stay on Claude — a missing key fails the run closed rather than
silently falling back. Full walkthrough of a real verdict: **[the "first audit"
guide](https://corralai.dev/docs/first-audit/)**.

### Certify a change by its declared check

The other entry point takes any commit and any command:

```bash
corral certify <ref> -- <cmd>      # e.g. corral certify HEAD -- go test ./...
```

It checks `<ref>` out into a jail, runs `<cmd>`, reads the exit code, and writes a
signed record you verify offline with `corral certify verify` — no server required.
`--brain <url>` optionally posts it to a running brain.

## Three constraints the machine enforces

Not a slogan — the code refuses to do otherwise.

1. **The judge is a different judge.** Cross-model checking is **decorrelated by
   construction**: the model that critiques a suite is *forced* to differ from the one
   that wrote the exposing test — the run refuses to start where they collapse onto
   one model. Most swarm frameworks run one LLM in N roles: parallelism with
   *correlated* blind spots, because the "reviewer" shares the "builder's" failure
   modes when it's the same model. Bring Claude, Gemini, GPT, anything
   OpenAI-compatible, or a local model — no lock-in.
2. **The verdict is measured, not reported.** The gate **runs the actual check** —
   `go test`, the build, the control owner's tests, your suite against the mutants —
   itself, in the jail, and reads the result. A worker's "it passed" is never the
   verdict; it's a claim, and the claim is checked by execution. The correctness call
   is a deterministic bit, not a judgment.
3. **It's built to be contained.** A model that writes and runs code is a security
   problem, so corral starts from *"an agent can be hijacked"* and answers it
   structurally: every command runs behind **fences** (a jail, a credential boundary,
   trust-tiered knowledge), and because all traffic funnels through the brain, every
   action is **recorded and attributable**. Prevention *and* forensics — see
   **[SECURITY.md](SECURITY.md)**.

The name is the metaphor: the **corral** is the enclosure the models work in, the
**fences** are the security boundaries, and the brain corrals a herd of (possibly
different) models — it coordinates and contains, it doesn't do the work itself.

> **Where it's at:** v0.1, solo-maintained, tested honestly — every claim in this
> README was run before it was written. Issues and verified-harness PRs welcome.

## The gate — for a repo, and for a control owner

Beyond the one-shot CLI, the headless **brain** daemon runs continuous gates that
branch protection can require:

- **The repo (merge) gate.** A poller watches each covered repo's open PRs; on a new
  head commit it checks the PR out, runs the repo's declared check **in the jail**
  (never a self-report), signs the result, and posts a `corral/gate` status.
- **The control gate.** The same poll-and-jail pattern, but it runs the **control
  owner's** independently-vetted tests against the PR head — not the repo's own check —
  and posts a distinct `corral/control-gate` status. The person accountable for code
  they didn't write sets the bar. It's separation of duties, mechanized: *a judge may
  not certify herself.*
- **Multi-forge primitives** (`internal/repo`) back both: clone, checkout,
  commit/push, and PR/review calls against **GitHub, GitLab, and Gitea**, including
  self-hosted instances (`CORRALAI_FORGES` maps a host to its type, API base, and
  token) — each forge's token stays isolated to its own host.

The same adversarial audit `--local` runs is available on the brain for a wired repo,
via the admin-only `start_adversarial_run` MCP tool (see [the flags reference
below](#the-audit-flags)).

## A knowledge corpus that makes every audit sharper

Audit knowledge compounds instead of dying with each context.

- **The corpus (`CORRAL.md`).** A repo carries its working knowledge as markdown in
  the repo itself — `CORRAL.md` at the root, `docs/corral/*.md` as the corpus. One
  corpus, four readers: developers read it as onboarding; any developer's coding agent
  queries it conversationally (point `.mcp.json` at the brain and ask); the herd
  searches it before working; and it grows the way code does — through pull requests,
  where **code review is the trust gate for knowledge exactly as it is for code**.
  Ingested as *advisory* memory (searchable, never auto-injected), so a repo you don't
  control can't smuggle authority in by shipping a file.
- **Shared memory** (DuckDB, full-text + optional HNSW vector) — a multi-tier
  searchable corpus; the source of truth is plain markdown. A finding one run learns
  becomes available to every later run, and every *model*. Lessons are
  **trust-tiered**: searchable, never auto-promoted into an authoritative instruction.
- **The learning loop — the herd proposes, a human approves.** Recurring finding
  signatures and clusters of similar lessons are swept into **skill proposals**: an
  LLM drafts corrective guidance plus a reusable skill, the operator approves or
  rejects it (a live **Proposals tab** or `corral-admin proposals`). Approval promotes
  it into vetted memory and a versioned skill; every later run starts already warned.
  The loop watches its own efficacy — if a signature keeps recurring after promotion,
  a revision proposal reopens.
- **Shared skills, human-gated.** Approved skills sync across the fleet via
  `corral sync`, so what one machine's herd learns, every machine's herd can *do* —
  but publishing to the fleet is superuser-only (a worker proposes, it can't publish).
  Corralai ships a [`using-corralai`](skills/using-corralai/SKILL.md) skill that
  teaches any coding agent to drive the gate.
- **Reference RAG** — upload your own grounding material (text · URLs · **PDFs**);
  chunked and vector-embedded (any OpenAI-compatible embedding endpoint) for agents to
  query. Runs on **embedded DuckDB — no Postgres, no separate vector database**.

## Watch it back — every run recorded and replayable

Nothing about a run is thrown away: every task's claim and completion, every finding
and its resolution, every command actually run, and the event log survive
indefinitely. `corral certify --record <file>.json` writes a replayable tape of an
audit — the pool's reasoning beats, the task lifecycle, the findings — in the same
`{events:[…]}` shape the corralai.dev cockpit replays. **With reasoning capture on,
the replay streams each model's own words, verbatim,** interleaved with the commands
they triggered (*"the retry test is flaky because the backoff refills too slowly"* →
`go test ✗`) — so you watch the herd *think*, not just move. One scrub bar moves the
whole cockpit — canvas, progress, files — to the same instant, at up to 16×; the
captured reasoning is real, never synthesized, which is what turns a replay into a
*debugger*.

**See it live at [corralai.dev](https://corralai.dev).** The hero is a real recorded
run replaying in your browser; the **recordings gallery** holds more, each labeled
with the hardware it ran on and honest per-run analytics. Full docs at
[corralai.dev/docs](https://corralai.dev/docs).

## Coordinate — one swarm or many

- **Coordination substrate** (SQLite, transactional) — atomic exclusive path/branch
  claims with TTL, presence, a lease/presence reaper, a completed-work log, one-call
  `bootstrap`.
- **Fleet analytics** (optional, MotherDuck) — runs and telemetry from many brains
  roll up into one place, retention/compaction built in.
- **Ask the fleet** — a natural-language oracle over that data ("what did agent X do
  across every run? who ingested that document?"), turning the audit trail into
  something you can query.
- **Cross-swarm coordination** — brains hold signed (Ed25519) identities and
  publish/read *advisory* claims through the fleet, so independent swarms avoid
  colliding — observe, never coerce.

## Run anywhere

- **Model-agnostic** — Ollama or any OpenAI-compatible backend (Gemini, OpenRouter,
  Anthropic, local, …). Not wired to one LLM.
- **Harness-agnostic** — the herd "contract" is nothing but MCP calls against the
  brain (`bootstrap → claim_task → work → complete_task`); `corral-agent` is its
  reference implementation. **`corral-harness`** loops any headless coding agent as a
  herd member — Claude Code, Gemini CLI, Codex, GitHub Copilot CLI — each bringing its
  own tool loop, sandbox, and **its own auth**: they run on their own Pro/Max/Plus
  subscriptions, no API billing.
  ```bash
  CORRAL_BRAIN=http://localhost:9019 AGENT_NAME=Cody AGENT_ROLE=reviewer \
  HARNESS_CMD='claude -p {prompt} --mcp-config {mcp_config} --allowedTools "mcp__corral,Read,Write,Edit,Bash" --permission-mode acceptEdits' \
  corral-harness
  ```
- **Auth from day 0** — identity was designed in, not bolted on:
  - **OIDC relying party, any provider** — point `CORRALAI_OIDC_ISSUER` at a discovery
    URL (Keycloak, Auth0, Okta, Dex, Zitadel, …); the brain validates bearer JWTs
    against its JWKS. Agents get tokens via `client_credentials`; humans via normal
    login. No bespoke auth.
  - **Principals & membership** — a member allowlist with superusers for the
    privileged surfaces. The verified principal from the token is AUTHORITATIVE:
    stamped over whatever name a client claims, so no agent can act as anyone else.
  - **Signed delegation tokens** — an agent can spawn an out-of-process subagent with a
    scoped, TTL-bound token: the subagent acts under its own identity, accountability
    rolls up to the spawning principal, the token dies on schedule.
  - **The human gate** — every admin write (approving a proposal, sharing memory,
    promoting a reference) refuses a delegation token even when it rolls up to a
    superuser: workers propose, the operator disposes. The same rule holds by
    convention in dev mode, so an agent can't vet its own knowledge.
  - **Read-only observer tokens** — for dashboards and demo audiences: watch the live
    swarm, every mutating call refused.
  - Dev mode (no issuer) runs open with the same code paths, so "works on my machine"
    and "works with auth" don't drift apart.

## Security model

The headline feature, not a footnote. Full write-up in **[SECURITY.md](SECURITY.md)**;
the short version is three pillars:

- **Prevention (the fences).** Every command runs in a `bwrap` jail (no network by
  default, workspace-confined, secret-free env). The git/forge token lives only in the
  brain — scrubbed from the environment, never written to `.git/config`, never given
  to an agent, never used against a forge other than its own. The "ask the fleet"
  query runs in a locked-down DuckDB connection that can't read files or reach secrets.
  Ingested knowledge is trust-tiered so a poisoned document can't become an
  instruction.

  This is what makes **full-auto safe**: an interactive harness gates risky commands on
  a human click — unworkable for a dozen autonomous agents overnight. Corralai bounds
  *what a command can touch* instead of asking *whether it may run*: the jail replaces
  the permission prompt. (Docker is only the demo's packaging — on bare-metal Linux the
  jail is one unprivileged `bubblewrap` package.)
- **Detection (forensics).** Because every agent acts *through* the brain — the single
  trusted egress — the brain records every consequential action, attributed to a
  verified principal. Agents can't forge or erase their own trail; the subject of the
  record doesn't control the ledger.
- **Isolated artifact storage.** Task outputs (screenshots, files) decouple into an
  isolated `corralai_task_artifacts.sqlite3` database. Uploads pass multiple gates:
  the uploader must hold an active lease on the target task, magic-byte inspection
  enforces a strict MIME allowlist (blocking executable/HTML scripts), size is capped
  at 5 MB, and paths are sanitized against traversal.
- **Portable, secure key storage.** Provider API keys and the worker token never sit
  in plaintext or leak into a process listing. `corral secret set NAME` reads the value
  from **stdin, never a CLI argument**, and the keystore resolves each secret through
  **env var → OS keyring → an age-encrypted file** — your OS keychain on a desktop, an
  age-encrypted store on a headless server (the identity fails closed, protected by a
  systemd credential or a `0600` key). Every log redacts secret values to a
  fingerprint. It's the GCP-ADC pattern, shipped in one binary.

Every security core was adversarially red-teamed, and the tests ship with the repo.
The codebase runs clean through **`gosec`** (0 findings at medium+ — every one fixed or
adjudicated inline) and **`govulncheck`** (0 known dependency vulnerabilities), both
enforced in CI by [`scripts/check-security.sh`](scripts/check-security.sh).

**Don't trust the claims — run them:** `go test ./...` and `bash scripts/check-security.sh`.

## The fleet — a daemon and its client apps

Corralai is a **headless server with thin client apps**, like a backup system:
`corral` holds the state and authority; everything else connects over MCP/HTTP.

| Binary | Role | CGO | Ships as |
|--------|------|-----|----------|
| **`corral`** | the **brain** — MCP coordination, the gates, task queue, memory, reference RAG, repo-work + multi-forge, the fleet oracle, embedded UI; owns the databases | yes | `deploy/demo/Dockerfile.brain` |
| **`corral-agent`** | the reference **agent** — model-agnostic worker | no | `deploy/demo/Dockerfile.agent` (distroless) |
| **`corral-observe`** | the **observer** — read-only credentialed window onto a brain's live UI | no | `deploy/observe/Dockerfile` (distroless) |
| **`corral-admin`** | the **operator** — privileged live console plus command verbs over MCP | no | binary / `go install` |
| **`corral-harness`** | the **harness-agent launcher** — loops any headless coding agent as a herd member on ITS auth | no | binary / `go install` |

The observer and admin consoles share one reverse-proxy core (`internal/console`),
parameterized read-only vs read-write.

## Platforms

The design premise keeps your OS mostly out of the picture: **the brain lives on a
Linux server; everything else joins it over MCP/HTTP.** A Mac or Windows developer
participates fully without installing anything beyond a config stanza.

| | Linux | macOS | Windows |
|---|---|---|---|
| **Thin client** (your coding agent + `.mcp.json`) | ✅ | ✅ | ✅ |
| **`corral-admin`** (operator CLI) | ✅ | ✅ compiles | ✅ compiles |
| **`corral-observe`** (read-only window) | ✅ | ✅ | ✅ |
| **`corral certify --local`** — real exec (bwrap jail) | ✅ | via Docker (`--jail container`) | via Docker/WSL2 |
| **`corral` (the brain)** | ✅ first-class | ⚠️ untested | via Docker/WSL2 |

**The jail is a Linux capability — and that's the point.** `bwrap` (bubblewrap) is
Linux namespaces; on a bare-metal Linux host it runs **unprivileged** (one package,
no root, no daemon). macOS and Windows have no equivalent, so exec runs inside a Linux
environment — Docker Desktop or WSL2, or the `--jail container` fallback. The brain's
two CGO deps (DuckDB memory, tree-sitter code index) make it the one binary that cares
about its platform; deploy it once on a Linux host (systemd + your tunnel/proxy).

## Why Go — and why your stack doesn't have to be

**The substrate is Go** because a coordination brain has infrastructure-shaped
requirements, and Go is the boring, correct answer: **one static binary per
component** (no runtime, no virtualenv, no `node_modules` on the server); **mostly
concurrent I/O** (dozens of agents heart-beating over MCP/HTTP+SSE is exactly what
goroutines are for); **embedded databases without an ops bill** (SQLite + DuckDB
compile straight in — no Postgres, no separate vector DB); and it **cross-compiles
honestly** (the Platforms table was produced with `GOOS=darwin|windows go build`).

**What corral audits is a different axis — any language the models know.** The gate
takes any command — `go test`, `pytest`, `npm test`, `cargo test` — and refuses to
certify until a passing run is on record. A Python-and-Svelte team never writes a line
of Go to run, join, or benefit from the gate; Go is just what the corral fence is made
of.

## The audit flags

`corral certify --local` is one command, but it fans out:

- **Sharded generation.** The file's top-level functions are bin-packed
  (complexity-balanced, deterministic) into up to `--max-shards` (default 8) generator
  seats, each attacking a different group of functions, so **every function gets
  probed** instead of whatever one generator happened to pick. `--n-mutants` (default
  5) is a **per-shard** budget; the default 8 shards means up to ~40 mutants scored.
- **The shadow challenger.** `--shadow-model` (default `claude-haiku-4-5` — the
  critic's model, no extra credential) fans a cheap challenger seat across every region
  for a region-controlled, execution-proven head-to-head between generator models —
  same file, same goal, same commit. **It never affects the verdict**: shadow mutants
  are scored and recorded to the scorecard (`corral scorecard`), but only the primary
  generator's mutants feed the kill-rate. It roughly doubles generator API calls and
  jail wall-clock; `--shadow-model off` disables it.
- **Robustness.** A non-terminating mutant is killed fast and counted (a broken loop
  can't stall the run); `--test-timeout` overrides the auto-derived per-run cap. The
  run always converges to a signed verdict — even when the herd can't author a killing
  test for a survivor, it routes to `needs-review` rather than spinning.
- **`--swarm N`** bounds how many audit tasks run concurrently (0 = auto-size to the
  host's cores, capped). **`--repo-dir <path>`** audits `--code` in the context of a
  whole cloned repo (the tree is seeded into the jail and the project's own test
  command, after `--`, grades it). **`--record <file>.json`** writes the replayable
  tape.

**The audit always runs sandboxed** (`bwrap` on Linux by default; `--jail container`
for a docker/podman fallback; `sandbox-exec` on macOS) — there is no unsandboxed
option. On Ubuntu 24.04+, apparmor disables unprivileged user namespaces and bwrap
won't start; the CLI's error message spells out the one-line fix, or use `--jail
container` with a toolchain image (`export CORRALAI_EXEC_IMAGE=python:3`). One gotcha:
the language toolchain has to be **jail-visible** — installed system-wide under `/usr`,
not a `--user`/snap/pyenv install invisible to the sandboxed mount namespace.

The hosted brain runs the same sharded + shadow machinery via `start_adversarial_run`
(`max_shards` default 8, ceilinged at 20 for a hosted run; `shadow_model` defaults on
daemon-wide via `CORRALAI_ADVPOOL_SHADOW_MODEL`, `off` to disable; the run deadline is
widened automatically when a shadow model is set, so shadow work can never force a
timeout `needs-review`).

## Running the brain

```bash
go test ./...
go run ./cmd/corral     # MCP /mcp/ · health /healthz · UI / · on 127.0.0.1:9019
```

Common knobs: `CORRALAI_OIDC_ISSUER`/`_AUDIENCE` (cross-machine auth) ·
`CORRALAI_GIT_TOKEN` + `CORRALAI_FORGES` (repo-work / multi-forge) ·
`CORRALAI_EMBED_URL` (reference RAG + vector search) · `CORRALAI_MOTHERDUCK` (fleet
analytics + oracle) · `MODEL_BACKEND`/`OPENAI_BASE_URL` (bring your own model). See
**[docs/DESIGN.md](docs/DESIGN.md)**.

### The jail, in detail

The `corral` / `corral-agent` process is never sandboxed — only the subprocess a check
spawns is isolated:

- **Default-deny.** Execution only runs once a backend has been resolved and
  preflighted. If the host can't isolate, execution stays disabled and returns a loud,
  actionable error — it never silently degrades to running unprotected.
- **`bwrap` backend (default, Linux).** Each command runs in an unprivileged namespace
  jail: network off, read-only root except the workspace, no privileged caps, a
  secret-free env (the token never reaches it). Needs `bubblewrap` present.
- **`container` backend.** `--jail container` (or `AGENT_EXEC_BACKEND=container`) runs
  the jailed command inside a docker/podman container with `--cap-drop=ALL`,
  `--read-only`, `--network=none`, and pid/memory limits — for hosts without bwrap.
- **Network off by default.** Opt a build step in only where it legitimately fetches
  deps.

bwrap shares the host kernel — it stops casual damage, egress, and filesystem escape,
**not** a kernel-exploit escape. For adversarial code use a stronger backend
(container/microVM); the pluggable `Isolator` makes that a drop-in.

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
**[corralai.dev](https://corralai.dev)** — a live-replay one-pager (`site/`, Astro,
Cloudflare Pages) · github.com/pdbethke/corralai. Full docs — concepts, a UI tour, and
a generated CLI reference — at [corralai.dev/docs](https://corralai.dev/docs).
