// SPDX-License-Identifier: Elastic-2.0

// Package queue is the brain's task queue: the pull-model substrate the swarm
// executes a mission through. The mission engine enqueues a dependency-ordered
// set of tasks; a hive of agent "bees" atomically claims ready tasks, runs them,
// and completes them; a reaper requeues the tasks of bees that die or go absent.
//
// It is backed by pure-Go SQLite (modernc.org/sqlite, no CGO) with the same
// recipe the coordination store uses — WAL + MaxOpenConns=1 — because the queue
// is hot OLTP with claim contention: the single serialized writer makes the
// atomic claim (one bee per task, ever) free and correct.
package queue

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Task statuses. cancelled/superseded are reserved for the re-planning work
// (sub-project #4) and are never transitioned here.
const (
	StatusPending    = "pending"
	StatusReady      = "ready"
	StatusClaimed    = "claimed"
	StatusDone       = "done"
	StatusCancelled  = "cancelled"  // reserved
	StatusSuperseded = "superseded" // reserved
)

var realNow = func() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// now is the clock seam (overridable in tests to drive lease expiry).
var now = realNow

// TaskSpec is one unit of a seed plan: what the mission engine enqueues.
type TaskSpec struct {
	Key         string   // per-mission unique key (e.g. "build-ui")
	Role        string   // builder|tester|pentester|reviewer|"" (any role)
	Title       string   // short label for the UI
	Instruction string   // what the bee must do
	DependsOn   []string // task keys (within the mission) that must be done first
	Verify      string   // command that MUST pass (exit 0) before this task can complete; "" = ungated
}

// Task is a queued unit of work.
type Task struct {
	ID             int64    `json:"id"`
	MissionID      int64    `json:"mission_id"`
	Key            string   `json:"key"`
	Role           string   `json:"role"`
	Title          string   `json:"title"`
	Instruction    string   `json:"instruction,omitempty"`
	Status         string   `json:"status"`
	DependsOn      []string `json:"depends_on,omitempty"`
	ClaimedBy      string   `json:"claimed_by,omitempty"`
	Result         string   `json:"result,omitempty"`
	CreatedTS      float64  `json:"created_ts"`
	ClaimedTS      float64  `json:"claimed_ts,omitempty"`
	DoneTS         float64  `json:"done_ts,omitempty"`
	ClaimExpiresTS float64  `json:"claim_expires_ts,omitempty"`
	Supersedes     int64    `json:"supersedes,omitempty"` // the task id this one replaces (lineage)
	Verify         string   `json:"verify,omitempty"`
	Reissued       bool     `json:"reissued,omitempty"` // this claim was already yours — the reply was lost, not the task
}

type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS tasks (
  id               INTEGER PRIMARY KEY,
  mission_id       INTEGER NOT NULL,
  key              TEXT    NOT NULL,
  role             TEXT    NOT NULL DEFAULT '',
  title            TEXT    NOT NULL DEFAULT '',
  instruction      TEXT    NOT NULL DEFAULT '',
  status           TEXT    NOT NULL,
  depends_on       TEXT    NOT NULL DEFAULT '[]',
  claimed_by       TEXT,
  claim_expires_ts REAL,
  result           TEXT,
  created_ts       REAL NOT NULL,
  claimed_ts       REAL,
  done_ts          REAL,
  supersedes       INTEGER NOT NULL DEFAULT 0,
  verify           TEXT    NOT NULL DEFAULT '',
  claimed_instance TEXT    NOT NULL DEFAULT '',
  UNIQUE(mission_id, key)
);
CREATE INDEX IF NOT EXISTS ix_tasks_claimable ON tasks(status, role);
CREATE INDEX IF NOT EXISTS ix_tasks_mission   ON tasks(mission_id);
CREATE INDEX IF NOT EXISTS ix_tasks_claimed   ON tasks(claimed_by);
CREATE INDEX IF NOT EXISTS ix_tasks_lease     ON tasks(claim_expires_ts);

CREATE TABLE IF NOT EXISTS findings (
  id               INTEGER PRIMARY KEY,
  mission_id       INTEGER NOT NULL,
  task_id          INTEGER NOT NULL DEFAULT 0,
  reporter         TEXT    NOT NULL,
  type             TEXT    NOT NULL,
  severity         TEXT    NOT NULL,
  target           TEXT    NOT NULL DEFAULT '',
  evidence         TEXT    NOT NULL DEFAULT '',
  suggested_action TEXT    NOT NULL DEFAULT '',
  status           TEXT    NOT NULL,
  recurring        INTEGER NOT NULL DEFAULT 0,
  created_ts       REAL    NOT NULL,
  reporter_model   TEXT    NOT NULL DEFAULT '',
  reporter_backend TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS ix_findings_mission ON findings(mission_id);
CREATE INDEX IF NOT EXISTS ix_findings_status  ON findings(status);
CREATE TABLE IF NOT EXISTS executions (
  id         INTEGER PRIMARY KEY,
  mission_id INTEGER NOT NULL,
  agent      TEXT    NOT NULL DEFAULT '',
  role       TEXT    NOT NULL DEFAULT '',
  command    TEXT    NOT NULL DEFAULT '',
  exit_code  INTEGER NOT NULL DEFAULT 0,
  ok         INTEGER NOT NULL DEFAULT 0,
  ts         INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS ix_executions_mission ON executions(mission_id);
`

// Open returns a Store backed by a SQLite file (WAL). MaxOpenConns=1 serializes
// writes — which is exactly what makes the atomic claim correct.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	// Idempotent migrations for DBs created before these columns existed.
	for _, stmt := range []string{
		`ALTER TABLE tasks ADD COLUMN supersedes INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE findings ADD COLUMN recurring INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE tasks ADD COLUMN verify TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE findings ADD COLUMN reporter_model TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE findings ADD COLUMN reporter_backend TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE tasks ADD COLUMN claimed_instance TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return nil, err
		}
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Enqueue inserts a mission's tasks, all pending. Re-enqueuing the same
// (mission, key) is rejected by the UNIQUE constraint.
func (s *Store) Enqueue(missionID int64, specs []TaskSpec) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, sp := range specs {
		deps := sp.DependsOn
		if deps == nil {
			deps = []string{}
		}
		b, _ := json.Marshal(deps)
		if _, err := tx.Exec(
			`INSERT INTO tasks (mission_id,key,role,title,instruction,status,depends_on,verify,created_ts)
			 VALUES (?,?,?,?,?,?,?,?,?)`,
			missionID, sp.Key, sp.Role, sp.Title, sp.Instruction, StatusPending, string(b), sp.Verify, now(),
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteMissionTasks removes all tasks for a mission. Used to roll back a
// partially-provisioned repo mission: when create_mission deletes the mission
// row (mission.Store) it must also drop the enqueued tasks here (queue.Store's
// separate DB) so no orphan tasks point at a deleted mission.
func (s *Store) DeleteMissionTasks(missionID int64) error {
	_, err := s.db.Exec(`DELETE FROM tasks WHERE mission_id=?`, missionID)
	return err
}

// PromoteReady flips pending → ready for every task whose dependencies are all
// done. Idempotent; returns how many it promoted. Reads are fully drained before
// writes so the single connection is never used concurrently.
func (s *Store) PromoteReady(missionID int64) (int, error) {
	done, err := s.keysWithStatus(missionID, StatusDone)
	if err != nil {
		return 0, err
	}

	type pend struct {
		id   int64
		deps []string
	}
	var pending []pend
	rows, err := s.db.Query(`SELECT id, depends_on FROM tasks WHERE mission_id=? AND status=?`, missionID, StatusPending)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var id int64
		var depJSON string
		if err := rows.Scan(&id, &depJSON); err != nil {
			_ = rows.Close()
			return 0, err
		}
		var deps []string
		_ = json.Unmarshal([]byte(depJSON), &deps)
		pending = append(pending, pend{id, deps})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	_ = rows.Close()

	n := 0
	for _, p := range pending {
		if allDone(p.deps, done) {
			if _, err := s.db.Exec(`UPDATE tasks SET status=? WHERE id=? AND status=?`, StatusReady, p.id, StatusPending); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

// ClaimNext atomically claims the oldest ready task this bee may run and returns
// it, or (nil, nil) when nothing is claimable. A task with role "" is claimable
// by any bee; a role-typed bee also picks up those generic tasks. Exactly one bee
// can win a given task — guaranteed by the transaction over the single writer.
func (s *Store) ClaimNext(bee string, roles []string, leaseSeconds float64) (*Task, error) {
	return s.ClaimNextAs(bee, "", roles, leaseSeconds)
}

// ClaimNextAs is ClaimNext with the caller's instance identity (its hostname).
// Compose `--scale` replicas share one AGENT_NAME, so the bee name alone can't
// distinguish "I lost the reply to my own claim" (re-issue immediately) from
// "my same-named sibling is working that task" (hands off until the lease
// expires). Instance disambiguates: a claimed task is re-issued to the SAME
// name+instance on its next poll — the lost-reply self-heal — while a
// different (or unknown) instance only inherits it once the lease runs out.
func (s *Store) ClaimNextAs(bee, instance string, roles []string, leaseSeconds float64) (*Task, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Self-heal an orphaned claim: without this, the claim is stuck forever —
	// the bee heartbeats, presence keeps it un-reapable by Reap, and everything
	// downstream deadlocks.
	{
		t0 := now()
		var t Task
		var depJSON string
		err := tx.QueryRow(
			`SELECT id,mission_id,key,role,title,instruction,depends_on,created_ts FROM tasks
			 WHERE status=? AND claimed_by=?
			   AND ((claimed_instance=? AND ?!='') OR claim_expires_ts < ?)
			 ORDER BY claimed_ts, id LIMIT 1`,
			StatusClaimed, bee, instance, instance, t0,
		).Scan(&t.ID, &t.MissionID, &t.Key, &t.Role, &t.Title, &t.Instruction, &depJSON, &t.CreatedTS)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		if err == nil {
			exp := t0 + leaseSeconds
			if _, err := tx.Exec(
				`UPDATE tasks SET claimed_ts=?, claim_expires_ts=?, claimed_instance=? WHERE id=?`,
				t0, exp, instance, t.ID,
			); err != nil {
				return nil, err
			}
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			_ = json.Unmarshal([]byte(depJSON), &t.DependsOn)
			t.Status = StatusClaimed
			t.ClaimedBy = bee
			t.ClaimedTS = t0
			t.ClaimExpiresTS = exp
			t.Reissued = true
			return &t, nil
		}
	}

	q := `SELECT id,mission_id,key,role,title,instruction,depends_on,created_ts FROM tasks WHERE status=?`
	args := []any{StatusReady}
	if len(roles) > 0 {
		// role IN (roles..., '') — a role-typed bee also serves untagged tasks.
		ph := strings.TrimSuffix(strings.Repeat("?,", len(roles)+1), ",")
		q += ` AND role IN (` + ph + `)`
		for _, r := range roles {
			args = append(args, r)
		}
		args = append(args, "")
	}
	q += ` ORDER BY created_ts, id LIMIT 1`

	var t Task
	var depJSON string
	err = tx.QueryRow(q, args...).Scan(&t.ID, &t.MissionID, &t.Key, &t.Role, &t.Title, &t.Instruction, &depJSON, &t.CreatedTS)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	ts := now()
	exp := ts + leaseSeconds
	if _, err := tx.Exec(
		`UPDATE tasks SET status=?, claimed_by=?, claimed_ts=?, claim_expires_ts=?, claimed_instance=? WHERE id=?`,
		StatusClaimed, bee, ts, exp, instance, t.ID,
	); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(depJSON), &t.DependsOn)
	t.Status = StatusClaimed
	t.ClaimedBy = bee
	t.ClaimedTS = ts
	t.ClaimExpiresTS = exp
	return &t, nil
}

// Complete marks a claimed task done — only if bee is its claimer. Idempotent: a
// second call (or a non-claimer) returns (false, nil) and changes nothing.
func (s *Store) Complete(id int64, bee, result string) (bool, error) {
	res, err := s.db.Exec(
		`UPDATE tasks SET status=?, result=?, done_ts=?, claim_expires_ts=NULL
		 WHERE id=? AND status=? AND claimed_by=?`,
		StatusDone, result, now(), id, StatusClaimed, bee,
	)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ReclaimIdle requeues (claimed → ready) the expired-lease tasks of a bee that
// just reported itself idle — the brain's slacker rule. A live bee is never
// reaped by Reap (presence is authoritative there), so an orphaned claim (the
// claim landed but the reply was lost) would otherwise deadlock the queue while
// its holder heartbeats "idle" forever. Requiring the lease to have expired —
// on top of the graceSeconds age floor — keeps this safe under compose --scale,
// where same-named replicas heartbeat idle while a sibling legitimately works:
// an in-flight sibling claim has a live lease and is left alone. Returns the
// reclaimed tasks so the caller can log/audit them.
func (s *Store) ReclaimIdle(bee string, graceSeconds float64) ([]Task, error) {
	t0 := now()
	cutoff := t0 - graceSeconds
	rows, err := s.db.Query(
		`SELECT id, mission_id, key, role, title FROM tasks
		 WHERE status=? AND claimed_by=? AND claimed_ts < ? AND claim_expires_ts < ?`,
		StatusClaimed, bee, cutoff, t0,
	)
	if err != nil {
		return nil, err
	}
	var stale []Task
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.MissionID, &t.Key, &t.Role, &t.Title); err != nil {
			_ = rows.Close()
			return nil, err
		}
		stale = append(stale, t)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()

	var reclaimed []Task
	for _, t := range stale {
		res, err := s.db.Exec(
			`UPDATE tasks SET status=?, claimed_by=NULL, claimed_ts=NULL, claim_expires_ts=NULL, claimed_instance=''
			 WHERE id=? AND status=? AND claimed_by=?`,
			StatusReady, t.ID, StatusClaimed, bee,
		)
		if err != nil {
			return reclaimed, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			reclaimed = append(reclaimed, t)
		}
	}
	return reclaimed, nil
}

// Reap requeues (claimed → ready) the tasks of bees that are gone. Presence is
// authoritative when known: with a present set supplied, a task is requeued iff
// its claimer is absent — so a live, heart-beating bee keeps its task no matter
// how long the task runs (no false reap of a busy bee). A nil present set means
// "presence unknown" (e.g. the lookup failed): the lease is the fallback, so a
// transient outage can't strand the hive's work forever.
func (s *Store) Reap(present map[string]bool) (int, error) {
	type held struct {
		id  int64
		by  string
		exp sql.NullFloat64
	}
	var claimed []held
	rows, err := s.db.Query(`SELECT id, claimed_by, claim_expires_ts FROM tasks WHERE status=?`, StatusClaimed)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var h held
		var by sql.NullString
		if err := rows.Scan(&h.id, &by, &h.exp); err != nil {
			_ = rows.Close()
			return 0, err
		}
		h.by = by.String
		claimed = append(claimed, h)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	_ = rows.Close()

	t := now()
	n := 0
	for _, h := range claimed {
		var requeue bool
		if present != nil {
			requeue = !present[h.by] // presence is authoritative when known
		} else {
			requeue = h.exp.Valid && h.exp.Float64 < t // fallback: lease expiry
		}
		if !requeue {
			continue
		}
		if _, err := s.db.Exec(
			`UPDATE tasks SET status=?, claimed_by=NULL, claimed_ts=NULL, claim_expires_ts=NULL, claimed_instance=''
			 WHERE id=? AND status=?`, StatusReady, h.id, StatusClaimed,
		); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// MissionDone reports whether a mission has tasks and none are still open
// (pending/ready/claimed). Terminal statuses — done, cancelled, superseded — do
// not block completion, so a mission whose remaining work the lead cancelled or
// superseded still converges.
func (s *Store) MissionDone(missionID int64) (bool, error) {
	var total, open int
	err := s.db.QueryRow(
		`SELECT COUNT(*), COUNT(CASE WHEN status IN (?,?,?) THEN 1 END)
		 FROM tasks WHERE mission_id=?`,
		StatusPending, StatusReady, StatusClaimed, missionID,
	).Scan(&total, &open)
	if err != nil {
		return false, err
	}
	return total > 0 && open == 0, nil
}

// MissionOfTask returns the mission a task belongs to (0 if no such task) — so a
// finding attached at complete_task time can be scoped to the right mission.
func (s *Store) MissionOfTask(id int64) (int64, error) {
	var m int64
	err := s.db.QueryRow(`SELECT mission_id FROM tasks WHERE id=?`, id).Scan(&m)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return m, err
}

// ClaimedMission returns the mission id of the task the bee currently holds
// (claimed), or 0 if it holds none. Used to attribute an agent's executions to
// its mission for the verification gate.
func (s *Store) ClaimedMission(bee string) (int64, error) {
	var mid int64
	err := s.db.QueryRow(
		`SELECT mission_id FROM tasks WHERE claimed_by=? AND status=? LIMIT 1`,
		bee, StatusClaimed,
	).Scan(&mid)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return mid, err
}

// List returns a mission's tasks, oldest first.
func (s *Store) List(missionID int64) ([]Task, error) {
	return s.query(taskSelect+` WHERE mission_id=? ORDER BY id`, missionID)
}

// Active returns the current task list across missions for the live UI.
func (s *Store) Active() ([]Task, error) {
	return s.query(taskSelect + ` ORDER BY mission_id, id`)
}

// ---- helpers ----

func (s *Store) keysWithStatus(missionID int64, status string) (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT key FROM tasks WHERE mission_id=? AND status=?`, missionID, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		set[k] = true
	}
	return set, rows.Err()
}

func (s *Store) query(q string, args ...any) ([]Task, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		var t Task
		var depJSON string
		var claimedBy, result sql.NullString
		var claimedTS, doneTS, exp sql.NullFloat64
		if err := rows.Scan(&t.ID, &t.MissionID, &t.Key, &t.Role, &t.Title, &t.Instruction,
			&t.Status, &depJSON, &claimedBy, &result, &t.CreatedTS, &claimedTS, &doneTS, &exp, &t.Supersedes, &t.Verify); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(depJSON), &t.DependsOn)
		t.ClaimedBy, t.Result = claimedBy.String, result.String
		t.ClaimedTS, t.DoneTS, t.ClaimExpiresTS = claimedTS.Float64, doneTS.Float64, exp.Float64
		out = append(out, t)
	}
	return out, rows.Err()
}

func allDone(deps []string, done map[string]bool) bool {
	for _, d := range deps {
		if !done[d] {
			return false
		}
	}
	return true
}
