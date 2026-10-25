// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"

	"github.com/pdbethke/corralai/internal/agentbackend"
)

// auditContext returns a context canceled on SIGINT/SIGTERM, so an interrupted
// audit unwinds through its deferred restores instead of dying mid-mutation.
//
// WHY THIS EXISTS. On the `workspace` substrate corral overlays mutants onto a
// REAL checkout and restores it from a deferred ledger. That defer covers a
// failing command, a timeout and a panic — each asserted by its own test — but
// a signal kills the process without running deferred functions at all. Ctrl-C
// is exactly how a human stops a long audit, so the one recovery path the
// design relies on was missing for the most likely interruption.
//
// Cancelation (not exit) is the mechanism: the context reaches the running test
// command, that command is killed, RunTest returns, and the deferred restore
// runs on the way out. A handler that called os.Exit would defeat the very
// defers it exists to protect.
//
// NOT signal.NotifyContext: that context is canceled by its stop function as
// well as by a signal, so the caller's ordinary `defer stop()` on a SUCCESSFUL
// run is indistinguishable from an interrupt — which announced "interrupted" at
// the end of every clean run, and was caught by the recording tests. A signal
// gets its own channel so the two causes stay distinguishable.
//
// Agent seats are not this handler's job: each runs in its own process group,
// out of the terminal's reach, and internal/agentbackend kills every live
// seat's group on the same signal (agentproc.go), in every verb — including
// the ones that never call auditContext.
//
// The SECOND signal is deliberately left to the default behavior — the handler
// is unregistered as soon as the first arrives — so an operator whose restore
// is itself wedged can still abandon the process. Trapping every signal would
// replace a stranded mutant with an unkillable run.
func auditContext(stderr io.Writer) (context.Context, context.CancelFunc) {
	return interruptContext(stderr, "\ncorral: interrupted — finishing the current step so the tree is restored; press Ctrl-C again to abandon it")
}

// setupContext is auditContext for `corral setup`, which has no tree to
// restore: an interrupt there means "start no new step". Apply checks ctx
// before every step, an agent CLI already running is killed through its
// context, and a file write in progress is a temp-file rename that either
// lands whole or not at all. The read-only re-detect still runs, so the result
// lines say what was done.
func setupContext(stderr io.Writer) (context.Context, context.CancelFunc) {
	return interruptContext(stderr, "\ncorral setup: interrupted — nothing more will be written: no new step starts and a running agent command is stopped. setup re-reads each agent so the lines below say what was done; press Ctrl-C again to exit at once")
}

// interruptContext is the mechanism behind auditContext and setupContext; msg
// is what the person is told when the first signal arrives.
func interruptContext(stderr io.Writer, msg string) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())

	sigc := make(chan os.Signal, 1)
	// The operator-interrupt signals, less any the process was started
	// ignoring (Notify would un-ignore them) — the same list the agent seats'
	// forwarding registers. An empty list must not reach Notify, which would
	// then register every signal.
	if sigs := agentbackend.NotifiableSignals(); len(sigs) > 0 {
		signal.Notify(sigc, sigs...)
	}

	done := make(chan struct{})
	go func() {
		select {
		case <-sigc:
			// Restore default handling first: from here a second Ctrl-C
			// terminates the process outright.
			signal.Stop(sigc)
			fmt.Fprintln(stderr, msg)
			// Recorded where every verb's "was this run interrupted" check
			// reads it (operatorInterrupted), and every live agent seat is
			// stopped: a run interrupted while only API seats were in
			// flight is interrupted in exactly the same sense.
			agentbackend.Interrupt()
			cancel()
		case <-done:
		}
	}()

	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			signal.Stop(sigc)
			close(done)
		})
		cancel()
	}
}
