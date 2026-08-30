// SPDX-License-Identifier: Elastic-2.0

package auditpush

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	_ "github.com/marcboeker/go-duckdb/v2"
)

// TestTwentyConcurrentPushesNeverCollide is the swarm requirement stated as a
// test. The design's whole distribution story is "the swarm is GitHub's
// runner pool" — twenty PRs land at once, twenty runners each finish an
// audit and each push to the SAME database — and corral's only obligation is
// that those twenty pushes cannot collide or corrupt.
//
// DuckDB grants ONE writer at a time on a file, so this is not free: a push
// must open its handle, write, and close it, and must retry rather than fail
// when another writer holds the lock. The same discipline is what twenty
// runners hitting one `md:` database need.
//
// Each pusher writes a DISTINCT (run_url, scan_id) — the key the design
// chose precisely so that no coordination is needed between writers.
func TestTwentyConcurrentPushesNeverCollide(t *testing.T) {
	target := filepath.Join(t.TempDir(), "swarm.duckdb")

	const pushers = 20
	const mutantsEach = 5

	var wg sync.WaitGroup
	errs := make([]error, pushers)
	for i := 0; i < pushers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b := sampleBundle()
			runURL := fmt.Sprintf("https://ci/%d", i)
			scanID := int64(1000 + i)
			b.Scan.RunURL, b.Scan.ScanID = runURL, scanID
			b.Files = b.Files[:1]
			b.Files[0].RunURL, b.Files[0].ScanID = runURL, scanID
			b.Calls, b.Events = nil, nil
			b.Mutants = nil
			for m := 0; m < mutantsEach; m++ {
				b.Mutants = append(b.Mutants, MutantRow{
					Repo: "o/r", RunURL: runURL, ScanID: scanID, Path: "pkg/a.go",
					MutantID: fmt.Sprintf("m%d", m), Outcome: "survived",
				})
			}
			_, errs[i] = PushBundle(target, b)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("pusher %d failed: %v", i, err)
		}
	}
	if t.Failed() {
		t.FailNow()
	}

	db, err := sql.Open("duckdb", target)
	if err != nil {
		t.Fatalf("open the warehouse: %v", err)
	}
	defer db.Close()

	var scans, mutants, distinct int
	if err := db.QueryRow(`SELECT count(*) FROM corral_scans`).Scan(&scans); err != nil {
		t.Fatalf("count corral_scans: %v", err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM corral_mutants`).Scan(&mutants); err != nil {
		t.Fatalf("count corral_mutants: %v", err)
	}
	if err := db.QueryRow(`SELECT count(DISTINCT (repo, run_url, scan_id)) FROM corral_scans`).Scan(&distinct); err != nil {
		t.Fatalf("count distinct keys: %v", err)
	}
	if scans != pushers {
		t.Errorf("corral_scans has %d rows, want %d — a push was lost", scans, pushers)
	}
	if mutants != pushers*mutantsEach {
		t.Errorf("corral_mutants has %d rows, want %d", mutants, pushers*mutantsEach)
	}
	if distinct != pushers {
		t.Errorf("%d distinct (repo, run_url, scan_id) keys, want %d — two writers collided", distinct, pushers)
	}
}
