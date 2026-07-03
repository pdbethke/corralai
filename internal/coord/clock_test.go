// SPDX-License-Identifier: Elastic-2.0

package coord

import (
	"path/filepath"
	"testing"
)

// withClock overrides the package clock for the duration of a test.
// WARNING: mutates a package-level var — tests that call withClock must NOT call t.Parallel().
func withClock(t *testing.T, f func() float64) {
	t.Helper()
	orig := now
	now = f
	t.Cleanup(func() { now = orig })
}

func openTmp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestClaimExpiresWithClock(t *testing.T) {
	clock := 1000.0
	withClock(t, func() float64 { return clock })
	s := openTmp(t)
	if err := s.Register("alice", "", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimPaths("alice", []string{"src/app.go"}, 10, true, ""); err != nil {
		t.Fatal(err)
	}
	// Before expiry: bob sees a conflict.
	clock = 1005
	r, err := s.ClaimPaths("bob", []string{"src/app.go"}, 10, true, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Conflicts) != 1 {
		t.Fatalf("before expiry want 1 conflict, got %d", len(r.Conflicts))
	}
	// After alice's lease expires: a fresh claimant sees none.
	clock = 1100
	r2, err := s.ClaimPaths("carol", []string{"src/app.go"}, 10, true, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(r2.Conflicts) != 0 {
		t.Fatalf("after expiry want 0 conflicts, got %d", len(r2.Conflicts))
	}
}
