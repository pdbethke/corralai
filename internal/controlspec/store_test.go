// SPDX-License-Identifier: Elastic-2.0

package controlspec

import (
	"path/filepath"
	"testing"
	"time"
)

func TestControlSpecStore(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "cs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Unix(1_700_000_000, 0).UTC()

	g := Goal{ID: "asvs-v2.1.1", Owner: "ciso@bankz", Standard: "OWASP ASVS 4.0.3", Ref: "V2.1.1",
		Intent: "user-set passwords are at least 12 characters", Level: "L1", Mode: "executable", CreatedTS: now}
	if err := s.SaveGoal(g); err != nil {
		t.Fatal(err)
	}

	got, ok, err := s.GetGoal("ciso@bankz", "asvs-v2.1.1")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got != g {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, g)
	}

	// A different owner cannot see it (owner isolation).
	if _, ok, _ := s.GetGoal("dev@bankz", "asvs-v2.1.1"); ok {
		t.Fatal("goal leaked across owners")
	}
	// Save a second owner's goal; each list is owner-scoped.
	if err := s.SaveGoal(Goal{ID: "custom-1", Owner: "dev@bankz", Intent: "x", Mode: "attested", CreatedTS: now}); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListGoals("ciso@bankz")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "asvs-v2.1.1" {
		t.Fatalf("ListGoals(ciso) = %+v, want just asvs-v2.1.1", list)
	}
}
