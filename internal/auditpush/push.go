// SPDX-License-Identifier: Elastic-2.0

// Package auditpush appends a scan's verdict to a DuckDB the OPERATOR owns —
// a local file, or their MotherDuck database.
//
// It is deliberately not a service. corral has no hosted tier and collects no
// telemetry: the key is theirs, the runner is theirs, and the warehouse is
// theirs too. That is also why the target is any DuckDB rather than MotherDuck
// specifically — `md:<db>` is one destination, a path on disk is another, and
// neither is a lock-in.
//
// What it exists for is the question a single pull request cannot answer.
// One kill rate is a sample; we have watched the same unchanged diff score 0.85
// and then 0.90. Forty of them are a distribution, and "this file has drifted
// from 0.9 to 0.6 over two months" is a claim no individual run can support.
//
// Two rules the schema enforces rather than documents:
//
//   - APPEND ONLY. A verified receipt that can be UPDATEd is not a receipt.
//     Rows are inserted, never modified, and each carries the sha256 of the
//     signed statement it came from so a row traces back to something a third
//     party can verify.
//   - THE QUALIFIERS TRAVEL WITH THE NUMBERS. proven_missed = 0 means "nothing
//     was proven" rather than "the suite is clean" whenever the writer failed
//     or its test never graded. Aggregation is exactly where that distinction
//     gets dropped and a zero silently becomes good news, so the columns sit
//     beside the number and every documented query carries them.
package auditpush

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"
)

// Row is one audited file, as it lands in the warehouse.
type Row struct {
	Repo             string
	Commit           string
	Path             string
	KillRate         float64
	Survivors        int
	ProvenMissed     int
	TimedOut         bool
	TestWriterFailed bool
	PoolTestUnsound  bool
	// Scope travels on every row, denormalized on purpose: a query that reads
	// one row must be able to see how much of the repo was looked at. "3 files
	// clean" reads very differently out of 4 than out of 400, and a join to
	// find that out is a join people skip.
	Audited    int
	Candidates int
	// Comparability. Without these a cross-project row is unusable: a kill
	// rate on a dense 200-line function and one on a 12-line accessor are not
	// the same measurement, and a reader cannot tell a hard file from a weak
	// suite.
	Lang            string
	MutantsPlanted  int
	ModelsByRole    string // JSON, so a new role does not need a migration
	MinKillRate     *float64
	MaxProvenMissed *int
	Passed          bool
	// StatementSHA256 ties this row to the signed in-toto statement the run
	// published. It is what makes the table evidence rather than self-report:
	// any row can be traced to an attestation a third party verifies.
	StatementSHA256 string
	RunURL          string
}

const schema = `
CREATE TABLE IF NOT EXISTS corral_audits (
  ts                 TIMESTAMPTZ NOT NULL,
  repo               VARCHAR     NOT NULL,
  commit_sha         VARCHAR     NOT NULL,
  path               VARCHAR     NOT NULL,
  lang               VARCHAR,
  kill_rate          DOUBLE,
  survivors          INTEGER,
  proven_missed      INTEGER,
  timed_out          BOOLEAN,
  test_writer_failed BOOLEAN,
  pool_test_unsound  BOOLEAN,
  audited            INTEGER,
  candidates         INTEGER,
  mutants_planted    INTEGER,
  models_by_role     VARCHAR,
  min_kill_rate      DOUBLE,
  max_proven_missed  INTEGER,
  passed             BOOLEAN,
  statement_sha256   VARCHAR,
  run_url            VARCHAR
);`

// Push appends rows to target, creating the table if it is not there.
//
// target is a DuckDB path or `md:<db>`. For MotherDuck the caller must have set
// motherduck_token in the environment — the same contract fleet sync uses, and
// the reason this takes no credential of its own: corral never holds one.
func Push(target string, rows []Row) (int, error) {
	if strings.TrimSpace(target) == "" {
		return 0, fmt.Errorf("auditpush: no target")
	}
	if len(rows) == 0 {
		return 0, nil
	}

	db, err := sql.Open("duckdb", "")
	if err != nil {
		return 0, err
	}
	defer db.Close()

	if strings.HasPrefix(target, "md:") {
		if _, err := db.Exec("INSTALL motherduck; LOAD motherduck;"); err != nil {
			return 0, fmt.Errorf("auditpush: load motherduck extension: %w", err)
		}
	}
	if _, err := db.Exec(fmt.Sprintf("ATTACH '%s' AS warehouse", strings.ReplaceAll(target, "'", "''"))); err != nil {
		return 0, fmt.Errorf("auditpush: attach %q: %w", target, err)
	}
	if _, err := db.Exec(strings.Replace(schema, "corral_audits", "warehouse.corral_audits", 1)); err != nil {
		return 0, fmt.Errorf("auditpush: create table: %w", err)
	}

	stmt, err := db.Prepare(`INSERT INTO warehouse.corral_audits VALUES
	  (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	now := time.Now().UTC()
	n := 0
	for _, r := range rows {
		var minKill any
		if r.MinKillRate != nil {
			minKill = *r.MinKillRate
		}
		var maxGaps any
		if r.MaxProvenMissed != nil {
			maxGaps = *r.MaxProvenMissed
		}
		if _, err := stmt.Exec(now, r.Repo, r.Commit, r.Path, r.Lang,
			r.KillRate, r.Survivors, r.ProvenMissed,
			r.TimedOut, r.TestWriterFailed, r.PoolTestUnsound,
			r.Audited, r.Candidates, r.MutantsPlanted, r.ModelsByRole,
			minKill, maxGaps, r.Passed, r.StatementSHA256, r.RunURL); err != nil {
			return n, fmt.Errorf("auditpush: insert %s: %w", r.Path, err)
		}
		n++
	}
	return n, nil
}
