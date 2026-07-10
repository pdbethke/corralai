// SPDX-License-Identifier: Elastic-2.0

// Package buildstore is corralai's signed build-record ledger: a DuckDB-backed
// store for `corral certify` build attestations (SLSA statement + Ed25519
// signature per build) and the loader for the persisted Ed25519 signing key
// those attestations are signed with. Task 3's report_build brain tool
// consumes both: it signs a build with the loaded key, then Saves the
// resulting statement + signature here for later Get/verify.
package buildstore

import (
	"crypto/ed25519"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"
)

var now = func() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// Store is a DuckDB-backed table of signed build records. dsn is kept opaque
// (never parsed/validated as a filesystem path) so a local `.duckdb` file and
// a MotherDuck `md:` DSN both work unchanged — federation is a config flip.
type Store struct{ db *sql.DB }

// Open opens (creating if absent) the build_records store at dsn.
func Open(dsn string) (*Store, error) {
	db, err := sql.Open("duckdb", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS build_records (
		id BIGINT PRIMARY KEY,
		repo VARCHAR,
		commit_sha VARCHAR,
		branch VARCHAR,
		actor VARCHAR,
		head VARCHAR,
		signature VARCHAR,
		statement JSON,
		created_ts DOUBLE)`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`CREATE SEQUENCE IF NOT EXISTS build_record_id START 1`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// Save inserts a signed build record and returns its assigned id.
func (s *Store) Save(repo, commit, branch, actor, head, sig, statementJSON string) (int64, error) {
	var id int64
	err := s.db.QueryRow(
		`INSERT INTO build_records (id, repo, commit_sha, branch, actor, head, signature, statement, created_ts)
		 VALUES (nextval('build_record_id'), ?, ?, ?, ?, ?, ?, ?, ?)
		 RETURNING id`,
		repo, commit, branch, actor, head, sig, statementJSON, now()).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("buildstore: save: %w", err)
	}
	return id, nil
}

// Get returns the stored SLSA statement (parsed to a map) and signature for
// id, plus false if no record with that id exists.
func (s *Store) Get(id int64) (map[string]any, bool, error) {
	var statement any
	var sig string
	err := s.db.QueryRow(
		`SELECT statement, signature FROM build_records WHERE id = ?`, id).Scan(&statement, &sig)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("buildstore: get: %w", err)
	}
	m, err := statementToMap(statement)
	if err != nil {
		return nil, false, fmt.Errorf("buildstore: get: decoding stored statement: %w", err)
	}
	m["signature"] = sig
	return m, true, nil
}

// statementToMap normalizes the driver's representation of the JSON column:
// go-duckdb decodes JSON directly to map[string]any for some paths, and to
// its raw string form (still needing json.Unmarshal) for others; handle both.
func statementToMap(v any) (map[string]any, error) {
	switch t := v.(type) {
	case map[string]any:
		return t, nil
	case string:
		var m map[string]any
		if err := json.Unmarshal([]byte(t), &m); err != nil {
			return nil, err
		}
		return m, nil
	case []byte:
		var m map[string]any
		if err := json.Unmarshal(t, &m); err != nil {
			return nil, err
		}
		return m, nil
	default:
		return nil, fmt.Errorf("unexpected statement column type %T", v)
	}
}

// LoadOrCreateSigningKey resolves the Ed25519 key `corral certify` signs
// build attestations with: env CORRALAI_CERTIFY_KEY (hex-encoded 32-byte
// seed) overrides everything; else a 0600 seed file at path is read if
// present; else a fresh key is generated and its seed persisted to path at
// 0600. Reloading from the same path always returns the byte-identical key —
// a fresh key each restart would invalidate every prior signature, which
// defeats the point of a signed, appendable build ledger. The seed is never
// logged or included in an error string.
func LoadOrCreateSigningKey(path string) (ed25519.PrivateKey, error) {
	if v := strings.TrimSpace(os.Getenv("CORRALAI_CERTIFY_KEY")); v != "" {
		seed, err := hex.DecodeString(v)
		if err != nil {
			return nil, fmt.Errorf("buildstore: CORRALAI_CERTIFY_KEY is not valid hex")
		}
		if len(seed) != ed25519.SeedSize {
			return nil, fmt.Errorf("buildstore: CORRALAI_CERTIFY_KEY must decode to %d bytes, got %d", ed25519.SeedSize, len(seed))
		}
		return ed25519.NewKeyFromSeed(seed), nil
	}

	if raw, err := os.ReadFile(path); err == nil { // #nosec G304 -- path is the operator-configured certify key path, not attacker input
		seed, err := hex.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil {
			return nil, fmt.Errorf("buildstore: signing key file %s is corrupt", path)
		}
		if len(seed) != ed25519.SeedSize {
			return nil, fmt.Errorf("buildstore: signing key file %s has an unexpected seed length", path)
		}
		return ed25519.NewKeyFromSeed(seed), nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("buildstore: reading signing key file %s: %w", path, err)
	}

	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, fmt.Errorf("buildstore: generating signing key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("buildstore: creating signing key directory: %w", err)
	}
	encoded := []byte(hex.EncodeToString(priv.Seed()))
	if err := os.WriteFile(path, encoded, 0o600); err != nil { // #nosec G306 -- 0600 is the correct, intended perm for a private signing-key seed file
		return nil, fmt.Errorf("buildstore: persisting signing key to %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("buildstore: setting permissions on %s: %w", path, err)
	}
	return priv, nil
}
