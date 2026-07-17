// SPDX-License-Identifier: Elastic-2.0

// Package bugcatch is the append-only, record-anchored DuckDB store behind the
// bug-catching scorecard: which model actually catches bugs, proven by
// execution. One row per (converged pool run × model × role); the headline
// recall/precision come only from execution-proven catches (advpool ProvenMissed).
// Mirrors internal/buildstore's DuckDB pattern (CREATE IF NOT EXISTS on open,
// parameterized SQL, timestamps supplied by the caller — no time.Now() here).
package bugcatch

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"
)

type Store struct{ db *sql.DB }

type Observation struct {
	TS         time.Time
	RecordID   int64
	RecordHead string
	MissionID  int64
	Repo       string
	Commit     string
	Model      string
	Role       string
	Source     string // "pool"

	Catches       int
	Opportunities int
	SoundTests    int
	AuthoredTests int

	CriticFlags     int
	MutantsPlanted  int
	MutantsSurvived int
}

type Cell struct {
	Model           string   `json:"model"`
	Role            string   `json:"role"`
	Catches         int      `json:"catches"`
	Opportunities   int      `json:"opportunities"`
	Recall          *float64 `json:"recall,omitempty"`
	SoundTests      int      `json:"sound_tests"`
	AuthoredTests   int      `json:"authored_tests"`
	Precision       *float64 `json:"precision,omitempty"`
	CriticFlags     int      `json:"critic_flags,omitempty"`
	MutantsPlanted  int      `json:"mutants_planted,omitempty"`
	MutantsSurvived int      `json:"mutants_survived,omitempty"`
	Runs            int      `json:"runs"`
}

func Open(dsn string) (*Store, error) {
	db, err := sql.Open("duckdb", dsn)
	if err != nil {
		return nil, fmt.Errorf("bugcatch: open %q: %w", dsn, err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS bugcatch_observations (
		ts TIMESTAMP, record_id BIGINT, record_head VARCHAR, mission_id BIGINT,
		repo VARCHAR, commit VARCHAR, model VARCHAR, role VARCHAR, source VARCHAR,
		catches INTEGER, opportunities INTEGER, sound_tests INTEGER, authored_tests INTEGER,
		critic_flags INTEGER, mutants_planted INTEGER, mutants_survived INTEGER
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("bugcatch: create table: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Record(ctx context.Context, obs []Observation) error {
	if len(obs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("bugcatch: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	for _, o := range obs {
		model := o.Model
		if model == "" {
			model = "(unknown model)"
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO bugcatch_observations VALUES
			(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			o.TS, o.RecordID, o.RecordHead, o.MissionID, o.Repo, o.Commit, model, o.Role, o.Source,
			o.Catches, o.Opportunities, o.SoundTests, o.AuthoredTests,
			o.CriticFlags, o.MutantsPlanted, o.MutantsSurvived); err != nil {
			return fmt.Errorf("bugcatch: insert: %w", err)
		}
	}
	return tx.Commit()
}

func (s *Store) Scorecard(ctx context.Context) ([]Cell, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT model, role,
		SUM(catches), SUM(opportunities),
		CASE WHEN SUM(opportunities) > 0 THEN SUM(catches)*1.0/SUM(opportunities) END,
		SUM(sound_tests), SUM(authored_tests),
		CASE WHEN SUM(authored_tests) > 0 THEN SUM(sound_tests)*1.0/SUM(authored_tests) END,
		SUM(critic_flags), SUM(mutants_planted), SUM(mutants_survived), COUNT(*)
		FROM bugcatch_observations
		GROUP BY model, role
		ORDER BY SUM(catches) DESC, model, role`)
	if err != nil {
		return nil, fmt.Errorf("bugcatch: scorecard: %w", err)
	}
	defer rows.Close()
	var out []Cell
	for rows.Next() {
		var c Cell
		if err := rows.Scan(&c.Model, &c.Role, &c.Catches, &c.Opportunities, &c.Recall,
			&c.SoundTests, &c.AuthoredTests, &c.Precision, &c.CriticFlags,
			&c.MutantsPlanted, &c.MutantsSurvived, &c.Runs); err != nil {
			return nil, fmt.Errorf("bugcatch: scan: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
