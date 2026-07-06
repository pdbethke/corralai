// SPDX-License-Identifier: Elastic-2.0

package recordings

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/marcboeker/go-duckdb/v2"
)

func openT(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "recordings.duckdb"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestUpsertListReplayAndQuery(t *testing.T) {
	s := openT(t)
	meta := MissionMeta{
		Slug:            "golden-run",
		MissionID:       42,
		Directive:       "ship feature X",
		TaskCount:       5,
		DoneTaskCount:   5,
		FindingCount:    2,
		DurationSeconds: 123.4,
		Models:          []string{"ollama:qwen3"},
		Platform:        map[string]any{"inference": "local ollama"},
		ExportedTS:      1710000000,
	}
	events := []Event{
		{TS: 10, Kind: "task_created", Subject: "build#1"},
		{TS: 11, Kind: "task_done", Actor: "builder", Subject: "build#1"},
		{TS: 12, Kind: "finding_reported", Actor: "tester", Subject: "api", Model: "qwen3", Detail: map[string]any{"severity": "high"}},
	}
	if err := s.Upsert(meta, events); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	list, err := s.List(10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list rows=%d, want 1", len(list))
	}
	if list[0].Slug != "golden-run" || list[0].EventCount != 3 {
		t.Fatalf("list row=%+v", list[0])
	}

	gotMeta, err := s.MissionByID(42)
	if err != nil || gotMeta == nil {
		t.Fatalf("mission by id: meta=%v err=%v", gotMeta, err)
	}
	if gotMeta.Slug != "golden-run" || gotMeta.Directive != "ship feature X" {
		t.Fatalf("bad meta: %+v", gotMeta)
	}

	replay, err := s.ReplayBySlug("golden-run")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(replay) != 3 || replay[2].Kind != "finding_reported" {
		t.Fatalf("bad replay: %+v", replay)
	}

	rep, err := s.Query("SELECT kind, count(*) AS n FROM recordings_events GROUP BY kind ORDER BY kind", 100)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(rep.Columns) != 2 || len(rep.Rows) != 3 {
		t.Fatalf("bad report: cols=%v rows=%v", rep.Columns, rep.Rows)
	}
}

func TestUpsertReplacesSlugRows(t *testing.T) {
	s := openT(t)
	meta := MissionMeta{Slug: "same", MissionID: 1}
	if err := s.Upsert(meta, []Event{{TS: 1, Kind: "task_created"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(meta, []Event{{TS: 2, Kind: "task_done"}}); err != nil {
		t.Fatal(err)
	}
	replay, err := s.ReplayBySlug("same")
	if err != nil {
		t.Fatal(err)
	}
	if len(replay) != 1 || replay[0].Kind != "task_done" {
		t.Fatalf("replay not replaced: %+v", replay)
	}
}

func TestQueryRejectsWrites(t *testing.T) {
	s := openT(t)
	if _, err := s.Query("DELETE FROM recordings_events", 100); err == nil {
		t.Fatal("expected write query rejection")
	}
	if _, err := s.Query("SELECT 1; DROP TABLE recordings_events", 100); err == nil {
		t.Fatal("expected multi-statement rejection")
	}
}

func TestOpenMigratesLegacySchemaAndShareRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recordings.duckdb")
	raw, err := sql.Open("duckdb", path)
	if err != nil {
		t.Fatalf("open raw duckdb: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE recordings_missions (
		slug VARCHAR PRIMARY KEY,
		mission_id BIGINT NOT NULL DEFAULT 0,
		directive VARCHAR,
		task_count INTEGER NOT NULL DEFAULT 0,
		done_task_count INTEGER NOT NULL DEFAULT 0,
		finding_count INTEGER NOT NULL DEFAULT 0,
		duration_seconds DOUBLE NOT NULL DEFAULT 0,
		models_json VARCHAR,
		platform_json VARCHAR,
		exported_ts DOUBLE NOT NULL
	)`); err != nil {
		t.Fatalf("create legacy missions table: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE recordings_events (
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
		t.Fatalf("create legacy events table: %v", err)
	}
	_ = raw.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.Upsert(MissionMeta{
		Slug:      "legacy",
		MissionID: 99,
		SharedBy:  "owner@x.com",
	}, []Event{{TS: 1, Kind: "task_created"}}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.Share("legacy", "team", "team-alpha", "owner@x.com", 1712345678); err != nil {
		t.Fatalf("share: %v", err)
	}
	meta, err := s.MissionBySlug("legacy")
	if err != nil || meta == nil {
		t.Fatalf("mission by slug: meta=%v err=%v", meta, err)
	}
	if meta.Visibility != "team" || meta.TeamID != "team-alpha" || meta.SharedBy != "owner@x.com" || meta.SharedTS != 1712345678 {
		t.Fatalf("unexpected share metadata: %+v", meta)
	}
	list, err := s.List(10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Visibility != "team" || list[0].TeamID != "team-alpha" {
		t.Fatalf("unexpected list row: %+v", list)
	}
}
