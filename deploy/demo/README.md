# corralai demo — watch a local-LLM swarm coordinate

A real, role-differentiated swarm of coding agents — **builder, tester, pentester,
reviewer** — each driven by a local Ollama model, coordinating through the corral
brain on a shared codebase.

> **No API keys, no cloud** — `make demo` and `make demo-mission` run fully local
> (bundled Ollama). `make demo-models` also runs key-free by default (two local
> models); a frontier key unlocks a more striking A-vs-B comparison (see below).

---

> **FOR THE FULL SHOWCASE: `make demo-mission`, then open http://localhost:9019 → Progress tab.**
>
> The complete adaptive loop: a directive becomes a mission, the herd builds it, the
> reflex re-planner reworks it when findings land, and a modeled client reviews the
> result. (`make demo` is a simpler warm-up if you want to see coordination first.)

---

## Run it

Only prerequisite: **Docker** (with the NVIDIA container runtime for GPU; see CPU
note below). Everything else — the brain, a bundled GPU Ollama, the model, and the
agents — comes up from one command:

```bash
cd deploy/demo
make demo            # builds, pulls the model on first run, starts the swarm
```

First run pulls `qwen2.5-coder:7b` (~4.7 GB) into a docker volume (cached after —
**expect ~3–10 min on first run**; watch the pull with
`docker compose -f docker-compose.yml logs -f ollama-pull`). Set
`OLLAMA_MODELS_DIR=~/.ollama` in `.env` to reuse a model already on your host and
skip the pull entirely (see `.env.example`).

Then open **http://localhost:9019** — the live swarm. You'll see `Bob [builder]`,
`Tess [tester]`, `Hawk [pentester]`, `Quill [reviewer]` claim files, do work, hand
off, and **back off on conflicts** — narrated live in the activity feed.

`Ctrl-C` to stop, then `make down` to clean up.

**No NVIDIA GPU?** `make demo-cpu` (uses the CPU override — slower, works anywhere).
No YAML surgery needed.

## Do I need Docker?

**Only for this demo.** Docker is the demo's packaging — one command brings up
the brain, the Ollama runtime, and twelve agents in a disposable environment. The
product itself doesn't require it anywhere:

- **The brain** is a single Go binary (`cmd/corral`). A real deployment runs it
  as a systemd service behind your reverse proxy / tunnel — no container needed.
- **Client machines run nothing extra at all.** A dev's coding agent (Claude
  Code, Cursor, Gemini CLI, …) joins the swarm by pointing its `.mcp.json` at the
  brain's URL. The "install" is a config stanza; there is no local daemon, no
  Docker, no sync agent.
- **Autonomous agents** (`corral-agent`) are also plain Go binaries. An
  exec-capable agent on bare-metal Linux needs exactly one extra package:
  `bubblewrap` (`bwrap`). The isolation comes from **bwrap's Linux namespace
  jail, not from Docker** — on a normal host it runs unprivileged (modern
  distros ship user namespaces enabled).
- The demo's scary-looking `SYS_ADMIN` / unconfined-seccomp flags exist ONLY
  because the demo nests the bwrap jail *inside* a container, and the inner jail
  needs to create namespaces. On bare metal that contortion — and those flags —
  disappear.

The practical exception: **macOS/Windows have no bwrap**, so exec-capable agents
are Linux-native; on a Mac, Docker (this demo) is the convenient way to get a
Linux environment for them. Narrate-mode agents and MCP thin clients run anywhere.

## The value, made obvious

```bash
make demo-clobber    # the SAME agents, but with coordination BYPASSED
```

With `CLOBBER=1` the agents ignore the brain's file locks and pile onto the same
files — they read-modify-write the shared workspace and trample each other's edits.
That's the "this will trample my stuff" fear, made real. `make demo` is the fix.
(The brain still runs and the UI is visible; only its lock enforcement is bypassed.)

## Watch read-only (the observer)

```bash
make demo-observe    # the swarm PLUS the read-only observer
```

This adds `corral-observe` — the standalone observer app — pointed at the demo
brain. It serves the **same swarm UI on a second port** at
**http://localhost:9020**, but as a look-don't-touch window: it injects its token
server-side (the browser never holds it) and refuses every non-GET method, so a
viewer physically can't act. This is the artifact you'd hand to an ops dashboard
or a demo audience. (The demo brain runs auth-off, so here the observer's own
method-gating is what enforces read-only; against an auth-enabled brain, a
read-only token minted by `mint_observer` adds the second layer — see
[../observe/README.md](../observe/README.md).)

## The full adaptive loop (the headline)

```bash
make demo-mission    # a directive -> the herd builds it -> it re-thinks itself
```

One command brings up the brain, the bundled GPU Ollama, a **team of queue agents**
(researcher/designer/builder/tester/pentester/perf/integrator/writer/reviewer), a **lead** agent, a
**scrum master** (`Shep` — posts standups in the live console, names stalled
claims, and nudges their holders), and a one-shot that seeds a directive. The
default directive is deliberately small — *build a Go `stack` package with
table-driven tests that must `go build` and `go test` clean* — so the **entire
loop, client review included, converges in minutes on the default local 7B model**
(pulled on first run — the weights aren't shipped, only the Ollama runtime is).
Want the ambitious version (a recursive-descent expression parser)?
`make demo-mission-epic`, or set `DEMO_DIRECTIVE` to anything you like. Then
open **http://localhost:9019** and click the **Progress tab**:

- the directive's **plan** fills in as the brain lays out the steps;
- **agents claim steps** (you see `← Bob`, `← Hawk` assignments) and complete them;
- a pentester/tester **reports a finding** → the **reflex re-planner** spawns
  `fix` + `re-verify` tasks inline;
- the **lead** supersedes/reworks stale work when a design flaw lands
  (`superseded → replacement`);
- the mission reaches **awaiting review** instead of auto-completing (the demo
  seeds it with `--review`);
- the **client** — a modeled product-owner agent, or *you* via the Progress tab's
  Accept / Request-changes buttons (or `corral-admin review <id> --accept |
  --changes "..."`) — reviews it: accept → done, or feedback → the **next
  sprint** (the lead routes the change-request into rework);
- the mission **converges** when the client accepts.

Scale the herd — more agents, more parallelism:

```bash
DEMO_DIRECTIVE="build a tic-tac-toe game" \
  docker compose -f docker-compose.yml --profile mission up --build \
  --scale mission-builder=3 --scale mission-tester=2
```

> Output quality — and convergence speed — track the model. The default
> `qwen2.5-coder:7b` (pulled on first run) shows the **mechanism**: coordination, findings, re-planning,
> review, all real, and the small default directive converges in minutes. A
> frontier model shows the **payoff**: point the agents at Gemini / GPT / Claude
> (see "Bring your own model") and the same swarm ships production-grade
> artifacts fast — that's the config for `make demo-mission-epic`'s parser.
> The choreography is the show either way.

## Model comparison — A vs B side-by-side (the `model_comparison` report)

```bash
make demo-models    # multi-model run: pentester on qwen2.5-coder:7b (Group A), reviewer on llama3.2:3b (Group B)
```

**demo-models works key-free** — both groups use local Ollama models (no API key
needed). For a more striking frontier-vs-local comparison, set `OPENAI_API_KEY` and
swap Group A to a frontier model — see `.env.example`.

This is the flagship analytics demo. It runs the same small default directive as
`demo-mission` but splits the herd across **two model groups**. The key is that
the **two finding-filers** — `pentester` and `reviewer` — run on *different*
models, so their findings carry distinct `reporter_model` values and the report
renders a genuine side-by-side (builder/tester don't file findings; they build
and test the artifact):

| Group | Roles | Default (key-free) | Override |
|---|---|---|---|
| A | `pentester` (files findings) | Ollama `qwen2.5-coder:7b` (pulled on first run) | `MODELS_MODEL_A=gemini-3.5-flash MODELS_BACKEND_A=openai` (needs `OPENAI_API_KEY`) |
| B | `reviewer` (files findings) | Ollama `llama3.2:3b` (pulled on first run) | `MODELS_MODEL_B=gpt-4o MODELS_BACKEND_B=openai` |

`builder` and `tester` always use `qwen2.5-coder:7b`; they don't file findings,
so their model doesn't affect the comparison table.

Because each agent announces its model when it reports findings, the brain's
telemetry store tags every finding with `reporter_model`. The
**`model_comparison` report** (`mission_analytics{report:"model_comparison"}`)
then shows, per model:

- **volume** — how many findings that model filed
- **severity mix** — critical / high / medium / low counts
- **confirmation rate** — `addressed / (addressed + dismissed)` (findings that
  stayed open are excluded from the denominator so noise doesn't penalise a
  model; division-by-zero → NULL)

### Where to see the report

1. **Swarm UI → Topology tab** → scroll to the *Model comparison* table
   (live, auto-refreshes).

2. **CLI** (from the host, after `make demo-models` is running):
   ```bash
   docker compose -f docker-compose.yml exec brain \
     corral-admin analyze model_comparison \
     --brain http://localhost:9019 --token demo
   ```
   Or from outside the container if you have `corral-admin` in PATH:
   ```bash
   corral-admin analyze model_comparison \
     --brain http://localhost:9019 --token demo
   ```
   (Flags follow the subcommand; `CORRAL_BRAIN` / `CORRAL_TOKEN` env vars work too.)

3. **MotherDuck / DuckDB** (if `CORRALAI_MOTHERDUCK_TOKEN` is set):
   ```sql
   SELECT model, volume, confirmation_rate FROM model_comparison ORDER BY volume DESC;
   ```
   The `fleet_telemetry` table carries `model` and `detail->>'outcome'` columns
   that back the report.

### How resolutions happen

- **Naturally:** the `models-lead` agent reads open findings and calls
  `resolve_finding` on each (addressed once it enqueues rework, dismissed if it
  flags a false positive). This populates the confirmation column automatically.
- **Auto-seed fallback:** the `models-finalize` one-shot service waits **3 minutes**
  then resolves any still-open findings as `addressed`. This guarantees a
  non-empty confirmation column even if the lead agent's model is slow or
  distracted. It is honest about what it does (the log line says "demo seed").

### Striking comparison vs mechanism demo

- **Two local models** (the default: qwen2.5-coder:7b vs llama3.2:3b) render a
  real side-by-side with zero keys — useful to show the mechanism.
- **Frontier + local** (e.g. Gemini Flash + qwen) surfaces genuine differences in
  finding style and severity calibration — set `OPENAI_API_KEY` in `.env` and
  override Group A (see `.env.example`).
- **Override both groups to frontier** for the most striking demo:
  ```bash
  MODELS_MODEL_A=gemini-2.5-flash MODELS_BACKEND_A=openai \
  MODELS_MODEL_B=gpt-4o MODELS_BACKEND_B=openai OPENAI_API_KEY=$OPENAI_KEY \
  make demo-models
  ```

## Bring your own model (not hard-wired to Ollama)

Ollama is just the zero-cost default. The agent drives itself through a `Backend`
interface, so swap in any provider with `MODEL_BACKEND` — the coordination is
**identical** (that's the model-agnostic substrate thesis):

```bash
# Gemini (its OpenAI-compatible endpoint)
MODEL_BACKEND=openai AGENT_MODEL=gemini-2.5-flash \
  OPENAI_BASE_URL=https://generativelanguage.googleapis.com/v1beta/openai \
  OPENAI_API_KEY=$GEMINI_KEY make demo

# OpenRouter → Claude, Gemini, anything
MODEL_BACKEND=openai AGENT_MODEL=anthropic/claude-sonnet-4-6 \
  OPENAI_BASE_URL=https://openrouter.ai/api/v1 OPENAI_API_KEY=$OPENROUTER_KEY make demo

# OpenAI, or any local OpenAI-compatible server (vLLM, LM Studio, …)
MODEL_BACKEND=openai AGENT_MODEL=gpt-4o OPENAI_API_KEY=$OPENAI_KEY make demo
```

And it isn't locked to *our* agent either: the brain is a plain MCP server, so any
MCP-speaking harness (Claude Code, Gemini CLI, Cursor, …) can point its `.mcp.json`
at `http://localhost:9019/mcp/` and join the swarm. `corral-agent` is the reference.

For **autonomous** harness agents, `corral-harness` loops a headless agent — one
task per invocation, fresh context each time — on the agent's own auth (a Claude
Code agent runs on your Claude Pro/Max subscription, not API billing):

```bash
go install github.com/pdbethke/corralai/cmd/corral-harness@latest   # or go build ./cmd/corral-harness
CORRAL_BRAIN=http://localhost:9019 BEE_NAME=Cody BEE_ROLE=builder \
HARNESS_CMD='claude -p {prompt} --mcp-config {mcp_config} --allowedTools "mcp__corral,Read,Write,Edit,Bash" --permission-mode acceptEdits' \
corral-harness
```

`{prompt}` is the agent contract (claim one task, work it with your own tools, run
the verify command, report the execution, complete); `{mcp_config}` is a
generated standard `.mcp.json` pointing at the brain. Swap `HARNESS_CMD` for any
headless agent — the brain can't tell the difference, and the topology view,
`model_comparison`, and audit trail treat it like any other agent. Mind your
subscription's usage limits: the economical shape is one or two frontier-harness
agents on the judgment roles (builder/reviewer/lead) alongside free local agents.

### A Max-plan agent in the wow demo (verified)

Run the mission demo with the container builder replaced by a **host-side Claude
Code agent on your subscription** — the containers and the host agent share one
workspace via a bind-mount override:

```bash
export HOST_WORKSPACE=$PWD/swarm-ws && mkdir -p "$HOST_WORKSPACE" && chmod 777 "$HOST_WORKSPACE"
docker compose -f docker-compose.yml -f docker-compose.hostws.yml \
  --profile mission up --build --scale mission-builder=0     # no container builder

# in another terminal — the Max-plan builder:
cd "$HOST_WORKSPACE" && CORRAL_BRAIN=http://localhost:9019 BEE_NAME=Cody BEE_ROLE=builder \
HARNESS_CMD='claude -p {prompt} --mcp-config {mcp_config} --allowedTools "mcp__corral,Read,Write,Edit,Bash" --permission-mode acceptEdits' \
HARNESS_DESC='claude-code (Max subscription)' corral-harness
```

Verified live: Cody idled while the local agents researched and designed, then
claimed the gated build, wrote an idiomatic stack package (edge cases, fuzz
tests, benchmarks), passed `go build`/`go test`, and — when the 7B lead
cancelled a task mid-pipeline — **filed a high-severity finding documenting the
stall for the lead to act on**. Mixed-fleet coordination, one subscription agent
among free local ones.

### Harness templates

| Harness | Auth it brings | `HARNESS_CMD` template | Status |
|---|---|---|---|
| **Claude Code** | Claude Pro/Max subscription | `claude -p {prompt} --mcp-config {mcp_config} --allowedTools "mcp__corral,Read,Write,Edit,Bash" --permission-mode acceptEdits` | ✅ **verified live** — claimed research → design → gated build, wrote real files, passed the `go build`/`go test` gate |
| **Gemini CLI** | Google account / Gemini Code Assist | `gemini -p {prompt} --yolo` — Gemini reads MCP servers from `~/.gemini/settings.json`, so add the brain there (`{mcp_config}` shows the shape); `--yolo` auto-approves tools, so contain it | ⚠️ untested — same contract, flags may drift by version |
| **Codex CLI** | ChatGPT Plus/Pro subscription | `codex exec {prompt} --full-auto` — MCP servers live in `~/.codex/config.toml` (`[mcp_servers.corral]` with the brain URL) | ⚠️ untested |
| **cursor-agent** | Cursor subscription | `cursor-agent -p {prompt} --force` — MCP servers from `~/.cursor/mcp.json` | ⚠️ untested |
| **corral-agent** | Ollama (free) or any API key | not a harness template — the reference agent, `deploy/demo` runs it for you | ✅ the demo itself |

The untested rows follow the same shape: a headless/print mode, tool
auto-approval scoped as tightly as the harness allows, and the brain registered
as an MCP server named `corral` (some harnesses take a config flag, most read a
settings file — the generated `{mcp_config}` file is the standard shape to copy
from). If you verify one, a PR updating its row with the exact working
invocation is very welcome.

> **Full-auto safety, again:** a headless agent approves its own tool calls, so
> give it the same courtesy the exec agents get — run it in a container, a VM, or
> at minimum a throwaway workspace directory. The corralai side is already
> bounded (advisory claims, verification gates, audited trail); the harness's
> own file/exec tools are what you're containing.

## Real execution (the bwrap sandbox)

**`make demo` agents narrate** what they would do (distroless image, no shell).
**`make demo-mission` runs REAL execution by default** for its builder / tester /
pentester / perf / integrator agents — they actually build, run, and test their
artifacts inside a sandbox (that's what makes the verification gate and the
Progress-tab findings real). To flip the mission agents back to narrate-only:

```bash
AGENT_ALLOW_EXEC=0 make demo-mission
```

Exec-capable agents are built from `Dockerfile.agent-exec` (a toolchain image with
bubblewrap). Each agent command runs inside a **Linux namespace jail** (`bwrap`):
no network by default, a private `/tmp`, read-only bind over the workspace (write
access scoped to the agent's checkout). The model can't reach the internet or other
containers from inside the jail.

**Why this matters: the jail is what makes full-auto safe.** Interactive
harnesses keep a human in the loop — every risky command waits for an approval
click, which is exactly what you can't do with twelve autonomous agents working
overnight. Corralai inverts the model: instead of gating each command on
permission, it **bounds what any command can touch**. A agent can run whatever it
decides to run — build, test, profile, attack its own artifact — and the worst
case is confined to its own checkout: no network egress, no secrets in the env
(the git/forge token never leaves the brain), no reach into the host or other
agents' work. The jail replaces the permission prompt, so the swarm gets the
"skip permissions" velocity without handing the model your machine.

Security note: the outer container itself carries `SYS_ADMIN` and unconfined
seccomp/AppArmor so the *inner* bwrap jail can create namespaces. This is
demo-grade. A production deployment should use a VM, gVisor, or rootless-Podman as
the outer boundary instead of trusting the container boundary. Set
`AGENT_EXEC_NET=1` for a build step that fetches dependencies.

## Knobs

| env | default | meaning |
|---|---|---|
| `MODEL_BACKEND` | `ollama` | `ollama` \| `openai` (OpenAI-compatible: Gemini/OpenRouter/local) \| `anthropic` |
| `AGENT_ALLOW_EXEC` | `1` for mission agents, else `0` | `0` flips the mission's exec agents back to narrate-only |
| `AGENT_MODEL` | `qwen2.5-coder:7b` | the model name for the chosen backend |
| `OLLAMA_MODELS_DIR` | `ollama-models` (volume) | set to `~/.ollama` to reuse a host download and skip the pull |
| `AGENT_ROLE` | per service | `builder` \| `tester` \| `pentester` \| `reviewer` |
| `AGENT_ROUNDS` | `0` (forever) | passes over the backlog |
| `CORRALAI_EMBED_URL` | _(unset)_ | (optional) embedding endpoint — enables reference RAG; off by default |
| `CORRALAI_EMBED_KEY` | _(unset)_ | (optional) API key for the embed endpoint |

See `.env.example` for all knobs with comments.
