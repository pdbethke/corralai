// SPDX-License-Identifier: Elastic-2.0

package agentbackend

import (
	"context"
	"os/exec"
	"time"
)

// agentWaitDelay is how long Wait gives an agent's output pipes to close
// after its context is done. A shell-wrapped agent's grandchild can hold
// stdout open after the shell is gone; without a bound, a timed-out call
// would wait for that grandchild to finish on its own.
const agentWaitDelay = 5 * time.Second

// agentCommand is the one way corral starts an agent CLI — a call and its
// --version probe alike: bounded by ctx, its pipes released agentWaitDelay
// after ctx ends, and (on unix) run in its own process group so the timeout
// kills everything the agent started, not only the process corral started.
func agentCommand(ctx context.Context, argv []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) // #nosec G204 -- the operator's own agent definition, as they defined it
	cmd.WaitDelay = agentWaitDelay
	ownProcessGroup(cmd)
	return cmd
}
