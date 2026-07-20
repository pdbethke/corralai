---
name: using-corralai
description: "Use when driving or querying a corralai brain — the audit-by-execution gate for software change. Covers `corral certify --local` (mutation-score a change's own tests), `corral certify <ref> -- <cmd>` (certify a change by its declared check), the repo gate + control gate, querying the shared knowledge corpus (`search_memory`), and the CLI (corral / corral-admin / corral-agent / corral-harness / corral-observe / corral-top). Invoke whenever the user mentions corral, certify, the audit, the gate, the herd, or wants to run/observe/steer a corralai brain."
---

# Using corralai

corralai certifies a software change **by execution, not opinion**: it runs the
check in a jail, measures the result itself — never a self-report — signs a
tamper-evident record, and gates the merge. This skill teaches any coding agent
how to drive that audit surface.

**The one thing that most determines a good audit: the check you hand it.**
Everything below serves that.

## `corral certify --local` — audit a change's own tests

The fastest path: mutate the code, run the dev's own test suite against every
mutant in a jail, and score what fraction it kills.

```bash
corral certify --local \
  --code path/to/file.py \
  --goal "what this code must guarantee" \
  --out verdict.json \
  -- python -m pytest
```

- A mutant-generator seeds real, goal-violating bugs into the code.
- The suite runs against every mutant, **in a jail**; the kill-rate is the
  adequacy score — measured, never reported.
- A test-writer authors a compiling test that kills whatever the suite missed.
- A test-critic (always a *different*, decorrelation-enforced model) flags
  vacuous tests as **unverified advice — it never gates the verdict.**

Output is a signed verdict (`certified` or `needs-review`), printed and written
to a local tamper-evident ledger; `--out` also writes a self-contained file
re-checkable offline with `corral certify verify verdict.json --pubkey "$(corral
certify pubkey)" --allow-unanchored`.

Key flags: `--repo-dir <path>` audits the file in the context of a whole cloned
repo; `--swarm N` bounds concurrent audit tasks (0 = auto-size to cores);
`--record <file>.json` writes a replayable tape; `--max-shards` /
`--n-mutants` control how many mutants get scored; `--shadow-model` runs a
second challenger model for a scorecard-only head-to-head (never affects the
verdict). Full flag reference in the root [README.md](../../README.md#the-audit-flags).

## `corral certify <ref> -- <cmd>` — certify a change by its declared check

The other entry point: any commit, any command.

```bash
corral certify HEAD -- go test ./...
```

It checks `<ref>` out into a jail, runs `<cmd>`, reads the exit code, and
writes a signed record — verify offline with `corral certify verify`, or
`--brain <url>` to post it to a running brain. No server required either way.

## The gate — repo and control owner

Against a running brain, the same certify-by-execution pattern runs
continuously on pull requests:

- **The repo (merge) gate.** Watches a covered repo's open PRs; on a new head
  commit it checks the PR out, runs the repo's declared check in the jail,
  signs the result, and posts a `corral/gate` status.
- **The control gate.** Same pattern, but runs the **control owner's**
  independently-vetted tests against the PR head instead of the repo's own
  check, posting a distinct `corral/control-gate` status — the party
  accountable for code they didn't write sets the bar.
- The admin-only `start_adversarial_run` MCP tool runs the same sharded +
  shadow adversarial audit `--local` runs, against a repo the brain already
  coordinates.

## Query the knowledge corpus

A repo carries its working knowledge as markdown (`CORRAL.md` at the root,
`docs/corral/*.md` as the corpus) plus a shared, searchable memory (DuckDB,
full-text + optional vector). Point any MCP-speaking coding agent's
`.mcp.json` at the brain and ask it to search — under the hood that calls
`search_memory`, which returns the repo's docs, prior findings, and any
human-approved lessons in one ranked list. Memory is **trust-tiered**:
searchable advisory knowledge is never auto-injected as an instruction, so a
repo (or a poisoned document) can't smuggle in authority by shipping a file.

## The binaries

| Binary | Role |
|---|---|
| `corral` | the **brain** — MCP coordination, the repo/control gates, memory, reference RAG, the fleet oracle, embedded UI |
| `corral-admin` | **operator** client: `mission`, `instruct`, `status`, `findings`, `review`, `proposals`, `member`, `reference`, `analyze`, `whoami`, `ui` (privileged live console), `mint-observer` |
| `corral-agent` | a **worker** that pulls tasks and executes; drives a model backend (Ollama/OpenAI/Anthropic) |
| `corral-harness` | a **worker wrapper** around a headless coding CLI (Claude Code, Gemini, Codex, Copilot) via a `HARNESS_CMD` template — bring your own frontier agent to staff an audit role |
| `corral-observe` | **read-only** credentialed proxy to the live UI — hand it to people who should watch but not touch |
| `corral-top` | terminal dashboard of the live corral |

Every binary supports `-h`. Run it to see its exact flags and env vars.

## The human gate — agents propose, you dispose

Nothing that shapes future audit behavior lands without a human. Recurring
finding signatures and clusters of similar lessons become **skill
proposals**: an LLM drafts corrective guidance plus a reusable skill, and the
operator approves or rejects it (`corral-admin proposals list | show | approve
| reject`, or the UI's Proposals tab). Approval promotes it into vetted memory
and a versioned skill the whole fleet equips. Worker/delegation tokens are
*refused* admin writes by design — a worker can propose, it cannot self-vet.

## Watch it, then watch it back

- **Live:** the UI at the brain's address, or `corral-top`, or `corral-observe`
  for a read-only share.
- **Replay:** finished runs record fully (tasks, findings, executions, the
  event log) and replay read-only on the same canvas with a scrub bar, up to
  16×. `corral certify --local --record <file>.json` writes the same
  replayable tape for a local run.

## Operator gotchas

- **The check you hand it is the whole audit** — a weak or absent verify
  command means a weak or absent gate. Sharpen the check, don't loosen it.
- **Decorrelation is enforced, not requested.** Cross-model checking refuses
  to start if the critic and the generator collapse onto the same model.
- **Jail-visibility.** The language toolchain a check needs must be installed
  system-wide (under `/usr`), not `--user`/pyenv/nvm-local — invisible to the
  sandboxed mount namespace means the gate fails closed with a loud error, not
  a silent false pass.
- **Fail-closed, always.** If the host can't isolate execution, or a run can't
  converge to a killing test for a survivor, corral returns a loud error or
  `needs-review` — never a silent, unsandboxed run and never an unearned
  `certified`.
- **Auth off = dev only.** A brain with no OIDC issuer configured is wide
  open — never expose one publicly. In production, configure an issuer (any
  OIDC provider) plus a principal allowlist.
