// SPDX-License-Identifier: Elastic-2.0

package auditpush

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/pdbethke/corralai/internal/review"
)

// The review loop's three grains (docs/design/adversarial-review.md),
// additive to the five `certify` pushes. A review is keyed by
// review_uid, the ledger entry's hash — the same identity `corral review
// adjudicate` names a finding by — so an adjudication row pushed later,
// from anywhere, joins to its finding on (review_uid, finding_id).
//
// Custody follows --push-source: a finding's script and what it printed
// quote the audited code, so the warehouse gets their hashes always and
// their bytes only when the run opted in; the view over a ledger
// directory (LoadDir) carries them, because the directory does.

const reviewsSchema = `
CREATE TABLE IF NOT EXISTS corral_reviews (
  review_uid       VARCHAR,
  ts               TIMESTAMPTZ NOT NULL,
  repo             VARCHAR     NOT NULL,
  commit_sha       VARCHAR     NOT NULL,
  scope            VARCHAR,
  lang             VARCHAR,
  reviewer_model   VARCHAR,
  verifier_model   VARCHAR,
  substrate        VARCHAR,
  started_at       TIMESTAMPTZ,
  files_shown      INTEGER,
  truncated        BOOLEAN,
  reproduced       INTEGER,
  code_read        INTEGER,
  hypothesis       INTEGER,
  sound_items      INTEGER,
  coverage         VARCHAR,
  opinion_sha256   VARCHAR,
  statement_sha256 VARCHAR,
  input_tokens     BIGINT,
  output_tokens    BIGINT,
  source_pushed    BOOLEAN,
  schema_version   INTEGER,
  author           VARCHAR,
  committer        VARCHAR,
  co_authors       VARCHAR
);`

const findingsSchema = `
CREATE TABLE IF NOT EXISTS corral_findings (
  review_uid                VARCHAR,
  ts                        TIMESTAMPTZ NOT NULL,
  repo                      VARCHAR     NOT NULL,
  commit_sha                VARCHAR     NOT NULL,
  finding_id                VARCHAR     NOT NULL,
  claim                     VARCHAR,
  declared_tier             VARCHAR,
  tier                      VARCHAR,
  file                      VARCHAR,
  line                      INTEGER,
  severity                  VARCHAR,
  script                    VARCHAR,
  script_sha256             VARCHAR,
  output                    VARCHAR,
  output_sha256             VARCHAR,
  exit_code                 INTEGER,
  demoted                   VARCHAR,
  refutation_model          VARCHAR,
  refutation_verdict        VARCHAR,
  refutation_declared_tier  VARCHAR,
  refutation_tier           VARCHAR,
  refutation_argument       VARCHAR,
  refutation_script         VARCHAR,
  refutation_script_sha256  VARCHAR,
  refutation_output_sha256  VARCHAR,
  refutation_exit_code      INTEGER,
  refutation_demoted        VARCHAR,
  schema_version            INTEGER
);`

const adjudicationsSchema = `
CREATE TABLE IF NOT EXISTS corral_adjudications (
  review_uid   VARCHAR,
  finding_id   VARCHAR,
  ts           TIMESTAMPTZ NOT NULL,
  verdict      VARCHAR,
  decided_by   VARCHAR,
  reason       VARCHAR,
  entry_hash   VARCHAR,
  schema_version INTEGER
);`

// The three tables are new at this schema version: nothing predates their
// CREATE, so the lists are empty and wired into the same migration loop
// so the next column added goes through the additive path.
var (
	corralReviewsMigrationCols = []struct{ name, ddl string }{
		// The audited party, by name (see Identity); co_authors one per line.
		{"author", "author VARCHAR"},
		{"committer", "committer VARCHAR"},
		{"co_authors", "co_authors VARCHAR"},
	}
	corralFindingsMigrationCols      = []struct{ name, ddl string }{}
	corralAdjudicationsMigrationCols = []struct{ name, ddl string }{}
)

// ReviewCounts is what a review push wrote.
type ReviewCounts struct {
	Reviews, Findings, Adjudications int
}

// insertReviewEntry writes one review entry as a corral_reviews row and one
// corral_findings row per finding. withSource decides whether scripts and
// outputs travel as bytes or only as hashes.
func insertReviewEntry(db sqlExecer, e LedgerEntry, withSource bool) (ReviewCounts, error) {
	r := e.Review
	if r == nil {
		return ReviewCounts{}, fmt.Errorf("auditpush: a review entry with no review")
	}
	rep, cr, hy := r.Counts()
	src := func(s string) any {
		if !withSource {
			return nil
		}
		return nullIfEmpty(s)
	}
	if _, err := db.Exec(`INSERT INTO corral_reviews (
	    review_uid, ts, repo, commit_sha, scope, lang, reviewer_model, verifier_model, substrate, started_at,
	    files_shown, truncated, reproduced, code_read, hypothesis, sound_items, coverage,
	    opinion_sha256, statement_sha256, input_tokens, output_tokens, source_pushed, schema_version,
	    author, committer, co_authors
	  ) VALUES (`+placeholders(26)+`)`, // #nosec G202 -- placeholders(n) emits only "?, ?, …" for a constant count
		e.Hash, e.Pushed, r.Repo, r.Commit, nullIfEmpty(r.Scope), nullIfEmpty(r.Lang), nullIfEmpty(r.ReviewerModel), nullIfEmpty(r.VerifierModel),
		nullIfEmpty(r.Substrate), nullTime(nilIfZero(r.StartedAt)), len(r.FilesShown), r.Truncated, rep, cr, hy, len(r.Sound), nullIfEmpty(r.Coverage),
		nullIfEmpty(sha256HexOf(r.Opinion)), nullIfEmpty(r.StatementSHA256), r.InputTokens, r.OutputTokens, withSource, SchemaVersion,
		nullIfEmpty(r.Author), nullIfEmpty(r.Committer), nullIfEmpty(r.CoAuthors),
	); err != nil {
		return ReviewCounts{}, fmt.Errorf("auditpush: insert review row: %w", err)
	}
	c := ReviewCounts{Reviews: 1}
	for _, f := range r.Findings {
		var x review.Refutation
		if f.Refutation != nil {
			x = *f.Refutation
		}
		if _, err := db.Exec(`INSERT INTO corral_findings (
		    review_uid, ts, repo, commit_sha, finding_id, claim, declared_tier, tier, file, line, severity,
		    script, script_sha256, output, output_sha256, exit_code, demoted,
		    refutation_model, refutation_verdict, refutation_declared_tier, refutation_tier, refutation_argument,
		    refutation_script, refutation_script_sha256, refutation_output_sha256, refutation_exit_code, refutation_demoted,
		    schema_version
		  ) VALUES (`+placeholders(28)+`)`, // #nosec G202 -- placeholders(n) emits only "?, ?, …" for a constant count
			e.Hash, e.Pushed, r.Repo, r.Commit, f.ID, f.Claim, f.Declared, f.Tier, nullIfEmpty(f.File), nullIfZeroInt(f.Line), nullIfEmpty(f.Severity),
			src(f.Script), nullIfEmpty(sha256HexOf(f.Script)), src(f.Stdout), nullIfEmpty(sha256HexOf(f.Stdout)), f.ExitCode, nullIfEmpty(f.Demoted),
			nullIfEmpty(x.Model), nullIfEmpty(x.Verdict), nullIfEmpty(x.Declared), nullIfEmpty(x.Tier), nullIfEmpty(x.Argument),
			src(x.Script), nullIfEmpty(sha256HexOf(x.Script)), nullIfEmpty(sha256HexOf(x.Stdout)), x.ExitCode, nullIfEmpty(x.Demoted),
			SchemaVersion,
		); err != nil {
			return ReviewCounts{}, fmt.Errorf("auditpush: insert finding %s: %w", f.ID, err)
		}
		c.Findings++
	}
	return c, nil
}

// insertAdjudicationEntry writes one adjudication entry as a row.
func insertAdjudicationEntry(db sqlExecer, e LedgerEntry) error {
	a := e.Adjudication
	if a == nil {
		return fmt.Errorf("auditpush: an adjudication entry with no adjudication")
	}
	reviewUID, id, _ := cutRef(a.Adjudicates)
	if _, err := db.Exec(`INSERT INTO corral_adjudications (review_uid, finding_id, ts, verdict, decided_by, reason, entry_hash, schema_version)
	  VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, reviewUID, id, e.Pushed, a.Verdict, a.By, a.Reason, e.Hash, SchemaVersion); err != nil {
		return fmt.Errorf("auditpush: insert adjudication: %w", err)
	}
	return nil
}

// sqlExecer is what the grain inserts need: a *sql.DB, or the *sql.Tx a
// push runs under.
type sqlExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// PushReviewEntry appends a review or adjudication entry's rows to a
// warehouse the operator owns (a file, or md:). withSource is the custody
// switch for a review's scripts and outputs; an adjudication carries no
// source. Append-only, like every push, and ONE transaction: a review row
// whose findings are absent is the half-landed push PushBundle's
// transaction exists to prevent (ed079ca08965#R6).
func PushReviewEntry(target string, e LedgerEntry, withSource bool) (ReviewCounts, error) {
	db, err := openWarehouseForWrite(target)
	if err != nil {
		return ReviewCounts{}, err
	}
	defer db.Close()
	return pushReviewEntryTx(db, e, withSource)
}

// pushReviewEntryTx is PushReviewEntry on an open warehouse, under a
// transaction; the ledger push shares it.
func pushReviewEntryTx(db *sql.DB, e LedgerEntry, withSource bool) (ReviewCounts, error) {
	tx, err := db.Begin()
	if err != nil {
		return ReviewCounts{}, fmt.Errorf("auditpush: begin: %w", err)
	}
	var c ReviewCounts
	switch e.Kind {
	case KindReview:
		c, err = insertReviewEntry(tx, e, withSource)
	case KindAdjudication:
		if err = insertAdjudicationEntry(tx, e); err == nil {
			c = ReviewCounts{Adjudications: 1}
		}
	default:
		err = fmt.Errorf("auditpush: %q is not a review or an adjudication entry", e.Kind)
	}
	if err != nil {
		_ = tx.Rollback()
		return ReviewCounts{}, err
	}
	if err := tx.Commit(); err != nil {
		return ReviewCounts{}, fmt.Errorf("auditpush: commit: %w", err)
	}
	return c, nil
}

func nilIfZero(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func cutRef(ref string) (hash, id string, ok bool) {
	for i := 0; i < len(ref); i++ {
		if ref[i] == '#' {
			return ref[:i], ref[i+1:], true
		}
	}
	return ref, "", false
}

// sha256HexOf is the hash the statement (internal/certify) also computes
// for a script or an output: "" for "", so an absent script has no hash.
func sha256HexOf(s string) string {
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
