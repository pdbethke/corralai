// SPDX-License-Identifier: Elastic-2.0

// Package criticscore is corralai's mutable, per-finding DuckDB store behind
// critic-accuracy metrics: unlike the append-only internal/bugcatch ledger,
// this store UPSERTs on a stable finding ID and lets a human's adjudication
// (Source "human") permanently outrank the critic's own auto-adjudication —
// a re-Record of the same finding must never claw a human verdict back to
// "unadjudicated"/"auto". Mirrors internal/controlspec/store.go for the
// mutable Open/Close/table shape and internal/bugcatch/store.go for the
// additive-migration column ledger and aggregate-query conventions.
package criticscore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"
)

// nowUnix is the wall-clock time Adjudicate stamps onto adjudicated_ts.
// Adjudicate has no ts parameter in its signature (unlike Record's TS,
// which callers always supply), so it is the one place in this store that
// calls time.Now() itself — the human-adjudication act happens right now,
// by definition.
func nowUnix() float64 { return float64(time.Now().Unix()) }

// Store is a DuckDB-backed table of critic_findings rows.
type Store struct{ db *sql.DB }

// criticFindingsMigrationCols is the additive set of columns this package
// has ever needed beyond the original CREATE TABLE, in the order they must
// be added — mirrors bugcatch's migration ledger so a store opened against
// an older schema version gets brought forward on open, idempotently.
var criticFindingsMigrationCols = []struct{ name, ddl string }{}

// Open opens (creating if absent) the critic_findings store at dsn. dsn is
// kept opaque, matching controlspec.OpenStore, so both a local `.duckdb`
// file and a MotherDuck `md:` DSN work unchanged.
func Open(dsn string) (*Store, error) {
	db, err := sql.Open("duckdb", dsn)
	if err != nil {
		return nil, fmt.Errorf("criticscore: open %q: %w", dsn, err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS critic_findings (
		id VARCHAR PRIMARY KEY, ts DOUBLE, record_id BIGINT, record_head VARCHAR,
		repo VARCHAR, commit VARCHAR, mission_id BIGINT, model VARCHAR,
		target_test VARCHAR, test_file VARCHAR, test_selector VARCHAR, scope VARCHAR,
		evidence VARCHAR, severity VARCHAR,
		adjudication VARCHAR NOT NULL DEFAULT 'unadjudicated', source VARCHAR NOT NULL DEFAULT 'auto',
		adjudicated_by VARCHAR, adjudicated_ts DOUBLE
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("criticscore: create table: %w", err)
	}
	if err := migrateCriticFindings(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// migrateCriticFindings additively brings a ledger created before a schema
// change up to the current column set, exactly as bugcatch does: probe
// information_schema.columns for what already exists, add only what's
// missing, surface any other ALTER failure as a real error. Idempotent —
// a table with every column runs zero ALTERs. The migration list is empty
// today (this store ships with its full column set from the start); it
// exists so a future additive column follows the same, already-proven path
// bugcatch uses instead of a bespoke one.
func migrateCriticFindings(db *sql.DB) error {
	if len(criticFindingsMigrationCols) == 0 {
		return nil
	}
	rows, err := db.Query(`SELECT column_name FROM information_schema.columns WHERE table_name = ?`, "critic_findings")
	if err != nil {
		return fmt.Errorf("criticscore: probe existing columns: %w", err)
	}
	existing := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return fmt.Errorf("criticscore: scan existing column: %w", err)
		}
		existing[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("criticscore: probe existing columns: %w", err)
	}
	rows.Close()

	for _, col := range criticFindingsMigrationCols {
		if existing[col.name] {
			continue
		}
		if _, err := db.Exec("ALTER TABLE critic_findings ADD COLUMN " + col.ddl); err != nil {
			return fmt.Errorf("criticscore: migrate: add column %s: %w", col.name, err)
		}
	}
	return nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// findingCols is the critic_findings column list, in the order scanFinding
// reads. Interpolated into SQL as a constant literal only — never caller
// input.
const findingCols = `id, ts, record_id, record_head, repo, commit, mission_id, model, ` +
	`target_test, test_file, test_selector, scope, evidence, severity, ` +
	`adjudication, source, adjudicated_by, adjudicated_ts`

// rowScanner is satisfied by both *sql.Row (QueryRow) and *sql.Rows.
type rowScanner interface{ Scan(dest ...any) error }

// scanFinding decodes one critic_findings row selected as findingCols.
// adjudicated_by/adjudicated_ts are NULL until the first Adjudicate call, so
// they're read through sql.Null* and zero-valued back onto the plain Finding
// fields.
func scanFinding(sc rowScanner) (Finding, error) {
	var f Finding
	var adjudicatedBy sql.NullString
	var adjudicatedTS sql.NullFloat64
	if err := sc.Scan(&f.ID, &f.TS, &f.RecordID, &f.RecordHead, &f.Repo, &f.Commit, &f.MissionID, &f.Model,
		&f.TargetTest, &f.TestFile, &f.TestSelector, &f.Scope, &f.Evidence, &f.Severity,
		&f.Adjudication, &f.Source, &adjudicatedBy, &adjudicatedTS); err != nil {
		return Finding{}, err
	}
	f.AdjudicatedBy = adjudicatedBy.String
	f.AdjudicatedTS = adjudicatedTS.Float64
	return f, nil
}

// Record upserts findings keyed on ID. It NEVER downgrades a finding whose
// current stored Source is "human" — a re-Record with an auto adjudication
// (e.g. re-running the critic on the same code) leaves that row's
// adjudication/source/adjudicated_by/adjudicated_ts exactly as a human left
// them. ts is set on first insert only (never overwritten by a later
// upsert), matching the other fields' upsert-but-preserve-history-fields
// treatment.
func (s *Store) Record(ctx context.Context, fs []Finding) error {
	if len(fs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("criticscore: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	for _, f := range fs {
		var existingSource sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT source FROM critic_findings WHERE id = ?`, f.ID).Scan(&existingSource)
		switch {
		case err == sql.ErrNoRows:
			if _, err := tx.ExecContext(ctx, `INSERT INTO critic_findings (`+findingCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				f.ID, f.TS, f.RecordID, f.RecordHead, f.Repo, f.Commit, f.MissionID, f.Model,
				f.TargetTest, f.TestFile, f.TestSelector, f.Scope, f.Evidence, f.Severity,
				f.Adjudication, f.Source, nullIfEmpty(f.AdjudicatedBy), nullIfZero(f.AdjudicatedTS)); err != nil {
				return fmt.Errorf("criticscore: insert %s: %w", f.ID, err)
			}
		case err != nil:
			return fmt.Errorf("criticscore: record: lookup %s: %w", f.ID, err)
		case existingSource.String == "human":
			// Human adjudication wins: update the descriptive fields (repo,
			// evidence, etc. may legitimately change on re-record) but never
			// touch adjudication/source/adjudicated_by/adjudicated_ts.
			if _, err := tx.ExecContext(ctx, `UPDATE critic_findings SET
				record_id = ?, record_head = ?, repo = ?, commit = ?, mission_id = ?, model = ?,
				target_test = ?, test_file = ?, test_selector = ?, scope = ?, evidence = ?, severity = ?
				WHERE id = ?`,
				f.RecordID, f.RecordHead, f.Repo, f.Commit, f.MissionID, f.Model,
				f.TargetTest, f.TestFile, f.TestSelector, f.Scope, f.Evidence, f.Severity, f.ID); err != nil {
				return fmt.Errorf("criticscore: update (human-preserved) %s: %w", f.ID, err)
			}
		default:
			if _, err := tx.ExecContext(ctx, `UPDATE critic_findings SET
				record_id = ?, record_head = ?, repo = ?, commit = ?, mission_id = ?, model = ?,
				target_test = ?, test_file = ?, test_selector = ?, scope = ?, evidence = ?, severity = ?,
				adjudication = ?, source = ?
				WHERE id = ?`,
				f.RecordID, f.RecordHead, f.Repo, f.Commit, f.MissionID, f.Model,
				f.TargetTest, f.TestFile, f.TestSelector, f.Scope, f.Evidence, f.Severity,
				f.Adjudication, f.Source, f.ID); err != nil {
				return fmt.Errorf("criticscore: update %s: %w", f.ID, err)
			}
		}
	}
	return tx.Commit()
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullIfZero(f float64) any {
	if f == 0 {
		return nil
	}
	return f
}

// Adjudicate records a human verdict for finding id: verdict must be
// "confirmed" or "refuted" (any other value, including "unadjudicated", is
// rejected — a human can only ever move a finding to a decided state, never
// back to pending via this call). It always sets source="human", which is
// what makes the verdict win over any later auto Record. Returns whether a
// row was actually changed (false only when id doesn't exist).
func (s *Store) Adjudicate(ctx context.Context, id, verdict, by string) (bool, error) {
	if verdict != "confirmed" && verdict != "refuted" {
		return false, fmt.Errorf("criticscore: adjudicate: invalid verdict %q (want confirmed|refuted)", verdict)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE critic_findings SET adjudication = ?, source = 'human', adjudicated_by = ?, adjudicated_ts = ?
		WHERE id = ? AND adjudication IN ('unadjudicated', 'confirmed', 'refuted')`,
		verdict, by, nowUnix(), id)
	if err != nil {
		return false, fmt.Errorf("criticscore: adjudicate %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("criticscore: adjudicate %s: rows affected: %w", id, err)
	}
	return n > 0, nil
}

// ListPending returns every finding still awaiting adjudication, ordered by
// ID for deterministic output.
func (s *Store) ListPending(ctx context.Context) ([]Finding, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+findingCols+` FROM critic_findings WHERE adjudication = 'unadjudicated' ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("criticscore: list pending: %w", err)
	}
	defer rows.Close()
	var out []Finding
	for rows.Next() {
		f, err := scanFinding(rows)
		if err != nil {
			return nil, fmt.Errorf("criticscore: list pending: scan: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Get looks up a single finding by ID, returning (Finding{}, false, nil)
// when no such row exists.
func (s *Store) Get(ctx context.Context, id string) (Finding, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+findingCols+` FROM critic_findings WHERE id = ?`, id)
	f, err := scanFinding(row)
	if err == sql.ErrNoRows {
		return Finding{}, false, nil
	}
	if err != nil {
		return Finding{}, false, fmt.Errorf("criticscore: get %s: %w", id, err)
	}
	return f, true, nil
}

// List returns every finding, ordered by ID.
func (s *Store) List(ctx context.Context) ([]Finding, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+findingCols+` FROM critic_findings ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("criticscore: list: %w", err)
	}
	defer rows.Close()
	var out []Finding
	for rows.Next() {
		f, err := scanFinding(rows)
		if err != nil {
			return nil, fmt.Errorf("criticscore: list: scan: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Precision rolls findings up per critic model: how many of its findings
// have been confirmed vs refuted vs are still unadjudicated, plus the
// confirmed/(confirmed+refuted) ratio computed in Go so a model with zero
// adjudicated findings gets a nil Precision rather than a misleading 0.
func (s *Store) Precision(ctx context.Context) ([]CriticCell, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT model,
		SUM(CASE WHEN adjudication = 'confirmed' THEN 1 ELSE 0 END),
		SUM(CASE WHEN adjudication = 'refuted' THEN 1 ELSE 0 END),
		SUM(CASE WHEN adjudication = 'unadjudicated' THEN 1 ELSE 0 END)
		FROM critic_findings GROUP BY model ORDER BY model`)
	if err != nil {
		return nil, fmt.Errorf("criticscore: precision: %w", err)
	}
	defer rows.Close()
	var out []CriticCell
	for rows.Next() {
		var c CriticCell
		if err := rows.Scan(&c.Model, &c.Confirmed, &c.Refuted, &c.Unadjudicated); err != nil {
			return nil, fmt.Errorf("criticscore: precision: scan: %w", err)
		}
		if denom := c.Confirmed + c.Refuted; denom > 0 {
			p := float64(c.Confirmed) / float64(denom)
			c.Precision = &p
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
