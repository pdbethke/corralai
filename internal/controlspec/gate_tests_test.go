// SPDX-License-Identifier: Elastic-2.0

package controlspec

import (
	"path/filepath"
	"testing"
	"time"
)

func TestGateTestsSaveGetPending(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "cs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Unix(1_700_000_000, 0).UTC()
	gt := GateTest{Owner: "ciso@bankz", Goal: "asvs-v2.1.1", Target: "bankz/app:auth.go",
		Test: "package target\n// test", KillRate: 0.83, Survived: []string{"m2"}, Discarded: []string{"m5"}, CreatedTS: now}
	gt.VerdictsJSON = `[{"MutantID":"m2","RealGap":true,"Rationale":"misses empty grants"}]`
	if err := s.SaveCandidate(gt); err != nil {
		t.Fatal(err)
	}

	// unvetted → NOT returned by GetVetted
	if _, ok, _ := s.GetVetted("ciso@bankz", "asvs-v2.1.1", "bankz/app:auth.go"); ok {
		t.Fatal("an unvetted candidate must not be gettable as vetted")
	}
	// but IS in the pending list, with fields intact
	pend, err := s.ListPending("ciso@bankz")
	if err != nil {
		t.Fatal(err)
	}
	if len(pend) != 1 || pend[0].Goal != "asvs-v2.1.1" || pend[0].KillRate != 0.83 ||
		len(pend[0].Survived) != 1 || pend[0].Survived[0] != "m2" || pend[0].Vetted {
		t.Fatalf("pending wrong: %+v", pend)
	}
	if pend[0].VerdictsJSON != gt.VerdictsJSON {
		t.Fatalf("verdicts json not round-tripped: %q", pend[0].VerdictsJSON)
	}
	// owner isolation
	if p, _ := s.ListPending("dev@bankz"); len(p) != 0 {
		t.Fatalf("candidate leaked across owners: %+v", p)
	}
}

func TestGateTestsPromoteReject(t *testing.T) {
	s, _ := OpenStore(filepath.Join(t.TempDir(), "cs.db"))
	defer s.Close()
	now := time.Unix(1_700_000_000, 0).UTC()
	vetTime := time.Unix(1_700_000_500, 0).UTC()
	gt := GateTest{Owner: "ciso@bankz", Goal: "g1", Target: "t1", Test: "x", KillRate: 1, CreatedTS: now}
	_ = s.SaveCandidate(gt)

	// Promote an existing unvetted candidate → ok, then it's vetted + gettable + not pending.
	ok, err := s.Promote("ciso@bankz", "g1", "t1", vetTime)
	if err != nil || !ok {
		t.Fatalf("promote: ok=%v err=%v", ok, err)
	}
	got, ok, _ := s.GetVetted("ciso@bankz", "g1", "t1")
	if !ok || !got.Vetted || !got.VettedTS.Equal(vetTime) {
		t.Fatalf("after promote: %+v ok=%v", got, ok)
	}
	if p, _ := s.ListPending("ciso@bankz"); len(p) != 0 {
		t.Fatalf("promoted test still pending: %+v", p)
	}
	// Promote when there is no UNVETTED row → ok=false (already vetted / absent).
	if ok, _ := s.Promote("ciso@bankz", "g1", "t1", vetTime); ok {
		t.Fatal("re-promoting an already-vetted test should report ok=false")
	}
	// Reject removes it entirely.
	if ok, _ := s.Reject("ciso@bankz", "g1", "t1"); !ok {
		t.Fatal("reject of an existing test should report ok=true")
	}
	if _, ok, _ := s.GetVetted("ciso@bankz", "g1", "t1"); ok {
		t.Fatal("rejected test must be gone")
	}
}

func TestListVetted(t *testing.T) {
	s, _ := OpenStore(filepath.Join(t.TempDir(), "cs.db"))
	defer s.Close()
	now := time.Unix(1_700_000_000, 0).UTC()
	_ = s.SaveCandidate(GateTest{Owner: "ciso@bankz", Goal: "g1", Target: "t1", Test: "x", KillRate: 1, CreatedTS: now})
	_ = s.SaveCandidate(GateTest{Owner: "ciso@bankz", Goal: "g2", Target: "t2", Test: "y", KillRate: 1, CreatedTS: now})
	// vet only g1
	_, _ = s.Promote("ciso@bankz", "g1", "t1", now)

	v, err := s.ListVetted("ciso@bankz")
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 1 || v[0].Goal != "g1" || !v[0].Vetted {
		t.Fatalf("ListVetted should return only the promoted g1: %+v", v)
	}
	// owner isolation
	if o, _ := s.ListVetted("dev@bankz"); len(o) != 0 {
		t.Fatalf("vetted test leaked across owners: %+v", o)
	}
}

func TestRecipeRoundTrip(t *testing.T) {
	s, _ := OpenStore(filepath.Join(t.TempDir(), "cs.db"))
	defer s.Close()
	now := time.Unix(1_700_000_000, 0).UTC()
	gt := GateTest{Owner: "ciso@bankz", Goal: "g1", Target: "internal/auth/login.go",
		Test: "package control\n// t", KillRate: 1,
		CodePath: "login.go", TestPath: "login_control_test.go", CreatedTS: now}
	if err := s.SaveCandidate(gt); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Promote("ciso@bankz", "g1", "internal/auth/login.go", now); err != nil {
		t.Fatal(err)
	}
	v, err := s.ListVetted("ciso@bankz")
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 1 || v[0].CodePath != "login.go" || v[0].TestPath != "login_control_test.go" {
		t.Fatalf("recipe did not round-trip through ListVetted: %+v", v)
	}
	got, ok, _ := s.GetVetted("ciso@bankz", "g1", "internal/auth/login.go")
	if !ok || got.CodePath != "login.go" || got.TestPath != "login_control_test.go" {
		t.Fatalf("recipe wrong from GetVetted: %+v", got)
	}
}
