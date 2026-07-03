// SPDX-License-Identifier: Elastic-2.0

// Package artifacts is CorralAI's fleet-shared file layer: skills (~/.claude/
// skills/*) and hooks (guardrail scripts + config) versioned in the brain so a
// machine that runs `corral sync` converges to the team's canonical set — and
// pushes its own edits back. This is the memory corpus's pattern (files as the
// unit) but BIDIRECTIONAL, with a real reconcile.
//
// Sync model: every change gets the next global REV (a monotonic logical clock).
// A client tracks the highest rev it has seen and pulls everything newer; it
// pushes files whose content changed locally. Deletes are tombstones (deleted=1,
// new rev) so a removal propagates. Conflict detection (both sides changed the
// same path) lives in the CLIENT against its last-synced manifest — the brain is
// just the authoritative, append-rev'd store.
package artifacts

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS artifacts (
  path        TEXT PRIMARY KEY,            -- e.g. "skills/deploy/SKILL.md", "hooks/branch-guard.sh"
  kind        TEXT NOT NULL,               -- 'skill' | 'hook'
  content     BLOB NOT NULL,
  sha256      TEXT NOT NULL,
  rev         INTEGER NOT NULL,            -- global monotonic; pull is "rev > sinceRev"
  updated_ts  REAL NOT NULL,
  updated_by  TEXT NOT NULL DEFAULT '',
  deleted     INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS ix_artifacts_rev ON artifacts(rev);
CREATE TABLE IF NOT EXISTS artifacts_meta (k TEXT PRIMARY KEY, v INTEGER NOT NULL);
INSERT OR IGNORE INTO artifacts_meta(k, v) VALUES('rev', 0);
`

// Artifact is one versioned file. Content is nil for a pulled tombstone.
type Artifact struct {
	Path      string  `json:"path"`
	Kind      string  `json:"kind"`
	Content   []byte  `json:"-"`
	Sha256    string  `json:"sha256"`
	Rev       int64   `json:"rev"`
	UpdatedTS float64 `json:"updated_ts"`
	UpdatedBy string  `json:"updated_by"`
	Deleted   bool    `json:"deleted"`
}

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func now() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// Sha returns the lowercase hex sha256 of content (the sync identity of a file).
func Sha(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// kindOf derives the artifact kind from its path prefix.
func kindOf(path string) string {
	if strings.HasPrefix(path, "hooks/") {
		return "hook"
	}
	return "skill"
}

// HeadRev returns the current max rev (0 when empty).
func (s *Store) HeadRev() int64 {
	var v int64
	_ = s.db.QueryRow(`SELECT v FROM artifacts_meta WHERE k='rev'`).Scan(&v)
	return v
}

func (s *Store) bumpRev() (int64, error) {
	if _, err := s.db.Exec(`UPDATE artifacts_meta SET v = v + 1 WHERE k='rev'`); err != nil {
		return 0, err
	}
	return s.HeadRev(), nil
}

// Put stores (or replaces) a file's content under the next rev. updatedTS is the
// client's EDIT time (file mtime) so conflict last-write-wins reflects who edited
// last, not who pushed last; <=0 falls back to server now(). Returns the new rev +
// sha. No-op-with-current-rev if the content is byte-identical to what's stored.
func (s *Store) Put(path string, content []byte, by string, updatedTS float64) (int64, string, error) {
	sha := Sha(content)
	var curSha string
	var curRev int64
	var curDeleted int
	err := s.db.QueryRow(`SELECT sha256, rev, deleted FROM artifacts WHERE path=?`, path).Scan(&curSha, &curRev, &curDeleted)
	if err == nil && curSha == sha && curDeleted == 0 {
		return curRev, sha, nil // unchanged — keep its rev
	}
	ts := updatedTS
	if ts <= 0 {
		ts = now()
	}
	rev, err := s.bumpRev()
	if err != nil {
		return 0, "", err
	}
	_, err = s.db.Exec(`
		INSERT INTO artifacts(path, kind, content, sha256, rev, updated_ts, updated_by, deleted)
		VALUES(?,?,?,?,?,?,?,0)
		ON CONFLICT(path) DO UPDATE SET
		  kind=excluded.kind, content=excluded.content, sha256=excluded.sha256,
		  rev=excluded.rev, updated_ts=excluded.updated_ts, updated_by=excluded.updated_by, deleted=0`,
		path, kindOf(path), content, sha, rev, ts, by)
	return rev, sha, err
}

// Delete tombstones a path (new rev, deleted=1) so the removal propagates.
// Returns the new rev, or (0,false) if the path is absent / already deleted.
func (s *Store) Delete(path, by string) (int64, bool, error) {
	var curDeleted int
	if err := s.db.QueryRow(`SELECT deleted FROM artifacts WHERE path=?`, path).Scan(&curDeleted); err != nil {
		return 0, false, nil // absent
	}
	if curDeleted == 1 {
		return 0, false, nil // already a tombstone
	}
	rev, err := s.bumpRev()
	if err != nil {
		return 0, false, err
	}
	_, err = s.db.Exec(`UPDATE artifacts SET deleted=1, content=x'', sha256='', rev=?, updated_ts=?, updated_by=? WHERE path=?`,
		rev, now(), by, path)
	return rev, true, err
}

// Get returns one live artifact (with content) or nil if absent/deleted.
func (s *Store) Get(path string) (*Artifact, error) {
	a := &Artifact{}
	var del int
	err := s.db.QueryRow(`SELECT path, kind, content, sha256, rev, updated_ts, updated_by, deleted FROM artifacts WHERE path=?`, path).
		Scan(&a.Path, &a.Kind, &a.Content, &a.Sha256, &a.Rev, &a.UpdatedTS, &a.UpdatedBy, &del)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if del == 1 {
		return nil, nil
	}
	a.Deleted = false
	return a, nil
}

// Changes returns all artifacts (including tombstones) with rev > sinceRev,
// oldest-first, WITH content (so a single pull carries everything the client
// needs to write). For incremental sync the client passes its last-seen rev.
func (s *Store) Changes(sinceRev int64) ([]Artifact, error) {
	rows, err := s.db.Query(`
		SELECT path, kind, content, sha256, rev, updated_ts, updated_by, deleted
		FROM artifacts WHERE rev > ? ORDER BY rev ASC`, sinceRev)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Artifact
	for rows.Next() {
		var a Artifact
		var del int
		if err := rows.Scan(&a.Path, &a.Kind, &a.Content, &a.Sha256, &a.Rev, &a.UpdatedTS, &a.UpdatedBy, &del); err != nil {
			return nil, err
		}
		a.Deleted = del == 1
		out = append(out, a)
	}
	return out, rows.Err()
}

// ChangesPrefix is Changes restricted to paths under prefix (e.g. a role's skill
// namespace "skills/roles/tester/"). Used to equip a subagent with only its role's
// skills rather than the whole corpus.
func (s *Store) ChangesPrefix(sinceRev int64, prefix string) ([]Artifact, error) {
	if prefix == "" {
		return s.Changes(sinceRev)
	}
	rows, err := s.db.Query(`
		SELECT path, kind, content, sha256, rev, updated_ts, updated_by, deleted
		FROM artifacts WHERE rev > ? AND substr(path, 1, ?) = ? ORDER BY rev ASC`,
		sinceRev, len(prefix), prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Artifact
	for rows.Next() {
		var a Artifact
		var del int
		if err := rows.Scan(&a.Path, &a.Kind, &a.Content, &a.Sha256, &a.Rev, &a.UpdatedTS, &a.UpdatedBy, &del); err != nil {
			return nil, err
		}
		a.Deleted = del == 1
		out = append(out, a)
	}
	return out, rows.Err()
}

// Count returns the number of live (non-tombstone) artifacts.
func (s *Store) Count() int {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM artifacts WHERE deleted=0`).Scan(&n)
	return n
}
