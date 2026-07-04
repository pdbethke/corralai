---
name: using-corralai
description: "Use when driving a corralai brain — the multi-agent 'herd' orchestrator where one directive becomes a mission a team of AI agents plans, builds, verifies, re-plans on findings, and iterates until you accept. Covers composing a good directive (the crisp verify gate that makes or breaks a run), the CLI (corral / corral-admin / corral-agent / corral-harness / corral-observe / corral-top), the mission lifecycle, the human gate, memory + the CORRAL.md knowledge corpus, and watching/replaying a run. Invoke whenever the user mentions corral, the herd, a mission, wrangling agents, or wants to run/observe/steer a build through corralai."
---

# Using corralai

corralai turns **one directive** into a **mission** that a **herd** of role-differentiated
AI agents executes: a headless **brain** decomposes it into a dependency-ordered task
queue; agents pull ready tasks and build; their structured **findings** feed a two-tier
re-planner; the mission **converges** when the client (you, or a modeled product-owner
agent) accepts. Agents run on any model — local Ollama (no API keys) or frontier CLIs.

**The one thing that most determines a good run: the directive's verify gate.** Everything
below serves that.

## Compose the directive (do this well and the rest follows)

A directive is a sentence plus a *checkable* definition of done. The brain gates task
completion on a **recorded passing run of a verify command** — so name it:

- Good: `Build a Python 'ratelimit' package: a RateLimiter with allow(key), configurable
  capacity + refill, docstrings, a README, and unittest tests; make 'python3 -m unittest' pass.`
- Weak: `Build a rate limiter.` (no gate → the herd can't prove it's done → sprawl.)

Rules of thumb:
- **State the verify command** (`go test ./...`, `pytest`, `node --test`). The language is
  inferred from it — corral is language-agnostic; the gate is what pins quality.
- **Bound the scope.** One artifact converges fast; a vague epic grows tasks without end.
- **Name the acceptance shape** (files, API, tests) so findings have something to check against.

## Run a mission

The **brain** is the coordination server; you talk to it with `corral-admin`, and agents
connect to it. Simplest first run is the bundled demo (local models, no keys):

```bash
go run ./cmd/corral                 # start the brain (dev: auth off) — MCP /mcp/, UI on :9019
cd deploy/demo && make demo-mission # a directive → the herd builds it → re-plans → converges
```

Then open `http://127.0.0.1:9019/` to watch the corral. Against a running brain:

```bash
corral-admin mission create "Build X … make <verify> pass"   # launch a mission
corral-admin status                                          # agents, claims, recent work
corral-admin mission list | status <id>                     # progress
```

## The binaries

| Binary | Role |
|---|---|
| `corral` | the **brain** — coordination + memory MCP server; validates bearer tokens, no browser-login of its own |
| `corral-admin` | **operator** client: `mission`, `instruct`, `status`, `findings`, `review`, `proposals`, `member`, `reference`, `analyze`, `whoami`, `ui` (privileged live console), `mint-observer` |
| `corral-agent` | a **worker** that pulls tasks and executes (queue mode) or self-organizes (demo mode); drives a model backend (Ollama/OpenAI/Anthropic) |
| `corral-harness` | a **worker wrapper** around a headless coding CLI (Claude Code, Gemini, Codex, Copilot) via a `HARNESS_CMD` template — bring your own frontier agent |
| `corral-observe` | **read-only** credentialed proxy to the live UI — hand it to people who should watch but not touch |
| `corral-top` | terminal dashboard of the live corral |

Every binary supports `-h`. Run it to see its exact flags and env vars.

## Mission lifecycle (what you're watching)

`directive → decompose into a task queue → LEAD orchestrates phases (research → design →
build-core → build → test ∥ secops ∥ perf → integrate → docs → retro) → agents claim ready
tasks (exclusive path leases, no trampling) → they report structured findings → a two-tier
re-planner reacts → the client reviews → converge.`

- **The verify gate:** a task with a verify command can't be marked done until a *passing*
  run of it is on record (`report_execution`). This is the quality floor — never bypass it.
- **Two-tier re-planning:** a *reflex* tier deterministically spawns fix + re-verify tasks
  from findings (bounded by caps, loop-until-dry); a *lead* tier can supersede or re-architect.
- **Claims & leases:** agents hold exclusive (or advisory) path claims with TTLs; an idle
  holder is reclaimed (the slacker rule) so a stalled agent doesn't wedge the mission.

## The human gate — agents propose, you dispose

Nothing that shapes the herd's future behavior lands without a human. Recurring findings and
lesson clusters become **skill proposals**; you approve or reject them (`corral-admin proposals
list | show | approve | reject`, or the UI's Proposals tab). Approval promotes vetted guidance
into memory and a versioned skill artifact the whole fleet equips. Worker/delegation tokens are
*refused* admin writes by design — a worker can propose, it cannot self-vet.

## Memory + the knowledge corpus

- **Memory tiers:** advisory → vetted. The herd searches memory before working and extends it as
  it learns; vetted lessons (human-approved) are injected into every mission's instructions, capped.
- **CORRAL.md:** a repo that corral runs on can carry its working knowledge as a markdown corpus —
  `CORRAL.md` at the root as the entry point, `docs/corral/*.md` as the corpus. Developers read it
  as onboarding; any coding agent queries it; the herd searches it and grows it by ordinary PRs
  (code review is the trust gate for knowledge, same as for code).

## Watch it, then watch it back

- **Live:** the UI at the brain's address (canvas of agents + claims, a live console of the exact
  commands they ran, tasks and findings panels), or `corral-top`, or `corral-observe` for a
  read-only share.
- **Replay:** finished missions are recorded (tasks, claims, findings, executions, the event log)
  and replay read-only on the same canvas with a scrub bar — reconstructed deterministically from
  durable rows, so scrubbing backward rebuilds a shorter history exactly.

## Operator gotchas

- **The gate is the hero, not an obstacle** — if a run sprawls, the directive's verify command was
  too weak. Sharpen it, don't loosen the gate.
- **Local 7B models are slow and argumentative** (many findings → many re-plan tasks). That's honest,
  not broken; frontier models converge far faster with fewer detours.
- **Auth off = dev only.** A brain with no OIDC issuer configured is wide open — never expose one
  publicly. In production, configure an issuer (any OIDC provider) + a principal allowlist.
- **A worker that shares your interactive model/quota can stall mid-mission** when the quota drains —
  pin worker models off whatever you're using by hand.
