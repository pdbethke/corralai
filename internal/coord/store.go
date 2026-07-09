// SPDX-License-Identifier: Elastic-2.0

// Package coord is CorralAI's coordination core: advisory path/branch leases with
// TTL, agent presence, a completed-work log, and an append-only audit trail —
// backed by pure-Go SQLite (modernc.org/sqlite, no CGO). It is the authoritative
// claim broker the "corral brain" serves to thin clients over MCP.
//
// Claims are ADVISORY: a conflict is reported alongside the grant, never blocked.
package coord

import (
	"database/sql"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const PresenceWindow = 300.0 // seconds; an agent is "active" if it heartbeat within this

const schema = `
CREATE TABLE IF NOT EXISTS agents (
  name TEXT PRIMARY KEY, program TEXT, model TEXT, task TEXT, session_id TEXT,
  registered_ts REAL, last_active_ts REAL,
  parent TEXT NOT NULL DEFAULT '',     -- subagent: its parent agent's name ('' = top-level swarm node)
  role TEXT NOT NULL DEFAULT '');       -- assigned role (e.g. tester, deployer) -> role-specific skill set
CREATE TABLE IF NOT EXISTS claims (
  id INTEGER PRIMARY KEY AUTOINCREMENT, agent_name TEXT NOT NULL, path TEXT NOT NULL,
  exclusive INTEGER NOT NULL DEFAULT 1, reason TEXT, created_ts REAL NOT NULL,
  expires_ts REAL NOT NULL, released_ts REAL);
CREATE INDEX IF NOT EXISTS ix_claims_active ON claims (released_ts, expires_ts);
CREATE INDEX IF NOT EXISTS ix_claims_agent ON claims (agent_name);
CREATE TABLE IF NOT EXISTS completed_work (
  id INTEGER PRIMARY KEY AUTOINCREMENT, agent_name TEXT NOT NULL, summary TEXT NOT NULL,
  paths TEXT, completed_ts REAL NOT NULL);
CREATE TABLE IF NOT EXISTS audit (
  id INTEGER PRIMARY KEY AUTOINCREMENT, ts REAL NOT NULL, agent_name TEXT,
  action TEXT NOT NULL, detail TEXT);
CREATE TABLE IF NOT EXISTS instructions (
  id INTEGER PRIMARY KEY AUTOINCREMENT, target TEXT NOT NULL, issuer TEXT,
  text TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending', result TEXT,
  created_ts REAL NOT NULL, acked_ts REAL);
CREATE INDEX IF NOT EXISTS ix_instr_target ON instructions (target, status);
`

type Store struct {
	db  *sql.DB
	bus *Bus // optional: signalled on every audited action (for instant SSE push)
}

// SetBus attaches a signal bus; audited actions publish to it.
func (s *Store) SetBus(b *Bus) { s.bus = b }

type Conflict struct {
	Path      string `json:"path"`
	HeldBy    string `json:"held_by"`
	TheirPath string `json:"their_path"`
	Reason    string `json:"reason"`
}

type ClaimResult struct {
	Granted   []string   `json:"granted"`
	Conflicts []Conflict `json:"conflicts"`
	ExpiresTS float64    `json:"expires_ts"`
	Advisory  bool       `json:"advisory"`
}

type Agent struct {
	Name         string  `json:"name"`
	Program      string  `json:"program,omitempty"`
	Model        string  `json:"model,omitempty"`
	Task         string  `json:"task,omitempty"`
	Parent       string  `json:"parent,omitempty"` // subagent: its parent agent's name
	Role         string  `json:"role,omitempty"`   // assigned role (-> role-specific skill set)
	Status       string  `json:"status,omitempty"`
	StatusSince  float64 `json:"status_since,omitempty"`
	LastActiveTS float64 `json:"last_active_ts"`
	RegisteredTS float64 `json:"registered_ts"`
}

type Claim struct {
	Path      string  `json:"path"`
	Exclusive bool    `json:"exclusive"`
	Reason    string  `json:"reason"`
	ExpiresTS float64 `json:"expires_ts"`
}

type Completed struct {
	AgentName   string   `json:"agent_name"`
	Summary     string   `json:"summary"`
	Paths       []string `json:"paths"`
	CompletedTS float64  `json:"completed_ts"`
}

type Bootstrap struct {
	You             map[string]string `json:"you"`
	ActivePeers     []Agent           `json:"active_peers"`
	YourClaims      []Claim           `json:"your_claims"`
	RecentCompleted []Completed       `json:"recent_completed"`
	Contested       []Conflict        `json:"contested,omitempty"`
	Hint            string            `json:"hint"`
}

var now = func() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// Open returns a Store backed by a SQLite file (WAL). MaxOpenConns=1 serializes
// writes — correct and ample for a single-process broker at agent-fleet scale.
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
	// Idempotent migrations for DBs created before these columns existed (ALTER
	// fails with "duplicate column" once applied — that's fine, ignore it).
	for _, stmt := range []string{
		`ALTER TABLE agents ADD COLUMN parent TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE agents ADD COLUMN role TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE agents ADD COLUMN status TEXT NOT NULL DEFAULT 'working'`,
		`ALTER TABLE agents ADD COLUMN status_since REAL NOT NULL DEFAULT 0`,
		// principal: the authenticated owner of this agent (''=dev/unattributed). Added by
		// migration so existing stores upgrade. Used by the brain's per-principal spawn budget.
		`ALTER TABLE agents ADD COLUMN principal TEXT NOT NULL DEFAULT ''`,
		// Index principal for CountLiveByPrincipal — the spawn-budget hot path. CREATE INDEX IF
		// NOT EXISTS is idempotent; no duplicate-column guard needed.
		`CREATE INDEX IF NOT EXISTS idx_agents_principal ON agents(principal)`,
	} {
		if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return nil, err
		}
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Audit writes an audit row (exported so other packages can emit audit events
// into the coord stream). Best-effort: errors are silently discarded.
func (s *Store) Audit(agent, action string, detail any) { s.audit(agent, action, detail) }

func (s *Store) audit(agent, action string, detail any) {
	b, _ := json.Marshal(detail)
	_, _ = s.db.Exec("INSERT INTO audit (ts, agent_name, action, detail) VALUES (?,?,?,?)",
		now(), agent, action, string(b))
	s.bus.Publish() // wake SSE subscribers so the UI reflects this action instantly
}

// Activity is one audited action (the live work stream).
type Activity struct {
	TS     float64 `json:"ts"`
	Agent  string  `json:"agent,omitempty"`
	Action string  `json:"action"`
	Detail string  `json:"detail,omitempty"`
}

func (s *Store) activity(where string, limit int, args ...any) ([]Activity, error) {
	if limit <= 0 {
		limit = 25
	}
	args = append(args, limit)
	rows, err := s.db.Query("SELECT ts, agent_name, action, detail FROM audit "+where+" ORDER BY ts DESC LIMIT ?", args...) // #nosec G202 -- not injectable: where is a constant string literal from internal callers only (WHERE agent_name=? or ""); agent_name value uses ? placeholder
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Activity{}
	for rows.Next() {
		var a Activity
		if err := rows.Scan(&a.TS, &a.Agent, &a.Action, &a.Detail); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// RecentActivity returns an agent's most recent audited actions (newest first).
func (s *Store) RecentActivity(agent string, limit int) ([]Activity, error) {
	return s.activity("WHERE agent_name=?", limit, agent)
}

// RecentActivityAll returns the whole swarm's most recent actions (the live feed).
func (s *Store) RecentActivityAll(limit int) ([]Activity, error) {
	return s.activity("", limit)
}

func pathsOverlap(a, b string) bool {
	a, b = strings.TrimRight(a, "/"), strings.TrimRight(b, "/")
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

func (s *Store) Register(name, program, model, task, sessionID, role string) error {
	if err := s.upsertAgent(name, program, model, task, sessionID, "", role); err != nil {
		return err
	}
	s.audit(name, "register", map[string]string{"task": task, "role": role})
	return nil
}

// upsertAgent inserts/refreshes an agent row. parent/role are only written when
// non-empty so a heartbeat/re-register from a subagent doesn't clobber them.
func (s *Store) upsertAgent(name, program, model, task, sessionID, parent, role string) error {
	n := now()
	_, err := s.db.Exec(`
		INSERT INTO agents (name, program, model, task, session_id, registered_ts, last_active_ts, parent, role)
		VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET
		  program=excluded.program, model=excluded.model, task=excluded.task,
		  session_id=excluded.session_id, last_active_ts=excluded.last_active_ts,
		  parent=CASE WHEN excluded.parent<>'' THEN excluded.parent ELSE agents.parent END,
		  role=CASE WHEN excluded.role<>'' THEN excluded.role ELSE agents.role END`,
		name, program, model, task, sessionID, n, n, parent, role)
	return err
}

// Spawn registers a subagent under parent with an optional role. The full name is
// "<parent>/<child>" (so it's unique and the hierarchy is explicit); callers
// enforce that parent is within the authenticated principal's namespace.
func (s *Store) Spawn(parent, child, role, program, model, task string) (string, error) {
	full := parent + "/" + child
	if err := s.upsertAgent(full, program, model, task, "", parent, role); err != nil {
		return "", err
	}
	s.audit(full, "spawn", map[string]string{"parent": parent, "role": role, "task": task})
	return full, nil
}

// Despawn retires a subagent: releases its claims and removes it from the swarm.
func (s *Store) Despawn(name string) (bool, error) {
	if _, err := s.db.Exec(`UPDATE claims SET released_ts=? WHERE agent_name=? AND released_ts IS NULL`, now(), name); err != nil {
		return false, err
	}
	res, err := s.db.Exec(`DELETE FROM agents WHERE name=?`, name)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		s.audit(name, "despawn", nil)
	}
	return n > 0, nil
}

// Subagents returns the direct children of an agent (one level).
func (s *Store) Subagents(parent string) ([]Agent, error) {
	rows, err := s.db.Query(
		"SELECT name, program, model, task, parent, role, last_active_ts, registered_ts FROM agents WHERE parent=? ORDER BY registered_ts",
		parent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Agent{}
	for rows.Next() {
		var a Agent
		if err := rows.Scan(&a.Name, &a.Program, &a.Model, &a.Task, &a.Parent, &a.Role, &a.LastActiveTS, &a.RegisteredTS); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// RecordPrincipal sets the owning principal on an agent, but only when it is not yet
// set — so a heartbeat / re-register never clobbers established ownership.
func (s *Store) RecordPrincipal(name, principal string) error {
	if principal == "" {
		return nil
	}
	_, err := s.db.Exec(`UPDATE agents SET principal=? WHERE name=? AND principal=''`, principal, name)
	return err
}

// CountLiveByPrincipal returns how many of a principal's agents are currently live
// (heartbeat within PresenceWindow). The brain's MaxAgentsPerPrincipal budget reads
// this; a crashed-but-not-despawned agent ages out automatically.
func (s *Store) CountLiveByPrincipal(principal string) (int, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM agents WHERE principal=? AND last_active_ts >= ?`,
		principal, now()-PresenceWindow,
	).Scan(&n)
	return n, err
}

func (s *Store) Heartbeat(name string) error {
	_, err := s.db.Exec("UPDATE agents SET last_active_ts=? WHERE name=?", now(), name)
	return err
}

// SetStatus records an agent's coordination posture (working | awaiting_approval |
// idle). status_since is stamped only when the value actually changes, so callers
// can measure how long an agent has been parked.
func (s *Store) SetStatus(name, status string) error {
	n := now()
	_, err := s.db.Exec(
		`UPDATE agents SET status=?, status_since=CASE WHEN status<>? THEN ? ELSE status_since END, last_active_ts=? WHERE name=?`,
		status, status, n, n, name)
	return err
}

func (s *Store) ListActive(window float64) ([]Agent, error) {
	rows, err := s.db.Query(
		"SELECT name, program, model, task, parent, role, status, status_since, last_active_ts, registered_ts FROM agents WHERE last_active_ts > ? ORDER BY last_active_ts DESC",
		now()-window)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Agent
	for rows.Next() {
		var a Agent
		if err := rows.Scan(&a.Name, &a.Program, &a.Model, &a.Task, &a.Parent, &a.Role, &a.Status, &a.StatusSince, &a.LastActiveTS, &a.RegisteredTS); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

type activeClaim struct {
	Agent, Path, Reason, Status string
	Exclusive                   bool
	StatusSince                 float64
}

func scanActiveClaims(rows *sql.Rows) ([]activeClaim, error) {
	defer rows.Close()
	var out []activeClaim
	for rows.Next() {
		var c activeClaim
		var excl int
		if err := rows.Scan(&c.Agent, &c.Path, &excl, &c.Reason, &c.Status, &c.StatusSince); err != nil {
			return nil, err
		}
		c.Exclusive = excl != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

const activeClaimsSQL = `
	SELECT c.agent_name, c.path, c.exclusive, c.reason,
	       COALESCE(a.status,'working'), COALESCE(a.status_since,0)
	FROM claims c LEFT JOIN agents a ON a.name = c.agent_name
	WHERE c.released_ts IS NULL AND c.expires_ts > ? ORDER BY c.created_ts`

func (s *Store) activeClaims() ([]activeClaim, error) {
	rows, err := s.db.Query(activeClaimsSQL, now())
	if err != nil {
		return nil, err
	}
	return scanActiveClaims(rows)
}

func activeClaimsTx(tx *sql.Tx, n float64) ([]activeClaim, error) {
	rows, err := tx.Query(activeClaimsSQL, n)
	if err != nil {
		return nil, err
	}
	return scanActiveClaims(rows)
}

// LiveClaimHolders returns the distinct agents holding live (unreleased,
// unexpired) path leases. The bug-#40 escalation checks each holder against
// the task queue: one with no claimed task is stale, and its leases are
// force-released so they stop starving the bees still working.
func (s *Store) LiveClaimHolders() ([]string, error) {
	rows, err := s.db.Query(
		"SELECT DISTINCT agent_name FROM claims WHERE released_ts IS NULL AND expires_ts > ? ORDER BY agent_name", now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// ReapAbsentClaims releases every live claim held by an agent NOT in the present
// set — a crashed/disconnected holder whose exclusive path lease would otherwise
// strand peers until the full TTL (up to an hour). It is the coord-lease sibling
// of the queue task reaper. A nil present set means presence is unavailable, so
// it reaps nothing (never releases a live agent's lease on a transient presence
// outage). Returns the names whose claims were released.
func (s *Store) ReapAbsentClaims(present map[string]bool) ([]string, error) {
	if present == nil {
		return nil, nil
	}
	holders, err := s.LiveClaimHolders()
	if err != nil {
		return nil, err
	}
	var reaped []string
	for _, h := range holders {
		if present[h] {
			continue
		}
		if _, err := s.ReleaseClaims(h, nil); err != nil {
			return reaped, err
		}
		reaped = append(reaped, h)
	}
	return reaped, nil
}

// ClaimPaths leases paths. Exclusive claims are ENFORCED: if an enforcing exclusive
// holder overlaps, the path is NOT granted and the conflict is reported. The whole
// check+insert runs in one transaction so a race yields exactly one winner.
func (s *Store) ClaimPaths(name string, paths []string, ttlSeconds float64, exclusive bool, reason string) (*ClaimResult, error) {
	n := now()
	expires := n + ttlSeconds
	res := &ClaimResult{Granted: []string{}, Conflicts: []Conflict{}, ExpiresTS: expires, Advisory: !exclusive}
	exclInt := 0
	if exclusive {
		exclInt = 1
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	active, err := activeClaimsTx(tx, n)
	if err != nil {
		return nil, err
	}
	for _, p := range paths {
		blocked := false
		for _, c := range active {
			if c.Agent == name || !c.Exclusive || !pathsOverlap(p, c.Path) {
				continue
			}
			res.Conflicts = append(res.Conflicts, Conflict{Path: p, HeldBy: c.Agent, TheirPath: c.Path, Reason: c.Reason})
			if exclusive && enforcing(c, n) {
				blocked = true
			}
		}
		if blocked {
			continue // exclusive claim loses to an enforcing holder: not granted
		}
		if _, err := tx.Exec(
			"INSERT INTO claims (agent_name, path, exclusive, reason, created_ts, expires_ts) VALUES (?,?,?,?,?,?)",
			name, p, exclInt, reason, n, expires); err != nil {
			return nil, err
		}
		res.Granted = append(res.Granted, p)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	_ = s.Heartbeat(name)
	s.audit(name, "claim", map[string]any{"paths": paths, "ttl": ttlSeconds, "conflicts": len(res.Conflicts)})
	return res, nil
}

// parkedGraceSeconds is how long an awaiting_approval holder keeps enforcing its
// exclusive lease before it derives down to advisory. Demo sets this low (~20).
func parkedGraceSeconds() float64 {
	if v := os.Getenv("CORRALAI_PARKED_GRACE_SECONDS"); v != "" {
		// f >= 0: zero is valid — no grace, parked owners become advisory immediately (used by the demo).
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			return f
		}
	}
	return 300
}

// enforcing reports whether an exclusive holder still blocks. A holder that
// has been awaiting_approval longer than the grace window is treated as advisory
// (non-blocking) — derived here, never mutated, so it reverts the moment the
// holder un-parks.
func enforcing(c activeClaim, n float64) bool {
	if c.Status == "awaiting_approval" && n-c.StatusSince > parkedGraceSeconds() {
		return false
	}
	return true
}

func (s *Store) ReleaseClaims(name string, paths []string) (int64, error) {
	var total int64
	n := now()
	if len(paths) > 0 {
		for _, p := range paths {
			r, err := s.db.Exec("UPDATE claims SET released_ts=? WHERE agent_name=? AND path=? AND released_ts IS NULL", n, name, p)
			if err != nil {
				return total, err
			}
			c, _ := r.RowsAffected()
			total += c
		}
	} else {
		r, err := s.db.Exec("UPDATE claims SET released_ts=? WHERE agent_name=? AND released_ts IS NULL", n, name)
		if err != nil {
			return total, err
		}
		total, _ = r.RowsAffected()
	}
	s.audit(name, "release", map[string]any{"released": total})
	return total, nil
}

func (s *Store) Whois(name string) (*Agent, []Claim, error) {
	var a Agent
	err := s.db.QueryRow("SELECT name, program, model, task, parent, role, last_active_ts, registered_ts FROM agents WHERE name=?", name).
		Scan(&a.Name, &a.Program, &a.Model, &a.Task, &a.Parent, &a.Role, &a.LastActiveTS, &a.RegisteredTS)
	if err == sql.ErrNoRows {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	rows, err := s.db.Query(
		"SELECT path, exclusive, reason, expires_ts FROM claims WHERE agent_name=? AND released_ts IS NULL AND expires_ts > ?",
		name, now())
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	claims := []Claim{}
	for rows.Next() {
		var c Claim
		var excl int
		if err := rows.Scan(&c.Path, &excl, &c.Reason, &c.ExpiresTS); err != nil {
			return nil, nil, err
		}
		c.Exclusive = excl != 0
		claims = append(claims, c)
	}
	return &a, claims, rows.Err()
}

func (s *Store) MarkDone(name, summary string, paths []string) error {
	if paths == nil {
		paths = []string{}
	}
	b, _ := json.Marshal(paths)
	_, err := s.db.Exec("INSERT INTO completed_work (agent_name, summary, paths, completed_ts) VALUES (?,?,?,?)",
		name, summary, string(b), now())
	if err == nil {
		s.audit(name, "mark_done", map[string]string{"summary": summary})
	}
	return err
}

func (s *Store) RecentCompleted(limit int) ([]Completed, error) {
	rows, err := s.db.Query(
		"SELECT agent_name, summary, paths, completed_ts FROM completed_work ORDER BY completed_ts DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Completed{}
	for rows.Next() {
		var c Completed
		var pj string
		if err := rows.Scan(&c.AgentName, &c.Summary, &pj, &c.CompletedTS); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(pj), &c.Paths)
		out = append(out, c)
	}
	return out, rows.Err()
}

// ContestedClaims returns the agent's own still-held paths that overlap ANOTHER
// agent's active exclusive claim — i.e. work it must re-validate before touching
// (a peer may have proceeded while it was parked).
func (s *Store) ContestedClaims(name string) ([]Conflict, error) {
	n := now()
	rows, err := s.db.Query(
		"SELECT path FROM claims WHERE agent_name=? AND released_ts IS NULL AND expires_ts > ?", name, n)
	if err != nil {
		return nil, err
	}
	var mine []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			_ = rows.Close()
			return nil, err
		}
		mine = append(mine, p)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	active, err := s.activeClaims()
	if err != nil {
		return nil, err
	}
	out := []Conflict{}
	for _, p := range mine {
		for _, c := range active {
			if c.Agent != name && c.Exclusive && pathsOverlap(p, c.Path) {
				out = append(out, Conflict{Path: p, HeldBy: c.Agent, TheirPath: c.Path, Reason: c.Reason})
			}
		}
	}
	return out, nil
}

// BootstrapSession collapses register -> who's active -> your claims -> recent
// completed work into one call, so an agent doesn't redo a peer's finished task.
func (s *Store) BootstrapSession(name, program, model, task, sessionID, role string) (*Bootstrap, error) {
	if err := s.Register(name, program, model, task, sessionID, role); err != nil {
		return nil, err
	}
	all, err := s.ListActive(PresenceWindow)
	if err != nil {
		return nil, err
	}
	peers := []Agent{}
	for _, a := range all {
		if a.Name != name {
			peers = append(peers, a)
		}
	}
	_, claims, err := s.Whois(name)
	if err != nil {
		return nil, err
	}
	if claims == nil {
		claims = []Claim{}
	}
	done, err := s.RecentCompleted(10)
	if err != nil {
		return nil, err
	}
	contested, err := s.ContestedClaims(name)
	if err != nil {
		return nil, err
	}
	return &Bootstrap{
		You:             map[string]string{"name": name, "task": task},
		ActivePeers:     peers,
		YourClaims:      claims,
		RecentCompleted: done,
		Contested:       contested,
		Hint:            "Check active_peers' claims and recent_completed before claiming work.",
	}, nil
}

type LiveClaim struct {
	Agent     string  `json:"agent"`
	Path      string  `json:"path"`
	Exclusive bool    `json:"exclusive"`
	Reason    string  `json:"reason"`
	ExpiresTS float64 `json:"expires_ts"`
}

type Status struct {
	ActiveAgents       []Agent     `json:"active_agents"`
	LiveClaims         []LiveClaim `json:"live_claims"`
	RecentCompleted    []Completed `json:"recent_completed"`
	RecentActivity     []Activity  `json:"recent_activity"`
	ServerNow          float64     `json:"server_now"`
	ParkedGraceSeconds float64     `json:"parked_grace_seconds"`
}

func (s *Store) CoordinationStatus(window float64) (*Status, error) {
	agents, err := s.ListActive(window)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(
		"SELECT agent_name, path, exclusive, reason, expires_ts FROM claims WHERE released_ts IS NULL AND expires_ts > ? ORDER BY created_ts",
		now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	live := []LiveClaim{}
	for rows.Next() {
		var c LiveClaim
		var excl int
		if err := rows.Scan(&c.Agent, &c.Path, &excl, &c.Reason, &c.ExpiresTS); err != nil {
			return nil, err
		}
		c.Exclusive = excl != 0
		live = append(live, c)
	}
	done, err := s.RecentCompleted(10)
	if err != nil {
		return nil, err
	}
	if agents == nil {
		agents = []Agent{}
	}
	acts, _ := s.RecentActivityAll(30)
	return &Status{
		ActiveAgents:       agents,
		LiveClaims:         live,
		RecentCompleted:    done,
		RecentActivity:     acts,
		ServerNow:          now(),
		ParkedGraceSeconds: parkedGraceSeconds(),
	}, rows.Err()
}
