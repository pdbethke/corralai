// SPDX-License-Identifier: Elastic-2.0

// Package recordings stores scrubbed replay exports in DuckDB for ad-hoc analysis.
package recordings

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/sqlguard"

	_ "github.com/marcboeker/go-duckdb/v2"
)

var (
	home      = mustHome()
	DefaultDB = filepath.Join(home, ".claude", "corralai_recordings.duckdb")
)

func mustHome() string { h, _ := os.UserHomeDir(); return h }

// MissionMeta is one exported recording's mission-level metadata.
type MissionMeta struct {
	Slug            string         `json:"slug"`
	MissionID       int64          `json:"mission_id"`
	Directive       string         `json:"directive"`
	TaskCount       int            `json:"task_count"`
	DoneTaskCount   int            `json:"done_task_count"`
	FindingCount    int            `json:"finding_count"`
	DurationSeconds float64        `json:"duration_seconds"`
	Models          []string       `json:"models,omitempty"`
	Platform        map[string]any `json:"platform,omitempty"`
	TeamID          string         `json:"team_id,omitempty"`
	Visibility      string         `json:"visibility,omitempty"`
	SharedBy        string         `json:"shared_by,omitempty"`
	SharedTS        float64        `json:"shared_ts,omitempty"`
	SourceBrainID   string         `json:"source_brain_id,omitempty"`
	ExportedTS      float64        `json:"exported_ts"`
}

// Event is one replay beat persisted for a recording slug.
type Event struct {
	TS      float64        `json:"ts"`
	Kind    string         `json:"kind"`
	Actor   string         `json:"actor,omitempty"`
	Subject string         `json:"subject,omitempty"`
	Model   string         `json:"model,omitempty"`
	Detail  map[string]any `json:"detail,omitempty"`
}

// Summary is a list-friendly projection for list_recordings.
type Summary struct {
	Slug            string   `json:"slug"`
	MissionID       int64    `json:"mission_id"`
	Directive       string   `json:"directive"`
	TaskCount       int      `json:"task_count"`
	DoneTaskCount   int      `json:"done_task_count"`
	FindingCount    int      `json:"finding_count"`
	DurationSeconds float64  `json:"duration_seconds"`
	EventCount      int      `json:"event_count"`
	FirstEventTS    *float64 `json:"first_event_ts,omitempty"`
	LastEventTS     *float64 `json:"last_event_ts,omitempty"`
	TeamID          string   `json:"team_id,omitempty"`
	Visibility      string   `json:"visibility,omitempty"`
	SharedBy        string   `json:"shared_by,omitempty"`
	SharedTS        float64  `json:"shared_ts,omitempty"`
	SourceBrainID   string   `json:"source_brain_id,omitempty"`
	ExportedTS      float64  `json:"exported_ts"`
}

// Report is a generic SQL result.
type Report struct {
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
}

type Store struct {
	db      *sql.DB
	brainID string
}

func nowTS() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func Open(path string) (*Store, error) {
	db, err := sql.Open("duckdb", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS recordings_missions (
		slug VARCHAR PRIMARY KEY,
		mission_id BIGINT NOT NULL DEFAULT 0,
		directive VARCHAR,
		task_count INTEGER NOT NULL DEFAULT 0,
		done_task_count INTEGER NOT NULL DEFAULT 0,
		finding_count INTEGER NOT NULL DEFAULT 0,
		duration_seconds DOUBLE NOT NULL DEFAULT 0,
		models_json VARCHAR,
		platform_json VARCHAR,
		team_id VARCHAR DEFAULT '',
		visibility VARCHAR DEFAULT 'private',
		shared_by VARCHAR DEFAULT '',
		shared_ts DOUBLE DEFAULT 0,
		source_brain_id VARCHAR DEFAULT '',
		exported_ts DOUBLE NOT NULL
	)`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrateMissionsColumns(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS recordings_events (
		slug VARCHAR NOT NULL,
		event_idx BIGINT NOT NULL,
		ts DOUBLE NOT NULL,
		kind VARCHAR NOT NULL,
		actor VARCHAR,
		subject VARCHAR,
		model VARCHAR,
		detail_json VARCHAR,
		PRIMARY KEY (slug, event_idx)
	)`); err != nil {
		_ = db.Close()
		return nil, err
	}
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS recordings_events_slug_ts ON recordings_events(slug, ts)`)
	return &Store{db: db, brainID: strings.TrimSpace(os.Getenv("CORRALAI_BRAIN_ID"))}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Upsert rewrites one slug atomically (mission row + full replay events).
func (s *Store) Upsert(meta MissionMeta, events []Event) error {
	if strings.TrimSpace(meta.Slug) == "" {
		return fmt.Errorf("slug required")
	}
	meta.Visibility = normalizedVisibility(meta.Visibility)
	if meta.Visibility == "team" {
		meta.TeamID = strings.TrimSpace(meta.TeamID)
	}
	if meta.Visibility != "team" {
		meta.TeamID = ""
	}
	meta.SharedBy = strings.TrimSpace(meta.SharedBy)
	if meta.SourceBrainID == "" {
		meta.SourceBrainID = s.brainID
	}
	if meta.ExportedTS <= 0 {
		meta.ExportedTS = nowTS()
	}
	modelsJSON := ""
	if len(meta.Models) > 0 {
		b, _ := json.Marshal(meta.Models)
		modelsJSON = string(b)
	}
	platformJSON := ""
	if len(meta.Platform) > 0 {
		b, _ := json.Marshal(meta.Platform)
		platformJSON = string(b)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM recordings_events WHERE slug=?`, meta.Slug); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`DELETE FROM recordings_missions WHERE slug=?`, meta.Slug); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO recordings_missions
		(slug, mission_id, directive, task_count, done_task_count, finding_count, duration_seconds, models_json, platform_json, team_id, visibility, shared_by, shared_ts, source_brain_id, exported_ts)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		meta.Slug, meta.MissionID, meta.Directive, meta.TaskCount, meta.DoneTaskCount, meta.FindingCount, meta.DurationSeconds,
		modelsJSON, platformJSON, meta.TeamID, meta.Visibility, meta.SharedBy, meta.SharedTS, meta.SourceBrainID, meta.ExportedTS,
	); err != nil {
		_ = tx.Rollback()
		return err
	}
	for i, ev := range events {
		detailJSON := ""
		if len(ev.Detail) > 0 {
			b, _ := json.Marshal(ev.Detail)
			detailJSON = string(b)
		}
		if _, err := tx.Exec(
			`INSERT INTO recordings_events
			(slug, event_idx, ts, kind, actor, subject, model, detail_json)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			meta.Slug, i+1, ev.TS, ev.Kind, ev.Actor, ev.Subject, ev.Model, detailJSON,
		); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) List(limit int) ([]Summary, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(`
		SELECT m.slug, m.mission_id, COALESCE(m.directive,''), m.task_count, m.done_task_count,
		       m.finding_count, m.duration_seconds, COALESCE(m.team_id,''), COALESCE(m.visibility,'private'),
		       COALESCE(m.shared_by,''), COALESCE(m.shared_ts,0), COALESCE(m.source_brain_id,''), m.exported_ts,
		       COUNT(e.event_idx) AS event_count, MIN(e.ts) AS first_event_ts, MAX(e.ts) AS last_event_ts
		FROM recordings_missions m
		LEFT JOIN recordings_events e ON e.slug = m.slug
		GROUP BY m.slug, m.mission_id, m.directive, m.task_count, m.done_task_count, m.finding_count, m.duration_seconds,
		         m.team_id, m.visibility, m.shared_by, m.shared_ts, m.source_brain_id, m.exported_ts
		ORDER BY m.exported_ts DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Summary
	for rows.Next() {
		var s1 Summary
		var first, last sql.NullFloat64
		if err := rows.Scan(&s1.Slug, &s1.MissionID, &s1.Directive, &s1.TaskCount, &s1.DoneTaskCount,
			&s1.FindingCount, &s1.DurationSeconds, &s1.TeamID, &s1.Visibility, &s1.SharedBy, &s1.SharedTS,
			&s1.SourceBrainID, &s1.ExportedTS, &s1.EventCount, &first, &last); err != nil {
			return nil, err
		}
		if first.Valid {
			v := first.Float64
			s1.FirstEventTS = &v
		}
		if last.Valid {
			v := last.Float64
			s1.LastEventTS = &v
		}
		out = append(out, s1)
	}
	return out, rows.Err()
}

func (s *Store) MissionBySlug(slug string) (*MissionMeta, error) {
	var meta MissionMeta
	var modelsJSON, platformJSON string
	err := s.db.QueryRow(
		`SELECT slug, mission_id, COALESCE(directive,''), task_count, done_task_count, finding_count, duration_seconds,
		        COALESCE(models_json,''), COALESCE(platform_json,''), COALESCE(team_id,''), COALESCE(visibility,'private'),
		        COALESCE(shared_by,''), COALESCE(shared_ts,0), COALESCE(source_brain_id,''), exported_ts
		   FROM recordings_missions WHERE slug=?`,
		slug,
	).Scan(&meta.Slug, &meta.MissionID, &meta.Directive, &meta.TaskCount, &meta.DoneTaskCount, &meta.FindingCount,
		&meta.DurationSeconds, &modelsJSON, &platformJSON, &meta.TeamID, &meta.Visibility, &meta.SharedBy, &meta.SharedTS,
		&meta.SourceBrainID, &meta.ExportedTS)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if modelsJSON != "" {
		_ = json.Unmarshal([]byte(modelsJSON), &meta.Models)
	}
	if platformJSON != "" {
		_ = json.Unmarshal([]byte(platformJSON), &meta.Platform)
	}
	return &meta, nil
}

func (s *Store) Share(slug, visibility, teamID, sharedBy string, sharedTS float64) error {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return fmt.Errorf("slug required")
	}
	vis := normalizedVisibility(visibility)
	if vis != "team" && vis != "public" {
		return fmt.Errorf("visibility must be team or public")
	}
	teamID = strings.TrimSpace(teamID)
	if vis == "team" && teamID == "" {
		return fmt.Errorf("team_id required when visibility=team")
	}
	if vis != "team" {
		teamID = ""
	}
	if sharedTS <= 0 {
		sharedTS = nowTS()
	}
	res, err := s.db.Exec(
		`UPDATE recordings_missions
		    SET visibility=?, team_id=?, shared_by=?, shared_ts=?
		  WHERE slug=?`,
		vis, teamID, strings.TrimSpace(sharedBy), sharedTS, slug,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("no recording for slug %q", slug)
	}
	return nil
}

func (s *Store) MissionByID(missionID int64) (*MissionMeta, error) {
	var slug string
	err := s.db.QueryRow(`SELECT slug FROM recordings_missions WHERE mission_id=? ORDER BY exported_ts DESC LIMIT 1`, missionID).Scan(&slug)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.MissionBySlug(slug)
}

func (s *Store) ReplayBySlug(slug string) ([]Event, error) {
	rows, err := s.db.Query(
		`SELECT ts, kind, COALESCE(actor,''), COALESCE(subject,''), COALESCE(model,''), COALESCE(detail_json,'')
		   FROM recordings_events WHERE slug=? ORDER BY ts ASC, event_idx ASC`, slug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var ev Event
		var detailJSON string
		if err := rows.Scan(&ev.TS, &ev.Kind, &ev.Actor, &ev.Subject, &ev.Model, &detailJSON); err != nil {
			return nil, err
		}
		if detailJSON != "" {
			_ = json.Unmarshal([]byte(detailJSON), &ev.Detail)
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (s *Store) ReplayByMissionID(missionID int64) ([]Event, error) {
	meta, err := s.MissionByID(missionID)
	if err != nil || meta == nil {
		return nil, err
	}
	return s.ReplayBySlug(meta.Slug)
}

// Query runs read-only ad-hoc analysis on a dedicated, locked-down connection:
// sqlguard.ApplyLockdown (the real wall — no local filesystem, no extension
// autoload, config frozen) is applied to the SAME conn the query then runs on,
// with sqlguard.ValidateReadOnly as defense-in-depth. Safe because recordings
// only ever queries its own in-db tables (no read_csv/ATTACH/COPY/glob).
func (s *Store) Query(sqlText string, rowCap int) (Report, error) {
	if rowCap <= 0 {
		rowCap = 1000
	}
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return Report{}, err
	}
	defer conn.Close()
	if err := sqlguard.ApplyLockdown(ctx, conn); err != nil {
		return Report{}, err
	}
	if err := sqlguard.ValidateReadOnly(sqlText); err != nil {
		return Report{}, err
	}
	rows, err := conn.QueryContext(ctx, sqlText)
	if err != nil {
		return Report{}, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return Report{}, err
	}
	out := Report{Columns: cols}
	for rows.Next() && len(out.Rows) < rowCap {
		raw := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return Report{}, err
		}
		row := make([]string, len(cols))
		for i, v := range raw {
			row[i] = fmt.Sprintf("%v", v)
		}
		out.Rows = append(out.Rows, row)
	}
	return out, rows.Err()
}

func normalizedVisibility(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "private":
		return "private"
	case "team":
		return "team"
	case "public":
		return "public"
	default:
		return "private"
	}
}

func migrateMissionsColumns(db *sql.DB) error {
	add := []struct {
		name string
		def  string
	}{
		{name: "team_id", def: "VARCHAR DEFAULT ''"},
		{name: "visibility", def: "VARCHAR DEFAULT 'private'"},
		{name: "shared_by", def: "VARCHAR DEFAULT ''"},
		{name: "shared_ts", def: "DOUBLE DEFAULT 0"},
		{name: "source_brain_id", def: "VARCHAR DEFAULT ''"},
	}
	for _, c := range add {
		ok, err := missionColumnExists(db, c.name)
		if err != nil {
			return err
		}
		if ok {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE recordings_missions ADD COLUMN ` + c.name + ` ` + c.def); err != nil {
			return err
		}
	}
	return nil
}

func missionColumnExists(db *sql.DB, name string) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_schema='main' AND table_name='recordings_missions' AND column_name=?`, name).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
