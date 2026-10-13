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
// corral's own interrupt handling (auditContext) treats as an abort. SIGHUP
// is deliberately absent: corral does not abort on it, so catching it here
// would leave a run going with every agent seat killed.
var interruptSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}

// ownProcessGroup starts cmd as the leader of a new process group, so a kill
// reaches everything a shell-wrapped agent started.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// sysKill is syscall.Kill; a variable so a test can produce the answers a
// platform gives that this one does not (darwin's EPERM).
var sysKill = syscall.Kill

// killGroup kills the process group led by pid.
//
// ESRCH — the group is gone — is not an error: a timeout firing as the agent
// finishes must not turn a clean exit into a failure (as
// internal/sandbox/proc_unix.go).
//
// EPERM is treated the same way, which is safe for this reason: every member
// of the group is a process corral started, as the same user, so permission
// cannot be what is missing for a LIVE ordinary member. Darwin answers EPERM
// for a group whose only member is an exited leader not yet reaped; a member
// that changed identity (a setuid program the agent ran) could not be killed
// by any retry either. The leader is signalled directly as well, so a live
// leader is never left behind on that answer.
func killGroup(pid int) error {
	err := sysKill(-pid, syscall.SIGKILL)
	if errors.Is(err, syscall.EPERM) {
		err = sysKill(pid, syscall.SIGKILL)
		if errors.Is(err, syscall.EPERM) {
			return nil
		}
	}
	if err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
