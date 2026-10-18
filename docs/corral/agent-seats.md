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
the agent itself. Agent names are case-insensitive, and certify and review
compare seats the same way, ignoring case. A pin that starts with `-` is
refused, because it would reach the CLI as a flag.

Every verb checks that the seat's CLI is installed when it resolves the seat,
so a missing binary is refused before any jail, store or model call, naming
the seat and the binary. A built-in definition is checked by the binary it
runs (`claude`, `codex`, `agy`), even though `antigravity`'s is shell-wrapped;
only an operator's own shell-wrapped definition proceeds unchecked.

## What was measured

All runs: 2026-10-13, one workstation, no API keys in the environment,
codex-cli 0.160.0, Claude Code 2.1.296. These are single observations, not
benchmarks.

| run | result |
|---|---|
| `corral demo --mutant-model codex --writer-model codex --critic-model claude-code` | the dev suite killed 5 of 11 mutants; 6 of 6 survivors were PROVED by an authored test. Cost line: 0.1M tokens in / 1.9k out across 8 calls (86.0k cached). The verdict's models line read `codex [codex-cli 0.160.0]` / `claude-code [2.1.296 (Claude Code)]`. |
| the same demo swapped (`claude-code` mutant and writer, `codex` critic) | the dev suite killed 2 of 11; 9 of 9 PROVED; 39.3k in / 6.1k out across 11 calls. |
| one `corral review` round (`claude-code` reviewer, `codex` verifier, scope `internal/seatrecord`) | completed. The record marks both unpinned seats model-unresolved. |
| `corral demo --mutant-model antigravity --writer-model codex --critic-model off` (2026-10-17, no API key) | the dev suite killed 2 of 11; 9 of 9 PROVED. The mutant seat's one call shows as `not metered`. |
| the same with `--critic-model antigravity` | the critic answered in the keyed JSON shape and judged both demo tests sound. The mutant seat returned an empty reply (below). |

`antigravity` speaks the `text` wire, so it is not metered (below). In 2 of
the 3 certify runs above its mutant seat answered; in the third, headless
`agy` exited 0 with nothing on stdout and said on stderr that the model had
reached for a tool needing the `command` permission, which it auto-denied. The
run stops on that reply rather than grading nothing; rerunning is the remedy.

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

A seat that fails because of its environment is classified as a
seat-environment failure and is never charged to the model. What is
classified, by construction:

- **The binary is missing.** Refused when the seat is resolved, before
  anything runs.
- **`claude-code` (wire `claude-json`)** reports `is_error`, or a
  `terminal_reason` other than `completed`.
- **`codex` (wire `codex-jsonl`)** emits an `error` or `turn.failed` event.

The error records that were captured and tested were bad-model errors (a model
the account cannot use). A **subscription limit** has **not yet been
observed** on either CLI. If one arrives through those same fields, it is
classified the same way, but no captured limit notice shows that it does.

A seat on the **`text` wire** (antigravity, or any definition that declares no
wire) cannot make this distinction at all. It has no error field, so a limit
notice printed on stdout is indistinguishable from a reply and is handed on
as one. A CLI that exits non-zero still fails the call as a seat error.

## What a seat can reach

corral confines a seat only as far as its CLI lets it:

- **`claude-code`** is given no tools in certify, and only `Read`, `Grep` and
  `Glob` in review.
- **`codex`** has no tool switch. `--sandbox read-only` is its only
  confinement, in every verb. That stops writes, not reads, so its shell can
  still **read the host filesystem** outside its working directory.
- **`antigravity`** in review runs with `--dangerously-skip-permissions`,
  because headless `agy` denies every tool it cannot prompt for and a review
  seat must read the tree. In certify it is granted no tools, so it runs
  without the flag and relies on that denial. That denial was observed in
  certify (2026-10-17): it holds, and the call it denies returns an empty
  reply, which corral reports with `agy`'s own stderr line.

A certify prompt carries the **audited source**, and source can carry text
aimed at a model. A seat that can read the host could be steered by it. Audit
code you are willing to show a coding agent running as you.

**The CLI inherits corral's whole environment.** It needs `PATH`, `HOME` and
its own token variables, so corral does not strip anything. That means a
vendor API key you exported for corral's API seats (`ANTHROPIC_API_KEY`,
`OPENAI_API_KEY`, ...) reaches the agent CLI too. A CLI that prefers an API
key over its subscription login will **bill that key** instead of the
subscription. Unset the key for an agent-only run if you want the
subscription to pay.

## Interrupting a run

Each agent runs in its own process group, so a timeout kills everything the
agent started, a shell wrapper's children included. That takes it out of the
terminal's foreground group, so corral forwards the interrupt itself: Ctrl-C
or SIGTERM kills every live agent seat's process group at once, and no further
agent call is started in that run. This holds in every verb, including
`review`, which has no other signal handling.

**An interrupted run records nothing**, whatever its seats are (API models
included). `certify --repo`, `certify --local` and `review` write no ledger
entry, signed verdict, attestation, `--record-mutants` set or push, label no
file as failed, and exit **130** with `interrupted — nothing recorded`. That
includes a signal that lands after the last model call, right before the
writes. The one exception is `certify --local`'s live event stream, which
writes each event as it happens by design, so it holds the events up to the
interrupt.

Two cases are reported as what they are rather than as "nothing recorded":
- **certify --local, signal after signing.** certify --local signs its verdict
  inside the run, so a signal that lands after the verdict was signed found a
  complete, recorded run. It is reported as recorded.
- **review, signal between entry and push.** A review interrupted after its
  ledger entry was written, but before the push, says the entry was written
  and not pushed.

When no agent is running, Ctrl-C otherwise means what it always did.

A signal the process was started ignoring stays ignored: neither corral's own
handler nor the agent forwarding registers it. (A shell running a background
job without job control starts it with SIGINT ignored, for example; `nohup`
ignores SIGHUP.)

SIGHUP is not forwarded, and corral does not catch it. A terminal or SSH
hangup therefore kills corral by default, as it always has. An agent CLI in
its own process group does not receive that hangup; it dies on its next
write to the pipe corral held, or at its own timeout. Run long audits under
`tmux`, `screen` or `nohup` if a dropped session must not end them.

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
beside the CLI's `--version` when corral can run it.

**Never put a credential in a `CORRALAI_AGENT_*` definition.** The definition
is published verbatim, in the verdict's `SeatTools`, in the ledger
entry and in review's `ReviewerTool`. Reference an environment variable the
CLI reads instead (for example `CLAUDE_CODE_OAUTH_TOKEN`), and set it outside
the definition. `corral doctor` checks the
binary is installed without making a call, and reports
`binary ok (<version>), definition ok, login not verified (no call is spent)`.
For a shell-wrapped definition (such as `sh -c`) it reports
`binary not checked (shell-wrapped definition)`. Every other verb runs the same
check when it resolves the seat: a missing binary is refused, and a
shell-wrapped definition proceeds unchecked.

An Ollama tag that starts with an agent's name, such as `codex:latest`, reads as
that agent pinned to the model `latest`; rename the model or undefine the agent.

## In analysis

`corral models rank`, the bug-catching scorecard and the public warehouse's
`bug_catches` extract leave out an unpinned agent seat, review included: a row
that names no model is not evidence about a model. Before this change they
counted such rows.

The Action keeps no list of seat names. An agent seat on a runner needs its
CLI installed and a login, which the `claude-code-oauth-token` and
`codex-api-key` inputs pass through; see [github-action.md](github-action.md).
(This section used to say the Action refuses `claude-code` and `codex` by
name. It no longer does.)
