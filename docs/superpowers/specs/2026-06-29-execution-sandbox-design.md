# Execution Sandbox — Design (sub-project #10)

**Status:** design · **Date:** 2026-06-29

## Where this fits

This closes the #1 gap. Today the bees `edit_file` (append a marker) and the
test/secops/perf phases are LLM *narration* — findings are "the model thinks,"
not "a test proved." #10 lets agents **write real files** and **run real
commands** (build, tests, the program), so completions and findings reflect
actual execution.

## The honest security boundary

Running LLM-generated code is dangerous. corralai is portable Go, so true OS
isolation (network-off, FS jail, resource caps via namespaces/gVisor/microVM)
isn't something we can do purely in-process. The design is explicit about this:

- **The isolation boundary is the agent's container/VM.** A bee that runs code
  should run in a locked-down container (the demo agents already are). The
  sandbox package provides *in-process guardrails*, not a security boundary by
  itself.
- **In-process guardrails:** a hard **timeout** (context deadline → process
  kill), **output cap**, **workspace-confined** working dir, and a **minimal,
  secret-free env** (the bee's `CORRAL_TOKEN` etc. are never exported to executed
  code).
- **Opt-in:** execution is OFF unless `AGENT_ALLOW_EXEC=1`, so an agent never
  silently runs model code on a host. The demo's exec profile sets it (in a
  container).

We will NOT claim "secure sandbox" — we claim "bounded execution, isolated by the
agent's container." Strong isolation (nsjail/gVisor/microVM) is a documented
hardening follow-up.

## Components

### 1. `internal/sandbox` (pure Go, the runner)

`Run(ctx, command string, opts Options) Result`:
- `Options{ Workspace string; Timeout time.Duration; MaxOutput int; Env []string }`
- runs `sh -c <command>` with `Dir=Workspace`, the deadline (kill on timeout),
  combined stdout+stderr captured and truncated to `MaxOutput`, and `Env` set to
  a **minimal allowlist** (PATH/HOME/LANG) — never the parent's secrets.
- `Result{ ExitCode int; Output string; TimedOut bool; Err string }`.
- `MinimalEnv()` helper builds the safe env from the host's PATH/HOME/LANG.

### 2. `corral-agent` — real work tools

- `write_file { path, content }`: write the actual file content into the
  workspace (real artifact), path cleaned + confined under the workspace.
- `run_command { command }`: execute via `internal/sandbox` (gated on
  `AGENT_ALLOW_EXEC`; refused with a clear message otherwise). Returns
  `{exit_code, output, timed_out}` — fed back to the model so it acts on real
  results.
- The existing marker-`edit_file` stays for the coordination demos; mission bees
  use `write_file` + `run_command`.
- Phase prompts: build *writes real files*; test/perf/secops *run real commands*
  and `report_finding` on failure (non-zero exit / failing tests).

### 3. Execution-capable agent image / env

`sh` + the project toolchains must exist where the bee runs — distroless has
neither. The MVP: the runner is real and proven with shell commands; a
toolchain-equipped agent image (or host-run in dev with `AGENT_ALLOW_EXEC=1`) is
how real builds/tests run. A `Dockerfile.agent-exec` (golang/node base) is a
follow-up; the mechanism doesn't depend on it.

## Testing strategy

- **sandbox:** `echo hello` → exit 0 + output; `exit 3` → exit 3; `sleep`
  past the timeout → `TimedOut` + killed; cwd is the workspace; a parent secret
  in the environment is NOT visible to the command (env isolation).
- **agent:** `write_file` writes real content under the workspace (path confined);
  `run_command` refused without `AGENT_ALLOW_EXEC`, runs via the sandbox with it.
- **e2e:** with exec enabled, write a tiny program + `run_command` it → real exit
  code + output captured; a failing command surfaces for the model to report.

## Decisions deferred to the plan

- Whether `run_command` takes an allowlist of binaries vs. free shell (start free
  shell inside the container boundary; allowlist is extra hardening).
- The toolchain agent image (`Dockerfile.agent-exec`) — follow-up.
- Network-off / resource caps via OS sandboxing — documented hardening, not MVP.
