// SPDX-License-Identifier: Elastic-2.0

// Package controlspec is corralai's owner-scoped store for control-owner test goals:
// the durable control-spec that a control owner's gate is judged against. It's a
// thin DuckDB table (`control_goals`) keyed on (owner, id) so one owner's
// goals never leak into another's lookups or lists — the isolation that
// makes goals dev-untouchable once the auth gate (Plan 3) sits in front of
// this store. Task 2 adds the embedded OWASP ASVS bundle + import on top of
// this store.
package controlspec

import (
	"database/sql"
	"encoding/json"
	"fmt"

	_ "github.com/marcboeker/go-duckdb/v2"
)

// Store is a DuckDB-backed table of control_goals rows.
type Store struct{ db *sql.DB }

// OpenStore opens (creating if absent) the control_goals store at dsn. dsn
// is kept opaque — never parsed/validated as a filesystem path — so a local
// `.duckdb` file and a MotherDuck `md:` DSN both work unchanged.
func OpenStore(dsn string) (*Store, error) {
	db, err := sql.Open("duckdb", dsn)
	if err != nil {
		return nil, fmt.Errorf("controlspec: open: %w", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS control_goals (
		owner VARCHAR NOT NULL, id VARCHAR NOT NULL,
		standard VARCHAR, ref VARCHAR, intent VARCHAR NOT NULL,
		level VARCHAR, mode VARCHAR NOT NULL, created_ts TIMESTAMP NOT NULL,
		PRIMARY KEY (owner, id)
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("controlspec: creating control_goals table: %w", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS gate_tests (
		owner VARCHAR NOT NULL, goal VARCHAR NOT NULL, target VARCHAR NOT NULL,
		test VARCHAR NOT NULL, kill_rate DOUBLE NOT NULL,
		survived VARCHAR NOT NULL, discarded VARCHAR NOT NULL,
		vetted BOOLEAN NOT NULL, created_ts TIMESTAMP NOT NULL, vetted_ts TIMESTAMP,
		verdicts VARCHAR NOT NULL DEFAULT '',
		code_path VARCHAR NOT NULL DEFAULT '', test_path VARCHAR NOT NULL DEFAULT '',
		PRIMARY KEY (owner, goal, target)
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("controlspec: creating gate_tests table: %w", err)
	}
	for _, alter := range []string{
		`ALTER TABLE gate_tests ADD COLUMN IF NOT EXISTS code_path VARCHAR DEFAULT ''`,
		`ALTER TABLE gate_tests ADD COLUMN IF NOT EXISTS test_path VARCHAR DEFAULT ''`,
	} {
		if _, err := db.Exec(alter); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("controlspec: migrating gate_tests: %w", err)
		}
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// SaveGoal upserts a goal row keyed on (Owner, ID). CreatedTS is persisted
// exactly as given — the store never calls time.Now() itself; the caller is
// responsible for stamping it, which keeps this store deterministic under
// test.
func (s *Store) SaveGoal(g Goal) error {
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO control_goals (owner, id, standard, ref, intent, level, mode, created_ts)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		g.Owner, g.ID, g.Standard, g.Ref, g.Intent, g.Level, g.Mode, g.CreatedTS)
	if err != nil {
		return fmt.Errorf("controlspec: save goal: %w", err)
	}
	return nil
}

// GetGoal looks up the goal for (owner, id), returning (Goal{}, false, nil)
// when no such row exists — including when the row exists for a different
// owner, which is the owner isolation this store exists to provide.
func (s *Store) GetGoal(owner, id string) (Goal, bool, error) {
	var g Goal
	g.Owner = owner
	g.ID = id
	var createdTS sql.NullTime
	err := s.db.QueryRow(
		`SELECT standard, ref, intent, level, mode, created_ts FROM control_goals WHERE owner = ? AND id = ?`,
		owner, id).Scan(&g.Standard, &g.Ref, &g.Intent, &g.Level, &g.Mode, &createdTS)
	if err == sql.ErrNoRows {
		return Goal{}, false, nil
	}
	if err != nil {
		return Goal{}, false, fmt.Errorf("controlspec: get goal: %w", err)
	}
	g.CreatedTS = createdTS.Time.UTC()
	return g, true, nil
}

// ListGoals returns all goals owned by owner, ordered by ID. A different
// owner's goals are never included — the owner scoping this store exists to
// provide.
func (s *Store) ListGoals(owner string) ([]Goal, error) {
	rows, err := s.db.Query(
		`SELECT id, standard, ref, intent, level, mode, created_ts FROM control_goals WHERE owner = ? ORDER BY id`,
		owner)
	if err != nil {
		return nil, fmt.Errorf("controlspec: list goals: %w", err)
	}
	defer rows.Close()

	var goals []Goal
	for rows.Next() {
		g := Goal{Owner: owner}
		var createdTS sql.NullTime
		if err := rows.Scan(&g.ID, &g.Standard, &g.Ref, &g.Intent, &g.Level, &g.Mode, &createdTS); err != nil {
			return nil, fmt.Errorf("controlspec: list goals: scan: %w", err)
		}
		g.CreatedTS = createdTS.Time.UTC()
		goals = append(goals, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("controlspec: list goals: %w", err)
	}
	return goals, nil
}

// SaveCandidate upserts a candidate gate test keyed on (Owner, Goal,
// Target). It always persists the row as unvetted (vetted=false,
// vetted_ts=NULL) regardless of what gt.Vetted/gt.VettedTS hold: a fresh or
// re-authored candidate must always be re-approved by a human (Task 2's
// Promote) before it can gate. CreatedTS is persisted exactly as given — the
// store never calls time.Now() itself.
func (s *Store) SaveCandidate(gt GateTest) error {
	survived, err := json.Marshal(gt.Survived)
	if err != nil {
		return fmt.Errorf("controlspec: save candidate: marshal survived: %w", err)
	}
	discarded, err := json.Marshal(gt.Discarded)
	if err != nil {
		return fmt.Errorf("controlspec: save candidate: marshal discarded: %w", err)
	}
	_, err = s.db.Exec(
		`INSERT OR REPLACE INTO gate_tests (owner, goal, target, test, kill_rate, survived, discarded, vetted, created_ts, vetted_ts, verdicts, code_path, test_path)
		 VALUES (?, ?, ?, ?, ?, ?, ?, FALSE, ?, NULL, ?, ?, ?)`,
		gt.Owner, gt.Goal, gt.Target, gt.Test, gt.KillRate, string(survived), string(discarded), gt.CreatedTS, gt.VerdictsJSON, gt.CodePath, gt.TestPath)
	if err != nil {
		return fmt.Errorf("controlspec: save candidate: %w", err)
	}
	return nil
}

// gateTestCols is the gate_tests column list, in the order scanGateTest reads.
// Interpolated into SQL as a constant literal only — never caller input.
const gateTestCols = `goal, target, test, kill_rate, survived, discarded, ` +
	`vetted, created_ts, vetted_ts, verdicts, code_path, test_path`

// rowScanner is satisfied by both *sql.Row (QueryRow) and *sql.Rows.
type rowScanner interface{ Scan(dest ...any) error }

// scanGateTest decodes one gate_tests row selected as gateTestCols.
func scanGateTest(sc rowScanner, owner string) (GateTest, error) {
	gt := GateTest{Owner: owner}
	var survived, discarded string
	var createdTS, vettedTS sql.NullTime
	if err := sc.Scan(&gt.Goal, &gt.Target, &gt.Test, &gt.KillRate, &survived, &discarded,
		&gt.Vetted, &createdTS, &vettedTS, &gt.VerdictsJSON, &gt.CodePath, &gt.TestPath); err != nil {
		return GateTest{}, err
	}
	if err := json.Unmarshal([]byte(survived), &gt.Survived); err != nil {
		return GateTest{}, err
	}
	if err := json.Unmarshal([]byte(discarded), &gt.Discarded); err != nil {
		return GateTest{}, err
	}
	gt.CreatedTS = createdTS.Time.UTC()
	gt.VettedTS = vettedTS.Time.UTC()
	return gt, nil
}

// listGateTests is the shared body for ListPending/ListVetted. whereVetted is
// an internal constant literal ("vetted = FALSE"/"vetted = TRUE"), never input.
func (s *Store) listGateTests(op, whereVetted, owner string) ([]GateTest, error) {
	rows, err := s.db.Query(
		`SELECT `+gateTestCols+` FROM gate_tests WHERE owner = ? AND `+whereVetted+` ORDER BY goal, target`, // #nosec G202 -- not injectable: gateTestCols and whereVetted are constant literals from internal callers only; owner uses ? placeholder
		owner)
	if err != nil {
		return nil, fmt.Errorf("controlspec: %s: %w", op, err)
	}
	defer rows.Close()
	var out []GateTest
	for rows.Next() {
		gt, err := scanGateTest(rows, owner)
		if err != nil {
			return nil, fmt.Errorf("controlspec: %s: scan: %w", op, err)
		}
		out = append(out, gt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("controlspec: %s: %w", op, err)
	}
	return out, nil
}

// GetVetted looks up the vetted gate test for (owner, goal, target),
// returning (GateTest{}, false, nil) when no such row exists — including
// when the row exists but is still unvetted, which is the human gate this
// store exists to enforce.
func (s *Store) GetVetted(owner, goal, target string) (GateTest, bool, error) {
	row := s.db.QueryRow(
		`SELECT `+gateTestCols+` FROM gate_tests WHERE owner = ? AND goal = ? AND target = ? AND vetted = TRUE`,
		owner, goal, target)
	gt, err := scanGateTest(row, owner)
	if err == sql.ErrNoRows {
		return GateTest{}, false, nil
	}
	if err != nil {
		return GateTest{}, false, fmt.Errorf("controlspec: get vetted: %w", err)
	}
	return gt, true, nil
}

// GetCandidate returns the UNVETTED candidate for (owner, goal, target) —
// the vetted=FALSE twin of GetVetted, for the owner-review surface.
func (s *Store) GetCandidate(owner, goal, target string) (GateTest, bool, error) {
	row := s.db.QueryRow(
		`SELECT `+gateTestCols+` FROM gate_tests WHERE owner = ? AND goal = ? AND target = ? AND vetted = FALSE`,
		owner, goal, target)
	gt, err := scanGateTest(row, owner)
	if err == sql.ErrNoRows {
		return GateTest{}, false, nil
	}
	if err != nil {
		return GateTest{}, false, fmt.Errorf("controlspec: get candidate: %w", err)
	}
	return gt, true, nil
}

// ListPending returns all unvetted candidate gate tests owned by owner,
// ordered by (goal, target). A different owner's candidates are never
// included — the owner scoping this store exists to provide.
func (s *Store) ListPending(owner string) ([]GateTest, error) {
	return s.listGateTests("list pending", "vetted = FALSE", owner)
}

// ListVetted returns all control-owner-approved gate tests owned by owner, ordered by
// (goal, target) — the counterpart to ListPending, and the set the running
// gate tier executes against head code. A different owner's tests are never
// included — the owner scoping this store exists to provide.
func (s *Store) ListVetted(owner string) ([]GateTest, error) {
	return s.listGateTests("list vetted", "vetted = TRUE", owner)
}
