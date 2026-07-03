// SPDX-License-Identifier: Elastic-2.0

//go:build windows

package sandbox

import "os/exec"

// Windows has no Unix process groups. Narrate-mode agents never exec anything,
// and there is no isolation backend on Windows anyway (bwrap is Linux), so
// these are best-effort: kill the direct process and let cmd.WaitDelay
// force-close the pipes if a child lingers.

func setProcGroup(_ *exec.Cmd) {}

func killProcGroup(cmd *exec.Cmd) error {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	return nil
}
