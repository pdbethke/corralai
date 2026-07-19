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

// bugcatchObservationsMigrationCols is the additive set of columns this
// package has ever needed beyond the original CREATE TABLE, in the order
// they must be added — a ledger created before swarm slice 2 gets them added
// on open; a ledger created after already has them and none are re-added.
var bugcatchObservationsMigrationCols = []struct{ name, ddl string }{
	{"shard", "shard INTEGER"},
	{"region", "region VARCHAR"},
	{"region_complexity", "region_complexity INTEGER"},
	{"region_lines", "region_lines INTEGER"},
	{"test_complexity", "test_complexity INTEGER"},
	{"parse_retries", "parse_retries INTEGER"},
	{"dropped", "dropped BOOLEAN"},
	{"shadow", "shadow BOOLEAN"},
}

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

	// Per-shard generator dimensions (zero for the single-seat roles). See
	// advpool.BugCatchObservation for the "why": one row PER SHARD, never
	// summed, with RegionComplexity/RegionLines as the difficulty CONTROL a
	// raw per-shard yield cannot supply on its own.
	Shard            int
	Region           string
	RegionComplexity int
	RegionLines      int
	TestComplexity   int
	ParseRetries     int
	Dropped          bool
	Shadow           bool
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
		critic_flags INTEGER, mutants_planted INTEGER, mutants_survived INTEGER,
		shard INTEGER, region VARCHAR, region_complexity INTEGER, region_lines INTEGER,
		test_complexity INTEGER, parse_retries INTEGER, dropped BOOLEAN, shadow BOOLEAN
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("bugcatch: create table: %w", err)
	}
	if err := migrateBugcatchObservations(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// migrateBugcatchObservations additively brings a ledger created before swarm
// slice 2 up to the current column set. DuckDB has no
// `ADD COLUMN IF NOT EXISTS`, and this is a ledger — silently discarding
// every ALTER error would make a genuinely broken migration indistinguishable
// from an already-applied one. Instead: probe information_schema.columns for
// what already exists, add only what's missing, and surface any other ALTER
// failure as a real error. Idempotent across repeated opens: a table that
// already has every column runs zero ALTERs.
func migrateBugcatchObservations(db *sql.DB) error {
	rows, err := db.Query(`SELECT column_name FROM information_schema.columns WHERE table_name = ?`, "bugcatch_observations")
	if err != nil {
		return fmt.Errorf("bugcatch: probe existing columns: %w", err)
	}
	existing := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return fmt.Errorf("bugcatch: scan existing column: %w", err)
		}
		existing[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("bugcatch: probe existing columns: %w", err)
	}
	rows.Close()

	for _, col := range bugcatchObservationsMigrationCols {
		if existing[col.name] {
			continue
		}
		if _, err := db.Exec("ALTER TABLE bugcatch_observations ADD COLUMN " + col.ddl); err != nil {
			return fmt.Errorf("bugcatch: migrate: add column %s: %w", col.name, err)
		}
	}
	return nil
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
		if _, err := tx.ExecContext(ctx, `INSERT INTO bugcatch_observations (
			ts, record_id, record_head, mission_id, repo, commit, model, role, source,
			catches, opportunities, sound_tests, authored_tests,
			critic_flags, mutants_planted, mutants_survived,
			shard, region, region_complexity, region_lines,
			test_complexity, parse_retries, dropped, shadow
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			o.TS, o.RecordID, o.RecordHead, o.MissionID, o.Repo, o.Commit, model, o.Role, o.Source,
			o.Catches, o.Opportunities, o.SoundTests, o.AuthoredTests,
			o.CriticFlags, o.MutantsPlanted, o.MutantsSurvived,
			o.Shard, o.Region, o.RegionComplexity, o.RegionLines,
			o.TestComplexity, o.ParseRetries, o.Dropped, o.Shadow); err != nil {
			return fmt.Errorf("bugcatch: insert: %w", err)
		}
	}
	return tx.Commit()
}

// observationsLimit bounds Observations to the most recent N rows so it can
// never scan an arbitrarily large production ledger into memory — this
// method is for ad hoc debugging and round-trip tests, not a paging API.
const observationsLimit = 10000

// Observations returns the most recent rows (newest record first, capped at
// observationsLimit), unaggregated — unlike Scorecard, which only ever
// surfaces the SUM'd model×role cells. This exists to let a test assert the
// full round-trip (every Observation field survives Record unchanged,
// including the per-shard columns Scorecard never projects) and for ad hoc
// debugging; ordinary callers want Scorecard.
//
// The additive shard/region/*/dropped/shadow columns (swarm slice 2) are
// NULL on every row written before that migration; COALESCE'd to each
// field's zero value here so a legacy row reads back cleanly instead of
// failing to scan (int/bool destinations reject a raw NULL).
func (s *Store) Observations(ctx context.Context) ([]Observation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT
		ts, record_id, record_head, mission_id, repo, commit, model, role, source,
		catches, opportunities, sound_tests, authored_tests,
		critic_flags, mutants_planted, mutants_survived,
		COALESCE(shard, 0), COALESCE(region, ''), COALESCE(region_complexity, 0), COALESCE(region_lines, 0),
		COALESCE(test_complexity, 0), COALESCE(parse_retries, 0), COALESCE(dropped, false), COALESCE(shadow, false)
		FROM bugcatch_observations
		ORDER BY record_id DESC, shard ASC
		LIMIT ?`, observationsLimit)
	if err != nil {
		return nil, fmt.Errorf("bugcatch: observations: %w", err)
	}
	defer rows.Close()
	var out []Observation
	for rows.Next() {
		var o Observation
		if err := rows.Scan(&o.TS, &o.RecordID, &o.RecordHead, &o.MissionID, &o.Repo, &o.Commit, &o.Model, &o.Role, &o.Source,
			&o.Catches, &o.Opportunities, &o.SoundTests, &o.AuthoredTests,
			&o.CriticFlags, &o.MutantsPlanted, &o.MutantsSurvived,
			&o.Shard, &o.Region, &o.RegionComplexity, &o.RegionLines,
			&o.TestComplexity, &o.ParseRetries, &o.Dropped, &o.Shadow); err != nil {
			return nil, fmt.Errorf("bugcatch: scan observation: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *Store) Scorecard(ctx context.Context) ([]Cell, error) {
	// Runs is COUNT(DISTINCT record_id), never COUNT(*): swarm slice 2 fans the
	// mutant-generator role out into one row PER SHARD (up to DefaultMaxShards
	// per run), while every other role still writes exactly one row per run.
	// COUNT(*) would report a sharded run as 8 runs instead of 1, defeating the
	// provisionalBelow=3 gate after a single real run — see the field note this
	// migration fixed (Runs must count converged RUNS, not observation rows).
	rows, err := s.db.QueryContext(ctx, `SELECT model, role,
		SUM(catches), SUM(opportunities),
		CASE WHEN SUM(opportunities) > 0 THEN SUM(catches)*1.0/SUM(opportunities) END,
		SUM(sound_tests), SUM(authored_tests),
		CASE WHEN SUM(authored_tests) > 0 THEN SUM(sound_tests)*1.0/SUM(authored_tests) END,
		SUM(critic_flags), SUM(mutants_planted), SUM(mutants_survived), COUNT(DISTINCT record_id)
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
