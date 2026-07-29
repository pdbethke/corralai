// SPDX-License-Identifier: Elastic-2.0

package sandbox

import (
	"os/exec"
	"time"
)

// ProcessWaitDelay is how long a finished (or cancelled) command is given to
// let its I/O pipes drain before they are force-closed. It is a backstop, not
// a timeout: it only starts counting once the command itself is over.
const ProcessWaitDelay = 2 * time.Second

// GuardProcess applies the three guards that stop a command from outliving
// its own deadline, and MUST be applied to every os/exec command corral runs
// on someone else's code:
//
//   - its own process group, so a cancellation kills the command AND every
//     child it spawned rather than orphaning them;
//   - cmd.Cancel, so context cancellation actually signals that whole group
//     (the default cancel kills only the direct child);
//   - cmd.WaitDelay, so a grandchild that survives anyway cannot hold the
//     inherited write end of the output pipe open and block cmd.Wait
//     FOREVER — past the deadline, past the timeout, with no error.
//
// That last one is the non-obvious failure, and it does not need a hostile
// program to trigger it: an ordinary test suite that leaves a background
// worker running is enough (`sh -c "sleep 120 & echo hello; exit 0"` is the
// whole reproduction). A hang is neither fail-closed nor honest — the caller
// gets no verdict at all — so this is factored into one exported helper
// rather than re-derived per call site. sandbox.Run applies it; so does
// adequacy.WorkspaceRunner, which runs commands directly because the runner's
// own environment is the isolation boundary and so never passes through Run.
func GuardProcess(cmd *exec.Cmd) {
	// Process-group semantics are Unix-only; the Windows build uses a job
	// object to the same end — see proc_unix.go / proc_windows.go.
	setProcGroup(cmd)
	cmd.Cancel = func() error { return killProcGroup(cmd) }
	cmd.WaitDelay = ProcessWaitDelay
}
