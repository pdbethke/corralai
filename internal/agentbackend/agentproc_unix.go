// SPDX-License-Identifier: Elastic-2.0

//go:build unix

package agentbackend

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// interruptSignals are the signals forwarded to live agent groups: the ones
// a terminal would have delivered to them had they stayed in its foreground
// group (SIGINT, SIGHUP), and SIGTERM, which auditContext treats as Ctrl-C.
var interruptSignals = []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}

// ownProcessGroup starts cmd as the leader of a new process group, so a kill
// reaches everything a shell-wrapped agent started.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killAgent kills cmd's whole process group. A group that has already exited
// (ESRCH) is not an error: a timeout firing as the agent finishes must not
// turn a clean exit into a failure (as internal/sandbox/proc_unix.go).
func killAgent(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
