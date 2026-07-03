// SPDX-License-Identifier: Elastic-2.0

// Package telemetry is corralai's mission event log: an append-only, DuckDB-
// backed record of everything a mission does — created, tasks enqueued/claimed/
// completed/reaped, findings, re-plans, sprints, reviews, done — so the execution
// timeline can be analyzed columnar (named reports, ad-hoc SQL, or any DuckDB
// client against the file). The operational stores hold final state; this holds
// the history.
package telemetry

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"
)

var now = func() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// Event is one timestamped thing that happened in a mission.
type Event struct {
	MissionID int64
	Kind      string         // task_claimed, finding_reported, review_changes, …
	Actor     string         // agent / principal / "engine"
	Subject   string         // task key / finding target / …
	Model     string         // model that filed this event (reporter_model for findings); "" when unknown
	Detail    map[string]any // arbitrary extra context (stored as JSON)
}

// Report is a generic columnar result (so one surface renders any query).
type Report struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
}

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	db, err := sql.Open("duckdb", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS events (
		id BIGINT PRIMARY KEY,
		ts DOUBLE NOT NULL,
		mission_id BIGINT NOT NULL DEFAULT 0,
		kind VARCHAR NOT NULL,
		actor VARCHAR,
		subject VARCHAR,
		model VARCHAR,
		detail VARCHAR)`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`CREATE SEQUENCE IF NOT EXISTS event_id START 1`); err != nil {
		_ = db.Close()
		return nil, err
	}
	// Idempotent migration: add model column to DBs created before this field existed.
	if _, err := db.Exec(`ALTER TABLE events ADD COLUMN IF NOT EXISTS model VARCHAR`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Record appends an event. Best-effort: callers log and continue on error so
// telemetry can never block or fail a tool.
func (s *Store) Record(e Event) error {
	var detail string
	if len(e.Detail) > 0 {
		b, _ := json.Marshal(e.Detail)
		detail = string(b)
	}
	_, err := s.db.Exec(
		`INSERT INTO events (id, ts, mission_id, kind, actor, subject, model, detail)
		 VALUES (nextval('event_id'), ?, ?, ?, ?, ?, ?, ?)`,
		now(), e.MissionID, e.Kind, e.Actor, e.Subject, e.Model, detail)
	return err
}

// TimelineEntry is one durable event in an actor's history (for post-mortems).
type TimelineEntry struct {
	TS        float64
	MissionID int64
	Kind      string
	Subject   string
	Detail    string
}

// AgentTimeline returns up to limit of an actor's recorded events, newest first —
// the DURABLE timeline (claims, completions, findings, re-plans) the narrator uses
// to debrief a bee across a whole build, long after the in-memory rings have rolled.
// Parameterized so the actor name is never interpolated into SQL.
func (s *Store) AgentTimeline(actor string, limit int) ([]TimelineEntry, error) {
	if limit <= 0 {
		limit = 40
	}
	rows, err := s.db.Query(
		`SELECT ts, mission_id, kind, COALESCE(subject,''), COALESCE(detail,'')
		 FROM events WHERE actor=? ORDER BY ts DESC LIMIT ?`, actor, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TimelineEntry
	for rows.Next() {
		var e TimelineEntry
		if err := rows.Scan(&e.TS, &e.MissionID, &e.Kind, &e.Subject, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// reports are the fixed, named analytic queries.
var reports = map[string]string{
	"missions": `SELECT mission_id, count(*) AS events, min(ts) AS started, max(ts) AS ended,
	             round(max(ts)-min(ts), 1) AS duration_s FROM events GROUP BY mission_id ORDER BY mission_id`,
	"agents":   `SELECT actor, count(*) AS completed FROM events WHERE kind='task_completed' AND actor IS NOT NULL GROUP BY actor ORDER BY completed DESC`,
	"kinds":    `SELECT kind, count(*) AS n FROM events GROUP BY kind ORDER BY n DESC`,
	"findings": `SELECT kind, count(*) AS n FROM events WHERE kind LIKE 'finding%' GROUP BY kind ORDER BY n DESC`,
	"replans":  `SELECT kind, count(*) AS n FROM events WHERE kind IN ('task_superseded','task_cancelled','task_reopened') GROUP BY kind ORDER BY n DESC`,
	"sprints":  `SELECT kind, count(*) AS n FROM events WHERE kind IN ('review_accepted','review_changes') GROUP BY kind ORDER BY n DESC`,
	"model_comparison": `WITH rep AS (
  SELECT COALESCE(NULLIF(model,''),'(no model)') AS model,
    count(*) AS findings,
    count(*) FILTER (WHERE json_extract_string(detail,'$.severity')='critical') AS critical,
    count(*) FILTER (WHERE json_extract_string(detail,'$.severity')='high')     AS high,
    count(*) FILTER (WHERE json_extract_string(detail,'$.severity')='medium')   AS medium,
    count(*) FILTER (WHERE json_extract_string(detail,'$.severity')='low')      AS low
  FROM events WHERE kind='finding_reported' GROUP BY 1
),
res AS (
  SELECT model, fid, arg_max(outcome, ts) AS outcome FROM (
    SELECT COALESCE(NULLIF(model,''),'(no model)') AS model,
      COALESCE(NULLIF(json_extract_string(detail,'$.finding_id'),''), CAST(id AS VARCHAR)) AS fid,
      json_extract_string(detail,'$.outcome') AS outcome, ts
    FROM events WHERE kind='finding_resolved'
  ) GROUP BY model, fid
),
resagg AS (
  SELECT model,
    count(*) FILTER (WHERE outcome='addressed') AS addressed,
    count(*) FILTER (WHERE outcome='dismissed') AS dismissed
  FROM res GROUP BY model
)
SELECT rep.model, rep.findings, rep.critical, rep.high, rep.medium, rep.low,
  COALESCE(resagg.addressed,0) AS addressed,
  COALESCE(resagg.dismissed,0) AS dismissed,
  -- clamp: a model can't resolve more findings than it reported; defend against
  -- bad data (model-string mismatch, cross-model resolves) — never show a negative count.
  GREATEST(0, rep.findings - COALESCE(resagg.addressed,0) - COALESCE(resagg.dismissed,0)) AS open,
  CASE WHEN COALESCE(resagg.addressed,0)+COALESCE(resagg.dismissed,0)=0 THEN NULL
    ELSE round(100.0*resagg.addressed/(resagg.addressed+resagg.dismissed),1) END AS confirm_pct
FROM rep LEFT JOIN resagg USING (model)
ORDER BY rep.findings DESC`,
}

// ReportNames lists the available named reports.
func ReportNames() []string {
	out := make([]string, 0, len(reports))
	for k := range reports {
		out = append(out, k)
	}
	return out
}

// RunReport runs a named analytic query.
func (s *Store) RunReport(name string) (Report, error) {
	q, ok := reports[name]
	if !ok {
		return Report{}, fmt.Errorf("unknown report %q (have: %s)", name, strings.Join(ReportNames(), ", "))
	}
	return s.scan(q)
}

// Query runs read-only ad-hoc analysis (SELECT/WITH only — no writes, no attach).
func (s *Store) Query(q string) (Report, error) {
	t := strings.ToLower(strings.TrimSpace(q))
	if !(strings.HasPrefix(t, "select") || strings.HasPrefix(t, "with")) || strings.Contains(q, ";") {
		return Report{}, fmt.Errorf("only a single read-only SELECT/WITH query is allowed")
	}
	return s.scan(q)
}

func (s *Store) scan(q string) (Report, error) {
	rows, err := s.db.Query(q)
	if err != nil {
		return Report{}, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return Report{}, err
	}
	rep := Report{Columns: cols, Rows: [][]any{}}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return Report{}, err
		}
		rep.Rows = append(rep.Rows, vals)
	}
	return rep, rows.Err()
}
