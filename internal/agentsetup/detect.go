// SPDX-License-Identifier: Elastic-2.0

package agentsetup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// SkillState is the state of corral's skill copy in an agent's directory.
type SkillState string // "absent", "managed", "unmanaged", "n/a", "refused-symlink"

// Status is what detection found for one agent.
type Status struct {
	Agent      Agent
	Installed  bool     // CLI on PATH
	Registered bool     // the agent has a server named corral
	Command    []string // what the agent says it launches for "corral"
	Unknown    string   // non-empty: inspect failed or timed out; holds the reason and output
	Skill      SkillState
	ViaPlugin  bool // claude_code only: the corral@corralai plugin is installed and enabled
}

// pluginListArgv is `claude plugin list`; its text form is in FACTS.md.
var pluginListArgv = []string{"claude", "plugin", "list"}

// Detect inspects every installed agent concurrently (an unresponsive agent
// costs its own timeout, not the sum of all) and returns the statuses in table
// order. An agent whose CLI is not on PATH is never run. A failed or timed-out
// inspect is Unknown, never absent: setup must not register over something it
// could not see.
func Detect(ctx context.Context, r Runner, home string, lookPath func(string) (string, error)) []Status {
	agents := Agents()
	out := make([]Status, len(agents))
	var wg sync.WaitGroup
	for i, a := range agents {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = detectOne(ctx, r, home, lookPath, a)
		}()
	}
	wg.Wait()
	return out
}

func detectOne(ctx context.Context, r Runner, home string, lookPath func(string) (string, error), a Agent) Status {
	st := Status{Agent: a}
	if _, err := lookPath(a.CLI); err != nil {
		return st
	}
	st.Installed = true

	timedOut := false
	if a.Inspect == nil {
		fromFile(&st, a, home)
	} else {
		out, err := r.Run(ctx, a.Inspect)
		timedOut = errors.Is(err, context.DeadlineExceeded)
		interpret(&st, a, out, err, timedOut)
		if st.Unknown != "" && !timedOut && a.File != nil {
			// The agent's list verb could not run at all; its config file can
			// still say whether corral is registered (cursor).
			st.Unknown = ""
			fromFile(&st, a, home)
		}
		if st.Registered && st.Command == nil && a.File != nil {
			var fileSt Status
			fromFile(&fileSt, a, home)
			st.Command = fileSt.Command // best effort: list names no command
		}
	}

	if a.ID == "claude_code" {
		out, err := r.Run(ctx, pluginListArgv)
		// A failed plugin listing leaves ViaPlugin false: the plugin is a
		// supplementary route and the MCP inspect above is authoritative.
		st.ViaPlugin = err == nil && pluginEnabled(out)
	}
	return st
}

func interpret(st *Status, a Agent, out []byte, err error, timedOut bool) {
	if timedOut {
		st.Unknown = fmt.Sprintf("%s did not answer within %s", strings.Join(a.Inspect, " "), Timeout)
		return
	}
	cmd, found := a.ParseInspect(out)
	var ee *exec.ExitError
	switch {
	case found:
		st.Registered, st.Command = true, cmd
	case err == nil:
		// listed, not there
	case errors.As(err, &ee) && ee.ExitCode() == 1 && a.AbsentOnExit1:
		// the get verb's way of saying absent
	default:
		st.Unknown = fmt.Sprintf("%s failed: %v: %s", strings.Join(a.Inspect, " "), err, strings.TrimSpace(string(out)))
	}
}

// fromFile reads the agent's config file. A missing file is not registered;
// a file that does not parse is Unknown, because setup would otherwise write
// over something it could not read.
func fromFile(st *Status, a Agent, home string) {
	path := a.File.Path(home)
	data, err := os.ReadFile(path) // #nosec G304 -- the agent's own config path under home
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		st.Unknown = fmt.Sprintf("reading %s: %v", path, err)
		return
	}
	cmd, found, perr := parseServersJSON(data, a.File.ServersKey)
	if perr != nil {
		st.Unknown = fmt.Sprintf("%s does not parse: %v", path, perr)
		return
	}
	st.Registered, st.Command = found, cmd
}

// pluginEnabled finds the corral@corralai block in `claude plugin list` text
// and reports whether its Status line says enabled.
func pluginEnabled(out []byte) bool {
	lines := strings.Split(string(out), "\n")
	for i, l := range lines {
		if !strings.Contains(l, "corral@corralai") {
			continue
		}
		for _, n := range lines[i+1:] {
			t := strings.TrimSpace(n)
			if strings.HasPrefix(t, "Status:") {
				return strings.Contains(t, "enabled") && !strings.Contains(t, "disabled")
			}
			if strings.Contains(n, "❯") {
				break // the next plugin's block
			}
		}
	}
	return false
}
