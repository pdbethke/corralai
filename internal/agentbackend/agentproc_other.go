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

// killGroup kills the process corral started; agentWaitDelay still releases
// the pipes a surviving child holds. An already-finished process is not an
// error.
func killGroup(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}
	if err := p.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
