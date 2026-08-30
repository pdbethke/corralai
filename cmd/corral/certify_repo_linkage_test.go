// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/marcboeker/go-duckdb/v2"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// oneAuditedFileReport is a RepoReport with one WeakFile whose kill rate is
// 0.5 — just enough shape for writeAuditStatement and the push to work with,
// reusing the same reposcan types the rest of this package's tests use.
func oneAuditedFileReport() reposcan.RepoReport {
	return reposcan.RepoReport{
		Repo: "o/r", Commit: "deadbeef", Audited: 1, Candidates: 1,
		Weakest: []reposcan.WeakFile{
			{Path: "pkg/a.go", KillRate: 0.5, Survivors: 2, ProvenMissed: 0},
		},
	}
}

// queryRows runs query against db and returns every row as a slice of `any`,
// scanned generically enough to compare against expected sha/scan-id values.
func queryRows(t *testing.T, dbPath, query string) [][]any {
	t.Helper()
	db, err := sql.Open("duckdb", dbPath)
	if err != nil {
		t.Fatalf("open %s: %v", dbPath, err)
	}
	defer db.Close()

	rows, err := db.Query(query)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("columns: %v", err)
	}

	var out [][]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, vals)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

// oneAuditedFileBundle is the LEDGER half of the same scan: the rows
// buildScanFileRows would have produced, mapped through the one bundle
// builder. Since Task 2 the pushed rows come from the ledger, not from the
// report, so a linkage test has to travel the same road the command does.
func oneAuditedFileBundle(scanID int64) auditpush.Bundle {
	rate := 0.5
	scan := scanstore.Scan{Repo: "o/r", Commit: "deadbeef", Audited: 1, Candidates: 1}
	files := []scanstore.File{{
		Path: "pkg/a.go", Lang: "go", Disposition: "audited",
		KillRate: &rate, Survivors: 2, Gradable: true, Evidence: "proven",
	}}
	return buildBundle(scan, scanID, files, nil, nil, nil,
		auditpush.Link{}, false, "o/r", "deadbeef", "",
		bundleMeta{ModelsByRole: `{"writer":"m"}`, Passed: true})
}

// linkageStatement mirrors the shape readStatement needs out of the written
// DSSE payload: the predicate fields writeAuditStatement adds so a pushed row
// and the statement it came from can be checked against each other.
type linkageStatement struct {
	Predicate struct {
		ScanID              int64  `json:"scanId"`
		WarehouseRowsSHA256 string `json:"warehouseRowsSha256"`
	} `json:"predicate"`
}

// readStatement parses the DSSE payload writeAuditStatement wrote at path.
func readStatement(t *testing.T, path string) linkageStatement {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read statement: %v", err)
	}
	var s linkageStatement
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("unmarshal statement: %v", err)
	}
	return s
}

// TestPushedRowsCarryTheStatementSHA is the linkage this task exists to
// build: a row pushed to the warehouse carries the sha256 of the statement
// it came from, and the statement names the scan and the hash of the rows
// it will be (or was) pushed with.
func TestPushedRowsCarryTheStatementSHA(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "w.duckdb")
	att := filepath.Join(dir, "att.json")
	rep := oneAuditedFileReport()

	bundle := oneAuditedFileBundle(7 /* ledger scan id */)
	sha, err := writeAuditStatement(att, dir, rep, map[string]string{"writer": "m"}, nil, nil, true, 7, bundle)
	if err != nil {
		t.Fatal(err)
	}
	if len(sha) != 64 {
		t.Fatalf("sha %q", sha)
	}

	bundle.Link = auditpush.Link{ScanID: 7, StatementSHA256: sha}
	if _, err := pushBundle(db, bundle); err != nil {
		t.Fatal(err)
	}

	rows := queryRows(t, db, `SELECT statement_sha256, scan_id FROM corral_audits`)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0][0] != sha {
		t.Errorf("statement_sha256 = %v, want %s", rows[0][0], sha)
	}
	if rows[0][1] != int64(7) {
		t.Errorf("scan_id = %v, want 7", rows[0][1])
	}

	// And the statement names the scan and the rows it produced.
	stmt := readStatement(t, att)
	if stmt.Predicate.ScanID != 7 {
		t.Fatalf("statement scanId = %d, want 7", stmt.Predicate.ScanID)
	}
	if stmt.Predicate.WarehouseRowsSHA256 == "" {
		t.Fatal("statement must carry the hash of the rows it will be pushed with")
	}
}

// TestWriteAuditStatementHonestlyRecordsScanIDZero is the documented, honest
// value when --record was not given: the ledger never ran, so 0 is not a
// sentinel for "unknown" — it is the actual absence of a ledger row.
func TestWriteAuditStatementHonestlyRecordsScanIDZero(t *testing.T) {
	dir := t.TempDir()
	att := filepath.Join(dir, "att.json")
	rep := oneAuditedFileReport()

	if _, err := writeAuditStatement(att, dir, rep, map[string]string{"writer": "m"}, nil, nil, true, 0, oneAuditedFileBundle(0)); err != nil {
		t.Fatal(err)
	}
	stmt := readStatement(t, att)
	if stmt.Predicate.ScanID != 0 {
		t.Fatalf("statement scanId = %d, want 0", stmt.Predicate.ScanID)
	}
}

// TestPushWithoutAttestCarriesNoStatementSHA — without --attest, a push still
// happens, but the row's statement_sha256 is empty rather than fabricated: it is
// traceable only when --attest actually produced a statement.
func TestPushWithoutAttestCarriesNoStatementSHA(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "w.duckdb")

	if _, err := pushBundle(db, oneAuditedFileBundle(0)); err != nil {
		t.Fatal(err)
	}

	rows := queryRows(t, db, `SELECT statement_sha256, scan_id FROM corral_audits`)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0][0] != "" {
		t.Errorf("statement_sha256 = %v, want empty", rows[0][0])
	}
	if rows[0][1] != int64(0) {
		t.Errorf("scan_id = %v, want 0", rows[0][1])
	}
}
