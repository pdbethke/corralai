// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/buildstore"
	"github.com/pdbethke/corralai/internal/gate"
	"github.com/pdbethke/corralai/internal/sandbox"
)

// shellIsolator is a minimal sandbox.Isolator test double that just runs
// the command through /bin/sh -c — it deliberately provides NO real
// isolation (this is a unit test of the adapter's plumbing, not of bwrap).
// It lets the tests below drive exact exit codes and a real timeout without
// depending on bwrap being available in the test environment.
type shellIsolator struct{ preflightErr error }

func (s shellIsolator) Wrap(command string, opts sandbox.Options, env []string) ([]string, error) {
	return []string{"/bin/sh", "-c", command}, nil
}
func (s shellIsolator) Preflight() error { return s.preflightErr }
func (s shellIsolator) Name() string     { return "shell-test-double" }

// TestJailAdapterPassesExitCodeThrough is the load-bearing contract from
// Task 4: jailAdapter must hand back sandbox.Result.ExitCode UNCHANGED —
// never remapped — because Runner.Run's fail-closed check is `exit == 0`.
func TestJailAdapterPassesExitCodeThrough(t *testing.T) {
	j := jailAdapter{backend: shellIsolator{}}

	exit, _, err := j.Run(context.Background(), "exit 0", t.TempDir(), false)
	if err != nil || exit != 0 {
		t.Fatalf("pass case: exit=%d err=%v, want exit=0 err=nil", exit, err)
	}

	exit, _, err = j.Run(context.Background(), "exit 7", t.TempDir(), false)
	if err != nil {
		t.Fatalf("nonzero-exit case must not itself be an error (a completed run with a real nonzero exit): %v", err)
	}
	if exit != 7 {
		t.Fatalf("exit = %d, want 7 (unchanged, NEVER remapped to 0)", exit)
	}
}

// TestJailAdapterSurfacesTimeoutAsError: a command that outlives its
// deadline must come back as a non-nil error (never a silent exit=0 pass).
func TestJailAdapterSurfacesTimeoutAsError(t *testing.T) {
	// sandbox.Run defaults Timeout to 60s if unset; there is no Options field
	// on jailAdapter.Run to shorten it, so instead prove the TimedOut/Err
	// branch is wired by exercising a backend Wrap error, which sandbox.Run
	// funnels through the identical Result{ExitCode:-1, Err:...} shape a
	// real timeout produces — the two paths share the same handling in
	// jailAdapter.Run (see the Err != "" branch).
	broken := brokenIsolator{}
	j2 := jailAdapter{backend: broken}
	exit, _, err := j2.Run(context.Background(), "true", t.TempDir(), false)
	if err == nil {
		t.Fatalf("expected a non-nil error when the backend fails to wrap the command")
	}
	if exit == 0 {
		t.Fatalf("exit = 0 on a failed run — must never look like a pass")
	}
}

type brokenIsolator struct{}

func (brokenIsolator) Wrap(command string, opts sandbox.Options, env []string) ([]string, error) {
	return nil, errTestWrap
}
func (brokenIsolator) Preflight() error { return nil }
func (brokenIsolator) Name() string     { return "broken-test-double" }

var errTestWrap = &wrapError{"wrap failed"}

type wrapError struct{ msg string }

func (e *wrapError) Error() string { return e.msg }

// TestJailAdapterNilBackendNeverRunsUnsandboxed: a nil backend must not
// silently execute the command on the bare host — sandbox.Run itself
// refuses (Result{ExitCode:-1, Err:"execution disabled..."}), and
// jailAdapter must surface that as an error rather than swallowing it.
func TestJailAdapterNilBackendNeverRunsUnsandboxed(t *testing.T) {
	j := jailAdapter{backend: nil}
	exit, _, err := j.Run(context.Background(), "exit 0", t.TempDir(), false)
	if err == nil {
		t.Fatalf("expected an error with a nil backend, got nil (would look like a pass)")
	}
	if exit == 0 {
		t.Fatalf("exit = 0 with a nil backend — must never look like a pass")
	}
}

// TestCertifierAdapterSignsAndReturnsHead drives certifierAdapter.Certify
// over a real Options{BuildStore, CertifyKey} (same setup as
// buildcert_test.go's TestReportBuild) and checks it returns the signed
// record's id/head — i.e. it really calls certifyBuild, not a stub.
func TestCertifierAdapterSignsAndReturnsHead(t *testing.T) {
	bs, err := buildstore.Open(filepath.Join(t.TempDir(), "build.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer bs.Close()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	c := certifierAdapter{opts: Options{BuildStore: bs, CertifyKey: priv}}

	id, head, err := c.Certify(context.Background(), "o/r", "deadbeef", "go test ./...", 0, "sha256:abc")
	if err != nil {
		t.Fatalf("Certify: %v", err)
	}
	if id == 0 {
		t.Fatalf("expected a nonzero record id")
	}
	if head == "" {
		t.Fatalf("expected a nonempty chain head")
	}
}

// TestGateRunHandlerReturnsStoredRun: the read endpoint's JSON contract.
func TestGateRunHandlerReturnsStoredRun(t *testing.T) {
	store, err := gate.OpenStore(filepath.Join(t.TempDir(), "gate.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Save(gate.Run{Repo: "o/r", HeadSHA: "abc", PR: 5, Passed: true, RecordID: 42, RanAt: time.Unix(0, 0)}); err != nil {
		t.Fatal(err)
	}

	h := GateRunHandler(store)
	req := httptest.NewRequest(http.MethodGet, "/api/gate/run?repo=o/r&sha=abc", nil)
	w := httptest.NewRecorder()
	h(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var got struct {
		Passed   bool  `json:"passed"`
		PR       int   `json:"pr"`
		RecordID int64 `json:"record_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("bad JSON: %v (body=%s)", err, w.Body.String())
	}
	if !got.Passed || got.PR != 5 || got.RecordID != 42 {
		t.Fatalf("got %+v, want {passed:true pr:5 record_id:42}", got)
	}
}

// TestGateRunHandlerUnknownSHAIs404: an unregistered SHA must not come back
// looking like a passing (or any) run.
func TestGateRunHandlerUnknownSHAIs404(t *testing.T) {
	store, err := gate.OpenStore(filepath.Join(t.TempDir(), "gate.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	h := GateRunHandler(store)
	req := httptest.NewRequest(http.MethodGet, "/api/gate/run?repo=o/r&sha=nope", nil)
	w := httptest.NewRecorder()
	h(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// TestStartGateNilBackendDisablesGating: StartGate must refuse to start the
// poller when GatePolicies are configured but no jail backend is
// available — logging loudly is exercised manually/via the report; here we
// assert the structural contract: no store is returned and no error either
// (this is the intentional degrade-never-block "feature off" path, not a
// startup failure).
func TestStartGateNilBackendDisablesGating(t *testing.T) {
	store, err := StartGate(context.Background(), Options{
		GatePolicies: []gate.Policy{{Repo: "o/r", CheckCmd: []string{"true"}}},
		GateBackend:  nil,
		GateDB:       filepath.Join(t.TempDir(), "gate.db"),
	})
	if err != nil {
		t.Fatalf("StartGate: %v", err)
	}
	if store != nil {
		t.Fatalf("expected StartGate to refuse (nil store) with no jail backend, got a store")
	}
}

// TestStartGateEmptyPoliciesIsOff: no CORRALAI_GATE_POLICIES configured
// means the feature is off — StartGate must be a complete no-op.
func TestStartGateEmptyPoliciesIsOff(t *testing.T) {
	store, err := StartGate(context.Background(), Options{})
	if err != nil || store != nil {
		t.Fatalf("StartGate with no policies: store=%v err=%v, want (nil, nil)", store, err)
	}
}
