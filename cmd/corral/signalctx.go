// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
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
// The SECOND signal is deliberately left to the default behavior — the handler
// is unregistered as soon as the first arrives — so an operator whose restore
// is itself wedged can still abandon the process. Trapping every signal would
// replace a stranded mutant with an unkillable run.
func auditContext(stderr io.Writer) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM)

	done := make(chan struct{})
	go func() {
		select {
		case <-sigc:
			// Restore default handling first: from here a second Ctrl-C
			// terminates the process outright.
			signal.Stop(sigc)
			fmt.Fprintln(stderr, "\ncorral: interrupted — finishing the current step so the tree is restored; press Ctrl-C again to abandon it")
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
