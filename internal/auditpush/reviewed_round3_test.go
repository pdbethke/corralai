// SPDX-License-Identifier: Elastic-2.0

package auditpush

import (
	"database/sql"
	"testing"

	_ "github.com/marcboeker/go-duckdb/v2"
)

// Review e00b52bab444 (Gemini reviewing internal/auditpush a third time,
// Codex verifying), both inverted.

// R1: a bundle with no scan header must not grow one. writeLedgerFile
// stamped PushedBy on the empty ScanRow, and the row was no longer empty.
func TestBundleWithoutAScanHeaderWritesNoScanRow(t *testing.T) {
	dir := t.TempDir() + "/"
	b := Bundle{Files: []Row{{Repo: "o/r", Commit: "c", Path: "a.go", Disposition: "audited"}}}
	if _, err := PushBundle(dir, b); err != nil {
		t.Fatal(err)
	}
	entries, _ := ReadLedgerDir(dir)
	if len(entries) != 1 || entries[0].Bundle.Scan != (ScanRow{}) {
		t.Fatalf("a phantom scan row was minted: %+v", entries[0].Bundle.Scan)
	}
	db, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM corral_scans`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("corral_scans holds %d row(s) for a bundle with no scan: %v", n, err)
	}
}

// R2: EnsureSchema's contract is any open handle; the migration probe
// asked duckdb_columns() about a catalog named 'warehouse' and found
// nothing on a plain database, so every ALTER failed.
func TestEnsureSchemaWorksOnAPlainDuckDBHandle(t *testing.T) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	// The warehouse's tables in the DEFAULT catalog (no ATTACH ... AS
	// warehouse), corral_scans as an older binary created it — missing the
	// columns migrated since.
	if _, err := db.Exec(`CREATE TABLE corral_scans (scan_uid VARCHAR, ts TIMESTAMPTZ, repo VARCHAR, commit_sha VARCHAR)`); err != nil {
		t.Fatal(err)
	}
	for _, ddl := range schemaDDL {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if err := EnsureSchema(db); err != nil {
		t.Fatalf("EnsureSchema on a plain handle: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM duckdb_columns() WHERE table_name = 'corral_scans' AND column_name = 'author'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the migration did not reach the plain catalog: %d %v", n, err)
	}
}
