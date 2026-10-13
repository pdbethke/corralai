// SPDX-License-Identifier: Elastic-2.0

//go:build !unix

package agentbackend

import "os/exec"

// ownProcessGroup is a no-op where there are no unix process groups:
// cancellation kills the process corral started, and agentWaitDelay still
// releases the pipes a surviving child holds.
func ownProcessGroup(*exec.Cmd) {}
