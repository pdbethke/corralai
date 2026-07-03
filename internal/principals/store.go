// SPDX-License-Identifier: Elastic-2.0

// Package principals is CorralAI's Django-style user/role authority: a small
// SQLite table of who may use the brain (the allowlist) and who is a superuser
// (admin — may promote gateway endpoints/memory and manage other principals).
//
// Model (mirrors Django auth): the DB is canonical at runtime; environment
// variables (CORRALAI_ADMIN_PRINCIPALS / CORRALAI_ALLOWED_PRINCIPALS) are only a
// day-0 SEED, like DJANGO_SUPERUSER_*. A `corral createsuperuser` writes here.
//
// Open semantics (so a fresh brain isn't locked out): an EMPTY table means
// "dev/unconfigured" — everyone is allowed and everyone is a superuser, exactly
// like a fresh Django project before its first createsuperuser. Once any principal
// exists the allowlist is strict; once any SUPERUSER exists the admin gate is strict.
package principals

import (
	"database/sql"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS principals (
  email        TEXT PRIMARY KEY,             -- lowercased verified email
  is_superuser INTEGER NOT NULL DEFAULT 0,
  created_ts   REAL NOT NULL,
  created_by   TEXT NOT NULL DEFAULT '');
`

// Principal is a row in the table.
type Principal struct {
	Email       string  `json:"email"`
	IsSuperuser bool    `json:"is_superuser"`
	CreatedTS   float64 `json:"created_ts"`
	CreatedBy   string  `json:"created_by"`
}

// Store is the principal/role table.
type Store struct{ db *sql.DB }

// Open opens (creating if needed) the principals SQLite store.
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

func norm(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

func now() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// Seed idempotently inserts day-0 principals from env: superusers (is_superuser=1)
// and plain members. Existing rows are left untouched EXCEPT a seeded superuser is
// promoted if it was a member. Returns the number of rows created or promoted.
func (s *Store) Seed(superusers, members []string) (int, error) {
	n := 0
	for _, m := range members {
		e := norm(m)
		if e == "" {
			continue
		}
		res, err := s.db.Exec(
			`INSERT OR IGNORE INTO principals(email, is_superuser, created_ts, created_by) VALUES(?,0,?,'seed')`, e, now())
		if err != nil {
			return n, err
		}
		if c, _ := res.RowsAffected(); c > 0 {
			n++
		}
	}
	for _, su := range superusers {
		e := norm(su)
		if e == "" {
			continue
		}
		// Upsert as superuser (create, or promote an existing member).
		res, err := s.db.Exec(`
			INSERT INTO principals(email, is_superuser, created_ts, created_by) VALUES(?,1,?,'seed')
			ON CONFLICT(email) DO UPDATE SET is_superuser=1 WHERE principals.is_superuser=0`, e, now())
		if err != nil {
			return n, err
		}
		if c, _ := res.RowsAffected(); c > 0 {
			n++
		}
	}
	return n, nil
}

// Count returns the total number of principals.
func (s *Store) Count() int {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM principals`).Scan(&n)
	return n
}

// SuperuserCount returns the number of superusers.
func (s *Store) SuperuserCount() int {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM principals WHERE is_superuser=1`).Scan(&n)
	return n
}

func (s *Store) exists(email string) bool {
	var x int
	return s.db.QueryRow(`SELECT 1 FROM principals WHERE email=?`, norm(email)).Scan(&x) == nil
}

// Allowed reports whether a principal may use the brain. Empty table => open (dev).
func (s *Store) Allowed(email string) bool {
	if s.Count() == 0 {
		return true
	}
	return s.exists(email)
}

// IsSuperuser reports admin rights. No superuser configured yet => open (dev),
// mirroring a fresh Django project before its first createsuperuser.
func (s *Store) IsSuperuser(email string) bool {
	if s.SuperuserCount() == 0 {
		return true
	}
	var su int
	if err := s.db.QueryRow(`SELECT is_superuser FROM principals WHERE email=?`, norm(email)).Scan(&su); err != nil {
		return false
	}
	return su == 1
}

// CreateSuperuser adds (or promotes) a principal to superuser.
func (s *Store) CreateSuperuser(email, by string) error {
	_, err := s.db.Exec(`
		INSERT INTO principals(email, is_superuser, created_ts, created_by) VALUES(?,1,?,?)
		ON CONFLICT(email) DO UPDATE SET is_superuser=1`, norm(email), now(), norm(by))
	return err
}

// AddMember adds a non-superuser principal (no-op if already present).
func (s *Store) AddMember(email, by string) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO principals(email, is_superuser, created_ts, created_by) VALUES(?,0,?,?)`,
		norm(email), now(), norm(by))
	return err
}

// SetSuperuser promotes/demotes an EXISTING principal. Returns false if absent.
func (s *Store) SetSuperuser(email string, v bool) (bool, error) {
	su := 0
	if v {
		su = 1
	}
	res, err := s.db.Exec(`UPDATE principals SET is_superuser=? WHERE email=?`, su, norm(email))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Remove deletes a principal. Returns false if absent.
func (s *Store) Remove(email string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM principals WHERE email=?`, norm(email))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Get returns one principal or nil.
func (s *Store) Get(email string) (*Principal, error) {
	p := &Principal{}
	var su int
	err := s.db.QueryRow(`SELECT email, is_superuser, created_ts, created_by FROM principals WHERE email=?`, norm(email)).
		Scan(&p.Email, &su, &p.CreatedTS, &p.CreatedBy)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.IsSuperuser = su == 1
	return p, nil
}

// List returns all principals, superusers first then by email.
func (s *Store) List() ([]Principal, error) {
	rows, err := s.db.Query(`SELECT email, is_superuser, created_ts, created_by FROM principals ORDER BY is_superuser DESC, email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Principal
	for rows.Next() {
		var p Principal
		var su int
		if err := rows.Scan(&p.Email, &su, &p.CreatedTS, &p.CreatedBy); err != nil {
			return nil, err
		}
		p.IsSuperuser = su == 1
		out = append(out, p)
	}
	return out, rows.Err()
}
