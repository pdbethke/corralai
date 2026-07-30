// SPDX-License-Identifier: Elastic-2.0

package scanstore_test

import (
	"context"
	"database/sql"
	"math"
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
		TotalFiles: 4, Candidates: 2, Audited: 1, KillRate: ptr(0.5),
		StartedAt: time.Unix(1700000000, 0).UTC(), FinishedAt: time.Unix(1700000060, 0).UTC(),
	}
	files := []scanstore.File{
		{Path: "a.go", Lang: "go", Disposition: "audited", KillRate: ptr(0.5), Survivors: 1, Gradable: true, Evidence: "proven"},
		{Path: "b.go", Lang: "go", Disposition: "rejected", Reason: "no-paired-test", Evidence: "paired"},
		{Path: "c.md", Disposition: "rejected", Reason: "no-language", Evidence: "paired"},
		{Path: "d.go", Lang: "go", Disposition: "rejected", Reason: "not-selected", PreflightState: "not-executed", Evidence: "coverage"},
		{Path: "e.py", Lang: "python", Disposition: "rejected", Reason: "executor-error", Evidence: "proven",
			Detail: "python toolchain unavailable — refusing to grade: pytest not importable"},
		{Path: "f.py", Lang: "python", Disposition: "audited", KillRate: ptr(0.46), Survivors: 13, Gradable: true,
			Evidence: "proven", TimedOut: true},
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
	if e := byPath["e.py"]; e.Detail != "python toolchain unavailable — refusing to grade: pytest not importable" {
		t.Fatalf("e.py Detail round-tripped as %q; the executor's diagnosis must survive into the ledger, not just the reason code", e.Detail)
	}
	if f := byPath["f.py"]; !f.TimedOut {
		t.Fatalf("f.py TimedOut round-tripped as false, want true — a banked-but-unconverged score must be distinguishable in the ledger, not just by its reason column")
	}
	if a := byPath["a.go"]; a.TimedOut {
		t.Fatalf("a.go TimedOut round-tripped as true, want false — a cleanly-converged row must not carry the marker")
	}
}

// TestZeroAuditScanKillRateStoresNullNotNaN pins the Task 1 review defect:
// DuckDB sorts NaN as larger than any other DOUBLE value, so a scan.KillRate
// of math.NaN() (reposcan.RepoReport.KillRate's deliberate value when
// Audited == 0) stored as a bare float64 would make a scan that measured
// NOTHING look like the ledger's BEST-scoring scan under MAX() or
// `ORDER BY kill_rate DESC LIMIT 1`. This proves the *float64 fix: a NaN
// scan reads back with a NULL kill_rate (Go nil), and a real score
// (0.9) still wins both aggregate forms with the NaN scan present in the
// same table.
func TestZeroAuditScanKillRateStoresNullNotNaN(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "scans.duckdb")
	st, err := scanstore.Open(dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	nan := math.NaN()
	zeroAuditID, err := st.Record(context.Background(), scanstore.Scan{
		Owner: "local", Repo: "demo", Commit: "nothing-audited",
		TotalFiles: 1, Candidates: 1, Audited: 0, KillRate: &nan,
	}, nil)
	if err != nil {
		t.Fatalf("Record (zero-audit, NaN kill rate): %v", err)
	}

	good := 0.9
	goodID, err := st.Record(context.Background(), scanstore.Scan{
		Owner: "local", Repo: "demo", Commit: "real-score",
		TotalFiles: 1, Candidates: 1, Audited: 1, KillRate: &good,
	}, nil)
	if err != nil {
		t.Fatalf("Record (real score): %v", err)
	}

	// Read the NaN scan's row back directly: kill_rate must be SQL NULL
	// (Go nil), never a float that happens to be NaN — sql.Scan into
	// *float64 would itself error on a NULL if the column round-tripped
	// NaN as a literal value instead.
	db, err := sql.Open("duckdb", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	var gotNull sql.NullFloat64
	if err := db.QueryRow(`SELECT kill_rate FROM scans WHERE id = ?`, zeroAuditID).Scan(&gotNull); err != nil {
		t.Fatalf("select zero-audit kill_rate: %v", err)
	}
	if gotNull.Valid {
		t.Fatalf("zero-audit scan's kill_rate = %v (valid), want SQL NULL", gotNull.Float64)
	}

	// The inversion this test exists to catch: with the NaN scan sitting in
	// the same table, MAX(kill_rate) must still surface the REAL score, not
	// NaN displacing it.
	var maxRate sql.NullFloat64
	if err := db.QueryRow(`SELECT MAX(kill_rate) FROM scans WHERE id IN (?, ?)`, zeroAuditID, goodID).Scan(&maxRate); err != nil {
		t.Fatalf("select MAX(kill_rate): %v", err)
	}
	if !maxRate.Valid || maxRate.Float64 != good {
		t.Fatalf("MAX(kill_rate) = %v, want the real score %v — a NaN row must not displace it", maxRate, good)
	}

	// And ORDER BY kill_rate DESC LIMIT 1 must surface the real-score scan
	// FIRST, never the never-measured one.
	var topCommit string
	if err := db.QueryRow(`SELECT commit FROM scans WHERE id IN (?, ?) ORDER BY kill_rate DESC LIMIT 1`, zeroAuditID, goodID).Scan(&topCommit); err != nil {
		t.Fatalf("select ORDER BY kill_rate DESC LIMIT 1: %v", err)
	}
	if topCommit != "real-score" {
		t.Fatalf("ORDER BY kill_rate DESC LIMIT 1 surfaced commit %q, want %q (the never-measured scan must not rank first)", topCommit, "real-score")
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
