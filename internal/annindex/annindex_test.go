// SPDX-License-Identifier: Elastic-2.0

package annindex

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/marcboeker/go-duckdb/v2"
)

// openDB opens a file-backed DuckDB at dir/name.db and creates the docs table
// with a FLOAT[] embedding column. Returns the *sql.DB ready for seeding.
func openDB(t *testing.T, name string) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("duckdb", filepath.Join(dir, name+".db"))
	if err != nil {
		t.Fatalf("open duckdb: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`CREATE TABLE docs (id BIGINT, embedding FLOAT[])`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	return db
}

// seedRows inserts n deterministic rows with the given embedding dimension.
// Vectors are [float32(i), float32(i)+0.01, ..., float32(i)+(dim-1)*0.01].
func seedRows(t *testing.T, db *sql.DB, n, dim int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		parts := make([]string, dim)
		for j := 0; j < dim; j++ {
			parts[j] = fmt.Sprintf("%g", float32(i)+float32(j)*0.01)
		}
		lit := "[" + strings.Join(parts, ",") + "]::FLOAT[]"
		if _, err := db.Exec(fmt.Sprintf("INSERT INTO docs VALUES (%d, %s)", i, lit)); err != nil {
			t.Fatalf("insert row %d (dim %d): %v", i, dim, err)
		}
	}
}

// checkHNSWExists reports whether an HNSW index named idxName exists on the
// docs table (uses duckdb_indexes which has no index_type column in 1.4.x;
// presence is sufficient — non-HNSW user-defined indexes on 'docs' are absent
// in tests).
func checkHNSWExists(t *testing.T, db *sql.DB, idxName string) bool {
	t.Helper()
	var cnt int
	err := db.QueryRow(
		`SELECT count(*) FROM duckdb_indexes WHERE index_name=? AND table_name='docs'`,
		idxName,
	).Scan(&cnt)
	if err != nil {
		t.Fatalf("duckdb_indexes query: %v", err)
	}
	return cnt > 0
}

// colType returns the data_type of docs.embedding from information_schema.
func colType(t *testing.T, db *sql.DB) string {
	t.Helper()
	var dt string
	err := db.QueryRow(
		`SELECT data_type FROM information_schema.columns
		 WHERE table_name='docs' AND column_name='embedding'`,
	).Scan(&dt)
	if err != nil {
		t.Fatalf("col type query: %v", err)
	}
	return dt
}

// requireVSSOrSkip loads vss and skips the test with an explanation if it fails.
func requireVSSOrSkip(t *testing.T, db *sql.DB) {
	t.Helper()
	if !Loaded(db) {
		t.Skip("vss extension could not be loaded in this environment — " +
			"HNSW-dependent tests require vss (core extension in DuckDB 1.4.x, " +
			"should be available on this workstation)")
	}
}

// ─── Tests ────────────────────────────────────────────────────────────────────

// TestBelowThresholdNoIndex: below threshold → active=false, no HNSW index,
// column stays FLOAT[].
func TestBelowThresholdNoIndex(t *testing.T) {
	db := openDB(t, "below")
	requireVSSOrSkip(t, db)
	seedRows(t, db, 10, 8)

	cfg := Config{Threshold: 100}
	active, dim, err := Ensure(db, "docs", "embedding", "idx_hnsw", cfg)

	if err != nil {
		t.Fatalf("Ensure error: %v", err)
	}
	if active {
		t.Errorf("want active=false (below threshold), got true")
	}
	if dim != 0 {
		t.Errorf("want dim=0, got %d", dim)
	}
	if checkHNSWExists(t, db, "idx_hnsw") {
		t.Error("HNSW index must not exist below threshold")
	}
	if ct := colType(t, db); ct != "FLOAT[]" {
		t.Errorf("column must remain FLOAT[] below threshold, got %q", ct)
	}
}

// TestAboveThresholdCreatesHNSW: above threshold → active=true, dim=8, HNSW
// index exists, column migrated to FLOAT[8]. Calling Ensure again is idempotent.
func TestAboveThresholdCreatesHNSW(t *testing.T) {
	db := openDB(t, "above")
	requireVSSOrSkip(t, db)
	seedRows(t, db, 200, 8)

	cfg := Config{Threshold: 100}
	active, dim, err := Ensure(db, "docs", "embedding", "idx_hnsw", cfg)

	if err != nil {
		t.Fatalf("Ensure error: %v", err)
	}
	if !active {
		t.Fatal("want active=true above threshold")
	}
	if dim != 8 {
		t.Errorf("want dim=8, got %d", dim)
	}
	if !checkHNSWExists(t, db, "idx_hnsw") {
		t.Error("HNSW index must exist above threshold")
	}
	if ct := colType(t, db); ct != "FLOAT[8]" {
		t.Errorf("column must be FLOAT[8] after migration, got %q", ct)
	}

	// Idempotent: second Ensure must not error and must return active=true.
	active2, dim2, err2 := Ensure(db, "docs", "embedding", "idx_hnsw", cfg)
	if err2 != nil {
		t.Fatalf("second Ensure error: %v", err2)
	}
	if !active2 {
		t.Error("second Ensure: want active=true (idempotent)")
	}
	if dim2 != 8 {
		t.Errorf("second Ensure: want dim=8, got %d", dim2)
	}
}

// TestMixedDimSkips: rows with mixed embedding dimensions → active=false, no
// migration, no index created.
func TestMixedDimSkips(t *testing.T) {
	db := openDB(t, "mixed")
	requireVSSOrSkip(t, db)
	// Seed above threshold with two different dims.
	seedRows(t, db, 120, 8) // first 120 rows: dim 8
	seedRows(t, db, 80, 4)  // next 80 rows: dim 4

	cfg := Config{Threshold: 100}
	active, _, err := Ensure(db, "docs", "embedding", "idx_hnsw", cfg)

	if err != nil {
		t.Fatalf("Ensure error: %v", err)
	}
	if active {
		t.Error("want active=false for mixed-dim data")
	}
	if checkHNSWExists(t, db, "idx_hnsw") {
		t.Error("HNSW index must not exist for mixed-dim data")
	}
	// Column must not have been migrated (still FLOAT[]).
	if ct := colType(t, db); ct != "FLOAT[]" {
		t.Errorf("column must stay FLOAT[] for mixed-dim data, got %q", ct)
	}
}

// TestDisabledForcesBruteforce: Config{Disabled:true} always returns
// active=false regardless of row count.
func TestDisabledForcesBruteforce(t *testing.T) {
	db := openDB(t, "disabled")
	requireVSSOrSkip(t, db)
	seedRows(t, db, 200, 8)

	cfg := Config{Threshold: 100, Disabled: true}
	active, dim, err := Ensure(db, "docs", "embedding", "idx_hnsw", cfg)

	if err != nil {
		t.Fatalf("Ensure error: %v", err)
	}
	if active {
		t.Error("want active=false when Disabled=true")
	}
	if dim != 0 {
		t.Errorf("want dim=0 when Disabled, got %d", dim)
	}
	if checkHNSWExists(t, db, "idx_hnsw") {
		t.Error("HNSW index must not be created when Disabled=true")
	}
}

// TestRebuild: after Ensure builds the index, delete rows and call Rebuild;
// index must be recreated without error.
func TestRebuild(t *testing.T) {
	db := openDB(t, "rebuild")
	requireVSSOrSkip(t, db)
	seedRows(t, db, 200, 8)

	cfg := Config{Threshold: 100}
	active, dim, err := Ensure(db, "docs", "embedding", "idx_hnsw", cfg)
	if err != nil || !active {
		t.Fatalf("pre-Rebuild Ensure: active=%v err=%v", active, err)
	}
	if !checkHNSWExists(t, db, "idx_hnsw") {
		t.Fatal("HNSW index must exist before Rebuild")
	}

	// Delete some rows to simulate a store that needs index refresh.
	if _, err := db.Exec(`DELETE FROM docs WHERE id % 3 = 0`); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if err := Rebuild(db, "docs", "embedding", "idx_hnsw", dim); err != nil {
		t.Fatalf("Rebuild error: %v", err)
	}
	if !checkHNSWExists(t, db, "idx_hnsw") {
		t.Error("HNSW index must exist after Rebuild")
	}
}

// TestConfigFromEnv: basic smoke test of env-var parsing.
func TestConfigFromEnv(t *testing.T) {
	// Default
	cfg := ConfigFromEnv()
	if cfg.Threshold != 20000 {
		t.Errorf("default threshold: want 20000, got %d", cfg.Threshold)
	}
	if cfg.Disabled {
		t.Error("default Disabled must be false")
	}

	// Override threshold
	t.Setenv("CORRALAI_ANN_THRESHOLD", "500")
	cfg = ConfigFromEnv()
	if cfg.Threshold != 500 {
		t.Errorf("threshold override: want 500, got %d", cfg.Threshold)
	}

	// Disable
	t.Setenv("CORRALAI_ANN_DISABLE", "1")
	cfg = ConfigFromEnv()
	if !cfg.Disabled {
		t.Error("CORRALAI_ANN_DISABLE=1 must set Disabled=true")
	}
}
