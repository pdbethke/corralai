// SPDX-License-Identifier: Elastic-2.0

package coord

import (
	"path/filepath"
	"testing"
)

// LiveClaimHolders feeds the bug-#40 escalation: the brain needs the distinct
// agents currently holding live (unreleased, unexpired) path leases so it can
// check each against the queue for staleness.
func TestLiveClaimHolders(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	if _, err := s.ClaimPaths("Sage", []string{"lru.js"}, 3600, true, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimPaths("Tess", []string{"test/lru.test.js"}, 3600, true, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimPaths("Bob", []string{"README.md"}, 3600, true, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReleaseClaims("Bob", nil); err != nil {
		t.Fatal(err)
	}

	holders, err := s.LiveClaimHolders()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"Sage": true, "Tess": true}
	if len(holders) != len(want) {
		t.Fatalf("holders = %v, want exactly Sage+Tess", holders)
	}
	for _, h := range holders {
		if !want[h] {
			t.Fatalf("unexpected holder %q in %v (Bob released everything)", h, holders)
		}
	}
}

// TestReapAbsentClaimsReleasesDeadHolders is the coord-lease reaper: a crashed
// agent's exclusive path lease must not strand peers until its (up to 1h) TTL.
// The reaper releases every claim held by an agent absent from the live presence
// set, and leaves present holders' claims untouched.
func TestReapAbsentClaimsReleasesDeadHolders(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	if _, err := s.ClaimPaths("Ada", []string{"src/foo.go"}, 3600, true, "work"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimPaths("Bob", []string{"src/bar.go"}, 3600, true, "work"); err != nil {
		t.Fatal(err)
	}

	// Ada has crashed (absent from presence); Bob is still heart-beating.
	reaped, err := s.ReapAbsentClaims(map[string]bool{"Bob": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(reaped) != 1 || reaped[0] != "Ada" {
		t.Fatalf("expected to reap the dead holder Ada, got %v", reaped)
	}

	// foo.go is now free — a peer can take it exclusively.
	r, err := s.ClaimPaths("Cy", []string{"src/foo.go"}, 3600, true, "work")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Granted) != 1 {
		t.Fatalf("foo.go should be free after reaping its dead holder; granted=%v conflicts=%v", r.Granted, r.Conflicts)
	}
	// bar.go is still held by the present holder Bob.
	r2, err := s.ClaimPaths("Cy", []string{"src/bar.go"}, 3600, true, "work")
	if err != nil {
		t.Fatal(err)
	}
	if len(r2.Granted) != 0 {
		t.Fatalf("bar.go should still be held by present holder Bob, but it was granted to Cy")
	}

	// A nil presence set (presence unavailable) must reap nothing — never release
	// a live agent's lease on a transient presence outage.
	reaped, err = s.ReapAbsentClaims(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(reaped) != 0 {
		t.Fatalf("nil presence must reap nothing, got %v", reaped)
	}
}
