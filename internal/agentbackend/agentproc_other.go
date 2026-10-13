// SPDX-License-Identifier: Elastic-2.0

//go:build !unix

package agentbackend

import (
	"errors"
	"os"
	"os/exec"
)

// interruptSignals: only os.Interrupt is portable.
var interruptSignals = []os.Signal{os.Interrupt}

// ownProcessGroup is a no-op where there are no unix process groups.
func ownProcessGroup(*exec.Cmd) {}

// killAgent kills the process corral started; agentWaitDelay still releases
// the pipes a surviving child holds. An already-finished process is not an
// error.
func killAgent(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
