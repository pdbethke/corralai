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
	"strings"
	"time"
)

// Options configure a single Run.
type Options struct {
	Workspace string        // working directory (the command's cwd)
	Timeout   time.Duration // hard deadline; the process is killed past it (default 60s)
	MaxOutput int           // cap on combined stdout+stderr bytes (default 16 KiB)
	Env       []string      // environment; nil => MinimalEnv() (no inherited secrets)
	Network   bool          // allow network egress for the command (default false)
	Backend   Isolator      // isolation backend; nil => execution is disabled (used from Task 2)
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
	// Run in its own process group so a timeout kills the command AND its
	// children (a bare process kill orphans them and holds the output pipe open).
	// Process-group semantics are Unix-only — see proc_unix.go / proc_windows.go.
	setProcGroup(cmd)
	cmd.Cancel = func() error { return killProcGroup(cmd) }
	cmd.WaitDelay = 2 * time.Second // backstop: force-close pipes if a child lingers

	var buf capped
	buf.max = opts.MaxOutput
	cmd.Stdout = &buf
	cmd.Stderr = &buf

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

// capped is an io.Writer that stops storing past max bytes (so a runaway command
// can't flood memory), noting the truncation.
type capped struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (c *capped) Write(p []byte) (int, error) {
	if c.buf.Len() < c.max {
		room := c.max - c.buf.Len()
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

func (c *capped) String() string {
	s := strings.TrimRight(c.buf.String(), "\n")
	if c.truncated {
		s += "\n…[output truncated]"
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

	var buf capped
	buf.max = opts.MaxOutput

	cmd.Stdin = ws
	cmd.Stdout = io.MultiWriter(&buf, ws)
	cmd.Stderr = io.MultiWriter(&buf, ws)

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
