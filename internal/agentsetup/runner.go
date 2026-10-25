// SPDX-License-Identifier: Elastic-2.0

package agentsetup

import (
	"context"
	"errors"
	"os/exec"
	"time"
)

// Timeout bounds every agent CLI call. An agent that does not answer inside it
// is reported unknown, never absent: silence is not evidence of absence.
const Timeout = 15 * time.Second

// Runner runs one fixed argv and returns its combined stdout and stderr.
// Detection and (later) registration go through it so tests can substitute
// recorded agent output.
type Runner interface {
	Run(ctx context.Context, argv []string) ([]byte, error)
}

// ExecRunner runs argv directly, never through a shell. A zero Timeout leaves
// the deadline to ctx alone.
//
// Unlike the fixture-capture script (which runs under env -i so its recordings
// are reproducible), ExecRunner INHERITS the environment: in production setup
// must act on the operator's real agent configuration, including whatever
// override variables (CLAUDE_CONFIG_DIR, CODEX_HOME, ...) point the agent at
// it. Stripping them here would register corral into a config the agent does
// not read.
//
// Dir is the working directory every agent CLI runs in, and it is required:
// the agents' inspect verbs merge PROJECT-level config from their cwd (claude's
// .mcp.json, cursor's .cursor/mcp.json, gemini's workspace settings,
// opencode's project config), so running them in whatever directory corral was
// started from would make setup read, and act on, a view that includes some
// repository's servers. setup and doctor pass the home directory they operate
// on, which is also the condition every fixture in testdata/agents was
// captured under. An empty Dir is refused, never taken to mean the cwd.
type ExecRunner struct {
	Timeout time.Duration
	Dir     string
}

// Run implements Runner. A deadline overrun returns an error satisfying
// errors.Is(err, context.DeadlineExceeded); a nonzero exit returns the
// *exec.ExitError alongside the output.
func (e ExecRunner) Run(ctx context.Context, argv []string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, errors.New("agentsetup: empty argv")
	}
	if e.Dir == "" {
		return nil, errors.New("agentsetup: ExecRunner has no Dir; name the directory agent CLIs run in (the home directory setup operates on)")
	}
	if e.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) // #nosec G204 -- fixed argv from the agent table, no shell
	cmd.Dir = e.Dir
	cmd.WaitDelay = 2 * time.Second // a grandchild holding the pipe must not outlive the timeout
	out, err := cmd.CombinedOutput()
	if err != nil && ctx.Err() != nil {
		return out, ctx.Err()
	}
	return out, err
}
