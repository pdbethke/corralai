// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/agentbackend"
)

// TestAuditContextCancelsOnSignal pins the behavior the workspace substrate's
// restore depends on: a signal must CANCEL the run (so deferred restores get
// to run), not kill the process outright.
func TestAuditContextCancelsOnSignal(t *testing.T) {
	t.Cleanup(agentbackend.ResetInterrupted)
	var buf bytes.Buffer
	ctx, stop := auditContext(&buf)
	defer stop()

	if ctx.Err() != nil {
		t.Fatalf("context already done before any signal: %v", ctx.Err())
	}

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Skipf("cannot signal self: %v", err)
	}

	select {
	case <-ctx.Done():
		if !isCanceled(ctx) {
			t.Fatalf("context ended with %v, want context.Canceled", ctx.Err())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("signal did not cancel the audit context — an interrupted run would strand a mutant")
	}
}

func isCanceled(ctx context.Context) bool { return ctx.Err() == context.Canceled }

// stop() must be safe to call twice: the goroutine calls it on the first
// signal and the caller calls it via defer.
func TestAuditContextStopIsIdempotent(t *testing.T) {
	var buf bytes.Buffer
	_, stop := auditContext(&buf)
	stop()
	stop()
}

// TestAuditContextSilentOnCleanShutdown pins the defect the recording tests
// caught: signal.NotifyContext's context is canceled by its own stop function
// as well as by a signal, so a normal `defer stop()` announced "interrupted"
// at the end of every SUCCESSFUL run. A clean shutdown must say nothing.
func TestAuditContextSilentOnCleanShutdown(t *testing.T) {
	var buf bytes.Buffer
	ctx, stop := auditContext(&buf)
	stop() // the ordinary end-of-run path

	<-ctx.Done() // cancellation still happens; it just is not an interrupt
	time.Sleep(50 * time.Millisecond)

	if buf.Len() != 0 {
		t.Fatalf("clean shutdown wrote %q, want silence", buf.String())
	}
}

// A signal auditContext catches is the operator interrupting the run, whether
// or not an agent seat was live at that moment: the one record of that fact
// (agentbackend.WasInterrupted) must say so, so an API-seat run is refused
// its record exactly as an agent-seat run is.
func TestAuditContextRecordsTheInterrupt(t *testing.T) {
	t.Cleanup(agentbackend.ResetInterrupted)
	var buf bytes.Buffer
	ctx, stop := auditContext(&buf)
	defer stop()
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Skipf("cannot signal self: %v", err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("no cancellation")
	}
	if !agentbackend.WasInterrupted() {
		t.Fatal("auditContext caught the operator's signal and did not record the interrupt")
	}
}

// A process started with SIGINT ignored must keep ignoring it: Notify on an
// ignored signal un-ignores it. Run in a child whose shell ignores SIGINT.
func TestAuditContextLeavesAnIgnoredSignalIgnored(t *testing.T) {
	if os.Getenv("CORRAL_TEST_IGNORED_CHILD") == "1" {
		if !signal.Ignored(os.Interrupt) {
			t.Fatal("precondition: SIGINT should be ignored in the child")
		}
		_, stop := auditContext(&bytes.Buffer{})
		defer stop()
		if !signal.Ignored(os.Interrupt) {
			t.Fatal("auditContext un-ignored SIGINT")
		}
		return
	}
	cmd := exec.Command("sh", "-c", `trap "" INT; exec "$0" -test.run='^TestAuditContextLeavesAnIgnoredSignalIgnored$' -test.v`, os.Args[0])
	cmd.Env = append(os.Environ(), "CORRAL_TEST_IGNORED_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS") {
		t.Fatalf("child: %v\n%s", err, out)
	}
}
