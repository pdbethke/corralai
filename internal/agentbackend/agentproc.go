// SPDX-License-Identifier: Elastic-2.0

package agentbackend

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"time"
)

// agentWaitDelay is how long Wait gives an agent's output pipes to close
// after its context is done. A shell-wrapped agent's grandchild can hold
// stdout open after the shell is gone; without a bound, a timed-out call
// would wait for that grandchild to finish on its own.
const agentWaitDelay = 5 * time.Second

// agentCommand is the one way corral builds an agent CLI's command — a call
// and its --version probe alike: bounded by ctx, its pipes released
// agentWaitDelay after ctx ends, and (on unix) run in its own process group
// so the timeout kills everything the agent started, not only the process
// corral started. Run it with runAgent, never cmd.Run.
func agentCommand(ctx context.Context, argv []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) // #nosec G204 -- the operator's own agent definition, as they defined it
	cmd.WaitDelay = agentWaitDelay
	ownProcessGroup(cmd)
	cmd.Cancel = func() error { return killAgent(cmd) }
	return cmd
}

// ErrInterrupted is a call stopped, or refused, because corral was
// interrupted (Ctrl-C, SIGTERM). Once it happens no further agent call is
// started in this process: the run is unwinding, and an agent started now
// would spend for nothing.
var ErrInterrupted = errors.New("corral was interrupted — the agent seat was stopped and no further agent call is started")

// The live-seat registry, and why the INTERRUPT lives here.
//
// An agent runs in its own process group so a timeout can kill everything it
// started. The cost is that it leaves the terminal's foreground group, so
// Ctrl-C no longer reaches it: corral would unwind (or die) and the CLI would
// keep spending until its own timeout — or, with corral gone, forever.
//
// So corral forwards the interrupt itself, from the one place every agent
// call passes through. The alternative — an exported KillLiveSeats called
// from cmd/corral's auditContext — was rejected because auditContext is not
// at every door: `review` and goal derivation run agent seats without it, and
// there the default Ctrl-C would kill corral and orphan the group. A rule at
// some doors is the defect this project keeps finding.
//
// The handler is registered ONLY while at least one agent is live. With none
// live, a signal keeps whatever meaning the rest of corral gives it (default
// exit, or auditContext's unwind), so nothing changes for a process that
// never runs an agent. While agents are live, the first signal kills every
// live group, refuses every later call (ErrInterrupted), and unregisters —
// so a second Ctrl-C has its normal meaning and cannot orphan anything,
// because nothing is left running.
var live = struct {
	sync.Mutex
	cmds        map[*exec.Cmd]struct{}
	sigc        chan os.Signal
	stop        chan struct{}
	interrupted bool
}{cmds: map[*exec.Cmd]struct{}{}}

// runAgent starts cmd as a live seat and waits for it. Refused with
// ErrInterrupted once corral has been interrupted; a call that an interrupt
// stops returns ErrInterrupted too.
func runAgent(cmd *exec.Cmd) error {
	live.Lock()
	if live.interrupted {
		live.Unlock()
		return ErrInterrupted
	}
	live.cmds[cmd] = struct{}{}
	if live.sigc == nil {
		live.sigc, live.stop = make(chan os.Signal, 1), make(chan struct{})
		signal.Notify(live.sigc, interruptSignals...)
		go watchInterrupt(live.sigc, live.stop)
	}
	live.Unlock()
	defer untrack(cmd)

	if err := cmd.Start(); err != nil {
		return err
	}
	// A signal that arrived between registering and Start found no process
	// to kill; this one must not outlive it.
	live.Lock()
	if live.interrupted {
		_ = killAgent(cmd)
	}
	live.Unlock()
	err := cmd.Wait()
	live.Lock()
	stopped := live.interrupted
	live.Unlock()
	if stopped {
		return ErrInterrupted
	}
	return err
}

// untrack removes cmd and, with no seat left live, gives the signals back.
func untrack(cmd *exec.Cmd) {
	live.Lock()
	defer live.Unlock()
	delete(live.cmds, cmd)
	if len(live.cmds) == 0 {
		releaseHandler()
	}
}

// releaseHandler unregisters the interrupt handler. Callers hold live.
func releaseHandler() {
	if live.sigc == nil {
		return
	}
	signal.Stop(live.sigc)
	close(live.stop)
	live.sigc, live.stop = nil, nil
}

// watchInterrupt waits for one interrupt while seats are live.
func watchInterrupt(sigc <-chan os.Signal, stop <-chan struct{}) {
	select {
	case <-sigc:
		interruptLiveSeats()
	case <-stop:
	}
}

// interruptLiveSeats kills every live agent's process group, marks the
// process interrupted and unregisters the handler.
func interruptLiveSeats() {
	live.Lock()
	defer live.Unlock()
	live.interrupted = true
	for cmd := range live.cmds {
		if cmd.Process != nil {
			_ = killAgent(cmd)
		}
	}
	releaseHandler()
}

// signalHandlerActive reports whether the interrupt handler is registered.
func signalHandlerActive() bool {
	live.Lock()
	defer live.Unlock()
	return live.sigc != nil
}

// resetInterruptForTest clears the interrupted state, which is otherwise
// permanent for the life of the process.
func resetInterruptForTest() {
	live.Lock()
	defer live.Unlock()
	live.interrupted = false
}
