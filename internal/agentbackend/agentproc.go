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
	// Cancel runs on exec's own goroutine, only after Start succeeded, so
	// cmd.Process is set and not being written.
	cmd.Cancel = func() error { return killGroup(cmd.Process.Pid) }
	return cmd
}

// ErrInterrupted is a call stopped, or refused, because corral was
// interrupted (Ctrl-C, SIGTERM). Once it happens no further agent call is
// started in this process: the run is unwinding, and an agent started now
// would spend for nothing.
var ErrInterrupted = errors.New("corral was interrupted — the agent seat was stopped and no further agent call is started")

// Interrupted is THE test every caller applies before mapping an agent
// call's error to a recorded outcome (writer-failed, shadow-failed,
// derive-failed, "recorded unverified"): an interrupted call is none of
// those. The operator stopped the run, so it aborts — nothing recorded,
// nothing pushed.
func Interrupted(err error) bool { return errors.Is(err, ErrInterrupted) }

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
//
// A signal the process was started IGNORING is never registered
// (notifiableSignals): signal.Notify un-ignores it, which would make
// `nohup corral …` kill its agent seats when the session drops.
//
// THE INTERRUPTED STATE IS PERMANENT for the life of the process: every
// agent call after it is refused. That is right for a CLI run, which is
// unwinding. A long-lived process that runs agent seats (an MCP server, the
// ui) must call ResetInterrupted per run — today none does.
var live = struct {
	sync.Mutex
	pids        map[*exec.Cmd]int // 0 until Start returns; read only under the lock
	sigc        chan os.Signal
	stop        chan struct{}
	interrupted bool
}{pids: map[*exec.Cmd]int{}}

// NotifiableSignals are the signals that mean "the operator interrupted
// this run" (Ctrl-C, SIGTERM), less any the process was started IGNORING:
// signal.Notify un-ignores a signal, so registering an ignored one would
// override the operator's own choice. Every handler for an operator
// interrupt — this package's and cmd/corral's auditContext — registers
// exactly these. It may be empty; a caller must not pass an empty list to
// signal.Notify, which would register EVERY signal.
func NotifiableSignals() []os.Signal {
	var out []os.Signal
	for _, sig := range interruptSignals {
		if !signal.Ignored(sig) {
			out = append(out, sig)
		}
	}
	return out
}

// runAgent starts cmd as a live seat and waits for it. Refused with
// ErrInterrupted once corral has been interrupted; a call that an interrupt
// stops returns ErrInterrupted too.
func runAgent(cmd *exec.Cmd) error {
	live.Lock()
	if live.interrupted {
		live.Unlock()
		return ErrInterrupted
	}
	live.pids[cmd] = 0
	if live.sigc == nil {
		if sigs := NotifiableSignals(); len(sigs) > 0 {
			live.sigc, live.stop = make(chan os.Signal, 1), make(chan struct{})
			signal.Notify(live.sigc, sigs...)
			go watchInterrupt(live.sigc, live.stop)
		}
	}
	live.Unlock()
	defer untrack(cmd)

	if err := cmd.Start(); err != nil {
		return err
	}
	// The pid is published under the lock, so the interrupt path never
	// reads cmd.Process while Start may be writing it. A signal that arrived
	// before this found no pid to kill; this one must not outlive it.
	pid := cmd.Process.Pid
	live.Lock()
	live.pids[cmd] = pid
	if live.interrupted {
		_ = killGroup(pid)
	}
	live.Unlock()
	err := cmd.Wait()
	// Reaped: its pid may be reused from here, so the interrupt path must
	// not see it.
	live.Lock()
	live.pids[cmd] = 0
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
	delete(live.pids, cmd)
	if len(live.pids) == 0 {
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

// watchInterrupt waits for one interrupt while seats are live. When the last
// seat finishes, a signal may already sit in sigc and select may pick stop
// anyway; it is drained and acted on, because the operator did press it.
// With no seat live, acting means marking the run interrupted, so the next
// agent call is refused and the verb aborts.
func watchInterrupt(sigc <-chan os.Signal, stop <-chan struct{}) {
	select {
	case <-sigc:
		interruptLiveSeats()
	case <-stop:
		select {
		case <-sigc:
			interruptLiveSeats()
		default:
		}
	}
}

// interruptLiveSeats kills every live agent's process group, marks the
// process interrupted and unregisters the handler. It reads only the pids
// published under the lock, never cmd.Process.
func interruptLiveSeats() {
	live.Lock()
	defer live.Unlock()
	live.interrupted = true
	for _, pid := range live.pids {
		if pid != 0 {
			_ = killGroup(pid)
		}
	}
	releaseHandler()
}

// Interrupt records that the operator interrupted this run and stops every
// live agent seat. cmd/corral's auditContext calls it when IT receives the
// signal, so the record of an interrupt does not depend on an agent having
// been live at the moment the signal landed: an API-seat run is interrupted
// in exactly the same sense.
func Interrupt() { interruptLiveSeats() }

// WasInterrupted reports whether the operator interrupted this run — by a
// signal this package caught while a seat was live, or one auditContext
// caught and passed on through Interrupt. It is the half of "the operator
// interrupted this run" that does not depend on an error reaching the
// caller: a signal can land after the last agent call returned, and a write
// about to record the run must still see it.
func WasInterrupted() bool {
	live.Lock()
	defer live.Unlock()
	return live.interrupted
}

// signalHandlerActive reports whether the interrupt handler is registered.
func signalHandlerActive() bool {
	live.Lock()
	defer live.Unlock()
	return live.sigc != nil
}

// ResetInterrupted clears the interrupted state, which is otherwise permanent
// for the life of the process (see live). A process that runs more than one
// run — tests, and any future long-lived server that seats agents — resets it
// between runs.
func ResetInterrupted() {
	live.Lock()
	defer live.Unlock()
	live.interrupted = false
}
