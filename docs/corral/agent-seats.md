# Agent seats: run a seat on a coding-agent CLI

Every model seat in corral can be a **coding-agent CLI** instead of an API
model: `claude-code`, `codex` and `antigravity` come defined, and any other
agent you can start from a shell is defined by you (see "Define your own agent"
below). One resolver, `agentbackend.ForSeat`, serves `certify --local`,
`certify --repo`, `demo`, `doctor`, goal derivation (`--derive-model`), the
shadow seats and `review`. The agent brings its own login, so on a
subscription plan a seat costs no per-token API bill.

```bash
corral demo --writer-model codex --mutant-model codex --critic-model claude-code
```

A seat is `<agent>` (the CLI picks its own model) or `<agent>:<model>` (you pin
one). An unpinned seat is recorded as the tool and its version, with the model
marked unresolved, because corral was not told which model the CLI ran. For
the decorrelation rule (critic is not the writer) an unpinned agent counts as
the agent itself.

## What was measured

All runs: 2026-10-13, one workstation, no API keys in the environment,
codex-cli 0.160.0, Claude Code 2.1.296. These are single observations, not
benchmarks.

| run | result |
|---|---|
| `corral demo --mutant-model codex --writer-model codex --critic-model claude-code` | the dev suite killed 5 of 11 mutants; 6 of 6 survivors were PROVED by an authored test. Cost line: 0.1M tokens in / 1.9k out across 8 calls (86.0k cached). The verdict's models line read `codex [codex-cli 0.160.0]` / `claude-code [2.1.296 (Claude Code)]`. |
| the same demo swapped (`claude-code` mutant and writer, `codex` critic) | the dev suite killed 2 of 11; 9 of 9 PROVED; 39.3k in / 6.1k out across 11 calls. |
| one `corral review` round (`claude-code` reviewer, `codex` verifier, scope `internal/seatrecord`) | completed. The record marks both unpinned seats model-unresolved. |

`antigravity` was not measured. Its built-in definition speaks the `text`
wire, so it is not metered (below).

## Seats start cold

A coding agent started with its defaults loads the operator's own setup. A
corral audit must not: the seat would be graded on instructions the audit's
reader never sees. The built-in definitions start cold. Measured input tokens
for a one-line prompt:

- **`claude-code`: 561** with the flags the built-in uses (no settings
  sources, no MCP servers, no slash commands or skills, no session file, no
  tools unless the caller names them). With no flags, about 108,740 in the
  first spike and 94,073 in the negative control that checked a seat does not
  carry the operator's own setup into an audit. `--bare` would be leaner but
  refuses subscription auth, so it is not used.
- **`codex`: 14,643.** That is codex's own agent prompt. No codex `AGENTS.md`
  exists on the measured machine, so this run could not show whether codex would
  have loaded one; the built-in definition passes `--ignore-user-config
  --ignore-rules` so it does not.

For `review`, this changes behaviour: a review seat no longer sees the
operator's own `CLAUDE.md`, memory, skills or MCP servers.

## Metering: `not metered` and `--max-tokens`

Whether a seat reports tokens depends on its **wire**, not on its name.
`claude-code` (wire `claude-json`) and `codex` (wire `codex-jsonl`) report
usage. A seat on the `text` wire prints a reply and nothing else, so its calls
are shown as `not metered` on the cost line, never as a measured zero. When
nothing in a scan was metered, the scan's token totals are null in the
warehouse and omitted from the ledger.

A `--max-tokens` cap cannot bound a seat it cannot see, so a run that names a
cap and an unmetered seat is refused before any jail, store or model call.
The cap also counts goal derivation, and a file whose derivation was stopped
by the cap is excluded as `derive-budget-exhausted`.

## Failures are not the model's

A seat that fails because of its environment (the binary is missing, the CLI
reports an error) is classified as a seat-environment failure and is never
charged to the model. A subscription limit hit is classified the same way.

## Concurrency

Up to 10 agent calls ran at once on one subscription with no limit hit. That
was observed, not stress-tested. Default concurrency is unchanged.

## Define your own agent

An agent is a command line in the environment. NAME is the seat name
upper-cased with `-` as `_`:

```bash
export CORRALAI_AGENT_MYAGENT='myagent --model-flag {model:--model} --cwd {dir}'
export CORRALAI_AGENT_MYAGENT_WIRE=text      # claude-json | codex-jsonl | text
corral certify --local ... --writer-model myagent:some-model
```

corral writes the brief to the command's **stdin** and reads the reply as the
wire defines (below): on `text`, the `{out}` file if the agent wrote something
non-empty there, otherwise stdout. The command runs in a disposable working
directory. These words are substituted:

| word | becomes |
|---|---|
| `{dir}` | the agent's working directory |
| `{out}` | a file the agent may write its reply to. The `text` and `codex-jsonl` wires prefer it, when non-empty, over stdout / the last agent message |
| `{model}` | the pinned model. The definition then requires a pin: with no `:model` the seat is refused by name |
| `{model:FLAG}` | `FLAG <model>` when pinned, nothing otherwise |
| `{tools:FLAG}` | `FLAG <a,b,c>` for the tools the caller grants, and `FLAG ""` (no tools) when it grants none |

A pinned model on a definition with no `{model}` or `{model:FLAG}` word is
refused, because corral will not claim a pin the agent cannot carry.
`{dir}` and `{out}` are substituted anywhere inside a word (`--cwd={dir}`
works); `{model}`, `{model:FLAG}` and `{tools:FLAG}` match only as a whole word
of the command line.

**Wires.** `claude-json` reads `claude -p --output-format json`;
`codex-jsonl` reads `codex exec --json -o {out}` and prefers the `{out}` file
over its last agent message; `text` reads no structure and takes the `{out}`
file if the agent wrote something non-empty there, else stdout (an empty reply
is an error). A definition you write is read as `text` unless
`CORRALAI_AGENT_<NAME>_WIRE` names another wire, because corral cannot know
what your command prints. A name that is not one of the three is an error.
Redefining a built-in's command line without a wire also gives `text`. A
`text` seat is **unmetered**: it shows `not metered` and is refused under
`--max-tokens`.

Your definition wins over a built-in of the same name. corral does not confine
what the command does: the definition is yours, and the record carries it,
beside the CLI's `--version` when corral can run it. `corral doctor` checks the
binary is installed without making a call, and reports
`binary ok (<version>), definition ok, login not verified (no call is spent)`.
For a shell-wrapped definition (such as `sh -c`) it reports
`binary not checked (shell-wrapped definition)`.

An Ollama tag that starts with an agent's name, such as `codex:latest`, reads as
that agent pinned to the model `latest`; rename the model or undefine the agent.

## In analysis

`corral models rank` and the bug-catching scorecard leave out an unpinned agent
seat, review included: a row that names no model is not evidence about a model.
Before this change they counted such rows.

On a GitHub-hosted runner, the Action's `review` step refuses `claude-code` and
`codex` seats by name because a runner has no subscription login; see
[github-action.md](github-action.md).
