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
