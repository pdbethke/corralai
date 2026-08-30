// SPDX-License-Identifier: Elastic-2.0

// Package sandbox runs an untrusted command under an OS-level isolation boundary
// (see Isolator / Resolve) plus in-process guardrails — a hard timeout, an output
// cap, a workspace-confined cwd, and a minimal, secret-free environment. The
// boundary wraps ONLY the command, never the agent process. With no backend, Run
// refuses to execute.
package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Bind is a host directory mounted read-only into the jail at Target.
type Bind struct {
	Host   string // absolute host directory
	Target string // absolute path inside the jail (under Workspace)

	// PerEntry mounts each top-level ENTRY of Host separately, leaving Target
	// itself a writable directory in the workspace, instead of mounting Host
	// over Target as one read-only tree.
	//
	// This exists because a dependency tree is not purely read-only in
	// practice: essentially every JavaScript toolchain writes a cache INSIDE
	// node_modules (vite and vitest use .vite, jest and others use .cache), and
	// a whole-tree read-only mount makes that write fail with EROFS. The tests
	// themselves pass and the runner then exits non-zero, which the audit can
	// only report as "the baseline failed" — a build/environment verdict on a
	// project that is fine.
	//
	// Mounting per entry keeps every real path identical (node resolves module
	// realpaths, so a symlink farm would give the test file and the runner two
	// different copies of the same package and break test registration), copies
	// nothing, and leaves the parent writable so a cache directory can simply
	// be created — in the workspace, thrown away with it.
	//
	// Dot-entries are skipped, except .bin: a cache directory that already
	// exists on the host would otherwise be re-mounted read-only and reproduce
	// the very failure this avoids, while .bin holds the executables the test
	// command needs.
	PerEntry bool
}

// perEntryBinds expands a PerEntry bind into one Bind per top-level entry of
// b.Host. A Host that cannot be read expands to nothing rather than failing the
// run: the jail is then missing dependencies and the suite says so in its own
// words, which is a better diagnosis than an argv-construction error.
func perEntryBinds(b Bind) []Bind {
	ents, err := os.ReadDir(b.Host)
	if err != nil {
		return nil
	}
	out := make([]Bind, 0, len(ents))
	for _, e := range ents {
		name := e.Name()
		if strings.HasPrefix(name, ".") && name != ".bin" {
			continue
		}
		out = append(out, Bind{
			Host:   filepath.Join(b.Host, name),
			Target: path.Join(b.Target, name),
		})
	}
	return out
}

// Options configure a single Run.
type Options struct {
	Workspace string        // working directory (the command's cwd)
	Timeout   time.Duration // hard deadline; the process is killed past it (default 60s)
	MaxOutput int           // cap on combined stdout+stderr bytes (default 16 KiB)
	Env       []string      // environment; nil => MinimalEnv() (no inherited secrets)
	Network   bool          // allow network egress for the command (default false)
	// KeepTail flips WHICH END of an over-long output survives MaxOutput:
	// false (the default) keeps the first MaxOutput bytes, true keeps the
	// last. It exists for callers whose reader wants the END of a run — a
	// test runner's failure summary is printed last, so a head-kept capture
	// of a verbose suite yields the banner and nothing a failure parser can
	// use. Only the capture differs; the command, its exit code, TimedOut
	// and Err are identical either way. See TailWriter.
	KeepTail bool
	Backend  Isolator // isolation backend; nil => execution is disabled (used from Task 2)

	// ReadOnlyBinds are host directories mounted read-only into the jail at
	// Target (an absolute path under Workspace), so large read-only trees
	// (node_modules, vendor, .venv) are visible to the command without being
	// copied into the workspace. The sandboxed process can never write them.
	ReadOnlyBinds []Bind
}

// Result is the outcome of a Run.
type Result struct {
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output"`
	TimedOut bool   `json:"timed_out"`
	Err      string `json:"err,omitempty"`
}

// MinimalEnv returns a safe, secret-free environment for executed code: just
// PATH/HOME/LANG from the host. The bee's CORRAL_TOKEN and the like are never
// exported to commands.
func MinimalEnv() []string {
	var env []string
	for _, k := range []string{"PATH", "HOME", "LANG", "LC_ALL", "TMPDIR"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// Run executes command under the isolation backend in the workspace under the guardrails.
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

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) // #nosec G204 -- corral re-execs its own binary / bwrap by design; argv is constructed by the sandbox layer from server-controlled config, not raw agent input; agent command execution is separately sandboxed (bwrap)
	cmd.Dir = opts.Workspace
	cmd.Env = env
	// Process group + Cancel + WaitDelay, so a timeout kills the command AND
	// its children (a bare process kill orphans them and holds the output
	// pipe open). Shared with adequacy.WorkspaceRunner via GuardProcess —
	// see proc.go for why all three are load-bearing.
	GuardProcess(cmd)

	// Which end of an over-long run survives — see Options.KeepTail. Both
	// writers accept every byte and bound only what they HOLD, so neither can
	// block the command.
	var buf outputCapture = NewCappedWriter(opts.MaxOutput)
	if opts.KeepTail {
		buf = NewTailWriter(opts.MaxOutput)
	}
	cmd.Stdout = buf
	cmd.Stderr = buf

	runErr := runCommand(cmd)
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

// RunGuarded is THE single home of the "a failed run must not read as
// success" invariant that callers rely on: it runs command exactly as Run
// does, but returns a non-nil error whenever the run could not complete
// cleanly (Result.TimedOut, or Result.Err set — e.g. a nil backend or a
// Wrap failure). On err == nil, the returned Result reflects a genuine
// process exit — a timeout or start failure can NEVER be mistaken for exit
// 0 by a caller that only checks err. The Result is always returned
// alongside the error (ExitCode/Output passthrough) so callers that want
// the raw fields — e.g. for logging — still have them.
//
// Both jailAdapter (internal/brain/gate.go) and bwrapJail
// (internal/adequacy/jail.go) delegate to this so the interpretation lives
// in exactly one place and can't drift between the two callers.
func RunGuarded(ctx context.Context, command string, opts Options) (Result, error) {
	res := Run(ctx, command, opts)
	if res.TimedOut || res.Err != "" {
		return res, fmt.Errorf("sandbox: %s", res.Err)
	}
	return res, nil
}

// outputCapture is the seam Run holds its output buffer through: an
// io.Writer that can render what it kept. CappedWriter and TailWriter both
// satisfy it, and Run picks between them on Options.KeepTail — the ONE place
// the choice is made, so nothing downstream has to know which end was kept.
type outputCapture interface {
	io.Writer
	String() string
}

// CappedWriter is an io.Writer that stops storing past Max bytes (so a
// runaway command can't flood memory), noting the truncation. Run and
// RunInteractive use it internally to bound a Backend-wrapped command's
// output; it is exported so a caller running a command directly via
// os/exec — outside the isolation Backend entirely, e.g.
// adequacy.WorkspaceRunner on the workspace substrate, which IS the
// isolation boundary and so never goes through Run/Options.MaxOutput at
// all — can bound its own buffering the identical way, with the identical
// TruncationMarker a downstream caller already knows how to detect.
type CappedWriter struct {
	buf       bytes.Buffer
	Max       int
	truncated bool
}

// NewCappedWriter returns a CappedWriter bounded to max bytes. max <= 0
// falls back to 16 KiB — the same zero-value default Options.MaxOutput
// documents — never "unbounded": a caller that wants a cap at all should
// never silently get none because of an unset field.
func NewCappedWriter(max int) *CappedWriter {
	if max <= 0 {
		max = 16 << 10
	}
	return &CappedWriter{Max: max}
}

func (c *CappedWriter) Write(p []byte) (int, error) {
	if c.buf.Len() < c.Max {
		room := c.Max - c.buf.Len()
		if room >= len(p) {
			c.buf.Write(p)
		} else {
			c.buf.Write(p[:room])
			c.truncated = true
		}
	} else {
		c.truncated = true
	}
	return len(p), nil // always "accept" so the process isn't blocked
}

// TruncationMarker is appended to Result.Output whenever the combined
// stdout+stderr exceeded Options.MaxOutput and was head-truncated. It is
// exported so a caller that cares whether a run's output was cut off (rather
// than merely reading the—now incomplete—text) can detect that structurally,
// by substring search, instead of re-deriving the marker text itself and
// risking it drifting out of sync with CappedWriter.String below.
const TruncationMarker = "…[output truncated]"

func (c *CappedWriter) String() string {
	s := strings.TrimRight(c.buf.String(), "\n")
	if c.truncated {
		s += "\n" + TruncationMarker
	}
	return s
}

// RunInteractive executes command under the isolation backend, piping stdin/stdout to ws.
func RunInteractive(ctx context.Context, command string, opts Options, ws io.ReadWriter) Result {
	if opts.Timeout <= 0 {
		opts.Timeout = 300 * time.Second
	}
	if opts.MaxOutput <= 0 {
		opts.MaxOutput = 64 << 10
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

	// #nosec G204
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = opts.Workspace
	cmd.Env = env
	setProcGroup(cmd)
	cmd.Cancel = func() error { return killProcGroup(cmd) }
	cmd.WaitDelay = 2 * time.Second

	buf := NewCappedWriter(opts.MaxOutput)

	cmd.Stdin = ws
	cmd.Stdout = io.MultiWriter(buf, ws)
	cmd.Stderr = io.MultiWriter(buf, ws)

	runErr := runCommand(cmd)
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
