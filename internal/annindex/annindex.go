// SPDX-License-Identifier: Elastic-2.0

// Package annindex provides a threshold-gated HNSW accelerator for DuckDB
// vector stores. When a vector column exceeds a configurable row count, it
// migrates the column from FLOAT[] (list) to FLOAT[N] (fixed-size array) and
// creates an HNSW index (via the vss extension) for sub-linear ANN search.
// Below the threshold the column stays as-is and callers use brute-force.
//
// Every fallible path returns active=false (brute-force) rather than an error
// that would break the caller's search. Errors that are structural (e.g. the
// table doesn't exist) propagate from Ensure, but the caller must treat them
// as "use brute-force" too — the contract is: active=false ⟹ brute-force.
//
// Probe-verified SQL (DuckDB 1.4.x / go-duckdb v2.4.3):
//
//	INSTALL vss; LOAD vss; SET hnsw_enable_experimental_persistence = true
//	CREATE INDEX <name> ON <table> USING HNSW (<col>) WITH (metric='cosine')
//	array_cosine_distance(<col>, [...]::FLOAT[N])   ← triggers HNSW index scan
//	ALTER TABLE <t> ALTER COLUMN <v> TYPE FLOAT[N] USING <v>::FLOAT[N]
//	duckdb_indexes.sql LIKE '%HNSW%' to detect existing HNSW index by name
package annindex

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
)

// Config controls the ANN accelerator.
type Config struct {
	Threshold int  // min non-null rows before HNSW is built
	Disabled  bool // if true, always brute-force regardless of row count
}

// ConfigFromEnv reads Config from environment variables.
// CORRALAI_ANN_THRESHOLD overrides the default threshold (20 000).
// CORRALAI_ANN_DISABLE=1 forces brute-force mode permanently.
func ConfigFromEnv() Config {
	c := Config{Threshold: 20000}
	if v, err := strconv.Atoi(os.Getenv("CORRALAI_ANN_THRESHOLD")); err == nil && v > 0 {
		c.Threshold = v
	}
	c.Disabled = os.Getenv("CORRALAI_ANN_DISABLE") == "1"
	return c
}

// Loaded installs and loads the vss extension, then enables HNSW persistence.
// Returns false on any failure — the caller must treat false as "no HNSW
// available on this connection, stay in brute-force mode forever".
// Call once at store Open() on the file-backed *sql.DB.
func Loaded(db *sql.DB) bool {
	for _, s := range []string{
		"INSTALL vss",
		"LOAD vss",
		"SET hnsw_enable_experimental_persistence = true",
	} {
		if _, err := db.Exec(s); err != nil {
			return false
		}
	}
	return true
}

// Ensure is idempotent maintenance on one vector column.
//
//   - Below threshold: drops the HNSW index (if any) and returns active=false.
//   - Mixed or zero dim: returns active=false without touching anything.
//   - Above threshold, consistent dim: migrates FLOAT[]→FLOAT[N] if needed
//     then creates the HNSW index if absent.
//
// active=false always means "use brute-force" — that is the SOLE signal.
// Ensure never returns a non-nil err (migrate/HNSW failures are logged and
// swallowed to active=false), so a caller's idiomatic `if err != nil { return err }`
// can never abort a search. The err return is retained for API consistency.
func Ensure(db *sql.DB, table, col, idxName string, cfg Config) (active bool, dim int, err error) {
	if cfg.Disabled {
		return false, 0, nil
	}

	// Gate 1: row count.
	var n int
	if err = db.QueryRow(fmt.Sprintf(
		"SELECT count(*) FROM %s WHERE %s IS NOT NULL", table, col,
	)).Scan(&n); err != nil {
		return false, 0, nil // count failed → brute-force
	}
	if n < cfg.Threshold {
		// Store fell below (or never reached) threshold — remove stale index.
		_, _ = db.Exec(fmt.Sprintf("DROP INDEX IF EXISTS %s", idxName))
		return false, 0, nil
	}

	// Gate 2: one consistent dimension across all non-null rows.
	var lo, hi int
	if err = db.QueryRow(fmt.Sprintf(
		"SELECT min(len(%s)), max(len(%s)) FROM %s WHERE %s IS NOT NULL",
		col, col, table, col,
	)).Scan(&lo, &hi); err != nil || lo != hi || lo == 0 {
		// Mixed dim or query failure → brute-force, no migration.
		return false, 0, nil
	}
	dim = lo

	// Step 1: ensure column is FLOAT[dim] (not FLOAT[] list).
	// Any failure here degrades to brute-force — we swallow the error (log it)
	// and return active=false so callers can never propagate it and break search.
	if migrateErr := migrateToArray(db, table, col, dim); migrateErr != nil {
		log.Printf("annindex: migrate %s.%s failed, using brute-force: %v", table, col, migrateErr)
		return false, 0, nil
	}

	// Step 2: create HNSW index if not already present. Same degradation contract.
	if idxErr := ensureHNSW(db, table, col, idxName); idxErr != nil {
		log.Printf("annindex: HNSW create on %s.%s failed, using brute-force: %v", table, col, idxErr)
		return false, 0, nil
	}

	return true, dim, nil
}

// Rebuild drops and recreates the HNSW index. Call after bulk deletes to
// prevent stale graph edges from degrading search quality.
// The column must already be FLOAT[dim] (call Ensure first).
func Rebuild(db *sql.DB, table, col, idxName string, _ int) error {
	if _, err := db.Exec(fmt.Sprintf("DROP INDEX IF EXISTS %s", idxName)); err != nil {
		return fmt.Errorf("annindex: drop index %s: %w", idxName, err)
	}
	return ensureHNSW(db, table, col, idxName)
}

// migrateToArray migrates the named column from FLOAT[] (LIST) to FLOAT[dim]
// (fixed ARRAY) if it isn't already the target type. No-op when already correct.
// Uses ALTER COLUMN … TYPE … USING (probe-verified); no fallback needed.
func migrateToArray(db *sql.DB, table, col string, dim int) error {
	var dataType string
	err := db.QueryRow(
		`SELECT data_type FROM information_schema.columns
		 WHERE table_name = ? AND column_name = ?`,
		table, col,
	).Scan(&dataType)
	if err != nil {
		return fmt.Errorf("annindex: inspect column type %s.%s: %w", table, col, err)
	}

	target := fmt.Sprintf("FLOAT[%d]", dim)
	if strings.EqualFold(dataType, target) {
		return nil // already the right fixed-array type
	}

	// Probe-verified: ALTER COLUMN with USING cast works in DuckDB 1.4.x.
	alterSQL := fmt.Sprintf(
		"ALTER TABLE %s ALTER COLUMN %s TYPE %s USING %s::%s",
		table, col, target, col, target,
	)
	if _, err := db.Exec(alterSQL); err != nil {
		// Fallback: add new column, copy, drop old, rename.
		// (Kept as defence-in-depth; ALTER COLUMN works in 1.4.x probe.)
		steps := []string{
			fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s_ann_tmp %s", table, col, target),
			fmt.Sprintf("UPDATE %s SET %s_ann_tmp = %s::%s", table, col, col, target),
			fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s", table, col),
			fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s_ann_tmp TO %s", table, col, col),
		}
		for _, s := range steps {
			if _, err2 := db.Exec(s); err2 != nil {
				return fmt.Errorf("annindex: migrate column %s.%s (fallback): %w", table, col, err2)
			}
		}
	}
	return nil
}

// ensureHNSW creates the HNSW index on table.col if it does not yet exist.
// Detection uses duckdb_indexes.sql which contains "HNSW" for all HNSW indexes.
func ensureHNSW(db *sql.DB, table, col, idxName string) error {
	var cnt int
	err := db.QueryRow(
		`SELECT count(*) FROM duckdb_indexes
		 WHERE index_name = ? AND table_name = ?`,
		idxName, table,
	).Scan(&cnt)
	if err != nil {
		return fmt.Errorf("annindex: check index %s: %w", idxName, err)
	}
	if cnt > 0 {
		return nil // index already exists — idempotent
	}
	_, err = db.Exec(fmt.Sprintf(
		"CREATE INDEX %s ON %s USING HNSW (%s) WITH (metric='cosine')",
		idxName, table, col,
	))
	if err != nil {
		return fmt.Errorf("annindex: create HNSW index %s: %w", idxName, err)
	}
	return nil
}
