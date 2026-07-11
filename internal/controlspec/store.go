// SPDX-License-Identifier: Elastic-2.0

// Package controlspec is corralai's owner-scoped store for CISO test goals:
// the durable control-spec that a CISO's gate is judged against. It's a
// thin DuckDB table (`control_goals`) keyed on (owner, id) so one owner's
// goals never leak into another's lookups or lists — the isolation that
// makes goals dev-untouchable once the auth gate (Plan 3) sits in front of
// this store. Task 2 adds the embedded OWASP ASVS bundle + import on top of
// this store.
package controlspec

import (
	"database/sql"
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
