// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/controlgate"
	"github.com/pdbethke/corralai/internal/controlspec"
	"github.com/pdbethke/corralai/internal/gate"
)

type fakeCheckout struct{ err error }

func (f fakeCheckout) CheckoutPR(_ context.Context, _ string, _ int, _, _ string) error { return f.err }

type fakeReader struct{ files map[string]string } // path -> head content; missing = ReadFile error

func (f fakeReader) ReadFile(_, path string) (string, error) {
	if c, ok := f.files[path]; ok {
		return c, nil
	}
	return "", errors.New("not found")
}

type fakeCert struct{ err error }

func (f fakeCert) Certify(_ context.Context, _, _, _ string, _ int, _ string) (int64, string, error) {
	return 1, "head", f.err
}

type postCall struct{ state, ctx, desc string }
type fakePoster struct{ calls []postCall }

func (f *fakePoster) SetCommitStatus(_ context.Context, _, _, context, state, _, desc string) error {
	f.calls = append(f.calls, postCall{state: state, ctx: context, desc: desc})
	return nil
}
func (f *fakePoster) last() postCall { return f.calls[len(f.calls)-1] }

// fakeJail: a vetted test "passes" unless its content contains "FAIL".
type fakeJail struct{}

func (fakeJail) RunTest(_ context.Context, files map[string]string, _ []string) (bool, error) {
	for _, c := range files {
		if strings.Contains(c, "FAIL") {
			return false, nil
		}
	}
	return true, nil
}

func seedRunner(t *testing.T, reader fileReader, cert controlgate.Certifier, poster *fakePoster) (*controlRunner, *controlspec.Store) {
	t.Helper()
	spec, err := controlspec.OpenStore(filepath.Join(t.TempDir(), "cs.db"))
	if err != nil {
		t.Fatal(err)
	}
	runStore, err := gate.OpenStore(filepath.Join(t.TempDir(), "run.db"))
	if err != nil {
		t.Fatal(err)
	}
	r := &controlRunner{
		byRepo:          map[string]string{"o/r": "owner@x"},
		Base:            map[string]string{"go.mod": "module control\ngo 1.26\n"},
		TestCmd:         []string{"go", "test", "./"},
		Checkout:        fakeCheckout{},
		Reader:          reader,
		Cert:            cert,
		Status:          poster,
		Spec:            spec,
		Jail:            fakeJail{},
		RunStore:        runStore,
		Record:          func(_, sha string) string { return "/rec/" + sha },
		Now:             func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
		attempts:        make(map[string]int),
		MaxCertAttempts: controlCertMaxAttempts,
	}
	return r, spec
}

func vet(t *testing.T, s *controlspec.Store, owner, goal, target, test, codePath string) {
	t.Helper()
	now := time.Unix(1_700_000_000, 0).UTC()
	gt := controlspec.GateTest{Owner: owner, Goal: goal, Target: target, Test: test,
		CodePath: codePath, TestPath: "x_control_test.go", KillRate: 1, CreatedTS: now}
	if err := s.SaveCandidate(gt); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Promote(owner, goal, target, now); err != nil {
		t.Fatal(err)
	}
}

func policy() gate.Policy { return gate.Policy{Repo: "o/r", Context: "corral/control-gate"} }
func pr() gate.PRRef      { return gate.PRRef{Number: 1, HeadSHA: "abc"} }

func TestControlRunner_Pass(t *testing.T) {
	poster := &fakePoster{}
	reader := fakeReader{files: map[string]string{"internal/auth/login.go": "package control\n// head ok"}}
	r, spec := seedRunner(t, reader, fakeCert{}, poster)
	defer spec.Close()
	vet(t, spec, "owner@x", "g1", "internal/auth/login.go", "package control\n// clean", "login.go")
	if err := r.Run(context.Background(), "https://github.com/o/r", policy(), pr()); err != nil {
		t.Fatal(err)
	}
	if got := poster.last(); got.state != "success" || got.ctx != "corral/control-gate" {
		t.Fatalf("expected signed success, got %+v", got)
	}
}

func TestControlRunner_FailingControl(t *testing.T) {
	poster := &fakePoster{}
	reader := fakeReader{files: map[string]string{"a.go": "package control\n// FAIL head"}}
	r, spec := seedRunner(t, reader, fakeCert{}, poster)
	defer spec.Close()
	vet(t, spec, "owner@x", "g1", "a.go", "package control\n// t", "a.go")
	_ = r.Run(context.Background(), "https://github.com/o/r", policy(), pr())
	if got := poster.last(); got.state != "failure" {
		t.Fatalf("failing control → failure, got %+v", got)
	}
}

func TestControlRunner_MissingTarget(t *testing.T) {
	poster := &fakePoster{}
	reader := fakeReader{files: map[string]string{}} // target absent at head
	r, spec := seedRunner(t, reader, fakeCert{}, poster)
	defer spec.Close()
	vet(t, spec, "owner@x", "g1", "gone.go", "package control\n// t", "gone.go")
	_ = r.Run(context.Background(), "https://github.com/o/r", policy(), pr())
	if got := poster.last(); got.state != "failure" {
		t.Fatalf("missing target → failure (fail-closed), got %+v", got)
	}
}

func TestControlRunner_ZeroVetted(t *testing.T) {
	poster := &fakePoster{}
	r, spec := seedRunner(t, fakeReader{}, fakeCert{}, poster)
	defer spec.Close()
	_ = r.Run(context.Background(), "https://github.com/o/r", policy(), pr())
	if got := poster.last(); got.state != "failure" {
		t.Fatalf("zero vetted → failure, got %+v", got)
	}
}

func TestControlRunner_CertifyError_NoPost(t *testing.T) {
	poster := &fakePoster{}
	reader := fakeReader{files: map[string]string{"a.go": "package control\n// ok"}}
	r, spec := seedRunner(t, reader, fakeCert{err: errors.New("sign failed")}, poster)
	defer spec.Close()
	vet(t, spec, "owner@x", "g1", "a.go", "package control\n// t", "a.go")
	err := r.Run(context.Background(), "https://github.com/o/r", policy(), pr())
	if err == nil {
		t.Fatal("certify error must return an error")
	}
	for _, c := range poster.calls {
		if c.state == "success" {
			t.Fatal("must not post success when signing failed (no unsigned green)")
		}
	}
}

func TestControlRunner_CertifyError_BoundedRetry(t *testing.T) {
	poster := &fakePoster{}
	reader := fakeReader{files: map[string]string{"a.go": "package control\n// ok"}}
	r, spec := seedRunner(t, reader, fakeCert{err: errors.New("sign failed")}, poster)
	defer spec.Close()
	r.MaxCertAttempts = 2
	vet(t, spec, "owner@x", "g1", "a.go", "package control\n// t", "a.go")

	// Attempt 1 (below cap): transient — returns err to signal retry, posts no
	// terminal status, and does NOT record the SHA (so the poller retries it).
	if err := r.Run(context.Background(), "https://github.com/o/r", policy(), pr()); err == nil {
		t.Fatal("attempt 1 (below cap) must return an error to signal retry")
	}
	for _, c := range poster.calls {
		if c.state == "error" || c.state == "success" {
			t.Fatalf("attempt 1 must stay pending (no terminal status), got %+v", c)
		}
	}
	if _, ok, _ := r.RunStore.GetBySHA("o/r", "abc"); ok {
		t.Fatal("attempt 1 must NOT record the SHA (retry next poll)")
	}

	// Attempt 2 (at cap): a PERSISTENT signing failure is terminal — post `error`
	// and record the SHA so the poller stops re-checking-out + re-running the jail.
	_ = r.Run(context.Background(), "https://github.com/o/r", policy(), pr())
	if got := poster.last(); got.state != "error" {
		t.Fatalf("attempt 2 (at cap) must post a terminal error, got %+v", got)
	}
	if _, ok, _ := r.RunStore.GetBySHA("o/r", "abc"); !ok {
		t.Fatal("attempt 2 (at cap) must record the SHA so the poller stops re-running")
	}
}

func TestStartControlGate_OffSwitches(t *testing.T) {
	// no policies → complete no-op
	if rs, cs, err := StartControlGate(context.Background(), Options{}); rs != nil || cs != nil || err != nil {
		t.Fatalf("empty policies must be a no-op: %v %v %v", rs, cs, err)
	}
	// policies but nil backend → disabled (fail-closed: never run unsandboxed)
	opts := Options{ControlPolicies: []controlgate.ControlPolicy{{Repo: "o/r", Owner: "x", Lang: "go"}}}
	if rs, cs, _ := StartControlGate(context.Background(), opts); rs != nil || cs != nil {
		t.Fatal("nil GateBackend must disable the control gate")
	}
}
