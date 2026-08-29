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

// TestSelectionColumnsRoundTrip pins the ledger's half of "every verdict says
// which tests graded it". A kill rate earned against 14 of 1431 tests and one
// earned against the whole suite are answers to DIFFERENT questions, and a
// ledger that stores only the number cannot tell them apart afterwards.
func TestSelectionColumnsRoundTrip(t *testing.T) {
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
				KillRate: ptr(0.65), Survivors: 4, Evidence: "proven",
				TestSelection: "coverage-context", SelectedTests: 14, SuiteTests: 1431},
			{Path: "lib/a.rb", Lang: "ruby", Disposition: "audited", Gradable: true,
				KillRate: ptr(0.9), Evidence: "proven",
				SelectionFallback: "no selector for ruby"},
			{Path: "pkg/u.py", Lang: "python", Disposition: "audited", Gradable: true,
				KillRate: ptr(0), Evidence: "proven",
				TestSelection: "coverage-context", SuiteTests: 1431, Uncovered: true},
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
	if a.TestSelection != "coverage-context" || a.SelectedTests != 14 || a.SuiteTests != 1431 {
		t.Errorf("selected row = %+v; the method AND the counts must survive", a)
	}
	if a.Uncovered || a.SelectionFallback != "" {
		t.Errorf("a selected row must not read back as uncovered or fallen back: %+v", a)
	}

	rb := byPath["lib/a.rb"]
	if rb.SelectionFallback != "no selector for ruby" || rb.TestSelection != "" {
		t.Errorf("whole-suite row = %+v; the REASON is the disclosure, and it must survive", rb)
	}

	u := byPath["pkg/u.py"]
	if !u.Uncovered {
		t.Errorf("uncovered row round-tripped as covered: %+v", u)
	}
	// An uncovered file's rate is not a measurement — no test executes the
	// file, so 0.00 in the ledger would read as "your tests caught nothing
	// here" about a question nobody asked. NULL is the honest value.
	if u.KillRate != nil {
		t.Errorf("uncovered row kill rate = %v, want NULL: nothing graded this file", *u.KillRate)
	}
}

// TestSelectionColumnsMigrateOntoALegacyStore proves the five columns are
// ADDED to a ledger created before they existed, rather than the writes
// failing (or, worse, the reader silently never asking for them).
func TestSelectionColumnsMigrateOntoALegacyStore(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "legacy-selection.duckdb")
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
			KillRate: ptr(0.48), Survivors: 3, Evidence: "proven",
			TestSelection: "coverage-context", SelectedTests: 14, SuiteTests: 1431,
		}})
	if err != nil {
		t.Fatalf("Record after migration: %v", err)
	}
	got, err := st.FilesForScan(context.Background(), id)
	if err != nil {
		t.Fatalf("FilesForScan: %v", err)
	}
	if len(got) != 1 || got[0].TestSelection != "coverage-context" || got[0].SelectedTests != 14 || got[0].SuiteTests != 1431 {
		t.Fatalf("migrated selection columns did not round-trip: %+v", got)
	}
}
