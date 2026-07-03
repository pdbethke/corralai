// SPDX-License-Identifier: Elastic-2.0

package coord

import (
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "coord.sqlite3"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestBootstrapRegisterAndPresence(t *testing.T) {
	s := open(t)
	b, err := s.BootstrapSession("BlueLake", "", "", "refactor auth", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if b.You["name"] != "BlueLake" || len(b.ActivePeers) != 0 {
		t.Fatalf("unexpected bootstrap: %+v", b)
	}
	if _, err := s.BootstrapSession("GreenCastle", "", "", "write tests", "", ""); err != nil {
		t.Fatal(err)
	}
	active, _ := s.ListActive(PresenceWindow)
	if len(active) != 2 {
		t.Fatalf("want 2 active agents, got %d", len(active))
	}
}

func TestExclusiveClaimBlockedByEnforcingHolder(t *testing.T) {
	s := open(t)
	s.Register("BlueLake", "", "", "", "", "")
	s.Register("GreenCastle", "", "", "", "", "")
	r1, _ := s.ClaimPaths("BlueLake", []string{"src/auth.py"}, 3600, true, "refactor")
	if len(r1.Granted) != 1 || len(r1.Conflicts) != 0 {
		t.Fatalf("first claim should be clean: %+v", r1)
	}
	// Exclusive claims are now enforcing: a second exclusive claimant is NOT granted.
	r2, _ := s.ClaimPaths("GreenCastle", []string{"src/auth.py"}, 3600, true, "")
	if len(r2.Granted) != 0 {
		t.Fatalf("enforcing: second exclusive claim must not be granted, got %+v", r2)
	}
	if len(r2.Conflicts) != 1 || r2.Conflicts[0].HeldBy != "BlueLake" {
		t.Fatalf("should report conflict held by BlueLake: %+v", r2.Conflicts)
	}
}

func TestDirectoryPrefixOverlap(t *testing.T) {
	s := open(t)
	s.Register("BlueLake", "", "", "", "", "")
	s.Register("GreenCastle", "", "", "", "", "")
	s.ClaimPaths("BlueLake", []string{"src/api/"}, 3600, true, "")
	r, _ := s.ClaimPaths("GreenCastle", []string{"src/api/views.py"}, 3600, true, "")
	if len(r.Conflicts) != 1 {
		t.Fatalf("file under claimed dir should conflict: %+v", r.Conflicts)
	}
}

func TestReleaseFreesPath(t *testing.T) {
	s := open(t)
	s.Register("BlueLake", "", "", "", "", "")
	s.Register("GreenCastle", "", "", "", "", "")
	s.ClaimPaths("BlueLake", []string{"src/auth.py"}, 3600, true, "")
	if _, err := s.ReleaseClaims("BlueLake", []string{"src/auth.py"}); err != nil {
		t.Fatal(err)
	}
	r, _ := s.ClaimPaths("GreenCastle", []string{"src/auth.py"}, 3600, true, "")
	if len(r.Conflicts) != 0 {
		t.Fatalf("released path should not conflict: %+v", r.Conflicts)
	}
}

func TestTTLExpiryFreesPath(t *testing.T) {
	s := open(t)
	s.Register("BlueLake", "", "", "", "", "")
	s.Register("GreenCastle", "", "", "", "", "")
	s.ClaimPaths("BlueLake", []string{"src/auth.py"}, 0, true, "") // expires immediately
	time.Sleep(10 * time.Millisecond)
	r, _ := s.ClaimPaths("GreenCastle", []string{"src/auth.py"}, 3600, true, "")
	if len(r.Conflicts) != 0 {
		t.Fatalf("expired lease should not conflict: %+v", r.Conflicts)
	}
}

func TestPrincipalCountLive(t *testing.T) {
	s := open(t)
	// two agents owned by "alice", one by "bob"
	s.Register("a1", "", "", "", "", "")
	s.Register("a2", "", "", "", "", "")
	s.Register("b1", "", "", "", "", "")
	if err := s.RecordPrincipal("a1", "alice"); err != nil {
		t.Fatal(err)
	}
	s.RecordPrincipal("a2", "alice")
	s.RecordPrincipal("b1", "bob")
	n, err := s.CountLiveByPrincipal("alice")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("alice live count = %d, want 2", n)
	}
	if n, _ := s.CountLiveByPrincipal("bob"); n != 1 {
		t.Fatalf("bob live count = %d, want 1", n)
	}
}

func TestCompletedWorkSurfacesInBootstrap(t *testing.T) {
	s := open(t)
	s.Register("BlueLake", "", "", "", "", "")
	s.MarkDone("BlueLake", "migrated auth to OIDC", []string{"src/auth.py"})
	b, _ := s.BootstrapSession("GreenCastle", "", "", "", "", "")
	found := false
	for _, c := range b.RecentCompleted {
		if c.Summary == "migrated auth to OIDC" {
			found = true
		}
	}
	if !found {
		t.Fatalf("completed work should surface in bootstrap: %+v", b.RecentCompleted)
	}
}
