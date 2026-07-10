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
		steps JSON,
		rekor JSON,
		anchored BOOLEAN,
		created_ts DOUBLE)`); err != nil {
		_ = db.Close()
		return nil, err
	}
	// Idempotent for any dev DB created before `steps`/`rekor`/`anchored`
	// existed. DuckDB supports IF NOT EXISTS on ADD COLUMN; the fresh CREATE
	// TABLE above already covers brand-new stores, so these are a no-op there.
	if _, err := db.Exec(`ALTER TABLE build_records ADD COLUMN IF NOT EXISTS steps JSON`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`ALTER TABLE build_records ADD COLUMN IF NOT EXISTS rekor JSON`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`ALTER TABLE build_records ADD COLUMN IF NOT EXISTS anchored BOOLEAN`); err != nil {
		_ = db.Close()
		return nil, err
	}
	// The git-link columns (Task 2): commit message/author/date pulled from
	// `git show` at certify time, the parsed `git verify-commit` outcome, and
	// pass (execution exit_code == 0) — a cheap denormalized column so the
	// dashboard's status filter doesn't need to unpack `statement`/`steps` JSON
	// per row. Idempotent for the same reason as steps/rekor/anchored above.
	if _, err := db.Exec(`ALTER TABLE build_records ADD COLUMN IF NOT EXISTS commit_message VARCHAR`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`ALTER TABLE build_records ADD COLUMN IF NOT EXISTS commit_author VARCHAR`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`ALTER TABLE build_records ADD COLUMN IF NOT EXISTS commit_date VARCHAR`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`ALTER TABLE build_records ADD COLUMN IF NOT EXISTS commit_signature JSON`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`ALTER TABLE build_records ADD COLUMN IF NOT EXISTS pass BOOLEAN`); err != nil {
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

// Save inserts a signed build record — including the full ledger `steps`
// that produced `head`, so a stored record can be independently re-verified
// with certify.VerifyLedger without trusting the brain's in-process state —
// plus the transparency-witness evidence (rekorJSON, the marshaled
// transparency.Entry; anchored, whether anchoring actually succeeded), the
// git-link fields captured at certify time (commitMessage, commitAuthor,
// commitDate, commitSignatureJSON — the parsed `git verify-commit` outcome,
// a `{signed,signer,mechanism,verified}` object as JSON text, "" if git
// context was unavailable), and pass (the record's execution exit_code == 0,
// denormalized here so the dashboard's status filter is a cheap column read
// rather than an unpack of statement/steps JSON) — and returns its assigned
// id. rekorJSON=="" / anchored==false records a build that was signed but
// never reached (or wasn't offered) a transparency log — report_build
// degrades to this rather than failing the build.
func (s *Store) Save(repo, commit, branch, actor, head, sig, statementJSON, stepsJSON, rekorJSON string, anchored bool, commitMessage, commitAuthor, commitDate, commitSignatureJSON string, pass bool) (int64, error) {
	var id int64
	var rekorArg any
	if rekorJSON == "" {
		rekorArg = nil
	} else {
		rekorArg = rekorJSON
	}
	var commitSigArg any
	if commitSignatureJSON == "" {
		commitSigArg = nil
	} else {
		commitSigArg = commitSignatureJSON
	}
	err := s.db.QueryRow(
		`INSERT INTO build_records (id, repo, commit_sha, branch, actor, head, signature, statement, steps, rekor, anchored, commit_message, commit_author, commit_date, commit_signature, pass, created_ts)
		 VALUES (nextval('build_record_id'), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 RETURNING id`,
		repo, commit, branch, actor, head, sig, statementJSON, stepsJSON, rekorArg, anchored, commitMessage, commitAuthor, commitDate, commitSigArg, pass, now()).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("buildstore: save: %w", err)
	}
	return id, nil
}

// Get returns the stored SLSA statement (parsed to a map, with the ledger
// steps folded in under "steps", the signature under "signature", the
// transparency-witness evidence under "rekor" (a JSON-encoded string, "" if
// never anchored), whether it was anchored under "anchored", the git-link
// fields under "commit_message"/"commit_author"/"commit_date" (plain
// strings) and "commit_signature" (a JSON-encoded string, "" if git context
// was unavailable at certify time), and "pass" (whether the execution
// exited 0)) for id, plus false if no record with that id exists.
func (s *Store) Get(id int64) (map[string]any, bool, error) {
	var statement any
	var steps any
	var sig string
	var rekor any
	var anchored sql.NullBool
	var commitMessage, commitAuthor, commitDate sql.NullString
	var commitSignature any
	var pass sql.NullBool
	err := s.db.QueryRow(
		`SELECT statement, steps, signature, rekor, anchored, commit_message, commit_author, commit_date, commit_signature, pass FROM build_records WHERE id = ?`, id).
		Scan(&statement, &steps, &sig, &rekor, &anchored, &commitMessage, &commitAuthor, &commitDate, &commitSignature, &pass)
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
	stepsVal, err := jsonColumnToAny(steps)
	if err != nil {
		return nil, false, fmt.Errorf("buildstore: get: decoding stored steps: %w", err)
	}
	m["steps"] = stepsVal
	m["signature"] = sig
	m["rekor"] = ""
	if rekor != nil {
		// rekor is stored as a JSON column; the driver may hand back an
		// already-decoded Go value, a JSON string, or JSON bytes (same
		// variability jsonColumnToAny normalizes for `steps`). Re-encode to
		// the flat JSON string the "rekor" map key promises callers.
		rekorVal, err := jsonColumnToAny(rekor)
		if err != nil {
			return nil, false, fmt.Errorf("buildstore: get: decoding stored rekor: %w", err)
		}
		rekorBytes, err := json.Marshal(rekorVal)
		if err != nil {
			return nil, false, fmt.Errorf("buildstore: get: re-encoding stored rekor: %w", err)
		}
		m["rekor"] = string(rekorBytes)
	}
	m["anchored"] = anchored.Valid && anchored.Bool
	m["commit_message"] = commitMessage.String
	m["commit_author"] = commitAuthor.String
	m["commit_date"] = commitDate.String
	m["commit_signature"] = ""
	if commitSignature != nil {
		// commit_signature is a JSON column (same driver-representation
		// variability as rekor above); re-encode to the flat JSON string the
		// "commit_signature" map key promises callers.
		sigVal, err := jsonColumnToAny(commitSignature)
		if err != nil {
			return nil, false, fmt.Errorf("buildstore: get: decoding stored commit_signature: %w", err)
		}
		sigBytes, err := json.Marshal(sigVal)
		if err != nil {
			return nil, false, fmt.Errorf("buildstore: get: re-encoding stored commit_signature: %w", err)
		}
		m["commit_signature"] = string(sigBytes)
	}
	m["pass"] = pass.Valid && pass.Bool
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

// jsonColumnToAny normalizes a DuckDB JSON column's driver representation
// (already-decoded Go value, JSON string, or JSON bytes) to a plain Go value
// (e.g. []any for the `steps` array column). Unlike statementToMap it does
// not assume an object at the top level.
func jsonColumnToAny(v any) (any, error) {
	switch t := v.(type) {
	case string:
		var out any
		if err := json.Unmarshal([]byte(t), &out); err != nil {
			return nil, err
		}
		return out, nil
	case []byte:
		var out any
		if err := json.Unmarshal(t, &out); err != nil {
			return nil, err
		}
		return out, nil
	default:
		return t, nil
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
