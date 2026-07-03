// SPDX-License-Identifier: Elastic-2.0

// Package gateway is CorralAI's MCP-gateway registry: a catalog of upstream MCP
// servers the brain can proxy to, plus an append-only audit of every proxied call.
//
// Governance model (deliberate, to bound mischief):
//   - Any authorized user may register a PERSONAL endpoint — usable ONLY by them
//     (owner-scoped). A user's endpoint can never affect a teammate.
//   - An ADMIN can view every endpoint and PROMOTE one to public (team-wide, or
//     scoped to specific principals), optionally swapping in a team credential.
//   - The brain holds upstream secrets (never returns them) and audits every call
//     by the verified caller (even when a promoted endpoint uses the owner's token).
package gateway

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS endpoints (
  name TEXT PRIMARY KEY,
  owner TEXT NOT NULL DEFAULT '',                -- principal who registered it
  public INTEGER NOT NULL DEFAULT 0,             -- admin-promoted to team-wide
  transport TEXT NOT NULL DEFAULT 'http',
  endpoint TEXT NOT NULL,
  description TEXT,
  enabled INTEGER NOT NULL DEFAULT 1,
  allowed_principals TEXT NOT NULL DEFAULT '',   -- comma list when public; empty = all authorized
  auth_header TEXT NOT NULL DEFAULT '',
  auth_token TEXT NOT NULL DEFAULT '',           -- the secret the brain holds (never returned)
  created_ts REAL NOT NULL);
CREATE TABLE IF NOT EXISTS gateway_audit (
  id INTEGER PRIMARY KEY AUTOINCREMENT, ts REAL NOT NULL, principal TEXT,
  server TEXT, tool TEXT, args TEXT, outcome TEXT);
`

// Endpoint is a registered upstream MCP server (secrets NOT included).
type Endpoint struct {
	Name              string   `json:"name"`
	Owner             string   `json:"owner,omitempty"`
	Public            bool     `json:"public"`
	Transport         string   `json:"transport"`
	Endpoint          string   `json:"endpoint"`
	Description       string   `json:"description,omitempty"`
	Enabled           bool     `json:"enabled"`
	AllowedPrincipals []string `json:"allowed_principals,omitempty"`
}

// Auth carries the upstream credential the brain injects when proxying.
type Auth struct {
	Header string
	Token  string
}

type Store struct{ db *sql.DB }

func now() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func splitNE(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

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
	return &Store{db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Register creates/updates a PERSONAL endpoint owned by `owner`. A name owned by
// another user is rejected (no clobbering). public/owner are not changed on update
// (only admin promotion flips public). An empty token leaves the secret untouched.
func (s *Store) Register(e Endpoint, auth Auth, owner string) error {
	var exOwner string
	err := s.db.QueryRow(`SELECT owner FROM endpoints WHERE name=?`, e.Name).Scan(&exOwner)
	switch {
	case err == sql.ErrNoRows:
		// new
		if _, err := s.db.Exec(`
			INSERT INTO endpoints (name,owner,public,transport,endpoint,description,enabled,allowed_principals,auth_header,auth_token,created_ts)
			VALUES (?,?,0,?,?,?,?,?,?,?,?)`,
			e.Name, owner, e.Transport, e.Endpoint, e.Description, b2i(e.Enabled),
			strings.Join(e.AllowedPrincipals, ","), auth.Header, auth.Token, now()); err != nil {
			return err
		}
		return nil
	case err != nil:
		return err
	case exOwner != owner:
		return fmt.Errorf("endpoint name %q is owned by another user", e.Name)
	}
	// update own
	if _, err := s.db.Exec(`UPDATE endpoints SET transport=?, endpoint=?, description=?, enabled=? WHERE name=?`,
		e.Transport, e.Endpoint, e.Description, b2i(e.Enabled), e.Name); err != nil {
		return err
	}
	if auth.Token != "" {
		_, err = s.db.Exec(`UPDATE endpoints SET auth_header=?, auth_token=? WHERE name=?`, auth.Header, auth.Token, e.Name)
	}
	return err
}

// Promote (admin) makes an endpoint public/private, optionally scoping it to
// principals and swapping in a team credential (auth.Token != "").
func (s *Store) Promote(name string, public bool, allowed []string, auth Auth) (bool, error) {
	r, err := s.db.Exec(`UPDATE endpoints SET public=?, allowed_principals=? WHERE name=?`,
		b2i(public), strings.Join(allowed, ","), name)
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	if n > 0 && auth.Token != "" {
		_, err = s.db.Exec(`UPDATE endpoints SET auth_header=?, auth_token=? WHERE name=?`, auth.Header, auth.Token, name)
	}
	return n > 0, err
}

func (s *Store) SetEnabled(name string, enabled bool) (bool, error) {
	r, err := s.db.Exec(`UPDATE endpoints SET enabled=? WHERE name=?`, b2i(enabled), name)
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	return n > 0, nil
}

func (s *Store) Remove(name string) (bool, error) {
	r, err := s.db.Exec(`DELETE FROM endpoints WHERE name=?`, name)
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	return n > 0, nil
}

// OwnerOf returns the registered owner of name (""/false if missing).
func (s *Store) OwnerOf(name string) (string, bool) {
	var o string
	if err := s.db.QueryRow(`SELECT owner FROM endpoints WHERE name=?`, name).Scan(&o); err != nil {
		return "", false
	}
	return o, true
}

func scan(rows *sql.Rows) (Endpoint, error) {
	var e Endpoint
	var public, enabled int
	var allowed string
	err := rows.Scan(&e.Name, &e.Owner, &public, &e.Transport, &e.Endpoint, &e.Description, &enabled, &allowed)
	e.Public, e.Enabled, e.AllowedPrincipals = public != 0, enabled != 0, splitNE(allowed)
	return e, err
}

const selCols = `SELECT name,owner,public,transport,endpoint,description,enabled,allowed_principals FROM endpoints`

func (s *Store) query(where string, args ...any) ([]Endpoint, error) {
	rows, err := s.db.Query(selCols+" "+where+" ORDER BY name", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Endpoint{}
	for rows.Next() {
		e, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListAll returns every endpoint (admin view).
func (s *Store) ListAll() ([]Endpoint, error) { return s.query("") }

// ListOwned returns a principal's own endpoints.
func (s *Store) ListOwned(principal string) ([]Endpoint, error) {
	return s.query("WHERE owner=?", principal)
}

// Usable returns enabled endpoints the principal may use: their own, plus public
// ones permitted to them.
func (s *Store) Usable(principal string) ([]Endpoint, error) {
	all, err := s.query("WHERE enabled=1")
	if err != nil {
		return nil, err
	}
	out := []Endpoint{}
	for _, e := range all {
		if usableBy(e, principal) {
			out = append(out, e)
		}
	}
	return out, nil
}

func usableBy(e Endpoint, principal string) bool {
	if e.Owner == principal {
		return true
	}
	if !e.Public {
		return false
	}
	if len(e.AllowedPrincipals) == 0 {
		return true
	}
	for _, p := range e.AllowedPrincipals {
		if strings.EqualFold(p, principal) {
			return true
		}
	}
	return false
}

// Resolve returns a usable endpoint + its auth for proxying, or ok=false if
// missing/disabled/not-usable by this principal.
func (s *Store) Resolve(name, principal string) (Endpoint, Auth, bool, error) {
	var e Endpoint
	var public, enabled int
	var allowed, header, token string
	err := s.db.QueryRow(`SELECT name,owner,public,transport,endpoint,description,enabled,allowed_principals,auth_header,auth_token FROM endpoints WHERE name=?`, name).
		Scan(&e.Name, &e.Owner, &public, &e.Transport, &e.Endpoint, &e.Description, &enabled, &allowed, &header, &token)
	if err == sql.ErrNoRows {
		return Endpoint{}, Auth{}, false, nil
	}
	if err != nil {
		return Endpoint{}, Auth{}, false, err
	}
	e.Public, e.Enabled, e.AllowedPrincipals = public != 0, enabled != 0, splitNE(allowed)
	if !e.Enabled || !usableBy(e, principal) {
		return e, Auth{}, false, nil
	}
	return e, Auth{Header: header, Token: token}, true, nil
}

// Audit records a proxied call (args truncated by the caller).
func (s *Store) Audit(principal, server, tool, args, outcome string) {
	_, _ = s.db.Exec(`INSERT INTO gateway_audit (ts,principal,server,tool,args,outcome) VALUES (?,?,?,?,?,?)`,
		now(), principal, server, tool, args, outcome)
}
