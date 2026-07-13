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

	"github.com/pdbethke/corralai/internal/sqlguard"

	_ "github.com/marcboeker/go-duckdb/v2"
)

var now = func() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// Event is one timestamped thing that happened in a mission.
type Event struct {
	TS        float64 // set by Record on write; populated on read by EventsForMission
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
	// ts is stamped with the wall clock by default; a non-zero e.TS is honored
	// instead, so a caller (e.g. a test placing beats deterministically inside
	// a mission's replay window) can control the timestamp. Production callers
	// never set e.TS, so this preserves the append-with-now behavior exactly.
	ts := e.TS
	if ts == 0 {
		ts = now()
	}
	_, err := s.db.Exec(
		`INSERT INTO events (id, ts, mission_id, kind, actor, subject, model, detail)
		 VALUES (nextval('event_id'), ?, ?, ?, ?, ?, ?, ?)`,
		ts, e.MissionID, e.Kind, e.Actor, e.Subject, e.Model, detail)
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

// CountKind returns how many events of kind have been recorded for a mission
// — used to enforce per-mission volume guards (e.g. agent_activity's cap)
// without keeping an in-memory counter that would reset on restart.
func (s *Store) CountKind(missionID int64, kind string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE mission_id=? AND kind=?`, missionID, kind).Scan(&n)
	return n, err
}

// MissionCompletedAt returns the ts of the latest mission_completed event for
// missionID, if any — mission_history/replay use this to prefer event-based
// duration once mission_completed exists, falling back to task timestamps
// for missions recorded before this telemetry kind shipped.
func (s *Store) MissionCompletedAt(missionID int64) (float64, bool, error) {
	var ts float64
	err := s.db.QueryRow(
		`SELECT ts FROM events WHERE mission_id=? AND kind='mission_completed' ORDER BY ts DESC LIMIT 1`,
		missionID).Scan(&ts)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return ts, true, nil
}

// EventsForMission returns every recorded event for a mission, oldest first —
// Part B's replay merges this with the durable task/finding/execution rows so
// ambience (mission_completed, agent_activity, reviews, proposals, …) rides
// the same timeline as the mission's own state changes.
func (s *Store) EventsForMission(missionID int64) ([]Event, error) {
	rows, err := s.db.Query(
		`SELECT ts, kind, COALESCE(actor,''), COALESCE(subject,''), COALESCE(model,''), COALESCE(detail,'') FROM events WHERE mission_id=? ORDER BY ts ASC`,
		missionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var detail string
		if err := rows.Scan(&e.TS, &e.Kind, &e.Actor, &e.Subject, &e.Model, &detail); err != nil {
			return nil, err
		}
		if detail != "" {
			_ = json.Unmarshal([]byte(detail), &e.Detail)
		}
		e.MissionID = missionID
		out = append(out, e)
	}
	return out, rows.Err()
}

// GlobalAmbienceBetween returns global-ambience events (mission_id=0) of the
// given kinds whose timestamps fall within [lo, hi], oldest first. These beats
// (claim_made/claim_released, recorded by internal/coord with no mission to
// join on) carry no mission_id, so Part B's v2 replay merge folds them into a
// mission's stream by TIME-WINDOW inclusion — the file-tree replay lens
// reconstructs "who touched which path, when" from them (paths only; the tape
// never captures file contents). Returns nothing for an empty kind list or an
// inverted window. The kinds are parameterized, never interpolated.
func (s *Store) GlobalAmbienceBetween(kinds []string, lo, hi float64) ([]Event, error) {
	if len(kinds) == 0 || hi < lo {
		return nil, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(kinds)), ",")
	args := make([]any, 0, len(kinds)+2)
	for _, k := range kinds {
		args = append(args, k)
	}
	args = append(args, lo, hi)
	// #nosec G202 -- not injectable: ph is only literal "?," placeholder markers (one per kind); every value (kinds, lo, hi) is bound through ? placeholders in args.
	q := `SELECT ts, kind, COALESCE(actor,''), COALESCE(subject,''), COALESCE(model,''), COALESCE(detail,'') FROM events WHERE mission_id=0 AND kind IN (` + ph + `) AND ts BETWEEN ? AND ? ORDER BY ts ASC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var detail string
		if err := rows.Scan(&e.TS, &e.Kind, &e.Actor, &e.Subject, &e.Model, &detail); err != nil {
			return nil, err
		}
		if detail != "" {
			_ = json.Unmarshal([]byte(detail), &e.Detail)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountForMission returns how many telemetry events are recorded for a
// mission — the telemetry half of the DB relief valve's FOOTPRINT count (#66).
func (s *Store) CountForMission(missionID int64) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE mission_id=?`, missionID).Scan(&n)
	return n, err
}

// PruneMission deletes every telemetry event recorded for a mission — the
// telemetry half of the DB relief valve's PRUNE action (#66). DESTRUCTIVE and
// irreversible: callers must export the mission's replay tape (which reads
// these same rows via EventsForMission) BEFORE calling this, and must gate
// the call to a human admin — see internal/brain's PruneMission.
func (s *Store) PruneMission(missionID int64) error {
	_, err := s.db.Exec(`DELETE FROM events WHERE mission_id=?`, missionID)
	return err
}

// ActorRoleCount is a (actor, role) grouped count of events of one kind —
// the leaderboard's source for a rework/refusal signal it can attribute to an
// agent+role without a second join (role travels in the event's detail JSON
// for kinds like task_reissued; see internal/brain/tasks.go).
type ActorRoleCount struct {
	Actor string
	Role  string
	Count int
}

// CountByActorAndDetailRole groups events of kind by actor and detail.role —
// used by the model×role leaderboard to source a rework count from
// task_reissued events (a bee re-claiming its own lost-reply task is not
// rework by a peer, but a reissue burst for a role/model is still a useful
// friction signal). Rows with a null actor are excluded.
func (s *Store) CountByActorAndDetailRole(kind string) ([]ActorRoleCount, error) {
	rows, err := s.db.Query(
		`SELECT actor, COALESCE(json_extract_string(detail,'$.role'),'') AS role, count(*) AS n
		 FROM events WHERE kind=? AND actor IS NOT NULL GROUP BY actor, role`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ActorRoleCount
	for rows.Next() {
		var c ActorRoleCount
		if err := rows.Scan(&c.Actor, &c.Role, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
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

// Query runs read-only ad-hoc analysis (SELECT/WITH only — no writes, no attach,
// no file/network reach-out) on a dedicated, locked-down connection:
// sqlguard.ApplyLockdown (the real wall — no local filesystem, no extension
// autoload, config frozen) is applied to the SAME conn the query then runs on,
// with sqlguard.ValidateReadOnly as defense-in-depth (normalized + construct-
// banning, not a bare prefix check). Safe because telemetry only ever queries
// its own in-db events table (no read_csv/ATTACH/COPY/glob).
// Query runs an operator ad-hoc read-only SELECT/WITH against the event log.
// It validates via sqlguard but does NOT apply the DuckDB filesystem lockdown:
// this store's connection pool also serves the append-only WRITE path, and
// `SET disabled_filesystems`/`lock_configuration` take effect database-wide,
// which would fatally break the next checkpoint (audit-log loss + crash). The
// lockdown-as-real-wall belongs on a dedicated read-only handle (see oracle);
// here sqlguard.ValidateReadOnly is the wall. This is an admin-gated surface.
func (s *Store) Query(q string) (Report, error) {
	if err := sqlguard.ValidateReadOnly(q); err != nil {
		return Report{}, err
	}
	rows, err := s.db.Query(q)
	if err != nil {
		return Report{}, err
	}
	defer rows.Close()
	return materialize(rows)
}

// scan runs a TRUSTED fixed query (named reports) on the pool and materializes it.
func (s *Store) scan(q string) (Report, error) {
	rows, err := s.db.Query(q)
	if err != nil {
		return Report{}, err
	}
	defer rows.Close()
	return materialize(rows)
}

// materialize reads every row of rs into a columnar Report.
func materialize(rows *sql.Rows) (Report, error) {
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
