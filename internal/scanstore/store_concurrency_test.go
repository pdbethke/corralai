// SPDX-License-Identifier: Elastic-2.0

package scanstore_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/marcboeker/go-duckdb/v2"

	"github.com/pdbethke/corralai/internal/scanstore"
)

// TestConcurrencyColumnsRoundTrip pins the ledger's half of "every reader
// says how many trees scored the file, or why one". A rate earned by 6
// trees scoring mutants in parallel and one earned by a single tree after a
// concurrency downgrade are different runs, and a ledger that stores only
// the number cannot tell them apart afterwards.
func TestConcurrencyColumnsRoundTrip(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "scans.duckdb")
	st, err := scanstore.Open(dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	id, err := st.Record(context.Background(),
		scanstore.Scan{Owner: "local", Repo: "demo", Commit: "abc123"},
		[]scanstore.File{
			{Path: "pkg/a.py", Lang: "python", Disposition: "audited", Gradable: true,
				KillRate: ptr(0.65), Survivors: 4, Evidence: "proven", Trees: 6},
			{Path: "pkg/b.py", Lang: "python", Disposition: "audited", Gradable: true,
				KillRate: ptr(0.9), Survivors: 1, Evidence: "proven", Trees: 1,
				ConcurrencyNote: "suite is not concurrency-safe: baseline failed under 3"},
			{Path: "pkg/c.py", Lang: "python", Disposition: "audited", Gradable: true,
				KillRate: ptr(0.5), Survivors: 2, Evidence: "proven", Trees: 1},
		})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	got, err := st.FilesForScan(context.Background(), id)
	if err != nil {
		t.Fatalf("FilesForScan: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3", len(got))
	}
	byPath := map[string]scanstore.File{}
	for _, f := range got {
		byPath[f.Path] = f
	}

	a := byPath["pkg/a.py"]
	if a.Trees != 6 || a.ConcurrencyNote != "" {
		t.Errorf("many-tree row = %+v; the tree count must survive", a)
	}

	b := byPath["pkg/b.py"]
	if b.Trees != 1 || b.ConcurrencyNote != "suite is not concurrency-safe: baseline failed under 3" {
		t.Errorf("downgraded row = %+v; the REASON is the disclosure, and it must survive", b)
	}

	c := byPath["pkg/c.py"]
	if c.Trees != 1 || c.ConcurrencyNote != "" {
		t.Errorf("single-tree row = %+v; no note when the substrate simply had one tree", c)
	}
}

// TestConcurrencyColumnsMigrateOntoALegacyStore proves the two columns are
// ADDED to a ledger created before they existed, rather than the writes
// failing (or, worse, the reader silently never asking for them).
func TestConcurrencyColumnsMigrateOntoALegacyStore(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "legacy-concurrency.duckdb")
	db, err := sql.Open("duckdb", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE scan_files (
		scan_id BIGINT, path VARCHAR, lang VARCHAR,
		disposition VARCHAR, reason VARCHAR,
		kill_rate DOUBLE, survivors INTEGER, gradable BOOLEAN
	)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	st, err := scanstore.Open(dsn)
	if err != nil {
		t.Fatalf("Open (legacy): %v", err)
	}
	defer st.Close()

	id, err := st.Record(context.Background(),
		scanstore.Scan{Owner: "local", Repo: "flask"},
		[]scanstore.File{{
			Path: "src/flask/cli.py", Lang: "python", Disposition: "audited", Gradable: true,
			KillRate: ptr(0.48), Survivors: 3, Evidence: "proven", Trees: 6,
		}})
	if err != nil {
		t.Fatalf("Record after migration: %v", err)
	}
	got, err := st.FilesForScan(context.Background(), id)
	if err != nil {
		t.Fatalf("FilesForScan: %v", err)
	}
	if len(got) != 1 || got[0].Trees != 6 {
		t.Fatalf("migrated concurrency columns did not round-trip: %+v", got)
	}
}
