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

// TestMutantSelectionColumnsRoundTrip pins the ledger's half of "every mutant
// says how many tests graded it, and by which rule". Once each mutant is
// graded by the tests that reach ITS lines, a kill rate is an average over
// mutants that faced DIFFERENT test sets — and a table that stores only the
// outcome cannot say afterwards whether a survivor survived 41 tests or 3.
func TestMutantSelectionColumnsRoundTrip(t *testing.T) {
	st, err := scanstore.Open(filepath.Join(t.TempDir(), "scans.duckdb"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	id, err := st.Record(ctx, scanstore.Scan{Owner: "local", Repo: "flask"},
		[]scanstore.File{{Path: "src/flask/cli.py", Lang: "python", Disposition: "audited", Gradable: true}})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	in := []scanstore.Mutant{
		{ScanID: id, Path: "src/flask/cli.py", MutantID: "m1", Outcome: "killed",
			ParentSHA256: "abc", TestsRun: 3, SelectionRule: "lines"},
		{ScanID: id, Path: "src/flask/cli.py", MutantID: "m2", Outcome: "survived",
			ParentSHA256: "abc", Proven: true, TestsRun: 41, SelectionRule: "unreached"},
	}
	if err := st.RecordMutants(ctx, in); err != nil {
		t.Fatalf("RecordMutants: %v", err)
	}
	got, err := st.MutantsForScan(ctx, id)
	if err != nil {
		t.Fatalf("MutantsForScan: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d mutants, want 2", len(got))
	}
	byID := map[string]scanstore.Mutant{}
	for _, m := range got {
		byID[m.MutantID] = m
	}
	if m := byID["m1"]; m.TestsRun != 3 || m.SelectionRule != "lines" {
		t.Errorf("killed mutant = %+v; the count AND the rule must survive", m)
	}
	if m := byID["m2"]; m.TestsRun != 41 || m.SelectionRule != "unreached" {
		t.Errorf("survived mutant = %+v; the count AND the rule must survive", m)
	}
	// A run that graded every mutant with the file's shared command records
	// no per-mutant grading, and that must read back as zero rather than as
	// a wrong number or a scan error.
	if err := st.RecordMutants(ctx, []scanstore.Mutant{
		{ScanID: id, Path: "src/flask/app.py", MutantID: "m3", Outcome: "killed"},
	}); err != nil {
		t.Fatalf("RecordMutants (no grading): %v", err)
	}
	got, err = st.MutantsForScan(ctx, id)
	if err != nil {
		t.Fatalf("MutantsForScan (after): %v", err)
	}
	for _, m := range got {
		if m.MutantID == "m3" && (m.TestsRun != 0 || m.SelectionRule != "") {
			t.Errorf("an ungraded-per-mutant row must read back zero, got %+v", m)
		}
	}
}

// TestMutantSelectionColumnsMigrateOntoALegacyStore proves the two columns are
// ADDED to a scan_mutants created before they existed, rather than every
// mutant write failing on a ledger a previous version wrote.
func TestMutantSelectionColumnsMigrateOntoALegacyStore(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "legacy-mutants.duckdb")
	db, err := sql.Open("duckdb", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE scan_mutants (
		scan_id BIGINT, path VARCHAR, mutant_id VARCHAR,
		outcome VARCHAR CHECK (outcome IN ('killed', 'survived')),
		parent_sha256 VARCHAR,
		proven BOOLEAN
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
	ctx := context.Background()

	id, err := st.Record(ctx, scanstore.Scan{Owner: "local", Repo: "flask"},
		[]scanstore.File{{Path: "src/flask/cli.py", Lang: "python", Disposition: "audited", Gradable: true}})
	if err != nil {
		t.Fatalf("Record after migration: %v", err)
	}
	if err := st.RecordMutants(ctx, []scanstore.Mutant{{
		ScanID: id, Path: "src/flask/cli.py", MutantID: "m1", Outcome: "survived",
		ParentSHA256: "abc", TestsRun: 9, SelectionRule: "static",
	}}); err != nil {
		t.Fatalf("RecordMutants after migration: %v", err)
	}
	got, err := st.MutantsForScan(ctx, id)
	if err != nil {
		t.Fatalf("MutantsForScan: %v", err)
	}
	if len(got) != 1 || got[0].TestsRun != 9 || got[0].SelectionRule != "static" {
		t.Fatalf("migrated per-mutant columns did not round-trip: %+v", got)
	}
}
