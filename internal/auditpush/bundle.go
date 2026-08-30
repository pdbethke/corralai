// SPDX-License-Identifier: Elastic-2.0

package auditpush

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"
)

// SchemaVersion stamps every row this package writes. It does NOT mean the
// verdict changed — nothing about what corral measures is different — it
// means the row has the timing, usage and disposition columns at all, so a
// reader can tell a row that can answer "where did the minutes go" from one
// that cannot. A pre-2 row has the column (added by migration) and NULL in
// it, which is the honest value.
const SchemaVersion = 2

// ScanRow is the run itself: one row per `certify --repo` invocation. It is
// the header the other four grains hang from, and it exists because a
// per-file number without the box, the version and the substrate that
// produced it is a claim with no warrant.
//
// The key is (Repo, RunURL, ScanID), and that is deliberate: it is unique
// PER WRITER, so twenty runners pushing at once need no coordination and can
// never collide. A local run has RunURL "" and Host set.
//
// One case DOES repeat the key, and it is worth naming rather than
// discovering: a local run with no --record has RunURL "" and ScanID 0, so
// every such push to the same repo lands under ("repo", "", 0). That is
// safe, not a bug — these tables are APPEND-ONLY and every row carries ts,
// so the rows stack rather than overwrite and every reader (corral_seal
// included) orders by ts. What the repeated key costs is joinability: those
// rows cannot be tied back to a local ledger scan, because there is no
// ledger scan to tie them to. --record is what makes the key distinguishing.
type ScanRow struct {
	Repo   string
	RunURL string
	ScanID int64
	Commit string
	// CorralVersion is the same string `corral version` prints.
	CorralVersion string
	Substrate     string
	Host          string
	Cores         int
	// TreesRequested is what the scan ASKED each file's pool for on the
	// workspace substrate — an intention, not a result (the per-file probe
	// decides what it got, and corral_audits.trees records that). 0 on the
	// jail, which builds no trees, and written SQL NULL there.
	TreesRequested int
	DiffBase       string
	Candidates     int
	Audited        int
	Passed         bool
	// TotalMillis is the scan's own wall clock. *int64 so a run that did not
	// time itself is NULL rather than a scan that took no time.
	TotalMillis *int64
	// SelectionMillis is the scan's ONE instrumented coverage run — the pass
	// that decides which tests execute which file. It belongs at the SCAN
	// grain because that is where it happens: corral_audits.selection_ms
	// carries the same run on every file of the scan so a per-file readout
	// can name each phase, and summing THAT column would count one run once
	// per file. This is the column a cost query adds.
	//
	// NULL — never 0 — for a scan that instrumented nothing (`--whole-suite`,
	// an unsupported language, a runner that could not be built).
	SelectionMillis *int64
	InputTokens     int64
	OutputTokens    int64
	ModelCalls      int64
	// SourcePushed records whether THIS run carried source bytes to the
	// warehouse. A custody fact belongs in the record: "did our code leave
	// the box on that run" must be answerable from the table, not from
	// whoever remembers the argv.
	SourcePushed    bool
	StatementSHA256 string
}

// MutantRow is one mutant's fate. The warehouse table is new, so unlike the
// local scan_mutants (whose CHECK predates this schema and cannot be altered
// in place) it admits all four outcomes: killed, survived, invalid,
// timed_out. A kill rate computed over a denominator that silently excludes
// the invalid and timed-out mutants is a different number from one that
// discloses them.
type MutantRow struct {
	Repo         string
	RunURL       string
	ScanID       int64
	Path         string
	MutantID     string
	ParentSHA256 string
	Outcome      string
	// InvalidReason says WHY a mutant never graded (it did not compile, the
	// gate rejected it). Without it, "invalid" is a count with no diagnosis
	// and a generator that produces garbage looks the same as one that
	// produces hard mutants.
	InvalidReason string
	Proven        bool
	// ProvenByAuthoredAlone is the strict subset of Proven that is a
	// demonstrated GAP: the authored test killed it and the dev suite never
	// did.
	ProvenByAuthoredAlone bool
	TestsRun              int
	SelectionRule         string
	// DurationMillis is how long grading this mutant took. NULL, never 0,
	// when nothing timed it.
	DurationMillis *int64
	// KilledBy is the first failing test id, best effort, when the language
	// plugin can parse one out of the runner's output. "" when it cannot —
	// never inferred.
	KilledBy string
	// SpanStart and SpanEnd are the mutated line range. NOTHING produces
	// them yet (advpool.MutantRef carries no span), and they are 1-BASED, so
	// 0 is unambiguously "not recorded" and is written SQL NULL — line 0
	// does not exist, and a reader jumping to it would be sent to the top of
	// the file.
	SpanStart int
	SpanEnd   int
	// Code is the mutant's source. It IS the audited code, so it is written
	// only when the bundle says --push-source was given.
	Code            string
	StatementSHA256 string
}

// ModelCallRow is what one role's seat cost on one file — the money grain.
// The scan header's totals answer "what did this audit cost"; only this
// grain answers "which seat was slow, and on which file".
type ModelCallRow struct {
	Repo         string
	RunURL       string
	ScanID       int64
	Path         string
	Role         string
	Model        string
	Calls        int
	Retries      int
	InputTokens  int64
	OutputTokens int64
	WallMillis   int64

	StatementSHA256 string
}

// EventRow is one entry on the tape. Seq, not TS, is the ordering key: two
// events inside one millisecond are ordinary, and a tape whose order depends
// on clock granularity is not a tape.
type EventRow struct {
	Repo    string
	RunURL  string
	ScanID  int64
	Path    string
	Seq     int64
	Kind    string
	Actor   string
	Subject string
	Model   string
	// DurationMillis is set on the events that HAVE a duration (a completed
	// phase, a returned call) and nil on the ones that are a moment.
	DurationMillis *int64
	// Detail is JSON TEXT in a VARCHAR column — see the corral_events DDL
	// below for why the column is not DuckDB's JSON type.
	Detail string

	StatementSHA256 string
}

// Bundle is one scan at all five grains: the thing a push actually is. It
// exists as one value, rather than five calls, because the five tables must
// land together — a scan row with no mutants reads, to the next query, as a
// scan that produced none.
type Bundle struct {
	Scan    ScanRow
	Files   []Row
	Mutants []MutantRow
	Calls   []ModelCallRow
	Events  []EventRow
	// Link is stamped onto every row of every table by PushBundle, so a row
	// and the statement it names can never disagree about which run produced
	// them.
	Link Link
	// SourcePushed is the custody switch (`--push-source`). When false, the
	// three source-bearing columns are written SQL NULL no matter what the
	// rows carry: the writer enforces it, not the caller.
	SourcePushed bool
}

// Counts is what a push actually wrote, per table. One integer would have to
// be a sum, and a sum cannot tell "3 mutants" from "3 events" — which is the
// difference between a scan that graded something and one that did not.
type Counts struct {
	Scans   int
	Files   int
	Mutants int
	Calls   int
	Events  int
}

// Total is every row the push wrote, for a caller that only wants one number.
func (c Counts) Total() int { return c.Scans + c.Files + c.Mutants + c.Calls + c.Events }

const scansSchema = `
CREATE TABLE IF NOT EXISTS corral_scans (
  ts               TIMESTAMPTZ NOT NULL,
  repo             VARCHAR     NOT NULL,
  run_url          VARCHAR,
  scan_id          BIGINT,
  commit_sha       VARCHAR,
  corral_version   VARCHAR,
  substrate        VARCHAR,
  host             VARCHAR,
  cores            INTEGER,
  trees_requested  INTEGER,
  diff_base        VARCHAR,
  candidates       INTEGER,
  audited          INTEGER,
  passed           BOOLEAN,
  total_ms         BIGINT,
  input_tokens     BIGINT,
  output_tokens    BIGINT,
  model_calls      BIGINT,
  source_pushed    BOOLEAN,
  statement_sha256 VARCHAR,
  selection_ms     BIGINT,
  schema_version   INTEGER
);`

const auditsSchema = `
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
  run_url            VARCHAR,
  test_selection     VARCHAR,
  selected_tests     INTEGER,
  suite_tests        INTEGER,
  selection_fallback VARCHAR,
  uncovered          BOOLEAN,
  per_mutant              BOOLEAN,
  tests_per_mutant_min    INTEGER,
  tests_per_mutant_median INTEGER,
  tests_per_mutant_max    INTEGER,
  trees                   INTEGER,
  concurrency_note        VARCHAR,
  shared_dirs             VARCHAR,
  scan_id                 BIGINT,
  disposition             VARCHAR,
  reason                  VARCHAR,
  preflight_state         VARCHAR,
  evidence                VARCHAR,
  detail                  VARCHAR,
  status                  VARCHAR,
  cache_hit               BOOLEAN,
  reused_from_scan_id     BIGINT,
  cache_key               VARCHAR,
  parent_sha256           VARCHAR,
  mutants_graded          INTEGER,
  mutants_invalid         INTEGER,
  mutants_timed_out       INTEGER,
  regions_total           INTEGER,
  regions_probed          INTEGER,
  dropped_regions         VARCHAR,
  vacuous_findings        INTEGER,
  authored_test_not_collected BOOLEAN,
  baseline_failed         BOOLEAN,
  suite_baseline_ms       BIGINT,
  proven_mutant_ids       VARCHAR,
  challenger_jaccard      DOUBLE,
  challenger_kappa        DOUBLE,
  challenger_sufficient   BOOLEAN,
  goals_derived           INTEGER,
  selection_ms            BIGINT,
  generation_ms           BIGINT,
  pool_ms                 BIGINT,
  dev_pass_ms             BIGINT,
  authored_pass_ms        BIGINT,
  critic_ms               BIGINT,
  total_ms                BIGINT,
  mutant_ms_median        BIGINT,
  mutant_ms_max           BIGINT,
  authored_test           VARCHAR,
  verdict_json            VARCHAR,
  schema_version          INTEGER
);`

// The mutant grain's outcome CHECK is the same discipline scan_files'
// evidence CHECK is: this table is queried by exact string, and a typo'd
// label should fail loud at INSERT rather than quietly enter a leaderboard.
const mutantsSchema = `
CREATE TABLE IF NOT EXISTS corral_mutants (
  ts               TIMESTAMPTZ NOT NULL,
  repo             VARCHAR     NOT NULL,
  run_url          VARCHAR,
  scan_id          BIGINT,
  path             VARCHAR,
  mutant_id        VARCHAR,
  parent_sha256    VARCHAR,
  outcome          VARCHAR CHECK (outcome IN ('killed','survived','invalid','timed_out')),
  invalid_reason   VARCHAR,
  proven           BOOLEAN,
  proven_by_authored_alone BOOLEAN,
  tests_run        INTEGER,
  selection_rule   VARCHAR,
  duration_ms      BIGINT,
  killed_by        VARCHAR,
  span_start       INTEGER,
  span_end         INTEGER,
  code             VARCHAR,
  statement_sha256 VARCHAR,
  schema_version   INTEGER
);`

const modelCallsSchema = `
CREATE TABLE IF NOT EXISTS corral_model_calls (
  ts               TIMESTAMPTZ NOT NULL,
  repo             VARCHAR     NOT NULL,
  run_url          VARCHAR,
  scan_id          BIGINT,
  path             VARCHAR,
  role             VARCHAR,
  model            VARCHAR,
  calls            INTEGER,
  retries          INTEGER,
  input_tokens     BIGINT,
  output_tokens    BIGINT,
  wall_ms          BIGINT,
  statement_sha256 VARCHAR,
  schema_version   INTEGER
);`

// detail is VARCHAR holding JSON TEXT, deliberately, and this is a recorded
// deviation from the design's `detail JSON`: the target of a push is any
// DuckDB the OPERATOR owns, including one with no extensions installed and
// no network to fetch them. A schema that cannot be created on the
// operator's own machine is not a schema. DuckDB's json_* functions read a
// VARCHAR perfectly well.
const eventsSchema = `
CREATE TABLE IF NOT EXISTS corral_events (
  ts               TIMESTAMPTZ NOT NULL,
  repo             VARCHAR     NOT NULL,
  run_url          VARCHAR,
  scan_id          BIGINT,
  path             VARCHAR,
  seq              BIGINT,
  kind             VARCHAR,
  actor            VARCHAR,
  subject          VARCHAR,
  model            VARCHAR,
  duration_ms      BIGINT,
  detail           VARCHAR,
  statement_sha256 VARCHAR,
  schema_version   INTEGER
);`

// corralAuditsMigrationCols is the additive set of columns this package has
// ever needed on corral_audits beyond the original shape (ts … run_url), in
// the order they must be added.
//
// It exists because `CREATE TABLE IF NOT EXISTS` is a NO-OP on a warehouse an
// earlier corral already created: that table keeps its old column set forever,
// and an INSERT naming a column it does not have fails the whole push — a
// working `--push` breaking on upgrade, against the one table this tool asks
// operators to trust as a durable record. Both this list and the fresh
// CREATE TABLE above carry the columns, so a brand-new warehouse never runs
// an ALTER; this path is only for a table that predates them.
//
// DuckDB has no `ADD COLUMN IF NOT EXISTS`, and silently swallowing every
// ALTER error would make a genuinely broken migration indistinguishable from
// an already-applied one — so the existing columns are probed first and any
// other failure is surfaced. Same rule scanstore's scanFilesMigrationCols
// follows, for the same reason.
//
// NEVER drop a column and never change a CHECK in place: this table is an
// append-only record an operator may have years of, and DuckDB cannot alter
// a CHECK anyway.
var corralAuditsMigrationCols = []struct{ name, ddl string }{
	{"test_selection", "test_selection VARCHAR"},
	{"selected_tests", "selected_tests INTEGER"},
	{"suite_tests", "suite_tests INTEGER"},
	{"selection_fallback", "selection_fallback VARCHAR"},
	{"uncovered", "uncovered BOOLEAN"},
	{"per_mutant", "per_mutant BOOLEAN"},
	{"tests_per_mutant_min", "tests_per_mutant_min INTEGER"},
	{"tests_per_mutant_median", "tests_per_mutant_median INTEGER"},
	{"tests_per_mutant_max", "tests_per_mutant_max INTEGER"},
	{"trees", "trees INTEGER"},
	{"concurrency_note", "concurrency_note VARCHAR"},
	{"shared_dirs", "shared_dirs VARCHAR"},
	{"scan_id", "scan_id BIGINT"},
	{"disposition", "disposition VARCHAR"},
	{"reason", "reason VARCHAR"},
	{"preflight_state", "preflight_state VARCHAR"},
	{"evidence", "evidence VARCHAR"},
	{"detail", "detail VARCHAR"},
	{"status", "status VARCHAR"},
	{"cache_hit", "cache_hit BOOLEAN"},
	{"reused_from_scan_id", "reused_from_scan_id BIGINT"},
	{"cache_key", "cache_key VARCHAR"},
	{"parent_sha256", "parent_sha256 VARCHAR"},
	{"mutants_graded", "mutants_graded INTEGER"},
	{"mutants_invalid", "mutants_invalid INTEGER"},
	{"mutants_timed_out", "mutants_timed_out INTEGER"},
	{"regions_total", "regions_total INTEGER"},
	{"regions_probed", "regions_probed INTEGER"},
	{"dropped_regions", "dropped_regions VARCHAR"},
	{"vacuous_findings", "vacuous_findings INTEGER"},
	{"authored_test_not_collected", "authored_test_not_collected BOOLEAN"},
	{"baseline_failed", "baseline_failed BOOLEAN"},
	{"suite_baseline_ms", "suite_baseline_ms BIGINT"},
	{"proven_mutant_ids", "proven_mutant_ids VARCHAR"},
	{"challenger_jaccard", "challenger_jaccard DOUBLE"},
	{"challenger_kappa", "challenger_kappa DOUBLE"},
	{"challenger_sufficient", "challenger_sufficient BOOLEAN"},
	{"goals_derived", "goals_derived INTEGER"},
	{"selection_ms", "selection_ms BIGINT"},
	{"generation_ms", "generation_ms BIGINT"},
	{"pool_ms", "pool_ms BIGINT"},
	{"dev_pass_ms", "dev_pass_ms BIGINT"},
	{"authored_pass_ms", "authored_pass_ms BIGINT"},
	{"critic_ms", "critic_ms BIGINT"},
	{"total_ms", "total_ms BIGINT"},
	{"mutant_ms_median", "mutant_ms_median BIGINT"},
	{"mutant_ms_max", "mutant_ms_max BIGINT"},
	{"authored_test", "authored_test VARCHAR"},
	{"verdict_json", "verdict_json VARCHAR"},
	{"schema_version", "schema_version INTEGER"},
}

// The other four tables are NEW at schema_version 2, so nothing predates
// their CREATE and there is nothing to add today. The lists are here — and
// wired into the same migration loop — so the next column added to any of
// them goes through the additive path rather than into a CREATE TABLE an
// existing warehouse will never re-run.
var (
	// corral_scans grew selection_ms when the scan grain took ownership of
	// the one instrumented coverage run (it had been carried only per file,
	// where summing it over a scan counted one run once per file).
	corralScansMigrationCols = []struct{ name, ddl string }{
		{"selection_ms", "selection_ms BIGINT"},
	}
	corralMutantsMigrationCols    = []struct{ name, ddl string }{}
	corralModelCallsMigrationCols = []struct{ name, ddl string }{}
	corralEventsMigrationCols     = []struct{ name, ddl string }{}
)

// migrateTable additively brings one warehouse table up to the current
// column set. Idempotent: a table that already has every column runs zero
// ALTERs.
//
// The columns are probed through duckdb_columns() rather than
// information_schema.columns because the target is an ATTACHed catalog
// (`warehouse`), and information_schema is scoped to the current one — it
// would report the attached table as having no columns at all, and every
// ALTER would then run and fail on a table that was already current.
func migrateTable(db *sql.DB, table string, cols []struct{ name, ddl string }) error {
	if len(cols) == 0 {
		return nil
	}
	rows, err := db.Query(`SELECT column_name FROM duckdb_columns()
	    WHERE database_name = 'warehouse' AND table_name = ?`, table)
	if err != nil {
		return fmt.Errorf("auditpush: probe existing %s columns: %w", table, err)
	}
	existing := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return fmt.Errorf("auditpush: scan existing %s column: %w", table, err)
		}
		existing[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("auditpush: probe existing %s columns: %w", table, err)
	}
	rows.Close()

	for _, col := range cols {
		if existing[col.name] {
			continue
		}
		if _, err := db.Exec("ALTER TABLE " + table + " ADD COLUMN " + col.ddl); err != nil {
			return fmt.Errorf("auditpush: migrate %s: add column %s: %w", table, col.name, err)
		}
	}
	return nil
}

// lockRetryWindow is how long a push keeps trying when another writer holds
// the file. DuckDB grants ONE writer at a time, and the design's whole
// distribution story is twenty runners finishing at once and pushing to the
// same database, so "the lock was held" must be a wait, not a failure. 30s
// is long enough to serialize a swarm of twenty (each push is a handful of
// small INSERTs) and short enough that a genuinely stuck lock still surfaces
// inside a CI step's patience.
const lockRetryWindow = 30 * time.Second

// PushBundle appends one scan, at all five grains, to target — in ONE
// transaction, so the five tables land together or not at all. A push that
// half-lands is worse than one that fails: the next query sees a scan row
// with no mutants and reports it as a scan that produced none.
//
// target is a DuckDB path or `md:<db>`. For MotherDuck the caller must have
// set motherduck_token in the environment — the same contract fleet sync
// uses, and the reason this takes no credential of its own: corral never
// holds one.
//
// The handle is opened and closed inside this call, on purpose. DuckDB
// allows a single writer on a file, so holding a handle across a scan would
// lock every other writer (and every reader) out for the scan's whole
// duration; opening only for the write, and retrying when the lock is held,
// is what lets a swarm of runners share one database.
func PushBundle(target string, b Bundle) (Counts, error) {
	if strings.TrimSpace(target) == "" {
		return Counts{}, fmt.Errorf("auditpush: no target")
	}
	b = stampLink(b)
	if err := requireStatements(b); err != nil {
		return Counts{}, err
	}
	if b.Scan == (ScanRow{}) && len(b.Files) == 0 && len(b.Mutants) == 0 &&
		len(b.Calls) == 0 && len(b.Events) == 0 {
		return Counts{}, nil
	}

	// Serialize this PROCESS's pushes to this file before going near DuckDB.
	// Measured against this driver: twenty goroutines each attaching the same
	// path from their own in-memory database all SUCCEED and nineteen of the
	// twenty pushes are then silently lost — DuckDB's file lock is advisory
	// and per-process, so it never fires against a sibling in the same
	// binary. The retry loop below cannot help with that, because there is no
	// error to retry. Only this mutex prevents it, and losing a push silently
	// is the one failure mode a durable record must not have.
	unlock := lockTarget(target)
	defer unlock()

	deadline := time.Now().Add(lockRetryWindow)
	backoff := 25 * time.Millisecond
	for {
		c, err := pushBundleOnce(target, b)
		if err == nil || !isLockHeld(err) || !time.Now().Before(deadline) {
			return c, err
		}
		time.Sleep(backoff)
		if backoff < time.Second {
			backoff *= 2
		}
	}
}

// stampLink writes the bundle's Link onto every row of every table, so a row
// and the statement it names can never disagree about which run produced
// them. ScanID is stamped unconditionally (0 is the honest value when
// --record was not given); the statement hash only when there IS one, so the
// legacy Push path — whose callers set the field on the rows themselves —
// keeps working unchanged.
func stampLink(b Bundle) Bundle {
	sha := strings.TrimSpace(b.Link.StatementSHA256)
	if sha == "" {
		return b
	}
	files := make([]Row, len(b.Files))
	copy(files, b.Files)
	for i := range files {
		files[i].StatementSHA256 = sha
	}
	mutants := make([]MutantRow, len(b.Mutants))
	copy(mutants, b.Mutants)
	for i := range mutants {
		mutants[i].StatementSHA256 = sha
	}
	calls := make([]ModelCallRow, len(b.Calls))
	copy(calls, b.Calls)
	for i := range calls {
		calls[i].StatementSHA256 = sha
	}
	events := make([]EventRow, len(b.Events))
	copy(events, b.Events)
	for i := range events {
		events[i].StatementSHA256 = sha
	}
	b.Files, b.Mutants, b.Calls, b.Events = files, mutants, calls, events
	b.Scan.StatementSHA256 = sha
	return b
}

// requireStatements enforces Link.Require: refuse the whole push, naming the
// offending row, rather than writing a row that looks traceable when it is
// not.
func requireStatements(b Bundle) error {
	if !b.Link.Require {
		return nil
	}
	for _, r := range b.Files {
		if strings.TrimSpace(r.StatementSHA256) == "" {
			return fmt.Errorf("auditpush: row %s has no statement_sha256, but a signed statement is required", r.Path)
		}
	}
	return nil
}

// pushLocks serializes same-process pushes per target. Keyed by the absolute
// path (or the `md:` DSN as given) so two spellings of one file cannot each
// take their own lock.
var pushLocks sync.Map

func lockTarget(target string) func() {
	key := target
	if !strings.HasPrefix(target, "md:") {
		if abs, err := filepath.Abs(target); err == nil {
			key = abs
			// And through any symlink, because the mutex is the ONLY thing
			// standing between two same-process writers and a silently lost
			// push: two names for one file that hash to two keys would take
			// two locks and lose one of the writes. EvalSymlinks fails on a
			// warehouse that does not exist yet, which is fine — the
			// absolute path is already a correct key for that case.
			if real, err := filepath.EvalSymlinks(abs); err == nil {
				key = real
			}
		}
	}
	v, _ := pushLocks.LoadOrStore(key, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// isLockHeld reports whether err is DuckDB refusing because another writer
// holds the database file. Matched on the message because the driver
// surfaces it as a plain error with no code; the two phrasings below are
// what DuckDB emits for a conflicting file lock.
func isLockHeld(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "could not set lock") ||
		strings.Contains(msg, "conflicting lock") ||
		strings.Contains(msg, "database is locked")
}

func pushBundleOnce(target string, b Bundle) (Counts, error) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return Counts{}, err
	}
	// ONE connection, because `USE warehouse` (below) is per-connection and
	// database/sql would otherwise hand the next statement a fresh one still
	// pointed at the in-memory catalog.
	db.SetMaxOpenConns(1)
	defer db.Close()

	if strings.HasPrefix(target, "md:") {
		if _, err := db.Exec("INSTALL motherduck; LOAD motherduck;"); err != nil {
			return Counts{}, fmt.Errorf("auditpush: load motherduck extension: %w", err)
		}
	}
	if _, err := db.Exec(fmt.Sprintf("ATTACH '%s' AS warehouse", strings.ReplaceAll(target, "'", "''"))); err != nil {
		return Counts{}, fmt.Errorf("auditpush: attach %q: %w", target, err)
	}
	// From here every statement is unqualified and lands in the operator's
	// catalog. It also means the corral_seal view's stored body names
	// corral_audits without a catalog prefix, so the view still resolves
	// when the same file is later attached under a different alias.
	if _, err := db.Exec("USE warehouse"); err != nil {
		return Counts{}, fmt.Errorf("auditpush: use %q: %w", target, err)
	}

	for _, ddl := range []string{scansSchema, auditsSchema, mutantsSchema, modelCallsSchema, eventsSchema} {
		if _, err := db.Exec(ddl); err != nil {
			return Counts{}, fmt.Errorf("auditpush: create table: %w", err)
		}
	}
	// A warehouse an earlier corral created already exists, so the CREATEs
	// above did nothing and its column set is whatever that version wrote.
	// The INSERTs below name every current column, so without this an
	// upgrade turns a working push into a hard failure.
	for _, m := range []struct {
		table string
		cols  []struct{ name, ddl string }
	}{
		{"corral_scans", corralScansMigrationCols},
		{"corral_audits", corralAuditsMigrationCols},
		{"corral_mutants", corralMutantsMigrationCols},
		{"corral_model_calls", corralModelCallsMigrationCols},
		{"corral_events", corralEventsMigrationCols},
	} {
		if err := migrateTable(db, m.table, m.cols); err != nil {
			return Counts{}, err
		}
	}
	// The seal is created here, not by a separate command, so an operator's
	// warehouse always has it: the view IS the MotherDuck share, and a share
	// that has to be created by hand is one nobody creates.
	if _, err := db.Exec(SealViewDDL); err != nil {
		return Counts{}, fmt.Errorf("auditpush: create the corral_seal view: %w", err)
	}

	tx, err := db.Begin()
	if err != nil {
		return Counts{}, fmt.Errorf("auditpush: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	now := time.Now().UTC()
	var c Counts

	if b.Scan != (ScanRow{}) {
		if _, err := tx.Exec(`INSERT INTO corral_scans (
		    ts, repo, run_url, scan_id, commit_sha, corral_version, substrate,
		    host, cores, trees_requested, diff_base, candidates, audited, passed,
		    total_ms, input_tokens, output_tokens, model_calls,
		    source_pushed, statement_sha256, selection_ms, schema_version
		  ) VALUES (`+placeholders(22)+`)`,
			now, b.Scan.Repo, b.Scan.RunURL, b.Scan.ScanID, b.Scan.Commit,
			b.Scan.CorralVersion, b.Scan.Substrate, b.Scan.Host, b.Scan.Cores,
			nullIfZeroInt(b.Scan.TreesRequested), b.Scan.DiffBase,
			b.Scan.Candidates, b.Scan.Audited, b.Scan.Passed,
			b.Scan.TotalMillis, b.Scan.InputTokens, b.Scan.OutputTokens, b.Scan.ModelCalls,
			b.Scan.SourcePushed, b.Scan.StatementSHA256, b.Scan.SelectionMillis, SchemaVersion,
		); err != nil {
			return Counts{}, fmt.Errorf("auditpush: insert scan row: %w", err)
		}
		c.Scans = 1
	}

	for _, r := range b.Files {
		if err := insertFileRow(tx, now, r, b.SourcePushed); err != nil {
			return Counts{}, err
		}
		c.Files++
	}

	for _, m := range b.Mutants {
		// Code IS the audited source: withheld by the WRITER unless the run
		// opted in, so a caller that forgets to blank the field cannot leak
		// it.
		var code any
		if b.SourcePushed && m.Code != "" {
			code = m.Code
		}
		if _, err := tx.Exec(`INSERT INTO corral_mutants (
		    ts, repo, run_url, scan_id, path, mutant_id, parent_sha256, outcome,
		    invalid_reason, proven, proven_by_authored_alone, tests_run,
		    selection_rule, duration_ms, killed_by, span_start, span_end, code,
		    statement_sha256, schema_version
		  ) VALUES (`+placeholders(20)+`)`,
			now, m.Repo, m.RunURL, m.ScanID, m.Path, m.MutantID,
			nullIfEmpty(m.ParentSHA256), m.Outcome, nullIfEmpty(m.InvalidReason),
			m.Proven, m.ProvenByAuthoredAlone, m.TestsRun,
			nullIfEmpty(m.SelectionRule), m.DurationMillis, nullIfEmpty(m.KilledBy),
			nullIfZeroInt(m.SpanStart), nullIfZeroInt(m.SpanEnd), code, m.StatementSHA256, SchemaVersion,
		); err != nil {
			return Counts{}, fmt.Errorf("auditpush: insert mutant %s/%s: %w", m.Path, m.MutantID, err)
		}
		c.Mutants++
	}

	for _, mc := range b.Calls {
		if _, err := tx.Exec(`INSERT INTO corral_model_calls (
		    ts, repo, run_url, scan_id, path, role, model, calls, retries,
		    input_tokens, output_tokens, wall_ms, statement_sha256, schema_version
		  ) VALUES (`+placeholders(14)+`)`,
			now, mc.Repo, mc.RunURL, mc.ScanID, mc.Path, mc.Role, mc.Model,
			mc.Calls, mc.Retries, mc.InputTokens, mc.OutputTokens, mc.WallMillis,
			mc.StatementSHA256, SchemaVersion,
		); err != nil {
			return Counts{}, fmt.Errorf("auditpush: insert model call %s/%s: %w", mc.Path, mc.Role, err)
		}
		c.Calls++
	}

	for _, e := range b.Events {
		if _, err := tx.Exec(`INSERT INTO corral_events (
		    ts, repo, run_url, scan_id, path, seq, kind, actor, subject, model,
		    duration_ms, detail, statement_sha256, schema_version
		  ) VALUES (`+placeholders(14)+`)`,
			now, e.Repo, e.RunURL, e.ScanID, e.Path, e.Seq, e.Kind, e.Actor,
			e.Subject, e.Model, e.DurationMillis, nullIfEmpty(e.Detail),
			e.StatementSHA256, SchemaVersion,
		); err != nil {
			return Counts{}, fmt.Errorf("auditpush: insert event %s#%d: %w", e.Path, e.Seq, err)
		}
		c.Events++
	}

	if err := tx.Commit(); err != nil {
		return Counts{}, fmt.Errorf("auditpush: commit: %w", err)
	}
	return c, nil
}

// insertFileRow writes one corral_audits row. Kept as its own function only
// because seventy columns in the middle of the transaction loop hid the four
// rules that actually matter — the NULL-not-zero conversions below.
func insertFileRow(tx *sql.Tx, now time.Time, r Row, sourcePushed bool) error {
	var minKill any
	if r.MinKillRate != nil {
		minKill = *r.MinKillRate
	}
	var maxGaps any
	if r.MaxProvenMissed != nil {
		maxGaps = *r.MaxProvenMissed
	}
	// NULL, never 0.0, for a file nothing graded — a nil *float64 binds SQL
	// NULL, which is the only honest value for a rate that was never
	// measured. Belt and braces on Uncovered: the caller sets the rate nil,
	// and an uncovered row cannot carry one even if it did not.
	var killRate any
	if r.KillRate != nil && !r.Uncovered {
		killRate = *r.KillRate
	}
	// NULL, never 0, for a spread that was never measured — a per-mutant run
	// can end with no graded mutant at all, and a stored 0-to-0 range would
	// read as "every mutant ran no tests" instead of "no mutant was graded".
	var pmMin, pmMedian, pmMax any
	if s := r.TestsPerMutant; r.PerMutant && s != nil {
		pmMin, pmMedian, pmMax = s.Min, s.Median, s.Max
	}
	// The source columns: withheld by the writer, not by the caller.
	var authoredTest, verdictJSON any
	if sourcePushed {
		authoredTest = nullIfEmpty(r.AuthoredTest)
		verdictJSON = nullIfEmpty(r.VerdictJSON)
	}
	_, err := tx.Exec(`INSERT INTO corral_audits (
	    ts, repo, commit_sha, path, lang,
	    kill_rate, survivors, proven_missed,
	    timed_out, test_writer_failed, pool_test_unsound,
	    audited, candidates, mutants_planted, models_by_role,
	    min_kill_rate, max_proven_missed, passed, statement_sha256, run_url,
	    test_selection, selected_tests, suite_tests, selection_fallback, uncovered,
	    per_mutant, tests_per_mutant_min, tests_per_mutant_median, tests_per_mutant_max,
	    trees, concurrency_note, shared_dirs, scan_id,
	    disposition, reason, preflight_state, evidence, detail, status,
	    cache_hit, reused_from_scan_id, cache_key, parent_sha256,
	    mutants_graded, mutants_invalid, mutants_timed_out,
	    regions_total, regions_probed, dropped_regions, vacuous_findings,
	    authored_test_not_collected, baseline_failed, suite_baseline_ms,
	    proven_mutant_ids, challenger_jaccard, challenger_kappa,
	    challenger_sufficient, goals_derived,
	    selection_ms, generation_ms, pool_ms, dev_pass_ms, authored_pass_ms,
	    critic_ms, total_ms, mutant_ms_median, mutant_ms_max,
	    authored_test, verdict_json, schema_version
	  ) VALUES (`+placeholders(70)+`)`,
		now, r.Repo, r.Commit, r.Path, r.Lang,
		killRate, r.Survivors, r.ProvenMissed,
		r.TimedOut, r.TestWriterFailed, r.PoolTestUnsound,
		r.Audited, r.Candidates, r.MutantsPlanted, r.ModelsByRole,
		minKill, maxGaps, r.Passed, r.StatementSHA256, r.RunURL,
		r.TestSelection, r.SelectedTests, r.SuiteTests, r.SelectionFallback, r.Uncovered,
		r.PerMutant, pmMin, pmMedian, pmMax,
		// trees is SQL NULL, not 0, when nothing measured it: the jail
		// substrate builds no trees and a cached pre-concurrency verdict
		// carries none. A 0 in an INTEGER column is a value a cross-repo
		// query will average and rank on.
		nullIfZeroInt(r.Trees), nullIfEmpty(r.ConcurrencyNote), nullIfEmpty(r.SharedDirs), r.ScanID,
		r.Disposition, nullIfEmpty(r.Reason), r.PreflightState, r.Evidence,
		nullIfEmpty(r.Detail), nullIfEmpty(r.Status),
		r.CacheHit, r.ReusedFromScanID, nullIfEmpty(r.CacheKey), nullIfEmpty(r.ParentSHA256),
		r.MutantsGraded, r.MutantsInvalid, r.MutantsTimedOut,
		r.RegionsTotal, r.RegionsProbed, nullIfEmpty(r.DroppedRegions), r.VacuousFindings,
		r.AuthoredTestNotCollected, r.BaselineFailed, r.SuiteBaselineMillis,
		nullIfEmpty(r.ProvenMutantIDs), r.ChallengerJaccard, r.ChallengerKappa,
		r.ChallengerSufficient, r.GoalsDerived,
		r.SelectionMillis, r.GenerationMillis, r.PoolMillis, r.DevPassMillis,
		r.AuthoredPassMillis, r.CriticMillis, r.TotalMillis,
		r.MutantMillisMedian, r.MutantMillisMax,
		authoredTest, verdictJSON, SchemaVersion,
	)
	if err != nil {
		return fmt.Errorf("auditpush: insert %s: %w", r.Path, err)
	}
	return nil
}

// placeholders renders n comma-separated `?`. Written out rather than
// counted by hand because the audits INSERT names seventy columns, and a
// miscount there is a silent column-order mismatch that files kill rates
// under the wrong heading.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// nullIfEmpty binds SQL NULL for an empty string. "" and "this row does not
// say" are different answers everywhere in this schema — an empty
// concurrency_note would be indistinguishable from "the substrate had
// nothing to say", an empty parent_sha256 from a hash nobody computed.
func nullIfEmpty(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// nullIfZeroInt is nullIfEmpty for a count that is only ever positive when
// something measured it (trees, trees_requested). A 0 in an INTEGER column
// is a value a cross-repo query averages and ranks on; NULL is the only
// encoding of "this warehouse does not say".
func nullIfZeroInt(v int) any {
	if v < 1 {
		return nil
	}
	return v
}
