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
				KillRate: ptr(0.65), Survivors: 4, Evidence: "proven", Trees: 6, SharedDirs: ".venv"},
			{Path: "pkg/b.py", Lang: "python", Disposition: "audited", Gradable: true,
				KillRate: ptr(0.9), Survivors: 1, Evidence: "proven", Trees: 1,
				ConcurrencyNote: "suite is not concurrency-safe: baseline failed under 3"},
			{Path: "pkg/c.py", Lang: "python", Disposition: "audited", Gradable: true,
				KillRate: ptr(0.5), Survivors: 2, Evidence: "proven", Trees: 1},
			// Trees 0 is the one "not recorded" state — a rejected file, or a
			// substrate that builds no trees. It is written SQL NULL, not 0,
			// for the same reason mutants_from is: a stored 0 is a number,
			// and this column is only ever allowed to hold a measurement.
			{Path: "pkg/d.py", Lang: "python", Disposition: "rejected", Reason: "generated"},
		})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	got, err := st.FilesForScan(context.Background(), id)
	if err != nil {
		t.Fatalf("FilesForScan: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d rows, want 4", len(got))
	}
	byPath := map[string]scanstore.File{}
	for _, f := range got {
		byPath[f.Path] = f
	}

	a := byPath["pkg/a.py"]
	if a.Trees != 6 || a.ConcurrencyNote != "" {
		t.Errorf("many-tree row = %+v; the tree count must survive", a)
	}
	if a.SharedDirs != ".venv" {
		t.Errorf("many-tree row = %+v; the dep dirs the trees SHARED must survive too", a)
	}

	b := byPath["pkg/b.py"]
	if b.Trees != 1 || b.ConcurrencyNote != "suite is not concurrency-safe: baseline failed under 3" {
		t.Errorf("downgraded row = %+v; the REASON is the disclosure, and it must survive", b)
	}

	c := byPath["pkg/c.py"]
	if c.Trees != 1 || c.ConcurrencyNote != "" {
		t.Errorf("single-tree row = %+v; no note when the substrate simply had one tree", c)
	}

	d := byPath["pkg/d.py"]
	if d.Trees != 0 || d.ConcurrencyNote != "" {
		t.Errorf("unrecorded row = %+v; nothing scored it, so nothing is claimed", d)
	}
}

// TestUnrecordedConcurrencyIsStoredNull pins the storage half of the same
// rule: 0 trees is written SQL NULL, never the integer 0. A 0 in an INTEGER
// column is a value a later query will average, compare and rank; NULL is
// the only encoding of "this ledger does not say".
func TestUnrecordedConcurrencyIsStoredNull(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "unrecorded.duckdb")
	st, err := scanstore.Open(dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	id, err := st.Record(context.Background(),
		scanstore.Scan{Owner: "local", Repo: "demo"},
		[]scanstore.File{{Path: "pkg/d.py", Lang: "python", Disposition: "rejected", Reason: "generated"}})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db, err := sql.Open("duckdb", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	var trees sql.NullInt64
	if err := db.QueryRow(`SELECT trees FROM scan_files WHERE scan_id = ? AND path = 'pkg/d.py'`, id).Scan(&trees); err != nil {
		t.Fatalf("read trees back raw: %v", err)
	}
	if trees.Valid {
		t.Errorf("an unrecorded concurrency must be stored SQL NULL, got %d", trees.Int64)
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
			KillRate: ptr(0.48), Survivors: 3, Evidence: "proven", Trees: 6, SharedDirs: ".venv",
		}})
	if err != nil {
		t.Fatalf("Record after migration: %v", err)
	}
	got, err := st.FilesForScan(context.Background(), id)
	if err != nil {
		t.Fatalf("FilesForScan: %v", err)
	}
	if len(got) != 1 || got[0].Trees != 6 || got[0].SharedDirs != ".venv" {
		t.Fatalf("migrated concurrency columns did not round-trip: %+v", got)
	}
}
