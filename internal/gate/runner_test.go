// SPDX-License-Identifier: Elastic-2.0

package gate

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// fakeCheckouter is a Checkouter test double. When err is set, CheckoutPR
// fails (simulating a checkout error) and CheckoutPR is never expected to
// leave the workspace in a runnable state.
type fakeCheckouter struct {
	err   error
	calls int
}

func (f *fakeCheckouter) CheckoutPR(ctx context.Context, repoURL string, pr int, sha, destDir string) error {
	f.calls++
	return f.err
}

// fakeJail is a Jail test double returning a fixed exit code/output/error.
// It also captures the timeout it was called with, so tests can assert the
// runner computed the right effective timeout from the policy.
type fakeJail struct {
	exitCode int
	output   string
	err      error
	calls    int

	lastTimeout time.Duration
}

func (f *fakeJail) Run(ctx context.Context, command, workspace string, network bool, timeout time.Duration) (int, string, error) {
	f.calls++
	f.lastTimeout = timeout
	return f.exitCode, f.output, f.err
}

// fakeCertifier is a Certifier test double.
type fakeCertifier struct {
	recordID int64
	head     string
	err      error
	calls    int
}

func (f *fakeCertifier) Certify(ctx context.Context, repo, commit, command string, exitCode int, outputDigest string) (int64, string, error) {
	f.calls++
	return f.recordID, f.head, f.err
}

// fakeStatusPoster is a StatusPoster test double that records every posted
// state, so tests can assert "success" was never among them.
type fakeStatusPoster struct {
	states []string
	descs  []string
	err    error
}

func (f *fakeStatusPoster) SetCommitStatus(ctx context.Context, repoURL, sha, context, state, targetURL, description string) error {
	f.states = append(f.states, state)
	f.descs = append(f.descs, description)
	return f.err
}

func testPolicy() Policy {
	return Policy{Repo: "o/r", Base: []string{"main"}, Context: "corral/gate", CheckCmd: []string{"go", "test", "./..."}, AllowNet: false}
}

func testPR() PRRef {
	return PRRef{Number: 7, HeadSHA: "deadbeef", HeadRef: "corralai/m1", Base: "main"}
}

func newTestRunner(t *testing.T, checkout *fakeCheckouter, jail *fakeJail, cert *fakeCertifier, status *fakeStatusPoster) *Runner {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "gate.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &Runner{
		Checkout:  checkout,
		Jail:      jail,
		Certify:   cert,
		Status:    status,
		Store:     store,
		RecordURL: func(repo, sha string) string { return "http://x/api/gate/run?repo=" + repo + "&sha=" + sha },
		Now:       func() time.Time { return time.Unix(1000, 0) },
	}
}

// TestRunnerPassPostsSuccessAndStores: a checkout that succeeds, a jail run
// that exits 0, and a certifier that signs cleanly must result in a
// "success" commit status and a stored Run with Passed=true.
func TestRunnerPassPostsSuccessAndStores(t *testing.T) {
	checkout := &fakeCheckouter{}
	jail := &fakeJail{exitCode: 0, output: "ok"}
	cert := &fakeCertifier{recordID: 42, head: "chain-head"}
	status := &fakeStatusPoster{}
	r := newTestRunner(t, checkout, jail, cert, status)

	if err := r.Run(context.Background(), "https://github.com/o/r", testPolicy(), testPR()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if checkout.calls != 1 || jail.calls != 1 || cert.calls != 1 {
		t.Fatalf("expected each dep called once: checkout=%d jail=%d cert=%d", checkout.calls, jail.calls, cert.calls)
	}
	if len(status.states) == 0 || status.states[len(status.states)-1] != "success" {
		t.Fatalf("expected final status 'success', got %v", status.states)
	}

	run, ok, err := r.Store.GetBySHA("o/r", "deadbeef")
	if err != nil || !ok {
		t.Fatalf("expected stored run: ok=%v err=%v", ok, err)
	}
	if !run.Passed || run.RecordID != 42 {
		t.Fatalf("stored run wrong: %+v", run)
	}
}

// TestRunnerFailPostsFailure: a jail run that exits nonzero (but otherwise
// runs to completion, including a successful sign) must post "failure" and
// store Passed=false — never "success".
func TestRunnerFailPostsFailure(t *testing.T) {
	checkout := &fakeCheckouter{}
	jail := &fakeJail{exitCode: 1, output: "check failed"}
	cert := &fakeCertifier{recordID: 99, head: "chain-head"}
	status := &fakeStatusPoster{}
	r := newTestRunner(t, checkout, jail, cert, status)

	if err := r.Run(context.Background(), "https://github.com/o/r", testPolicy(), testPR()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(status.states) == 0 || status.states[len(status.states)-1] != "failure" {
		t.Fatalf("expected final status 'failure', got %v", status.states)
	}
	for _, s := range status.states {
		if s == "success" {
			t.Fatalf("success must never be posted on a failing check, got states %v", status.states)
		}
	}

	run, ok, err := r.Store.GetBySHA("o/r", "deadbeef")
	if err != nil || !ok {
		t.Fatalf("expected stored run: ok=%v err=%v", ok, err)
	}
	if run.Passed {
		t.Fatalf("expected Passed=false, got %+v", run)
	}
}

// TestRunnerCheckoutErrorNeverPostsSuccess is the named fail-closed test:
// when CheckoutPR errors (untrusted PR code was never even reached), the
// runner must NEVER post "success" — only "failure" or "error" — and must
// never store a passed=true row. This also implicitly proves the jail is
// never invoked when checkout fails (fail-closed short-circuit).
func TestRunnerCheckoutErrorNeverPostsSuccess(t *testing.T) {
	checkout := &fakeCheckouter{err: errors.New("boom: checkout failed")}
	jail := &fakeJail{exitCode: 0, output: "should never run"}
	cert := &fakeCertifier{recordID: 1, head: "chain-head"}
	status := &fakeStatusPoster{}
	r := newTestRunner(t, checkout, jail, cert, status)

	if err := r.Run(context.Background(), "https://github.com/o/r", testPolicy(), testPR()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, s := range status.states {
		if s == "success" {
			t.Fatalf("FAIL-CLOSED VIOLATION: success posted on a checkout error, states=%v", status.states)
		}
	}
	final := status.states[len(status.states)-1]
	if final != "failure" && final != "error" {
		t.Fatalf("expected final status in {failure,error}, got %q (all: %v)", final, status.states)
	}

	if jail.calls != 0 {
		t.Fatalf("jail must never run when checkout fails, but was called %d times", jail.calls)
	}
	if cert.calls != 0 {
		t.Fatalf("certifier must never run when checkout fails, but was called %d times", cert.calls)
	}

	run, ok, err := r.Store.GetBySHA("o/r", "deadbeef")
	if err != nil || !ok {
		t.Fatalf("expected a stored (failed) run: ok=%v err=%v", ok, err)
	}
	if run.Passed {
		t.Fatalf("FAIL-CLOSED VIOLATION: stored Passed=true on a checkout error: %+v", run)
	}
}

// TestRunnerPassesPolicyTimeoutToJail is the fix-#1 regression: a policy
// declaring TimeoutS must reach the jail as that many seconds — not the
// sandbox package's hardcoded 60s default, which times out any real test
// suite and permanently blocks merge.
func TestRunnerPassesPolicyTimeoutToJail(t *testing.T) {
	checkout := &fakeCheckouter{}
	jail := &fakeJail{exitCode: 0}
	cert := &fakeCertifier{recordID: 1}
	status := &fakeStatusPoster{}
	r := newTestRunner(t, checkout, jail, cert, status)

	pol := testPolicy()
	pol.TimeoutS = 5
	if err := r.Run(context.Background(), "https://github.com/o/r", pol, testPR()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if jail.lastTimeout != 5*time.Second {
		t.Fatalf("jail timeout = %v, want 5s", jail.lastTimeout)
	}
}

// TestRunnerUsesDefaultTimeoutWhenPolicyTimeoutSUnset: a policy with
// TimeoutS left at its zero value must fall back to DefaultGateTimeout, not
// 0 (which would mean "no time at all") and not the sandbox package's own
// default (which the runner must never rely on implicitly).
func TestRunnerUsesDefaultTimeoutWhenPolicyTimeoutSUnset(t *testing.T) {
	checkout := &fakeCheckouter{}
	jail := &fakeJail{exitCode: 0}
	cert := &fakeCertifier{recordID: 1}
	status := &fakeStatusPoster{}
	r := newTestRunner(t, checkout, jail, cert, status)

	pol := testPolicy() // TimeoutS is 0 (unset)
	if err := r.Run(context.Background(), "https://github.com/o/r", pol, testPR()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if jail.lastTimeout != DefaultGateTimeout {
		t.Fatalf("jail timeout = %v, want default %v", jail.lastTimeout, DefaultGateTimeout)
	}
}
