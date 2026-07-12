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
	// owner isolation
	if p, _ := s.ListPending("dev@bankz"); len(p) != 0 {
		t.Fatalf("candidate leaked across owners: %+v", p)
	}
}
