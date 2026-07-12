// SPDX-License-Identifier: Elastic-2.0

package controlspec

import (
	"path/filepath"
	"testing"
	"time"
)

func TestLoadAndImportASVS(t *testing.T) {
	b, err := LoadBundle("asvs-l1")
	if err != nil {
		t.Fatal(err)
	}
	if b.Standard != "OWASP ASVS" || b.Version != "4.0.3" || len(b.Requirements) < 5 {
		t.Fatalf("bundle looks wrong: %+v", b)
	}

	s, err := OpenStore(filepath.Join(t.TempDir(), "cs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Unix(1_700_000_000, 0).UTC()

	n, err := ImportBundle(s, "ciso@bankz", b, now)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(b.Requirements) {
		t.Fatalf("imported %d, want %d", n, len(b.Requirements))
	}

	g, ok, err := s.GetGoal("ciso@bankz", "asvs-v2.1.1")
	if err != nil || !ok {
		t.Fatalf("V2.1.1 goal missing: ok=%v err=%v", ok, err)
	}
	if g.Standard != "OWASP ASVS 4.0.3" || g.Ref != "V2.1.1" || g.Level != "L1" || g.Mode != "executable" || g.Intent == "" {
		t.Fatalf("imported goal fields wrong: %+v", g)
	}
	// Re-import is idempotent (no duplicates / no error).
	if _, err := ImportBundle(s, "ciso@bankz", b, now); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListGoals("ciso@bankz")
	if len(list) != len(b.Requirements) {
		t.Fatalf("re-import changed count: %d", len(list))
	}
}

func TestImportBundleAtomicCount(t *testing.T) {
	s, _ := OpenStore(filepath.Join(t.TempDir(), "cs.db"))
	defer s.Close()
	b := Bundle{Standard: "OWASP ASVS", Version: "4.0.3", Requirements: []Requirement{
		{Ref: "V2.1.1", Intent: "passwords >= 12", Level: "L1", Mode: "executable"},
		{Ref: "V4.1.1", Intent: "deny by default", Level: "L1", Mode: "executable"},
	}}
	n, err := ImportBundle(s, "o@x", b, time.Unix(1_700_000_000, 0).UTC())
	if err != nil || n != 2 {
		t.Fatalf("import: n=%d err=%v", n, err)
	}
	goals, _ := s.ListGoals("o@x")
	if len(goals) != 2 {
		t.Fatalf("expected 2 goals, got %d", len(goals))
	}
}
