// SPDX-License-Identifier: Elastic-2.0

// Package scanstore is corral's local CACHE for `certify --repo`: the goal
// cache and the selection cache (goal_cache.go, selection_cache.go), keyed
// by content, in the DuckDB file `--cache-db` names. Nothing a verdict
// rests on lives only here; deleting the file costs a re-derivation and one
// instrumented coverage run, never a fact. The record is the ledger
// directory (internal/auditpush's ledgerdir.go: one signed, hash-linked
// entry per scan, DuckDB as its view), and this package holds no copy of
// it — the row types in rows.go are the in-memory shape a scan builds
// before the bundle, nothing more.
package scanstore

import (
	"database/sql"
	"fmt"

	_ "github.com/marcboeker/go-duckdb/v2"
)

// Store is the open cache file.
type Store struct{ db *sql.DB }

// Open opens (creating if absent) the cache at dsn and ensures its two
// tables exist. CREATE TABLE IF NOT EXISTS on every Open is the whole
// story: the tables are content-addressed and have never needed a column
// migration.
func Open(dsn string) (*Store, error) {
	db, err := sql.Open("duckdb", dsn)
	if err != nil {
		return nil, fmt.Errorf("scanstore: open %q: %w", dsn, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("scanstore: open %q: %w", dsn, err)
	}
	// goal_cache is content-addressed: one row per (path, source_digest,
	// model, engine_prompt_rev), reused across every scan that asks the
	// same question about the same bytes.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS goal_cache (
		path VARCHAR, source_digest VARCHAR, model VARCHAR, engine_prompt_rev VARCHAR,
		goal VARCHAR, provenance VARCHAR, created_at TIMESTAMP,
		UNIQUE (path, source_digest, model, engine_prompt_rev)
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("scanstore: create goal_cache table: %w", err)
	}
	// selection_cache is content-addressed on (tree_digest, cmd_digest,
	// plugin, substrate): the evidence ONE instrumented run of a project's
	// suite produces is a property of the WHOLE checkout, the exact
	// instrumented command, and the substrate it ran on — a jail run's
	// evidence can be degraded in ways specific to that sandbox, and must
	// never be served to a workspace run over the identical tree (see
	// SelectionCacheGet).
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS selection_cache (
		tree_digest VARCHAR, cmd_digest VARCHAR, plugin VARCHAR, substrate VARCHAR,
		raw BLOB, note VARCHAR, created_at TIMESTAMP,
		UNIQUE (tree_digest, cmd_digest, plugin, substrate)
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("scanstore: create selection_cache table: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the file. DuckDB is single-writer per file, so a caller
// holds the store only for the instant of a lookup or a write.
func (s *Store) Close() error { return s.db.Close() }
