// SPDX-License-Identifier: Elastic-2.0

//go:build unix

package sandbox

import (
	"os/exec"
	"syscall"
)

// setProcGroup puts the command in its own process group, so a timeout can
// take out the command AND every child it spawned.
func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcGroup kills the command's whole process group (-pid). A bare process
// kill would orphan children and hold the output pipe open past the deadline.
func killProcGroup(cmd *exec.Cmd) error {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	return nil
}

func runCommand(cmd *exec.Cmd) error {
	return cmd.Run()
}
