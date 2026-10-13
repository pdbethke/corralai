// SPDX-License-Identifier: Elastic-2.0

//go:build unix

package agentbackend

import (
	"os/exec"
	"syscall"
)

// ownProcessGroup starts cmd as the leader of a new process group and makes
// its cancellation kill the whole group, so an agent wrapped in a shell does
// not leave its real CLI running past the call's timeout.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
