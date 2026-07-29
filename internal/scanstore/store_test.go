// SPDX-License-Identifier: Elastic-2.0

package scanstore_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"

	"github.com/pdbethke/corralai/internal/scanstore"
)

func ptr(f float64) *float64 { return &f }

func TestRecordRoundTripsEveryDisposition(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "scans.duckdb")
	st, err := scanstore.Open(dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	scan := scanstore.Scan{
		Owner: "local", Repo: "demo", Commit: "abc123",
		Substrate: "workspace", EngineVersion: "test-engine", ModelSet: "test-models",
		TotalFiles: 4, Candidates: 2, Audited: 1, KillRate: 0.5,
		StartedAt: time.Unix(1700000000, 0).UTC(), FinishedAt: time.Unix(1700000060, 0).UTC(),
	}
	files := []scanstore.File{
		{Path: "a.go", Lang: "go", Disposition: "audited", KillRate: ptr(0.5), Survivors: 1, Gradable: true, Evidence: "proven"},
		{Path: "b.go", Lang: "go", Disposition: "rejected", Reason: "no-paired-test", Evidence: "paired"},
		{Path: "c.md", Disposition: "rejected", Reason: "no-language", Evidence: "paired"},
		{Path: "d.go", Lang: "go", Disposition: "rejected", Reason: "not-selected", PreflightState: "not-executed", Evidence: "coverage"},
	}

	id, err := st.Record(context.Background(), scan, files)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if id == 0 {
		t.Fatal("Record returned scan id 0; the header row must be addressable")
	}

	// Every file must come back, keyed to this scan, with its evidence intact.
	got, err := st.FilesForScan(context.Background(), id)
	if err != nil {
		t.Fatalf("FilesForScan: %v", err)
	}
	if len(got) != len(files) {
		t.Fatalf("read back %d files, wrote %d", len(got), len(files))
	}
	byPath := map[string]scanstore.File{}
	for _, f := range got {
		byPath[f.Path] = f
	}
	if d := byPath["d.go"]; d.PreflightState != "not-executed" || d.Evidence != "coverage" {
		t.Fatalf("d.go round-tripped as %+v; preflight state and evidence must survive", d)
	}
	if a := byPath["a.go"]; a.KillRate == nil || *a.KillRate != 0.5 {
		t.Fatalf("a.go kill rate round-tripped as %v, want 0.5", a.KillRate)
	}
	if b := byPath["b.go"]; b.KillRate != nil {
		t.Fatalf("b.go has a kill rate (%v); a rejected file was never scored and must read back NULL, not 0", *b.KillRate)
	}
}

// TestOpenMigrationIsIdempotent mirrors migrateBugcatchObservations'
// contract: opening an already-current store must run zero ALTERs and must
// not error, and opening a store that predates a later column must add
// exactly that column. A migration that silently swallowed every ALTER
// error would make a genuinely broken migration indistinguishable from an
// already-applied one — this test is what would catch that.
func TestOpenMigrationIsIdempotent(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "scans.duckdb")

	st, err := scanstore.Open(dsn)
	if err != nil {
		t.Fatalf("Open (1st): %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Re-opening an already-current store must be a clean no-op.
	st2, err := scanstore.Open(dsn)
	if err != nil {
		t.Fatalf("Open (2nd, already current): %v", err)
	}
	if err := st2.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// A store created before a later column existed: build the original
	// table shape by hand, then confirm Open adds the missing column
	// without erroring.
	dsn2 := filepath.Join(t.TempDir(), "legacy.duckdb")
	db, err := sql.Open("duckdb", dsn2)
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

	st3, err := scanstore.Open(dsn2)
	if err != nil {
		t.Fatalf("Open (legacy, missing columns): %v", err)
	}
	defer st3.Close()

	scan := scanstore.Scan{Owner: "local", Repo: "demo", Commit: "abc123"}
	files := []scanstore.File{
		{Path: "a.go", Lang: "go", Disposition: "audited", KillRate: ptr(1.0), Evidence: "proven", PreflightState: "ran"},
	}
	id, err := st3.Record(context.Background(), scan, files)
	if err != nil {
		t.Fatalf("Record after migration: %v", err)
	}
	got, err := st3.FilesForScan(context.Background(), id)
	if err != nil {
		t.Fatalf("FilesForScan after migration: %v", err)
	}
	if len(got) != 1 || got[0].PreflightState != "ran" || got[0].Evidence != "proven" {
		t.Fatalf("migrated columns did not round-trip: %+v", got)
	}
}
