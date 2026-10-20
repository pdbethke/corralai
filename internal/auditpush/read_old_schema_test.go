// SPDX-License-Identifier: Elastic-2.0

package auditpush

import (
	"database/sql"
	"testing"
)

// A warehouse no push of this version has met yet lacks the columns the
// additive migration adds — and `verify --db` reads it without migrating.
// Selecting tree_dirty / repo_identity unconditionally failed every such
// read with "Referenced column 'tree_dirty' not found" (final whole-branch
// review, 2026-10-20). The row reads, and the absent columns read as not
// recorded: nil and "", never a measured false.
func TestReadingAWarehouseThatPredatesTheLateScanColumns(t *testing.T) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, ddl := range schemaDDL {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	// corral_scans as the previous release left it: every column but the
	// two this branch added.
	for _, col := range []string{"tree_dirty", "repo_identity"} {
		if _, err := db.Exec("ALTER TABLE corral_scans DROP COLUMN " + col); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO corral_scans (scan_uid, ts, repo, run_url, scan_id, commit_sha,
	    corral_version, substrate, host, diff_base, candidates, audited, model_calls,
	    source_pushed, statement_sha256)
	    VALUES ('uid-1', now(), 'r', '', 7, 'abc', 'v', 'jail', 'h', '', 1, 1, 0, false, 'stmt-sha')`); err != nil {
		t.Fatal(err)
	}

	got, err := LocateScans(db, "r", 7, "stmt-sha")
	if err != nil {
		t.Fatalf("LocateScans on a pre-migration warehouse: %v", err)
	}
	if len(got) != 1 || got[0].Repo != "r" || got[0].Commit != "abc" {
		t.Fatalf("want the one row back, got %+v", got)
	}
	if got[0].TreeDirty != nil || got[0].RepoIdentity != "" {
		t.Fatalf("an absent column must read as not recorded, got TreeDirty=%v RepoIdentity=%q", got[0].TreeDirty, got[0].RepoIdentity)
	}

	s, ok, err := readScanRow(db, "repo = ? AND scan_id = ?", "r", int64(7))
	if err != nil || !ok {
		t.Fatalf("readScanRow on a pre-migration warehouse: ok=%v err=%v", ok, err)
	}
	if s.TreeDirty != nil || s.RepoIdentity != "" {
		t.Fatalf("an absent column must read as not recorded, got TreeDirty=%v RepoIdentity=%q", s.TreeDirty, s.RepoIdentity)
	}
}
