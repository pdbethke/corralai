// SPDX-License-Identifier: Elastic-2.0

// Package learn is the learning loop's proposals store: findings and lessons
// cluster into signature-deduped proposals, which a human reviews and either
// approves into standing guidance (and, optionally, a new skill) or rejects.
// A rejected signature is suppressed until the volume behind it doubles, and
// an approved proposal that keeps recurring reopens as a revision — so the
// loop neither nags on noise nor goes silent on guidance that isn't working.
//
// It is backed by pure-Go SQLite (modernc.org/sqlite, no CGO) with the same
// recipe the queue and coordination stores use — WAL + MaxOpenConns=1 — since
// writes here are low-volume and correctness (never losing a dedup race)
// matters more than write concurrency.
package learn

import (
	"database/sql"
	"encoding/json"
	"time"

	_ "modernc.org/sqlite"
)

// Proposal statuses.
const (
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusRejected = "rejected"
)

// maxEvidence caps the JSON evidence array kept per proposal.
const maxEvidence = 20

var realNow = func() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// now is the clock seam (overridable in tests).
var now = realNow

// Proposal is a clustered finding/lesson awaiting (or having received)
// human review.
type Proposal struct {
	ID            int64
	Signature     string
	Kind          string
	Roles         string
	Evidence      string
	Guidance      string
	SkillName     string
	SkillBody     string
	Status        string
	RejectReason  string
	Count         int
	RecurredAfter int
	Supersedes    int64
	CreatedTS     float64
	UpdatedTS     float64
}

type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS proposals (
  id             INTEGER PRIMARY KEY,
  signature      TEXT NOT NULL,        -- e.g. "missing-req|go.mod" or "lesson|<cluster-slug>"
  kind           TEXT NOT NULL,        -- 'finding' | 'lesson'
  roles          TEXT NOT NULL DEFAULT '',
  evidence       TEXT NOT NULL DEFAULT '[]',   -- JSON array of strings, capped at 20
  count          INTEGER NOT NULL DEFAULT 0,   -- cluster size seen so far
  guidance       TEXT NOT NULL DEFAULT '',
  skill_name     TEXT NOT NULL DEFAULT '',
  skill_body     TEXT NOT NULL DEFAULT '',
  status         TEXT NOT NULL,                -- pending | approved | rejected
  reject_reason  TEXT NOT NULL DEFAULT '',
  rejected_count INTEGER NOT NULL DEFAULT 0,   -- count at rejection (suppression baseline)
  recurred_after INTEGER NOT NULL DEFAULT 0,
  supersedes     INTEGER NOT NULL DEFAULT 0,
  created_ts     REAL NOT NULL,
  updated_ts     REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_proposals_sig ON proposals(signature, status);`

// Open returns a Store backed by a SQLite file (WAL). MaxOpenConns=1
// serializes writes, which is what makes the dedup-by-signature logic race-free.
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
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

const proposalSelect = `SELECT id,signature,kind,roles,evidence,count,guidance,skill_name,skill_body,
	status,reject_reason,rejected_count,recurred_after,supersedes,created_ts,updated_ts FROM proposals`

// scannedProposal pairs a Proposal with its rejected_count baseline, which is
// only used internally by Upsert's suppression check (not part of the public
// Proposal shape).
type scannedProposal struct {
	Proposal
	rejectedCount int
}

func scanProposal(row *sql.Row) (*scannedProposal, error) {
	var p scannedProposal
	if err := row.Scan(&p.ID, &p.Signature, &p.Kind, &p.Roles, &p.Evidence, &p.Count, &p.Guidance,
		&p.SkillName, &p.SkillBody, &p.Status, &p.RejectReason, &p.rejectedCount, &p.RecurredAfter,
		&p.Supersedes, &p.CreatedTS, &p.UpdatedTS); err != nil {
		return nil, err
	}
	return &p, nil
}

func scanProposalRows(rows *sql.Rows) (*Proposal, error) {
	var p scannedProposal
	if err := rows.Scan(&p.ID, &p.Signature, &p.Kind, &p.Roles, &p.Evidence, &p.Count, &p.Guidance,
		&p.SkillName, &p.SkillBody, &p.Status, &p.RejectReason, &p.rejectedCount, &p.RecurredAfter,
		&p.Supersedes, &p.CreatedTS, &p.UpdatedTS); err != nil {
		return nil, err
	}
	return &p.Proposal, nil
}

// capEvidence JSON-encodes an evidence snapshot, capped at maxEvidence
// (oldest-within-the-snapshot dropped first).
func capEvidence(ev []string) string {
	if len(ev) > maxEvidence {
		ev = ev[len(ev)-maxEvidence:]
	}
	if ev == nil {
		ev = []string{}
	}
	b, _ := json.Marshal(ev)
	return string(b)
}

// Upsert clusters a finding/lesson into a proposal by signature. The caller
// (Sweep, fed by the ticker) passes the CURRENT cluster as an absolute
// snapshot — not an increment — because the ticker re-feeds the full
// finding/lesson history every tick. Upsert therefore SETS count/evidence to
// the incoming snapshot rather than adding to them; re-running the same
// sweep on unchanged input is a no-op in effect (same values written back),
// and a sweep with a shrunk or grown cluster reflects that exactly.
//
//   - A pending row for the signature: its count/evidence become the
//     snapshot (created=false).
//   - No pending row, but the most recent row for the signature is APPROVED:
//     no-op (nil, false, nil) — promoted guidance already covers this
//     signature, and post-promotion recurrence is exclusively the efficacy
//     hook's job (RecordRecurrence via report_finding), which counts only
//     genuinely new findings, not sweep re-feeds.
//   - No pending row, most recent row REJECTED: suppressed (count bumped to
//     the snapshot value, still created=false) until the snapshot reaches 2x
//     the count at rejection, at which point it reopens as a fresh pending
//     row.
//   - No row at all: fresh pending row.
func (s *Store) Upsert(signature, kind, roles string, evidence []string) (*Proposal, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()

	snapCount := len(evidence)
	snapJSON := capEvidence(evidence)
	ts := now()

	// A pending row (whether the original opening or a post-promotion
	// revision cloned by RecordRecurrence) always absorbs the snapshot —
	// this is the ONLY branch that can run when an approved row for the same
	// signature also exists, which is what keeps a pending revision live.
	row := tx.QueryRow(proposalSelect+` WHERE signature=? AND status=?`, signature, StatusPending)
	if p, err := scanProposal(row); err == nil {
		if _, err := tx.Exec(`UPDATE proposals SET count=?, evidence=?, updated_ts=? WHERE id=?`,
			snapCount, snapJSON, ts, p.ID); err != nil {
			return nil, false, err
		}
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		p.Count, p.Evidence, p.UpdatedTS = snapCount, snapJSON, ts
		return &p.Proposal, false, nil
	} else if err != sql.ErrNoRows {
		return nil, false, err
	}

	// No pending row: the single most recent row (any status) for this
	// signature decides what happens next.
	row = tx.QueryRow(proposalSelect+` WHERE signature=? ORDER BY id DESC LIMIT 1`, signature)
	latest, err := scanProposal(row)
	switch {
	case err == sql.ErrNoRows:
		// Brand-new signature: open a pending proposal.
		res, err := tx.Exec(
			`INSERT INTO proposals (signature,kind,roles,evidence,count,status,created_ts,updated_ts)
			 VALUES (?,?,?,?,?,?,?,?)`,
			signature, kind, roles, snapJSON, snapCount, StatusPending, ts, ts,
		)
		if err != nil {
			return nil, false, err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return nil, false, err
		}
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		return &Proposal{ID: id, Signature: signature, Kind: kind, Roles: roles, Evidence: snapJSON,
			Count: snapCount, Status: StatusPending, CreatedTS: ts, UpdatedTS: ts}, true, nil
	case err != nil:
		return nil, false, err
	case latest.Status == StatusApproved:
		// Promoted guidance already covers this signature; sweep re-feeds
		// must not reopen it. (RecordRecurrence handles genuine recurrence.)
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		return nil, false, nil
	}

	// latest.Status == StatusRejected: suppressed until the incoming
	// snapshot reaches 2x the count it had at rejection.
	if snapCount < 2*latest.rejectedCount {
		if _, err := tx.Exec(`UPDATE proposals SET count=?, evidence=?, updated_ts=? WHERE id=?`,
			snapCount, snapJSON, ts, latest.ID); err != nil {
			return nil, false, err
		}
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		latest.Count, latest.Evidence, latest.UpdatedTS = snapCount, snapJSON, ts
		return &latest.Proposal, false, nil
	}

	// Reopen: fresh pending row.
	res, err := tx.Exec(
		`INSERT INTO proposals (signature,kind,roles,evidence,count,status,created_ts,updated_ts)
		 VALUES (?,?,?,?,?,?,?,?)`,
		signature, kind, roles, snapJSON, snapCount, StatusPending, ts, ts,
	)
	if err != nil {
		return nil, false, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return &Proposal{ID: id, Signature: signature, Kind: kind, Roles: roles, Evidence: snapJSON,
		Count: snapCount, Status: StatusPending, CreatedTS: ts, UpdatedTS: ts}, true, nil
}

// SetDraft records the human-authored (or human-approved-AI-drafted) guidance
// and optional skill seed on a proposal, ahead of Approve.
func (s *Store) SetDraft(id int64, guidance, skillName, skillBody string) error {
	_, err := s.db.Exec(`UPDATE proposals SET guidance=?, skill_name=?, skill_body=?, updated_ts=? WHERE id=?`,
		guidance, skillName, skillBody, now(), id)
	return err
}

// List returns proposals, optionally filtered by status (empty = all),
// newest first.
func (s *Store) List(status string) ([]Proposal, error) {
	var rows *sql.Rows
	var err error
	if status == "" {
		rows, err = s.db.Query(proposalSelect + ` ORDER BY id DESC`)
	} else {
		rows, err = s.db.Query(proposalSelect+` WHERE status=? ORDER BY id DESC`, status)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Proposal
	for rows.Next() {
		p, err := scanProposalRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// ByID returns a single proposal.
func (s *Store) ByID(id int64) (*Proposal, error) {
	row := s.db.QueryRow(proposalSelect+` WHERE id=?`, id)
	p, err := scanProposal(row)
	if err != nil {
		return nil, err
	}
	return &p.Proposal, nil
}

// Approve marks a proposal approved — this is the standing-guidance
// promotion point later tasks act on.
func (s *Store) Approve(id int64) (*Proposal, error) {
	ts := now()
	if _, err := s.db.Exec(`UPDATE proposals SET status=?, updated_ts=? WHERE id=?`, StatusApproved, ts, id); err != nil {
		return nil, err
	}
	return s.ByID(id)
}

// Reject marks a proposal rejected and records the count-at-rejection as the
// suppression baseline for future Upsert calls against the same signature.
func (s *Store) Reject(id int64, reason string) error {
	ts := now()
	_, err := s.db.Exec(
		`UPDATE proposals SET status=?, reject_reason=?, rejected_count=count, updated_ts=? WHERE id=?`,
		StatusRejected, reason, ts, id)
	return err
}

// RecordRecurrence increments the recurrence counter on the latest approved
// proposal for a signature — evidence that the approved guidance did not
// stop the underlying finding/lesson from recurring. At 2 recurrences it
// clones a new pending proposal (Supersedes = the approved id, guidance and
// skill copied as draft seeds) and resets the counter; below that it returns
// (nil, nil). An unknown signature is a no-op.
func (s *Store) RecordRecurrence(signature string) (*Proposal, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	row := tx.QueryRow(proposalSelect+` WHERE signature=? AND status=? ORDER BY id DESC LIMIT 1`, signature, StatusApproved)
	ap, err := scanProposal(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	ts := now()
	recurred := ap.RecurredAfter + 1
	if recurred < 2 {
		if _, err := tx.Exec(`UPDATE proposals SET recurred_after=?, updated_ts=? WHERE id=?`, recurred, ts, ap.ID); err != nil {
			return nil, err
		}
		return nil, tx.Commit()
	}

	// Threshold crossed. If a pending revision for this signature is
	// already awaiting review, don't stack another one on top of it — just
	// reset the counter so the next recurrence starts counting fresh
	// instead of cloning a duplicate proposal every 2 recurrences forever.
	pendingRow := tx.QueryRow(proposalSelect+` WHERE signature=? AND status=?`, ap.Signature, StatusPending)
	if _, perr := scanProposal(pendingRow); perr == nil {
		if _, err := tx.Exec(`UPDATE proposals SET recurred_after=0, updated_ts=? WHERE id=?`, ts, ap.ID); err != nil {
			return nil, err
		}
		return nil, tx.Commit()
	} else if perr != sql.ErrNoRows {
		return nil, perr
	}

	// Reopen as a revision.
	if _, err := tx.Exec(`UPDATE proposals SET recurred_after=0, updated_ts=? WHERE id=?`, ts, ap.ID); err != nil {
		return nil, err
	}
	res, err := tx.Exec(
		`INSERT INTO proposals (signature,kind,roles,evidence,count,guidance,skill_name,skill_body,status,supersedes,created_ts,updated_ts)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		ap.Signature, ap.Kind, ap.Roles, "[]", 0, ap.Guidance, ap.SkillName, ap.SkillBody, StatusPending, ap.ID, ts, ts,
	)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &Proposal{ID: id, Signature: ap.Signature, Kind: ap.Kind, Roles: ap.Roles, Evidence: "[]",
		Guidance: ap.Guidance, SkillName: ap.SkillName, SkillBody: ap.SkillBody, Status: StatusPending,
		Supersedes: ap.ID, CreatedTS: ts, UpdatedTS: ts}, nil
}
