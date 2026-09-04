# The brain — the optional daemon, and what it is not

**Read this first.** Corral began as a coordination daemon — a **brain** with an
MCP tool surface, a task queue, shared memory, a learning loop, a live console
and a fleet of client binaries. The product moved: `corral certify` audits
in-process, on a laptop or a CI runner, and writes its rows to a ledger you own.
Everything in this document is **optional infrastructure**, and two limits hold
for all of it:

1. **Nothing here is required for a verdict.** `certify --local`, `certify
   --repo`, the GitHub Action, `verify`, `ui`, `seal`, `scans` and `models rank`
   never contact a brain.
2. **Nothing here is read by an audit.** Shared memory, the corpus, promoted
   skills and the learning loop's lessons are the brain's own state. An audit's
   prompts are built from the goal, the code and its symbols
   (`internal/testgen.GenerateMutantsPrompt`), and no `certify` path queries
   memory, skills or a prior run's mutants. A lesson the brain has learned does
   not change what corral plants or grades. Feeding the ledger back into an
   audit — as a *disclosed* input on the verdict — is designed and not built
   (see [`docs/design/adversarial-review.md`](../design/adversarial-review.md)).

The daemon still ships in the same binary (`corral` with no subcommand starts
it) and still runs corralai's own instance; it is kept as the substrate for
remote workers, a live console over a running herd, and the human-gated
proposal loop. It is not the first thing to run, and it is not a moving part of
the audit.

## The gate — for a repo, and for a control owner (brain-hosted)

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
via the admin-only `start_adversarial_run` MCP tool (the flags are the ones in
[the README](../../README.md#the-audit-flags)).

The hosted brain runs the same sharded + shadow machinery via `start_adversarial_run`
(`max_shards` default 8, ceilinged at 20 for a hosted run; `shadow_model` defaults on
daemon-wide via `CORRALAI_ADVPOOL_SHADOW_MODEL`, `off` to disable; the run deadline is
widened automatically when a shadow model is set, so shadow work can never force a
timeout `needs-review`).


## A knowledge corpus the brain's workers can read

Findings survive the context that produced them — for the brain's mission
workers, over MCP. **This corpus is not read by any audit** (see the top of
this page): `certify` never searches memory. What is shipped is the mechanism,
not a proven effect; corral does not measure whether a promoted lesson improves
anything, and until it does, treat this as plumbing rather than a result.

- **The corpus (`CORRAL.md`).** A repo carries its working knowledge as markdown in
  the repo itself — `CORRAL.md` at the root, `docs/corral/*.md` as the corpus. One
  corpus, four readers: developers read it as onboarding; any developer's coding agent
  queries it conversationally (point `.mcp.json` at the brain and ask); a
  mission worker can search it (`search_memory`) — the audit roles do not; and
  it grows the way code does — through pull requests,
  where **code review is the trust gate for knowledge exactly as it is for code**.
  Ingested as *advisory* memory (searchable, never auto-injected), so a repo you don't
  control can't smuggle authority in by shipping a file.
- **Shared memory** (DuckDB, full-text + optional HNSW vector) — a multi-tier
  searchable corpus; the source of truth is plain markdown. A finding a mission
  worker records becomes searchable by every later worker, and every *model*,
  that asks over MCP — an audit does not ask. Lessons are
  **trust-tiered**: searchable, never auto-promoted into an authoritative instruction.
- **The learning loop — the herd proposes, a human approves.** Recurring finding
  signatures and clusters of similar lessons are swept into **skill proposals**: an
  LLM drafts corrective guidance plus a reusable skill, the operator approves or
  rejects it (a live **Proposals tab** or `corral-admin proposals`). Approval promotes
  it into vetted memory and a versioned skill, available to the next worker that
  searches — not injected into any audit.
  The loop watches its own efficacy — if a signature keeps recurring after promotion,
  a revision proposal reopens.
- **Shared skills, human-gated.** Approved skills are shared across the fleet
  through the brain, so what one machine's herd learns, every machine's herd can
  *do* — but publishing to the fleet is superuser-only (a worker proposes, it
  can't publish).
  Corralai ships a [`using-corralai`](skills/using-corralai/SKILL.md) skill that
  teaches any coding agent to drive the gate.
- **Reference RAG** — upload your own grounding material (text · URLs · **PDFs**);
  chunked and vector-embedded (any OpenAI-compatible embedding endpoint) for agents to
  query. Runs on **embedded DuckDB — no Postgres, no separate vector database**.

## Watch it back — every run recorded and replayable

A brain-run mission keeps its event log — every task's claim and completion,
every finding and its resolution — in the brain's own store, and that is what
the cockpit's history and replay read. An audit keeps only what you ask it to:
`corral certify --local --record <file>.json` writes a replayable
tape of an audit — the pool's reasoning beats, the task lifecycle, the findings — in the same
`{events:[…]}` shape the corralai.dev cockpit replays. **With reasoning capture on,
the replay streams each model's own words, verbatim,** interleaved with the commands
they triggered (*"the retry test is flaky because the backoff refills too slowly"* →
`go test ✗`) — so you watch the herd *think*, not just move. One scrub bar moves the
whole cockpit — canvas, progress, files — to the same instant, at up to 16×; the
captured reasoning is real, never synthesized, which is what turns a replay into a
*debugger*.

**See it live at [corralai.dev](https://corralai.dev).** The hero is a real recorded
run replaying in your browser; the **recordings gallery** holds more, each labeled
with the hardware it ran on and honest per-run analytics. And
**[corralai.dev/warehouse](https://corralai.dev/warehouse/)** runs DuckDB itself — in
your browser, via WebAssembly — over the real audit ledger + execution telemetry, so
you can query the signed records with live SQL. Full docs at
[corralai.dev/docs](https://corralai.dev/docs).

## Coordinate — one brain or many

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
- **Shared reach (the MCP gateway)** — register any service (yours, wrapped as MCP)
  with the brain and the herd can *use* it without ever holding the key: the brain
  proxies the call, holds the upstream secret (never returns it), SSRF-guards every
  dial (resolve-and-pin), and appends the call to the audit ledger under the verified
  caller. Governance is scoped to bound mischief — `register_endpoint` makes an
  **owner-scoped** endpoint only that user can reach; only an admin's `promote_endpoint`
  makes it team-wide (optionally swapping in a team credential); `list_capabilities` /
  `call_capability` are the herd's use path. The same pattern as everything else:
  *share the capability, hold the credential.*

## Run anywhere

- **Model-agnostic** — Ollama or any OpenAI-compatible backend (Gemini, OpenRouter,
  Anthropic, local, …). Not wired to one LLM.
- **Harness-agnostic** — the worker contract is nothing but MCP calls against the
  brain (`bootstrap → claim_task → work → complete_task`, where the tasks are the
  adversarial-audit roles — mutant-generator, test-writer, test-critic); `corral-agent`
  is its reference implementation. **`corral-harness`** loops any headless coding-agent
  CLI as an audit-role worker — Claude Code, Gemini CLI, Codex, GitHub Copilot CLI —
  each bringing its own tool loop, sandbox, and **its own auth**: they run on their own
  Pro/Max/Plus subscriptions, no API billing.
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

## The brain's own security surfaces

The audit's security model is in [the README](../../README.md#security-model) and
[SECURITY.md](../../SECURITY.md). The daemon adds surfaces of its own:

- **The single trusted egress.** A worker acts *through* the brain, so the brain
  records every consequential action, attributed to a verified principal.
  Workers can't forge or erase their own trail; the subject of the record doesn't
  control the ledger. The git/forge token lives only in the brain — scrubbed from
  the environment, never written to `.git/config`, never given to a worker, never
  used against a forge other than its own. The "ask the fleet" query runs in a
  locked-down DuckDB connection that can't read files or reach secrets.
  Ingested knowledge is trust-tiered so a poisoned document can't become an
  instruction.
- **Isolated artifact storage.** Task outputs (screenshots, files) decouple into an
  isolated `corralai_task_artifacts.sqlite3` database. Uploads pass multiple gates:
  the uploader must hold an active lease on the target task, magic-byte inspection
  enforces a strict MIME allowlist (blocking executable/HTML scripts), size is capped
  at 5 MB, and paths are sanitized against traversal.
- **The console bundle needs a trust anchor you supply.** `corral-observe`,
  `corral-admin` and `corral-desktop` verify an Ed25519 signature over the console
  bundle before rendering it, against `CORRALAI_CONSOLE_PUBKEY` — for corralai's own
  brains that is the key published at
  [`deploy/console-release.pub`](../../deploy/console-release.pub). There is no
  default, on purpose: the development key in this repository has its private half
  committed, so treating it as a default would let anyone sign a bundle a client
  would render with the operator's session attached. `CORRALAI_CONSOLE_DEV=1`
  accepts that published key for local work and says so in the log.

## The fleet — a daemon and its client apps

When the daemon is running, it is a **headless server with thin client apps**:
`corral` holds the state and authority; everything else connects over MCP/HTTP.
(`TestDocsFleetTableCGOColumnIsTrue` builds every row of this table to keep the
CGO column honest.)

| Binary | Role | CGO | Ships as |
|--------|------|-----|----------|
| **`corral`** | the **brain** — MCP coordination, the gates, task queue, memory, reference RAG, repo-work + multi-forge, the fleet oracle, embedded UI; owns the databases | yes | `deploy/demo/Dockerfile.brain` |
| **`corral-agent`** | the reference **audit-role worker** — model-agnostic, claims an adversarial-audit role (mutant-generator / test-writer / test-critic) off the queue | no | `deploy/demo/Dockerfile.agent` (distroless) |
| **`corral-observe`** | the **observer** — read-only credentialed window onto a brain's live UI | no | `deploy/observe/Dockerfile` (distroless) |
| **`corral-admin`** | the **operator** — privileged live console plus command verbs over MCP | no | binary / `go install` |
| **`corral-desktop`** | the **desktop client** — native-window (`--app` mode) launcher onto a local console | no | binary / `go install` |
| **`corral-harness`** | the **harness-agent launcher** — loops any headless coding-agent CLI as an audit-role worker on ITS auth | no | binary / `go install` |
| **`corral-top`** | the **terminal dashboard** — a read-only TUI over a live brain (tasks, agents, findings), for a glanceable window without a browser | no | binary / `go install` |
| **`corral-recordings-import`** | the **recordings importer** — a maintainer tool that loads `certify --local --record` tapes into the recordings store the gallery reads; owns a database, so it carries the brain's CGO deps | yes | binary / `go install` |

The observer and admin consoles share one reverse-proxy core (`internal/console`),
parameterized read-only vs read-write.

## Platforms, for the daemon

The daemon's premise keeps your OS out of the picture: **the brain lives on a
Linux server; everything else joins it over MCP/HTTP.** A Mac or Windows developer
participates without installing anything beyond a config stanza.

| | Linux | macOS | Windows |
|---|---|---|---|
| **Thin client** (your coding agent + `.mcp.json`) | ✅ | ✅ | ✅ |
| **`corral-admin`** (operator CLI) | ✅ | ✅ compiles | ✅ compiles |
| **`corral-observe`** (read-only window) | ✅ | ✅ | ✅ |
| **`corral` (the brain)** | ✅ first-class | ⚠️ untested | via Docker/WSL2 |

Every client is pure Go and statically linked: `corral-observe` is 9.5 MB and
ships on `distroless/static`. Deploy the brain once on a Linux host (systemd +
your tunnel/proxy); the clients cross-compile anywhere with no C toolchain.

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

