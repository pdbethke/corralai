// SPDX-License-Identifier: Elastic-2.0

package sandbox

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRunCapturesOutputAndExit(t *testing.T) {
	r := Run(context.Background(), "echo hello world", Options{Workspace: t.TempDir(), Backend: noneIsolator{}})
	if r.ExitCode != 0 {
		t.Fatalf("exit = %d, want 0 (%s)", r.ExitCode, r.Err)
	}
	if !strings.Contains(r.Output, "hello world") {
		t.Fatalf("output = %q, want it to contain 'hello world'", r.Output)
	}
}

func TestRunNonzeroExit(t *testing.T) {
	r := Run(context.Background(), "exit 3", Options{Workspace: t.TempDir(), Backend: noneIsolator{}})
	if r.ExitCode != 3 {
		t.Fatalf("exit = %d, want 3", r.ExitCode)
	}
}

func TestRunTimesOut(t *testing.T) {
	start := time.Now()
	r := Run(context.Background(), "sleep 10", Options{Workspace: t.TempDir(), Timeout: 300 * time.Millisecond, Backend: noneIsolator{}})
	if !r.TimedOut {
		t.Fatalf("expected TimedOut, got %+v", r)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("timeout did not kill the process promptly")
	}
}

func TestRunCwdIsWorkspace(t *testing.T) {
	ws := t.TempDir()
	r := Run(context.Background(), "pwd", Options{Workspace: ws, Backend: noneIsolator{}})
	// macOS /tmp symlinks to /private/tmp; just require the basename matches.
	if !strings.Contains(r.Output, ws) && !strings.HasSuffix(strings.TrimSpace(r.Output), lastSeg(ws)) {
		t.Fatalf("pwd = %q, want the workspace %q", r.Output, ws)
	}
}

func TestRunEnvIsSecretFree(t *testing.T) {
	t.Setenv("CORRAL_TOKEN", "super-secret")
	// Default env is MinimalEnv(), which must not carry CORRAL_TOKEN.
	r := Run(context.Background(), "echo TOKEN=[$CORRAL_TOKEN]", Options{Workspace: t.TempDir(), Backend: noneIsolator{}})
	if strings.Contains(r.Output, "super-secret") {
		t.Fatalf("executed command saw a parent secret: %q", r.Output)
	}
	if !strings.Contains(r.Output, "TOKEN=[]") {
		t.Fatalf("expected the secret to be empty in the command env, got %q", r.Output)
	}
}

func TestRunOutputCap(t *testing.T) {
	r := Run(context.Background(), "yes x | head -c 100000", Options{Workspace: t.TempDir(), MaxOutput: 1000, Backend: noneIsolator{}})
	if len(r.Output) > 1200 { // cap + truncation note
		t.Fatalf("output not capped: %d bytes", len(r.Output))
	}
	if !strings.Contains(r.Output, "truncated") {
		t.Fatalf("expected a truncation note, got %d bytes", len(r.Output))
	}
}

func TestRunNilBackendDisabled(t *testing.T) {
	r := Run(context.Background(), "echo hi", Options{Workspace: t.TempDir()})
	if r.Err == "" || r.ExitCode != -1 {
		t.Fatalf("nil backend must be disabled, got %+v", r)
	}
}

// TestRunGuardedNilBackendErrors: RunGuarded is THE single home of the "a
// failed run must not read as success" invariant. A nil backend produces
// Result.Err (via Run), and RunGuarded must surface that as a non-nil error
// rather than letting a caller mistake it for a clean pass.
func TestRunGuardedNilBackendErrors(t *testing.T) {
	res, err := RunGuarded(context.Background(), "echo hi", Options{Workspace: t.TempDir()})
	if err == nil {
		t.Fatalf("expected a non-nil error for a nil backend, got nil (res=%+v)", res)
	}
	// The Result must still be returned (ExitCode passthrough) even on error.
	if res.ExitCode != -1 {
		t.Fatalf("ExitCode = %d, want -1 passed through from Run", res.ExitCode)
	}
}

// TestRunGuardedTimeoutErrors mirrors TestRunTimesOut: a timed-out run must
// come back as a non-nil error from RunGuarded, never as (res, nil).
func TestRunGuardedTimeoutErrors(t *testing.T) {
	res, err := RunGuarded(context.Background(), "sleep 10", Options{Workspace: t.TempDir(), Timeout: 300 * time.Millisecond, Backend: noneIsolator{}})
	if err == nil {
		t.Fatalf("expected a non-nil error for a timed-out run, got nil (res=%+v)", res)
	}
	if !res.TimedOut {
		t.Fatalf("expected the returned Result to still report TimedOut, got %+v", res)
	}
}

// TestRunGuardedCleanExitSucceeds: a genuine exit-0 run must come back as
// (res, nil) with the real ExitCode/Output — RunGuarded must not itself
// introduce a false negative on a clean pass.
func TestRunGuardedCleanExitSucceeds(t *testing.T) {
	res, err := RunGuarded(context.Background(), "echo hello world", Options{Workspace: t.TempDir(), Backend: noneIsolator{}})
	if err != nil {
		t.Fatalf("unexpected error on a clean exit-0 run: %v (res=%+v)", err, res)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (%s)", res.ExitCode, res.Err)
	}
	if !strings.Contains(res.Output, "hello world") {
		t.Fatalf("output = %q, want it to contain 'hello world'", res.Output)
	}
}

// TestRunGuardedNonzeroExitIsNotAnError: a genuine nonzero exit is a
// completed run, not a "could not complete cleanly" failure — RunGuarded
// must pass it through as (res, nil), matching Run's own semantics.
func TestRunGuardedNonzeroExitIsNotAnError(t *testing.T) {
	res, err := RunGuarded(context.Background(), "exit 3", Options{Workspace: t.TempDir(), Backend: noneIsolator{}})
	if err != nil {
		t.Fatalf("unexpected error on a genuine nonzero exit: %v", err)
	}
	if res.ExitCode != 3 {
		t.Fatalf("ExitCode = %d, want 3", res.ExitCode)
	}
}

func lastSeg(p string) string {
	parts := strings.Split(strings.TrimRight(p, "/"), "/")
	return parts[len(parts)-1]
}

// TestMinimalEnvHasNoGitToken is a credential-boundary guard: MinimalEnv() is the
// secret-free allowlist passed into sandboxed bee commands. It must never carry
// CORRALAI_GIT_TOKEN — the token is owned exclusively by cmd/corral and lives only
// in repo.Engine. This test passes immediately (MinimalEnv only allowlists
// PATH/HOME/LANG/LC_ALL/TMPDIR); it LOCKS the boundary so a future MinimalEnv
// change can't silently leak the token.
func TestMinimalEnvHasNoGitToken(t *testing.T) {
	t.Setenv("CORRALAI_GIT_TOKEN", "supersecret")
	for _, kv := range MinimalEnv() {
		if strings.Contains(kv, "CORRALAI_GIT_TOKEN") || strings.Contains(kv, "supersecret") {
			t.Fatalf("the secret-free jail env must never carry the git token: %q", kv)
		}
	}
}

// TestCancelledRunNeverReadsAsAFailingTest pins the fix for the defect that a
// Ctrl-C inflated the kill rate.
//
// THE CHAIN, which is why this lives in the sandbox package but is really
// about the scorer: cmd.Cancel SIGKILLs, so a cancelled run's ExitCode is -1.
// Before the fix, only context.DeadlineExceeded was classified, so a
// cancellation left Err empty and TimedOut false; -1 also slipped the
// `runErr != nil && res.ExitCode == 0` guard. RunGuarded then returned a nil
// error, bwrapJail.RunTest reported passed=false, and adequacy.Score recorded
// `killed: !passed` — every mutant still running when the operator pressed
// Ctrl-C was counted as CAUGHT.
//
// The assertion that matters is `err != nil`: it is what routes Score to its
// unmeasured-outcome path instead of its kill path.
func TestCancelledRunNeverReadsAsAFailingTest(t *testing.T) {
	opts := Options{Backend: noneIsolator{}, Workspace: t.TempDir(), Timeout: 30 * time.Second}

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(150 * time.Millisecond); cancel() }()
	res, err := RunGuarded(ctx, "sleep 10", opts)

	if err == nil {
		t.Fatalf("a cancelled run returned a nil error: RunTest would report passed=false and Score would record it as a KILLED mutant, crediting the suite with a divergence it never detected (got %+v)", res)
	}
	if res.Err == "" {
		t.Errorf("Result.Err is empty on a cancelled run — RunGuarded's invariant rests on it being set")
	}
	if res.TimedOut {
		t.Errorf("TimedOut is true on a CANCELLED run: that sends Score into the compliant-baseline re-probe, which exists to tell a slow box from a non-terminating mutant. Neither applies — the operator stopped the run")
	}
}

// TestDeadlineStillClassifiesAsTimeout is the other half: widening the check to
// every context error must not turn a genuine deadline into an ordinary
// cancellation, or Score loses the baseline re-probe that distinguishes a
// non-terminating mutant (a real kill) from a loaded machine (unmeasured).
func TestDeadlineStillClassifiesAsTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	res, err := RunGuarded(ctx, "sleep 10", Options{Backend: noneIsolator{}, Workspace: t.TempDir(), Timeout: 30 * time.Second})

	if err == nil {
		t.Fatalf("a timed-out run returned a nil error (got %+v)", res)
	}
	if !res.TimedOut {
		t.Errorf("TimedOut is false on a deadline-exceeded run — Score would take the plain-error path and never run the baseline re-probe, so a genuinely non-terminating mutant would go unrecorded as a kill")
	}
}
