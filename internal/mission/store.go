// SPDX-License-Identifier: Elastic-2.0

// Package mission is CorralAI's orchestration layer: a directive ("build me X")
// becomes a MISSION — a dependency-gated pipeline of phases (build → test ∥ secops
// → retro) that the brain drives over the subagent + instruction + memory
// primitives. The load-bearing principle is independence: each phase spawns its
// OWN fresh agents, so the tester is never the builder ("never proof-read your own
// work"). They still COLLABORATE through shared memory — a tester reads the
// builder's notes to verify with full intent — and every phase records findings
// back, so the corpus (and the swarm) learns from each mission.
package mission

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/fence"
	"github.com/pdbethke/corralai/internal/queue"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS missions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  directive TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'running',   -- running | paused | cancelled | awaiting_review | needs-review | done | failed
  sprint INTEGER NOT NULL DEFAULT 1,
  requires_review INTEGER NOT NULL DEFAULT 0,
  record_story INTEGER NOT NULL DEFAULT 0,
  created_ts REAL NOT NULL, updated_ts REAL NOT NULL);
CREATE TABLE IF NOT EXISTS phases (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  mission_id INTEGER NOT NULL,
  name TEXT NOT NULL, role TEXT NOT NULL DEFAULT '', program TEXT NOT NULL DEFAULT '',
  instruction TEXT NOT NULL,
  depends_on TEXT NOT NULL DEFAULT '',       -- comma list of phase names
  count INTEGER NOT NULL DEFAULT 1,
  status TEXT NOT NULL DEFAULT 'pending',     -- pending | running | done | failed
  position INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS ix_phases_mission ON phases(mission_id);
CREATE TABLE IF NOT EXISTS mission_herds (
  mission_id   INTEGER PRIMARY KEY,
  role_models  TEXT NOT NULL DEFAULT '{}',
  endpoints    TEXT NOT NULL DEFAULT '[]',
  lookbook_ids TEXT NOT NULL DEFAULT '[]',
  created_ts   REAL NOT NULL
);
`

type Mission struct {
	ID              int64   `json:"id"`
	Directive       string  `json:"directive"`
	Status          string  `json:"status"`
	Sprint          int64   `json:"sprint"`
	RequiresReview  bool    `json:"requires_review"`
	CreatedTS       float64 `json:"created_ts"`
	UpdatedTS       float64 `json:"updated_ts"`
	Repo            string  `json:"repo,omitempty"`
	Base            string  `json:"base,omitempty"`
	Branch          string  `json:"branch,omitempty"`
	PRURL           string  `json:"pr_url,omitempty"`
	ReviewRounds    int     `json:"review_rounds,omitempty"`
	ReviewWatermark string  `json:"review_watermark,omitempty"`
	ReviewParked    bool    `json:"review_parked,omitempty"`
	// RecordStory is the per-mission opt-in (default false) for the story
	// engine: only when true does report_thought record anything for this
	// mission, so a normal mission pays no telemetry cost for thought beats.
	RecordStory bool `json:"record_story,omitempty"`
}

type Phase struct {
	ID          int64    `json:"id"`
	MissionID   int64    `json:"mission_id"`
	Name        string   `json:"name"`
	Role        string   `json:"role,omitempty"`
	Program     string   `json:"program,omitempty"`
	Instruction string   `json:"instruction"`
	DependsOn   []string `json:"depends_on"`
	Count       int      `json:"count"`
	Status      string   `json:"status"`
	Position    int      `json:"position"`
}

// PhaseSpec is a phase as supplied in a plan (before persistence).
type PhaseSpec struct {
	Name        string
	Role        string
	Program     string // optional agent type (e.g. "gemini") — heterogeneous verification
	Instruction string
	DependsOn   []string
	Count       int
	Verify      string // command that must pass (exit 0) before a task of this phase can complete; "" = ungated
}

// MissionView is a mission with its phases + per-phase progress (status/UI). Fields
// are explicit (not embedded) so the MCP output-schema generator stays happy.
type MissionView struct {
	ID        int64       `json:"id"`
	Directive string      `json:"directive"`
	Status    string      `json:"status"`
	Sprint    int64       `json:"sprint"`
	CreatedTS float64     `json:"created_ts"`
	Phases    []PhaseView `json:"phases"`
}
type PhaseView struct {
	Name      string   `json:"name"`
	Role      string   `json:"role,omitempty"`
	Program   string   `json:"program,omitempty"`
	Status    string   `json:"status"`
	Count     int      `json:"count"`
	DependsOn []string `json:"depends_on"`
	DoneTasks int      `json:"done_tasks"`
	Agents    []string `json:"agents"` // the spawned subagent names for this phase
}

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, err
	}
	// Idempotent migrations for DBs created before the client/sprint columns.
	for _, stmt := range []string{
		`ALTER TABLE missions ADD COLUMN sprint INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE missions ADD COLUMN requires_review INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE missions ADD COLUMN repo VARCHAR NOT NULL DEFAULT ''`,
		`ALTER TABLE missions ADD COLUMN base VARCHAR NOT NULL DEFAULT ''`,
		`ALTER TABLE missions ADD COLUMN branch VARCHAR NOT NULL DEFAULT ''`,
		`ALTER TABLE missions ADD COLUMN pr_url VARCHAR NOT NULL DEFAULT ''`,
		`ALTER TABLE missions ADD COLUMN review_rounds INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE missions ADD COLUMN review_watermark VARCHAR NOT NULL DEFAULT ''`,
		`ALTER TABLE missions ADD COLUMN review_parked INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE missions ADD COLUMN record_story INTEGER NOT NULL DEFAULT 0`,
	} {
		if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			_ = db.Close()
			return nil, err
		}
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }
func now() float64            { return float64(time.Now().UnixNano()) / 1e9 }

// CreateMission persists a mission + its phases (the plan record for reporting)
// and enqueues the mission's tasks into the queue (the executable work the hive
// pulls). The build-plan sizer (ScaledPlan/DefaultPlan) is retired — callers
// must supply an explicit, non-empty plan; an empty plan fails closed rather
// than synthesizing a build arc.
func CreateMission(s *Store, q *queue.Store, directive string, plan []PhaseSpec, requiresReview bool) (int64, error) {
	if len(plan) == 0 {
		return 0, fmt.Errorf("build missions are retired: an explicit, non-empty plan is required")
	}
	n := now()
	rr := 0
	if requiresReview {
		rr = 1
	}
	res, err := s.db.Exec(`INSERT INTO missions(directive,status,sprint,requires_review,created_ts,updated_ts) VALUES(?,'running',1,?,?,?)`, directive, rr, n, n)
	if err != nil {
		return 0, err
	}
	mid, _ := res.LastInsertId()
	for i, p := range plan {
		c := p.Count
		if c <= 0 {
			c = 1
		}
		if _, err := s.db.Exec(`INSERT INTO phases(mission_id,name,role,program,instruction,depends_on,count,status,position)
			VALUES(?,?,?,?,?,?,?, 'pending', ?)`,
			mid, p.Name, p.Role, p.Program, p.Instruction, strings.Join(p.DependsOn, ","), c, i); err != nil {
			return mid, err
		}
	}
	if q != nil {
		if err := q.Enqueue(mid, planToTasks(plan)); err != nil {
			return mid, err
		}
	}
	return mid, nil
}

// Lesson is a single vetted lesson to be injected into phase instructions.
// Author carries the identity of whoever wrote the lesson so the fenced block
// is auditable inside the phase instruction.
type Lesson struct {
	Text   string
	Author string
}

// InjectLessons prepends a fenced, untrusted preamble to every phase's
// instruction so vetted lessons from prior missions shape the work while the
// fence prevents an agent-written lesson from executing as an authoritative
// instruction. No-op when lessons is empty or all entries are blank.
// This is the active half of the learning loop.
func InjectLessons(plan []PhaseSpec, lessons []Lesson) []PhaseSpec {
	var b strings.Builder
	for _, l := range lessons {
		t := strings.TrimSpace(l.Text)
		if t == "" {
			continue
		}
		b.WriteString("- ")
		b.WriteString(t)
		if l.Author != "" {
			b.WriteString(" (author: " + l.Author + ")")
		}
		b.WriteByte('\n')
	}
	if b.Len() == 0 {
		return plan
	}
	// The label is the corral-voice copy the plan mandates ("LESSONS FROM THE
	// HERD (vetted)"); the fence.Untrusted wrapper around it is the
	// prompt-injection control and must stay.
	preamble := fence.Untrusted("LESSONS FROM THE HERD (vetted)", "human-vetted memory", b.String()) + "\n\n"
	out := make([]PhaseSpec, len(plan))
	for i, p := range plan {
		p.Instruction = preamble + p.Instruction
		out[i] = p
	}
	return out
}

// planToTasks expands a phase plan into queue tasks. A phase with count N becomes
// N tasks keyed "<phase>#<i>"; a phase's dependency on another phase becomes a
// dependency on ALL of that phase's tasks (every test task waits on every build
// task). Title carries the phase name so the View can group tasks back by phase.
func planToTasks(plan []PhaseSpec) []queue.TaskSpec {
	taskKey := func(name string, i int) string { return fmt.Sprintf("%s#%d", name, i) }
	keysByPhase := map[string][]string{}
	for _, p := range plan {
		c := p.Count
		if c <= 0 {
			c = 1
		}
		for i := 1; i <= c; i++ {
			keysByPhase[p.Name] = append(keysByPhase[p.Name], taskKey(p.Name, i))
		}
	}
	var specs []queue.TaskSpec
	for _, p := range plan {
		c := p.Count
		if c <= 0 {
			c = 1
		}
		var deps []string
		for _, dp := range p.DependsOn {
			deps = append(deps, keysByPhase[dp]...)
		}
		for i := 1; i <= c; i++ {
			specs = append(specs, queue.TaskSpec{
				Key:         taskKey(p.Name, i),
				Role:        p.Role,
				Title:       p.Name,
				Instruction: p.Instruction,
				DependsOn:   deps,
				Verify:      p.Verify,
			})
		}
	}
	return specs
}

func splitDeps(s string) []string {
	if s == "" {
		return []string{}
	}
	return strings.Split(s, ",")
}

func (s *Store) RunningMissions() ([]Mission, error) {
	return s.queryMissions("WHERE status='running' ORDER BY id")
}

// MissionsPendingPR returns done repo-missions that have not yet had a PR opened
// (status='done' AND repo set AND pr_url empty). The engine's Tick reconcile pass
// uses this to push+PR review-accepted missions (which complete outside Tick) and
// to retry any mission whose earlier push/PR failed transiently.
func (s *Store) MissionsPendingPR() ([]Mission, error) {
	return s.queryMissions("WHERE status='done' AND repo != '' AND pr_url = '' ORDER BY id")
}
func (s *Store) ListMissions() ([]Mission, error) {
	return s.queryMissions("ORDER BY id DESC LIMIT 50")
}
func (s *Store) queryMissions(where string) ([]Mission, error) {
	rows, err := s.db.Query(`SELECT id,directive,status,sprint,requires_review,created_ts,updated_ts,repo,base,branch,pr_url,review_rounds,review_watermark,review_parked,record_story FROM missions ` + where) // #nosec G202 -- not injectable: where is a constant string literal from internal callers only (WHERE status=..., ORDER BY id, etc.); no user input reaches this parameter
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Mission{}
	for rows.Next() {
		var m Mission
		var rr int
		var parked int
		var story int
		if err := rows.Scan(&m.ID, &m.Directive, &m.Status, &m.Sprint, &rr, &m.CreatedTS, &m.UpdatedTS, &m.Repo, &m.Base, &m.Branch, &m.PRURL, &m.ReviewRounds, &m.ReviewWatermark, &parked, &story); err != nil {
			return nil, err
		}
		m.RequiresReview = rr == 1
		m.ReviewParked = parked != 0
		m.RecordStory = story != 0
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) Phases(missionID int64) ([]Phase, error) {
	rows, err := s.db.Query(`SELECT id,mission_id,name,role,program,instruction,depends_on,count,status,position
		FROM phases WHERE mission_id=? ORDER BY position`, missionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Phase{}
	for rows.Next() {
		var p Phase
		var deps string
		if err := rows.Scan(&p.ID, &p.MissionID, &p.Name, &p.Role, &p.Program, &p.Instruction, &deps, &p.Count, &p.Status, &p.Position); err != nil {
			return nil, err
		}
		p.DependsOn = splitDeps(deps)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) SetMissionStatus(id int64, status string) error {
	_, err := s.db.Exec(`UPDATE missions SET status=?, updated_ts=? WHERE id=?`, status, now(), id)
	return err
}

// PauseMission is #58's mid-mission human steering: an operator halts a
// RUNNING mission so the brain hands out no NEW tasks for it (the claim path
// enforces this — see queue.Store.HaltMission/ClaimNextAs); in-flight claims
// may still finish. Only a running mission can be paused (pausing an
// awaiting_review/done/cancelled mission is a no-op state and refused so the
// caller notices a stale assumption rather than silently doing nothing).
// Human-gated by the caller (see brain.SteerMission / the
// /api/mission/pause handler) exactly like PruneMission's contract.
func PauseMission(m *Store, q *queue.Store, id int64) (*Mission, error) {
	mi, err := m.Mission(id)
	if err != nil || mi == nil {
		return nil, fmt.Errorf("no mission %d", id)
	}
	if mi.Status != "running" {
		return nil, fmt.Errorf("mission %d is %s, not running — cannot pause", id, mi.Status)
	}
	if err := q.HaltMission(id, "paused"); err != nil {
		return nil, err
	}
	if err := m.SetMissionStatus(id, "paused"); err != nil {
		return nil, err
	}
	return m.Mission(id)
}

// ResumeMission restores normal flow for a paused mission: the claim path's
// halt is cleared and the mission returns to "running" so PromoteReady/
// ClaimNextAs treat it exactly as before the pause. Only a paused mission can
// be resumed.
func ResumeMission(m *Store, q *queue.Store, id int64) (*Mission, error) {
	mi, err := m.Mission(id)
	if err != nil || mi == nil {
		return nil, fmt.Errorf("no mission %d", id)
	}
	if mi.Status != "paused" {
		return nil, fmt.Errorf("mission %d is %s, not paused — cannot resume", id, mi.Status)
	}
	if err := q.UnhaltMission(id); err != nil {
		return nil, err
	}
	if err := m.SetMissionStatus(id, "running"); err != nil {
		return nil, err
	}
	return m.Mission(id)
}

// CancelMission stops a mission for good: the claim path halts it (same
// enforcement as pause, reason "cancelled") and its status flips to
// "cancelled" — it leaves the active set (RunningMissions no longer returns
// it) and, unlike a pause, there is no resume back from here. Allowed from
// any non-terminal state (running, paused, or awaiting_review); refused once
// already done or cancelled.
func CancelMission(m *Store, q *queue.Store, id int64) (*Mission, error) {
	mi, err := m.Mission(id)
	if err != nil || mi == nil {
		return nil, fmt.Errorf("no mission %d", id)
	}
	if mi.Status == "done" || mi.Status == "cancelled" {
		return nil, fmt.Errorf("mission %d is already %s — cannot cancel", id, mi.Status)
	}
	if err := q.HaltMission(id, "cancelled"); err != nil {
		return nil, err
	}
	if err := m.SetMissionStatus(id, "cancelled"); err != nil {
		return nil, err
	}
	return m.Mission(id)
}

// BumpSprint increments a mission's sprint counter (a new client-feedback round)
// and returns the new sprint number.
func (s *Store) BumpSprint(id int64) (int64, error) {
	if _, err := s.db.Exec(`UPDATE missions SET sprint=sprint+1, updated_ts=? WHERE id=?`, now(), id); err != nil {
		return 0, err
	}
	var sp int64
	err := s.db.QueryRow(`SELECT sprint FROM missions WHERE id=?`, id).Scan(&sp)
	return sp, err
}

// SprintCap bounds client-feedback rounds so a never-satisfied client can't loop
// the swarm forever.
const SprintCap = 5

// SubmitReview applies a client verdict on a mission. Accept completes it;
// otherwise the feedback becomes a change-request finding (which the lead routes
// into the next sprint's rework), the sprint bumps, and the mission returns to
// running. Shared by the review_mission MCP tool and the UI's /api/review.
func SubmitReview(m *Store, q *queue.Store, id int64, accept bool, feedback, reporter string) (*MissionView, error) {
	mi, err := m.Mission(id)
	if err != nil || mi == nil {
		return nil, fmt.Errorf("no mission %d", id)
	}
	if accept {
		if err := m.SetMissionStatus(id, "done"); err != nil {
			return nil, err
		}
	} else {
		if mi.Sprint >= SprintCap {
			return nil, fmt.Errorf("sprint cap (%d) reached for mission %d — accept it or revise the directive", SprintCap, id)
		}
		if reporter == "" {
			reporter = "client"
		}
		if q != nil {
			if _, err := q.AddFinding(queue.Finding{
				MissionID: id, Reporter: reporter, Type: "change-request", Severity: "high",
				Target: "deliverable", Evidence: feedback, SuggestedAction: "address the client's feedback",
			}); err != nil {
				return nil, err
			}
		}
		if _, err := m.BumpSprint(id); err != nil {
			return nil, err
		}
		if err := m.SetMissionStatus(id, "running"); err != nil {
			return nil, err
		}
	}
	return m.View(id, q)
}

// ResolveNeedsReview is the human-gate resolution path for a mission the
// convergence findings-gate parked at "needs-review" (see
// Engine.blockingFindingOpen). The human reviews the open critical/high
// findings and either dismisses them (not real) or acts on them; once every
// finding at/above blockSeverity is cleared, this certifies the mission "done"
// (the engine's reconcile pass then push+PRs any repo mission, exactly as for a
// review-accepted mission). While a blocker is still open it refuses, naming the
// count, so the caller can't certify a result that still holds a known defect.
// Only a needs-review mission is resolvable — any other state is a stale
// assumption and refused, mirroring the Pause/Resume/Cancel guards. Shared by
// the resolve_review MCP tool and the UI's resolution handler.
func ResolveNeedsReview(m *Store, q *queue.Store, id int64, blockSeverity string) (*MissionView, error) {
	mi, err := m.Mission(id)
	if err != nil || mi == nil {
		return nil, fmt.Errorf("no mission %d", id)
	}
	if mi.Status != "needs-review" {
		return nil, fmt.Errorf("mission %d is %s, not needs-review — nothing to resolve", id, mi.Status)
	}
	if blockSeverity != "" && q != nil {
		minRank := queue.SeverityRank(blockSeverity)
		fs, err := q.Findings(id, queue.FindingOpen)
		if err != nil {
			return nil, err
		}
		open := 0
		for _, f := range fs {
			if queue.SeverityRank(f.Severity) >= minRank {
				open++
			}
		}
		if open > 0 {
			return nil, fmt.Errorf("mission %d still has %d open finding(s) at/above %q — dismiss or address them before resolving", id, open, blockSeverity)
		}
	}
	if err := m.SetMissionStatus(id, "done"); err != nil {
		return nil, err
	}
	return m.View(id, q)
}

// Mission returns one mission by id, or nil.
func (s *Store) Mission(id int64) (*Mission, error) {
	var m Mission
	var rr int
	var parked int
	var story int
	err := s.db.QueryRow(`SELECT id,directive,status,sprint,requires_review,created_ts,updated_ts,repo,base,branch,pr_url,review_rounds,review_watermark,review_parked,record_story FROM missions WHERE id=?`, id).
		Scan(&m.ID, &m.Directive, &m.Status, &m.Sprint, &rr, &m.CreatedTS, &m.UpdatedTS, &m.Repo, &m.Base, &m.Branch, &m.PRURL, &m.ReviewRounds, &m.ReviewWatermark, &parked, &story)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	m.RequiresReview = rr == 1
	m.ReviewParked = parked != 0
	m.RecordStory = story != 0
	return &m, nil
}

// SetRecordStory sets a mission's story-engine opt-in (default false at
// creation). Follows the SetRepo/SetPRURL pattern: a small, focused setter
// rather than widening CreateMission's signature for every callsite.
func (s *Store) SetRecordStory(id int64, on bool) error {
	v := 0
	if on {
		v = 1
	}
	_, err := s.db.Exec(`UPDATE missions SET record_story=?, updated_ts=? WHERE id=?`, v, now(), id)
	return err
}

// DeleteMission removes a mission and its phases. Used to roll back a
// partially-provisioned repo mission (clone failed after the row was inserted).
func (s *Store) DeleteMission(id int64) error {
	_, err := s.db.Exec(`DELETE FROM phases WHERE mission_id=?`, id)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`DELETE FROM missions WHERE id=?`, id)
	return err
}

// SetRepo records the git repo, base branch, and working branch for a mission.
func (s *Store) SetRepo(id int64, repo, base, branch string) error {
	_, err := s.db.Exec(`UPDATE missions SET repo=?, base=?, branch=?, updated_ts=? WHERE id=?`, repo, base, branch, now(), id)
	return err
}

// SetPRURL records the pull-request URL once the mission's branch has been pushed.
func (s *Store) SetPRURL(id int64, url string) error {
	_, err := s.db.Exec(`UPDATE missions SET pr_url=?, updated_ts=? WHERE id=?`, url, now(), id)
	return err
}

// SetReviewState records the current PR-review round counter, the timestamp
// watermark of the last review event seen, and whether the mission has been
// parked (excluded from the open-PR polling set).
func (s *Store) SetReviewState(id int64, rounds int, watermark string, parked bool) error {
	p := 0
	if parked {
		p = 1
	}
	_, err := s.db.Exec(`UPDATE missions SET review_rounds=?, review_watermark=?, review_parked=?, updated_ts=? WHERE id=?`,
		rounds, watermark, p, now(), id)
	return err
}

// ReopenForReview reopens a done mission for a review-response round: appends the
// given phases (after the current max position), enqueues their tasks, flips the
// mission back to running, bumps review_rounds and advances the watermark — in one
// transaction. Phase names must be round-unique (caller prefixes "review-r<N>-…")
// so planToTasks keys don't collide with the mission's existing tasks.
func (s *Store) ReopenForReview(q *queue.Store, id int64, phases []PhaseSpec, watermark string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var maxPos sql.NullInt64
	if err := tx.QueryRow(`SELECT max(position) FROM phases WHERE mission_id=?`, id).Scan(&maxPos); err != nil {
		return err
	}
	pos := int(maxPos.Int64) + 1
	for _, p := range phases {
		c := p.Count
		if c <= 0 {
			c = 1
		}
		if _, err := tx.Exec(`INSERT INTO phases(mission_id,name,role,program,instruction,depends_on,count,status,position)
			VALUES(?,?,?,?,?,?,?, 'pending', ?)`,
			id, p.Name, p.Role, p.Program, p.Instruction, strings.Join(p.DependsOn, ","), c, pos); err != nil {
			return err
		}
		pos++
	}
	var rounds int
	if err := tx.QueryRow(`SELECT review_rounds FROM missions WHERE id=?`, id).Scan(&rounds); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE missions SET status='running', review_rounds=?, review_watermark=?, updated_ts=? WHERE id=?`,
		rounds+1, watermark, now(), id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// enqueue outside the mission-store tx (queue is a separate store/DB)
	return q.Enqueue(id, planToTasks(phases))
}

// MissionsWithOpenPR returns done missions that have a PR open and have not
// been parked. The engine's Tick polls this set to process incoming review
// events from GitHub.
func (s *Store) MissionsWithOpenPR() ([]Mission, error) {
	return s.queryMissions("WHERE status='done' AND repo!='' AND pr_url!='' AND review_parked=0 ORDER BY id")
}

// ParsePRNumber extracts the numeric change-request ID from a forge PR/MR URL.
// Supports GitHub/Gitea (…/pull/7) and GitLab (…/merge_requests/7).
func ParsePRNumber(prURL string) (int, error) {
	for _, seg := range []string{"/pull/", "/merge_requests/"} {
		if i := strings.LastIndex(prURL, seg); i >= 0 {
			n, err := strconv.Atoi(strings.TrimRight(prURL[i+len(seg):], "/"))
			if err != nil {
				return 0, fmt.Errorf("bad CR number in %q: %w", prURL, err)
			}
			return n, nil
		}
	}
	return 0, fmt.Errorf("no /pull/ or /merge_requests/ in %q", prURL)
}

// MissionDir is the per-mission working-copy path: <workspace>/m<id>. The engine,
// brain provisioning, the read tools, and #16's snapshot all resolve here, so a
// mission's working copy is keyed by id (concurrent missions never collide).
func MissionDir(workspace string, id int64) string {
	return filepath.Join(workspace, "m"+strconv.FormatInt(id, 10))
}

// View assembles a mission's full status for reporting + the UI, deriving each
// phase's progress from the queue's tasks (grouped by phase name = task Title):
// a phase is done when all its tasks are done, running once any task has been
// promoted/claimed/finished, else pending. Agents are the bees that claimed its
// tasks.
func (s *Store) View(missionID int64, q *queue.Store) (*MissionView, error) {
	m, err := s.Mission(missionID)
	if err != nil || m == nil {
		return nil, err
	}
	mv := &MissionView{ID: m.ID, Directive: m.Directive, Status: m.Status, Sprint: m.Sprint, CreatedTS: m.CreatedTS}
	phases, err := s.Phases(missionID)
	if err != nil {
		return nil, err
	}
	byPhase := map[string][]queue.Task{}
	if q != nil {
		tasks, err := q.List(missionID)
		if err != nil {
			return nil, err
		}
		for _, t := range tasks {
			byPhase[t.Title] = append(byPhase[t.Title], t)
		}
	}
	for _, p := range phases {
		ts := byPhase[p.Name]
		done := 0
		allPending := true
		var agents []string
		seen := map[string]bool{}
		for _, t := range ts {
			if t.Status == queue.StatusDone {
				done++
			}
			if t.Status != queue.StatusPending {
				allPending = false
			}
			if t.ClaimedBy != "" && !seen[t.ClaimedBy] {
				seen[t.ClaimedBy] = true
				agents = append(agents, t.ClaimedBy)
			}
		}
		status := "pending"
		if len(ts) > 0 && done == len(ts) {
			status = "done"
		} else if !allPending {
			status = "running"
		}
		mv.Phases = append(mv.Phases, PhaseView{
			Name: p.Name, Role: p.Role, Program: p.Program, Status: status,
			Count: p.Count, DependsOn: p.DependsOn, DoneTasks: done, Agents: agents,
		})
	}
	return mv, nil
}
