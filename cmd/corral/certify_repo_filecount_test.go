// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"testing"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// The count must survive every boundary (AGENTS.md: a field-by-field
// converter fails open on whatever field nobody remembered to add):
// File -> buildAuditRows -> entry on disk -> ReadLedgerDir -> fileFromRow,
// and -> LoadDir -> corral_audits.files, with NULL for a one-file row.
func TestFilesCountSurvivesTheLedgerAndTheWarehouse(t *testing.T) {
	files := []scanstore.File{
		{Path: "many.go", Lang: "go", Disposition: "excluded", Reason: "test file", Files: 50},
		{Path: "one.go", Lang: "go", Disposition: "excluded", Reason: "test file"},
	}
	rows := buildAuditRows(files, 1, bundleMeta{Repo: "o/r", Commit: "deadbeef"})
	if rows[0].Files != 50 || rows[1].Files != 0 {
		t.Fatalf("buildAuditRows dropped the count: got %d and %d, want 50 and 0", rows[0].Files, rows[1].Files)
	}
	dir := t.TempDir()
	b := auditpush.Bundle{Scan: auditpush.ScanRow{Repo: "o/r", Commit: "deadbeef", ScanID: 1, CorralVersion: "vtest"}, Files: rows}
	if _, err := auditpush.PushBundle(dir, b); err != nil {
		t.Fatal(err)
	}

	entries, err := auditpush.ReadLedgerDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("ReadLedgerDir: %d entries, err %v", len(entries), err)
	}
	got := map[string]int{}
	for _, r := range entries[0].Bundle.Files {
		got[r.Path] = fileFromRow(r).Files
	}
	if got["many.go"] != 50 || got["one.go"] != 0 {
		t.Errorf("fileFromRow lost the count: %v, want many.go 50 and one.go 0", got)
	}

	db, err := auditpush.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var many int
	if err := db.QueryRow(`SELECT files FROM corral_audits WHERE path = 'many.go'`).Scan(&many); err != nil || many != 50 {
		t.Errorf("warehouse files for many.go = %d, err %v, want 50", many, err)
	}
	var nulls int
	if err := db.QueryRow(`SELECT count(*) FROM corral_audits WHERE path = 'one.go' AND files IS NULL`).Scan(&nulls); err != nil || nulls != 1 {
		t.Errorf("a one-file row must write NULL files (got %d NULL rows, err %v)", nulls, err)
	}

	// The reader (read.go) gives NULL back as 0 and the count back as itself.
	read, err := auditpush.ReadBundle(db, "o/r", 1)
	if err != nil {
		t.Fatal(err)
	}
	back := map[string]int{}
	for _, r := range read.Files {
		back[r.Path] = r.Files
	}
	if back["many.go"] != 50 || back["one.go"] != 0 {
		t.Errorf("read.go's reader: %v, want many.go 50 and one.go 0", back)
	}
}
