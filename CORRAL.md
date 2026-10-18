# CORRAL.md

> **Visibility and accountability enhance, rather than restrict, performance.**

Entry point to corral's own knowledge corpus: the Markdown that ships in this
repository for the people and agents who use corral, as opposed to
[AGENTS.md](AGENTS.md), which is for an agent working *on* corral.

corral is an auditing engine. The `corral` CLI (`cmd/corral`) plants faults in
your code, runs your own test suite against each one in a sandbox, and for each
fault that survives attempts a test and keeps the ones that prove: tests that
fail on the fault and pass on your real code, proven missing tests. A survivor
the writer cannot prove stays unproven. `corral review`
hands a scope to a cold model and makes every claim prove itself by a script
corral runs. Both write hash-linked entries to a ledger in your own
repository, signed when a certify key is configured and unsigned otherwise. Neither reads this corpus, or any memory, when it judges code: an
audit's prompts are built from the goal, the code and its symbols. See
[README.md](README.md).

## The docs/corral/ corpus

The audit:

- [docs/corral/language-support.md](docs/corral/language-support.md) — what corral does with each language's tests, runner by runner, and why each gap exists. Generated from the runner table the code dispatches on; never hand-edited.
- [docs/corral/agent-seats.md](docs/corral/agent-seats.md) — running any seat on a coding-agent CLI (`claude-code`, `codex`, or one you define) instead of an API model, with no API key, and what was measured that way.
- [docs/corral/github-action.md](docs/corral/github-action.md) — the GitHub Action: inputs, the `corral/ledger` branch recipe, and the runs behind it.
- [docs/corral/internal-forge.md](docs/corral/internal-forge.md) — the same audit on GitLab, Gitea, Bitbucket or Gerrit: one `certify --repo` invocation plus a git branch.
- [docs/corral/actions-as-swarm.md](docs/corral/actions-as-swarm.md) — running the Action on every pull request and reading the results together from DuckDB or MotherDuck.
- [docs/corral/foreign-sweep.md](docs/corral/foreign-sweep.md) — the CI gate that runs `certify --repo --dry-run` over SHA-pinned third-party repositories, and how to move its golden numbers.

The optional daemon (`corral-wrangler`, frozen; no audit needs it or reads it):

- [docs/corral/brain.md](docs/corral/brain.md) — what the daemon is, what it is not, and its own security surfaces. Read this first.
- [docs/corral/verify-gate.md](docs/corral/verify-gate.md) — the daemon's task gate, which certifies a task by running its check rather than taking a worker's word.
- [docs/corral/claims-and-leases.md](docs/corral/claims-and-leases.md) — the daemon's path and branch claim broker.
- [docs/corral/memory-etiquette.md](docs/corral/memory-etiquette.md) — how to write and search the daemon's memory.
- [docs/corral/demo-map.md](docs/corral/demo-map.md) — the daemon demo's `make` targets and UI tabs. It describes a builder/tester herd from before corral became an auditor that never builds; it is kept as a record of that design.

Background:

- [docs/corral/sakana-fugu-analysis.md](docs/corral/sakana-fugu-analysis.md) — a comparison of Sakana AI's Fugu orchestrator with corral's design.

Anyone can add to this corpus with a pull request; see
[CONTRIBUTING.md](CONTRIBUTING.md). Knowledge is reviewed the way code is:
**code review is the trust gate for knowledge exactly as it is for code.**

## How a developer's agent reads corral's findings

`corral mcp` serves an audit's findings to a coding agent over MCP on stdio:
local, read-only, with no daemon and no network listener. It reads the store
`certify --local` writes, and offers two tools:

- `list_audit_findings` — the test-critic's findings, grouped by test file, each
  with its evidence and the signed record it came from.
- `get_audit_finding` — one finding in full, with any verdict a person gave it.

The findings are unverified model opinions until a person adjudicates them, and
the tools are read-only on purpose: confirming or refuting a finding
(`corral criticscore confirm|refute`) stays a person's action, so the critic's
precision is never graded by an agent.

The Claude Code plugin wires this up along with the `corral` skill
([skills/corral/](skills/corral/SKILL.md)):

```bash
claude plugin marketplace add pdbethke/corralai
claude plugin install corral@corralai
```

Any other MCP client takes one entry in `.mcp.json`:

```json
{ "mcpServers": { "corral": { "command": "corral", "args": ["mcp"] } } }
```

## If you run the optional daemon

The daemon ingests `CORRAL.md`, `AGENTS.md` and `docs/corral/*.md` from a
cloned repository as **advisory** memory entries (`shared=false`, tagged to
that repository; `internal/brain/seeddocs.go`). Its agents can find them by
search, but they are never injected into instructions automatically, so a
repository you do not control cannot pass off its own files as vetted
guidance. Only lessons a person promoted are `shared=true`. An agent connected
to the daemon's MCP endpoint searches all of this with `search_memory`; how to
connect is in [docs/corral/brain.md](docs/corral/brain.md).
