# Execution Isolation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `run_command` jail the untrusted command it spawns (default `bwrap` namespace jail, network off) and refuse to run at all when it can't isolate — without ever jailing the bee itself.

**Architecture:** Add a pluggable `Isolator` backend to `internal/sandbox` behind the existing `Run(ctx, command, opts)` signature. The agent resolves + preflights a backend once at startup; `run_command` consults that resolved state. `bwrap` wraps only the `sh -c` subprocess; the `corral-agent` process keeps full network, MCP, token, and research tools.

**Tech Stack:** Go 1.26, `bubblewrap` (`bwrap`) on Linux, pure-Go `internal/sandbox` (no new module deps), `os/exec`.

## Global Constraints

- **Jail the command, not the bee.** Isolation wraps only `run_command`'s subprocess. The `corral-agent` process is never sandboxed — it keeps full network, its MCP session to the brain, `CORRAL_TOKEN`, and research/RAG tools.
- **Default-deny, no fallback.** If the resolved backend can't isolate (preflight fails), execution stays DISABLED and `run_command` returns a loud actionable error. Never silently degrade to a weaker backend.
- **Network off by default** for executed commands; `AGENT_EXEC_NET=1` opts a build into egress.
- **`none` backend is explicit + unsafe-only:** selectable only with `AGENT_EXEC_BACKEND=none` AND `AGENT_EXEC_UNSAFE_HOST=1`. Never auto-selected, never a fallback.
- **The agent binary stays CGO-free** (`CGO_ENABLED=0 go build ./cmd/corral-agent` must pass). `internal/sandbox` adds no cgo.
- **The executed command never sees secrets:** env is `MinimalEnv()` (PATH/HOME/LANG/LC_ALL/TMPDIR only) AND bwrap `--clearenv` + explicit `--setenv` of exactly those.
- Default backend is `bwrap`; `AGENT_EXEC_BACKEND` ∈ {`bwrap`,`container`,`none`} (`container` is documented-not-built and returns a clear "not implemented" error).
- bwrap is Linux-only; non-Linux hosts must use `container` (future) or the explicit unsafe path.

---

## File Structure

- `internal/sandbox/isolator.go` (new): `Isolator` interface, `Config`, `Resolve`, `bwrapIsolator`, `noneIsolator`. Pure argv construction + preflight.
- `internal/sandbox/isolator_test.go` (new): argv-construction + `Resolve` unit tests (no bwrap needed).
- `internal/sandbox/sandbox.go` (modify): `Options` gains `Network`+`Backend`; `Run` wraps via the backend; package doc updated.
- `internal/sandbox/sandbox_test.go` (modify): existing guardrail tests pass `Backend: noneIsolator{}`; add nil-backend + live-bwrap tests.
- `cmd/corral-agent/main.go` (modify): add `execRuntime` package var + `setupExec()`, call it at startup, rewrite the `run_command` dispatch case.
- `cmd/corral-agent/dispatch_test.go` (modify): drive `run_command` through `execRuntime` instead of the `AGENT_ALLOW_EXEC` env; add a `setupExec` refuse-path test.
- `deploy/demo/Dockerfile.agent-exec` (modify): install `bubblewrap`.
- `deploy/demo/docker-compose.yml` (modify): pass `AGENT_EXEC_BACKEND` + `AGENT_EXEC_NET` through `x-agent-env`.
- `README.md` (modify): rewrite the "Real execution" section for the two-tier jail + new env vars.

---

## Task 1: Isolator interface, backends, and Resolve

**Files:**
- Create: `internal/sandbox/isolator.go`
- Test: `internal/sandbox/isolator_test.go`
- Modify: `internal/sandbox/sandbox.go` (add `Network` + `Backend` fields to `Options` so `isolator.go` compiles; `Run` keeps its current behavior until Task 2)

**Interfaces:**
- Consumes: `Options.Workspace`, `Options.Network` (the `Network` field is added to `Options` in this task; `Backend` is added here too but not used until Task 2).
- Produces:
  - `type Isolator interface { Wrap(command string, opts Options, env []string) (argv []string, err error); Preflight() error; Name() string }`
  - `type Config struct { Backend string; UnsafeHost bool }`
  - `func Resolve(cfg Config) (Isolator, error)`
  - `type bwrapIsolator struct{}`, `type noneIsolator struct{}` (unexported; obtained via `Resolve`)

- [ ] **Step 1: Write the failing tests**

Create `internal/sandbox/isolator_test.go`:

```go
package sandbox

import (
	"strings"
	"testing"
)

func argvHas(argv []string, want ...string) bool {
	for i := 0; i+len(want) <= len(argv); i++ {
		match := true
		for j, w := range want {
			if argv[i+j] != w {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func TestBwrapWrapNetOffByDefault(t *testing.T) {
	argv, err := bwrapIsolator{}.Wrap("echo hi", Options{Workspace: "/workspace"}, []string{"PATH=/usr/bin"})
	if err != nil {
		t.Fatal(err)
	}
	if !argvHas(argv, "--unshare-all") {
		t.Fatal("expected --unshare-all (net off)")
	}
	if argvHas(argv, "--share-net") {
		t.Fatal("did not expect --share-net when Network is false")
	}
	if !argvHas(argv, "--setenv", "PATH", "/usr/bin") {
		t.Fatal("expected env passed via --setenv")
	}
	if !argvHas(argv, "--bind", "/workspace", "/workspace") || !argvHas(argv, "--chdir", "/workspace") {
		t.Fatal("expected the workspace bound + chdir")
	}
	if !argvHas(argv, "--", "sh", "-c", "echo hi") {
		t.Fatal("expected the command after --")
	}
	if argvHas(argv, "--ro-bind-try", "/etc/resolv.conf", "/etc/resolv.conf") {
		t.Fatal("resolv.conf should not be bound when net is off")
	}
}

func TestBwrapWrapNetOn(t *testing.T) {
	argv, _ := bwrapIsolator{}.Wrap("go build", Options{Workspace: "/w", Network: true}, nil)
	if !argvHas(argv, "--share-net") {
		t.Fatal("expected --share-net when Network is true")
	}
	if !argvHas(argv, "--ro-bind-try", "/etc/resolv.conf", "/etc/resolv.conf") {
		t.Fatal("expected resolv.conf bound for DNS when networked")
	}
}

func TestBwrapWrapRequiresWorkspace(t *testing.T) {
	if _, err := bwrapIsolator{}.Wrap("x", Options{}, nil); err == nil {
		t.Fatal("expected an error when no workspace is set")
	}
}

func TestNoneWrapIsRawSh(t *testing.T) {
	argv, err := noneIsolator{}.Wrap("echo hi", Options{Workspace: "/w"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(argv, " ") != "sh -c echo hi" {
		t.Fatalf("none should be a raw sh -c, got %v", argv)
	}
}

func TestResolveNoneRequiresUnsafeOverride(t *testing.T) {
	if _, err := Resolve(Config{Backend: "none"}); err == nil {
		t.Fatal("none without UnsafeHost must be rejected")
	}
	iso, err := Resolve(Config{Backend: "none", UnsafeHost: true})
	if err != nil || iso.Name() != "none" {
		t.Fatalf("none with override should resolve, got %v %v", iso, err)
	}
}

func TestResolveContainerNotImplemented(t *testing.T) {
	if _, err := Resolve(Config{Backend: "container"}); err == nil {
		t.Fatal("container backend should report not-implemented")
	}
}

func TestResolveUnknownBackend(t *testing.T) {
	if _, err := Resolve(Config{Backend: "bogus"}); err == nil {
		t.Fatal("unknown backend should error")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/sandbox/ -run 'Bwrap|None|Resolve' -v`
Expected: FAIL — `undefined: bwrapIsolator`, `undefined: noneIsolator`, `undefined: Resolve`.

- [ ] **Step 3a: Add the `Network` + `Backend` fields to `Options`**

In `internal/sandbox/sandbox.go`, replace the `Options` struct with (this lets `isolator.go` reference `opts.Network`; `Run` ignores `Backend`/`Network` until Task 2):

```go
// Options configure a single Run.
type Options struct {
	Workspace string        // working directory (the command's cwd)
	Timeout   time.Duration // hard deadline; the process is killed past it (default 60s)
	MaxOutput int           // cap on combined stdout+stderr bytes (default 16 KiB)
	Env       []string      // environment; nil => MinimalEnv() (no inherited secrets)
	Network   bool          // allow network egress for the command (default false)
	Backend   Isolator      // isolation backend; nil => execution is disabled (used from Task 2)
}
```

- [ ] **Step 3b: Write `internal/sandbox/isolator.go`**

```go
package sandbox

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Isolator wraps an untrusted command in an OS-level isolation boundary. It wraps
// ONLY the command run_command spawns — never the agent process itself.
type Isolator interface {
	// Wrap returns the argv that runs `command` under isolation, given the
	// workspace/options and the (already minimal, secret-free) env. It does not exec.
	Wrap(command string, opts Options, env []string) (argv []string, err error)
	// Preflight verifies the backend can actually isolate on THIS host.
	Preflight() error
	// Name identifies the backend in logs and errors.
	Name() string
}

// Config selects a backend at startup.
type Config struct {
	Backend    string // "bwrap" (default) | "container" | "none"
	UnsafeHost bool   // required to select "none"
}

// Resolve picks and preflights a backend. It NEVER falls back to a weaker
// backend: if the requested backend can't isolate, it returns an error and the
// caller must refuse to execute.
func Resolve(cfg Config) (Isolator, error) {
	switch cfg.Backend {
	case "", "bwrap":
		b := bwrapIsolator{}
		if err := b.Preflight(); err != nil {
			return nil, fmt.Errorf("bwrap backend unavailable: %w", err)
		}
		return b, nil
	case "none":
		if !cfg.UnsafeHost {
			return nil, errors.New(`backend "none" runs commands unisolated; set AGENT_EXEC_UNSAFE_HOST=1 to confirm this host is already a disposable sandbox`)
		}
		return noneIsolator{}, nil
	case "container":
		return nil, errors.New(`backend "container" is not implemented yet; use "bwrap"`)
	default:
		return nil, fmt.Errorf("unknown exec backend %q (want bwrap|container|none)", cfg.Backend)
	}
}

// --- bwrap: Linux unprivileged namespace jail (default) ---

type bwrapIsolator struct{}

func (bwrapIsolator) Name() string { return "bwrap" }

func (bwrapIsolator) Preflight() error {
	if _, err := exec.LookPath("bwrap"); err != nil {
		return fmt.Errorf("bwrap not found on PATH: %w", err)
	}
	// Prove unprivileged user namespaces actually work — a version check alone
	// passes on kernels that compiled userns out, then every real run fails.
	out, err := exec.Command("bwrap", "--unshare-all", "--ro-bind", "/", "/", "--", "true").CombinedOutput()
	if err != nil {
		return fmt.Errorf("bwrap cannot create a sandbox (user namespaces disabled?): %v: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

func (bwrapIsolator) Wrap(command string, opts Options, env []string) ([]string, error) {
	if opts.Workspace == "" {
		return nil, errors.New("bwrap: workspace required")
	}
	argv := []string{"bwrap",
		"--unshare-all",     // user+pid+ipc+uts+cgroup+net namespaces; no privileged caps
		"--die-with-parent", // killed if the agent dies
		"--new-session",     // detach controlling terminal
		"--clearenv",        // start from nothing; only --setenv below reaches the command
	}
	if opts.Network {
		argv = append(argv, "--share-net") // undo --unshare-all's net isolation
	}
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i > 0 {
			argv = append(argv, "--setenv", kv[:i], kv[i+1:])
		}
	}
	// Minimal read-only root (usrmerged Linux): the command can't read /home or
	// host secrets, only the toolchain. The workspace is the ONLY writable path.
	argv = append(argv,
		"--ro-bind", "/usr", "/usr",
		"--symlink", "usr/bin", "/bin",
		"--symlink", "usr/sbin", "/sbin",
		"--symlink", "usr/lib", "/lib",
		"--symlink", "usr/lib64", "/lib64",
		"--ro-bind-try", "/etc/ssl", "/etc/ssl",
		"--ro-bind-try", "/etc/ca-certificates", "/etc/ca-certificates",
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
		"--bind", opts.Workspace, opts.Workspace, // bind AFTER tmpfs so a /tmp workspace survives
		"--chdir", opts.Workspace,
	)
	if opts.Network {
		argv = append(argv, "--ro-bind-try", "/etc/resolv.conf", "/etc/resolv.conf")
	}
	return append(argv, "--", "sh", "-c", command), nil
}

// --- none: raw execution, explicit + unsafe only ---

type noneIsolator struct{}

func (noneIsolator) Name() string     { return "none" }
func (noneIsolator) Preflight() error { return nil }
func (noneIsolator) Wrap(command string, opts Options, env []string) ([]string, error) {
	return []string{"sh", "-c", command}, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/sandbox/ -run 'Bwrap|None|Resolve' -v`
Expected: PASS (7 tests). The `Bwrap...Wrap` tests are pure argv construction — they pass with or without bwrap installed.

- [ ] **Step 5: Commit**

```bash
git add internal/sandbox/isolator.go internal/sandbox/isolator_test.go internal/sandbox/sandbox.go
git commit -m "feat(sandbox): pluggable Isolator backends (bwrap/none) + Resolve"
```

---

## Task 2: Thread the backend through Run

**Files:**
- Modify: `internal/sandbox/sandbox.go`
- Modify: `internal/sandbox/sandbox_test.go`

**Interfaces:**
- Consumes: `Isolator`, `bwrapIsolator`, `noneIsolator` (Task 1).
- Produces: `Options` with `Network bool` and `Backend Isolator`; `Run` now execs the backend-wrapped argv and returns a disabled-error when `Backend == nil`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/sandbox/sandbox_test.go`:

```go
func TestRunNilBackendDisabled(t *testing.T) {
	r := Run(context.Background(), "echo hi", Options{Workspace: t.TempDir()})
	if r.Err == "" || r.ExitCode != -1 {
		t.Fatalf("nil backend must be disabled, got %+v", r)
	}
}

func bwrapOrSkip(t *testing.T) Isolator {
	b := bwrapIsolator{}
	if err := b.Preflight(); err != nil {
		t.Skipf("bwrap unavailable: %v", err)
	}
	return b
}

func TestBwrapConfinesWritesAndReads(t *testing.T) {
	iso := bwrapOrSkip(t)
	ws := t.TempDir()
	r := Run(context.Background(), "echo hi > made.txt && cat made.txt", Options{Workspace: ws, Backend: iso})
	if r.ExitCode != 0 || !strings.Contains(r.Output, "hi") {
		t.Fatalf("in-workspace write should work: %+v", r)
	}
	r = Run(context.Background(), "echo x > /etc/pwned", Options{Workspace: ws, Backend: iso})
	if r.ExitCode == 0 {
		t.Fatalf("write outside the workspace must fail: %+v", r)
	}
	r = Run(context.Background(), "cat /etc/hostname", Options{Workspace: ws, Backend: iso})
	if r.ExitCode == 0 {
		t.Fatalf("host files outside the minimal root must be unreadable: %+v", r)
	}
}

func TestBwrapCwdAndSecretFree(t *testing.T) {
	iso := bwrapOrSkip(t)
	t.Setenv("CORRAL_TOKEN", "super-secret")
	ws := t.TempDir()
	r := Run(context.Background(), "echo TOKEN=[$CORRAL_TOKEN]", Options{Workspace: ws, Backend: iso})
	if !strings.Contains(r.Output, "TOKEN=[]") {
		t.Fatalf("the command's env must be secret-free, got %q", r.Output)
	}
}
```

Then update the SIX existing tests in this file to pass a backend, since `Run` now requires one. Change each `Options{...}` literal to include `Backend: noneIsolator{}`:

```go
// TestRunCapturesOutputAndExit
r := Run(context.Background(), "echo hello world", Options{Workspace: t.TempDir(), Backend: noneIsolator{}})
// TestRunNonzeroExit
r := Run(context.Background(), "exit 3", Options{Workspace: t.TempDir(), Backend: noneIsolator{}})
// TestRunTimesOut
r := Run(context.Background(), "sleep 10", Options{Workspace: t.TempDir(), Timeout: 300 * time.Millisecond, Backend: noneIsolator{}})
// TestRunCwdIsWorkspace
r := Run(context.Background(), "pwd", Options{Workspace: ws, Backend: noneIsolator{}})
// TestRunEnvIsSecretFree
r := Run(context.Background(), "echo TOKEN=[$CORRAL_TOKEN]", Options{Workspace: t.TempDir(), Backend: noneIsolator{}})
// TestRunOutputCap
r := Run(context.Background(), "yes x | head -c 100000", Options{Workspace: t.TempDir(), MaxOutput: 1000, Backend: noneIsolator{}})
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/sandbox/ -run 'TestRun|Bwrap' -v`
Expected: FAIL to compile — `Options` has no field `Backend`.

- [ ] **Step 3: Modify `internal/sandbox/sandbox.go`**

Update the package doc comment (top of file) to:

```go
// Package sandbox runs an untrusted command under an OS-level isolation boundary
// (see Isolator / Resolve) plus in-process guardrails — a hard timeout, an output
// cap, a workspace-confined cwd, and a minimal, secret-free environment. The
// boundary wraps ONLY the command, never the agent process. With no backend, Run
// refuses to execute.
```

(`Options` already has `Network` + `Backend` from Task 1.) Rewrite `Run` to gate on the backend, wrap the command, and exec the wrapped argv. The body becomes:

```go
func Run(ctx context.Context, command string, opts Options) Result {
	if opts.Timeout <= 0 {
		opts.Timeout = 60 * time.Second
	}
	if opts.MaxOutput <= 0 {
		opts.MaxOutput = 16 << 10
	}
	env := opts.Env
	if env == nil {
		env = MinimalEnv()
	}

	if opts.Backend == nil {
		return Result{ExitCode: -1, Err: "execution disabled: no isolation backend"}
	}
	argv, werr := opts.Backend.Wrap(command, opts, env)
	if werr != nil {
		return Result{ExitCode: -1, Err: werr.Error()}
	}

	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = opts.Workspace
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	cmd.WaitDelay = 2 * time.Second

	var buf capped
	buf.max = opts.MaxOutput
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	runErr := cmd.Run()
	res := Result{Output: buf.String()}
	if ctx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
		res.ExitCode = -1
		res.Err = "timed out after " + opts.Timeout.String()
		return res
	}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	if runErr != nil && res.ExitCode == 0 {
		res.ExitCode = -1
		res.Err = runErr.Error()
	}
	return res
}
```

(The only changes from the current `Run` are the two `env`-then-backend lines and `exec.CommandContext(ctx, argv[0], argv[1:]...)` in place of the hard-coded `"sh", "-c", command`.)

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/sandbox/ -v`
Expected: PASS. On a host without bwrap/userns, `TestBwrapConfinesWritesAndReads` and `TestBwrapCwdAndSecretFree` report SKIP; everything else PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/sandbox/sandbox.go internal/sandbox/sandbox_test.go
git commit -m "feat(sandbox): Run execs the backend-wrapped argv; nil backend = disabled"
```

---

## Task 3: Agent startup guard + run_command rewrite

**Files:**
- Modify: `cmd/corral-agent/main.go`
- Modify: `cmd/corral-agent/dispatch_test.go`

**Interfaces:**
- Consumes: `sandbox.Resolve`, `sandbox.Config`, `sandbox.Options{Backend,Network}`, `sandbox.Isolator` (Tasks 1-2).
- Produces: package var `execRuntime` and `func setupExec()`; `run_command` dispatch reads `execRuntime`.

- [ ] **Step 1: Write the failing tests**

Replace `TestDispatchRunCommandGated` in `cmd/corral-agent/dispatch_test.go` with versions that drive the resolved runtime (the env-only gate is gone). Add a `setupExec` refuse-path test:

```go
func TestDispatchRunCommandDisabled(t *testing.T) {
	saved := execRuntime
	t.Cleanup(func() { execRuntime = saved })

	execRuntime = execState{enabled: false, reason: "execution disabled — test"}
	out := dispatch("Ada", t.TempDir(), nil, "run_command", map[string]any{"command": "echo hi"})
	if !strings.Contains(out, "execution disabled") {
		t.Fatalf("disabled runtime must refuse, got %s", out)
	}
}

func TestDispatchRunCommandRuns(t *testing.T) {
	saved := execRuntime
	t.Cleanup(func() { execRuntime = saved })

	iso, err := sandbox.Resolve(sandbox.Config{Backend: "none", UnsafeHost: true})
	if err != nil {
		t.Fatal(err)
	}
	execRuntime = execState{enabled: true, backend: iso}

	ws := t.TempDir()
	out := dispatch("Ada", ws, nil, "run_command", map[string]any{"command": "echo built-and-ran"})
	var r struct {
		ExitCode int    `json:"exit_code"`
		Output   string `json:"output"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("decode: %v (%s)", err, out)
	}
	if r.ExitCode != 0 || !strings.Contains(r.Output, "built-and-ran") {
		t.Fatalf("did not really execute: %+v", r)
	}
	out = dispatch("Ada", ws, nil, "run_command", map[string]any{"command": "exit 1"})
	json.Unmarshal([]byte(out), &r)
	if r.ExitCode != 1 {
		t.Fatalf("failing command should report exit 1, got %+v", r)
	}
}

func TestSetupExecRefusesOnUnavailableBackend(t *testing.T) {
	saved := execRuntime
	t.Cleanup(func() { execRuntime = saved })
	t.Setenv("AGENT_ALLOW_EXEC", "1")
	t.Setenv("AGENT_EXEC_BACKEND", "container") // resolves to a not-implemented error
	t.Setenv("AGENT_EXEC_UNSAFE_HOST", "")

	setupExec()
	if execRuntime.enabled {
		t.Fatal("exec must stay disabled when the backend is unavailable")
	}
	if !strings.Contains(execRuntime.reason, "container") {
		t.Fatalf("reason should name the failure, got %q", execRuntime.reason)
	}
}

func TestSetupExecDisabledWithoutOptIn(t *testing.T) {
	saved := execRuntime
	t.Cleanup(func() { execRuntime = saved })
	t.Setenv("AGENT_ALLOW_EXEC", "")

	setupExec()
	if execRuntime.enabled {
		t.Fatal("exec must be off unless AGENT_ALLOW_EXEC=1")
	}
}
```

The existing `TestDispatchWriteFile` stays as-is (write_file is unaffected). Add `"github.com/pdbethke/corralai/internal/sandbox"` to the test's imports.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/corral-agent/ -run 'Dispatch|SetupExec' -v`
Expected: FAIL to compile — `undefined: execRuntime`, `execState`, `setupExec`.

- [ ] **Step 3: Modify `cmd/corral-agent/main.go`**

Add the package-level state + setup function (place near `dispatch`, e.g. just above `func dispatch`):

```go
// execState is resolved once at startup; run_command consults it. The bee itself
// is never jailed — this governs only the subprocess run_command spawns.
type execState struct {
	enabled bool
	backend sandbox.Isolator
	network bool
	reason  string // why exec is unavailable, surfaced to the model
}

var execRuntime execState

// setupExec resolves + preflights the isolation backend once. Default-deny: if
// exec is requested but can't be isolated, it stays disabled with a loud reason.
func setupExec() {
	if os.Getenv("AGENT_ALLOW_EXEC") != "1" {
		execRuntime = execState{reason: "execution disabled — set AGENT_ALLOW_EXEC=1 to enable run_command"}
		return
	}
	iso, err := sandbox.Resolve(sandbox.Config{
		Backend:    os.Getenv("AGENT_EXEC_BACKEND"),
		UnsafeHost: os.Getenv("AGENT_EXEC_UNSAFE_HOST") == "1",
	})
	if err != nil {
		execRuntime = execState{reason: "execution unavailable: " + err.Error() +
			" — install bubblewrap, or set AGENT_EXEC_BACKEND=container, or run in a disposable container with AGENT_EXEC_BACKEND=none AGENT_EXEC_UNSAFE_HOST=1. Refusing to run untrusted commands unprotected."}
		fmt.Printf("[exec] DISABLED: %s\n", execRuntime.reason)
		return
	}
	execRuntime = execState{enabled: true, backend: iso, network: os.Getenv("AGENT_EXEC_NET") == "1"}
	fmt.Printf("[exec] enabled: backend=%s network=%v\n", iso.Name(), execRuntime.network)
}
```

Call it once at startup — in `main`, immediately after `os.MkdirAll(ws, 0o755)` (currently line 114):

```go
	os.MkdirAll(ws, 0o755)
	setupExec()
```

Replace the `run_command` case body in `dispatch` (currently lines 579-591) with:

```go
	case "run_command":
		// Real execution — only via the backend resolved at startup. Default-deny:
		// if no backend could isolate, refuse with the reason. The jail wraps this
		// subprocess, never the agent.
		if !execRuntime.enabled {
			return fmt.Sprintf(`{"error":%q}`, execRuntime.reason)
		}
		command, _ := args["command"].(string)
		if command == "" {
			return `{"error":"command required"}`
		}
		res := sandbox.Run(context.Background(), command, sandbox.Options{
			Workspace: ws, Timeout: 120 * time.Second,
			Backend: execRuntime.backend, Network: execRuntime.network,
		})
		b, _ := json.Marshal(res)
		return string(b)
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cmd/corral-agent/ -v && CGO_ENABLED=0 go build -o /dev/null ./cmd/corral-agent`
Expected: PASS (all dispatch + setupExec tests) and the agent still builds CGO-free.

- [ ] **Step 5: Commit**

```bash
git add cmd/corral-agent/main.go cmd/corral-agent/dispatch_test.go
git commit -m "feat(sandbox): startup default-deny guard; run_command uses the resolved backend"
```

---

## Task 4: Demo image + docs

**Files:**
- Modify: `deploy/demo/Dockerfile.agent-exec`
- Modify: `deploy/demo/docker-compose.yml`
- Modify: `README.md`

**Interfaces:**
- Consumes: the env vars from Task 3 (`AGENT_EXEC_BACKEND`, `AGENT_EXEC_NET`).
- Produces: an exec demo image with `bwrap` present; accurate docs.

- [ ] **Step 1: Install bubblewrap in the exec image**

In `deploy/demo/Dockerfile.agent-exec`, after `WORKDIR /app` and before `COPY go.mod go.sum ./`, add:

```dockerfile
RUN apt-get update && apt-get install -y --no-install-recommends bubblewrap \
    && rm -rf /var/lib/apt/lists/*
```

- [ ] **Step 2: Pass the new env through the compose anchor**

In `deploy/demo/docker-compose.yml`, in the `x-agent-env: &agent-env` block, immediately after the `AGENT_ALLOW_EXEC: ${AGENT_ALLOW_EXEC:-0}` line, add:

```yaml
  # Isolation backend for run_command (bwrap = Linux namespace jail). Network off
  # by default; flip AGENT_EXEC_NET=1 for a build step that fetches deps.
  AGENT_EXEC_BACKEND: ${AGENT_EXEC_BACKEND:-bwrap}
  AGENT_EXEC_NET: ${AGENT_EXEC_NET:-0}
```

- [ ] **Step 3: Rewrite the README "Real execution" section**

Replace the existing `### Real execution` section in `README.md` with:

```markdown
### Real execution

By default bees produce artifacts as text. With execution enabled, a bee can
`write_file` the actual artifact and `run_command` to build, run, and test it,
then report the real exit code and output (a failing build becomes a finding
instead of an assumption).

**The jail wraps the command, not the bee.** The `corral-agent` process is never
sandboxed — it keeps full network, its MCP session to the brain, its token, and
all research/RAG tools. Only the subprocess `run_command` spawns is isolated:

- **Default-deny.** `AGENT_ALLOW_EXEC=1` turns on `run_command`, but it only runs
  once a backend has been resolved and preflighted. If the host can't isolate,
  execution stays disabled and `run_command` returns a loud, actionable error —
  it never silently degrades to running unprotected.
- **`bwrap` backend (default, Linux).** Each command runs in an unprivileged
  namespace jail: network off, read-only root except the workspace, no privileged
  caps, a secret-free env (the bee's `CORRAL_TOKEN` never reaches it). Needs
  `bubblewrap` present (the demo's `Dockerfile.agent-exec` installs it).
- **Network off by default.** Set `AGENT_EXEC_NET=1` for a build step that
  legitimately fetches deps (`go mod download`, `npm install`).
- **`none` backend** runs commands unisolated and is opt-in only via
  `AGENT_EXEC_BACKEND=none AGENT_EXEC_UNSAFE_HOST=1` — for a host you've already
  hardened yourself.

| Var | Default | Meaning |
|---|---|---|
| `AGENT_ALLOW_EXEC` | `0` | Master gate for `run_command`. |
| `AGENT_EXEC_BACKEND` | `bwrap` | `bwrap` \| `container` (future) \| `none`. |
| `AGENT_EXEC_NET` | `0` | Network access for executed commands. |
| `AGENT_EXEC_UNSAFE_HOST` | `0` | Required to select `none`. |

bwrap shares the host kernel — it stops casual damage, egress, and filesystem
escape, **not** a kernel-exploit escape. For adversarial code use a stronger
backend (container/microVM); the pluggable `Isolator` makes that a drop-in. See
**[the design note](docs/superpowers/specs/2026-06-29-exec-isolation-design.md)**.
```

- [ ] **Step 4: Verify the compose + Dockerfile parse and the docs build**

Run: `cd deploy/demo && docker compose config >/dev/null && echo COMPOSE_OK; cd ../..`
Expected: `COMPOSE_OK` (the merged config is valid YAML with the new env keys).
Run: `grep -c 'AGENT_EXEC_BACKEND' deploy/demo/docker-compose.yml`
Expected: `1` (the anchor passthrough).

- [ ] **Step 5: Commit**

```bash
git add deploy/demo/Dockerfile.agent-exec deploy/demo/docker-compose.yml README.md
git commit -m "feat(sandbox): exec demo ships bwrap; docs for the two-tier jail"
```

---

## Final verification (after all tasks)

- [ ] Run the full suite + vet + CGO-free agent build:

```bash
go build ./... && go vet ./... && go test ./... && CGO_ENABLED=0 go build -o /dev/null ./cmd/corral-agent
```
Expected: all packages PASS (bwrap live tests SKIP if userns is unavailable), vet clean, agent builds CGO-free.
