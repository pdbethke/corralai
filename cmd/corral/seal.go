// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/reposcan"
)

// defaultSealTop bounds how many of a repo's highest-ranked (churn x size)
// files count as "hot" when `corral seal --repo` judges coverage. Kept
// SMALLER than certify --repo's own defaultScanTop (25): seal is a read of
// what has ALREADY been earned, not a bound on what a scan is about to pay
// for, and a tighter default keeps the coverage line meaningful for a repo
// that has only ever run a handful of scoped audits.
const defaultSealTop = 20

// sealRow is one row read back from corral_seal — the warehouse's latest,
// kill-rate-bearing row per (repo, path). Field names mirror the ledger's
// own (see internal/auditpush/bundle.go's auditsSchema) rather than
// reinventing a naming scheme a reader of the warehouse has to re-learn.
type sealRow struct {
	Repo         string  `json:"repo"`
	Path         string  `json:"path"`
	ParentSHA256 string  `json:"parent_sha256"`
	KillRate     float64 `json:"kill_rate"`
	Survivors    int     `json:"survivors"`
	ProvenMissed int     `json:"proven_missed"`
	Trees        int     `json:"trees"`
	// TS is when the row was PUSHED. AuditedAt is when the scan actually
	// ran, and is what a reader means by "when was this audited"; nil for
	// rows written before the column existed.
	TS        time.Time  `json:"ts"`
	AuditedAt *time.Time `json:"audited_at,omitempty"`
	// The honesty flags. They were in the view and dropped on the way to the
	// terminal, so "5 survivors / 0 proven" read as "tried and proved
	// nothing" for a file whose authored test never compiled.
	TestWriterFailed bool `json:"test_writer_failed"`
	PoolTestUnsound  bool `json:"pool_test_unsound"`
	BaselineFailed   bool `json:"baseline_failed"`
}

// caveat is the one-word reason a seal row's numbers must not be read at
// face value, or "" when they may.
func (r sealRow) caveat() string {
	switch {
	case r.BaselineFailed:
		return "BASELINE-FAILED"
	case r.TestWriterFailed:
		return "WRITER-FAILED"
	case r.PoolTestUnsound:
		return "TEST-UNSOUND"
	}
	return ""
}

// sealReader is the read-only surface `corral seal` needs, kept as an
// interface (mirroring scansReader) so the command is unit-testable without
// a real DuckDB warehouse.
type sealReader interface {
	// SealRows returns corral_seal's rows, newest write order irrelevant —
	// callers sort what they need. repo == "" means every repo the warehouse
	// has ever seen a verdict for; a non-empty repo filters to it.
	SealRows(ctx context.Context, repo string) ([]sealRow, error)
	// UncoveredPaths returns the paths, scoped to repo, whose truly-latest
	// corral_audits row (across EVERY disposition, not just the graded ones
	// corral_seal itself is filtered to — see SealViewDDL's `kill_rate IS
	// NOT NULL`) is an uncovered one: the selection evidence measured the
	// file and found no test that executes it
	// (reposcan.ReasonUncovered). "Truly latest" is what keeps this
	// honest — a file that was once uncovered and has since been paired
	// with a real test and graded must not be reported uncovered forever,
	// so the comparison is against the newest row for the path, period, not
	// the newest UNCOVERED one.
	UncoveredPaths(ctx context.Context, repo string) (map[string]bool, error)
	// ImportOnlyPaths is UncoveredPaths' refinement: the paths, scoped to
	// repo, whose truly-latest corral_audits row is import-only
	// (reposcan.ReasonImportOnly) — the file WAS executed, at
	// import/module-load time, just never by a test directly. Every
	// import-only path is ALSO an uncovered one (the `uncovered` column is
	// the union both shapes set — see auditpush.Row.ImportOnly's own doc),
	// so a caller reporting a path's state MUST check THIS map first:
	// falling through to UncoveredPaths alone reintroduces the exact false
	// "UNCOVERED — no test executes this file" claim
	// reposcan.ReasonImportOnly exists to correct. Same "truly latest"
	// contract as UncoveredPaths.
	ImportOnlyPaths(ctx context.Context, repo string) (map[string]bool, error)
	Close() error
}

// dbSealReader is the production sealReader: a single DuckDB connection
// ATTACHed to the operator's warehouse, opened read-only wherever DuckDB
// allows it. See openSealDB.
type dbSealReader struct{ db *sql.DB }

func (r dbSealReader) SealRows(ctx context.Context, repo string) ([]sealRow, error) {
	q := `SELECT repo, path, COALESCE(parent_sha256, ''), kill_rate,
	      COALESCE(survivors, 0), COALESCE(proven_missed, 0), COALESCE(trees, 0), ts, started_at,
	      COALESCE(test_writer_failed, false), COALESCE(pool_test_unsound, false), COALESCE(baseline_failed, false)
	      FROM corral_seal`
	args := []any{}
	if strings.TrimSpace(repo) != "" {
		q += " WHERE repo = ?"
		args = append(args, repo)
	}
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []sealRow
	for rows.Next() {
		var r sealRow
		var started sql.NullTime
		if err := rows.Scan(&r.Repo, &r.Path, &r.ParentSHA256, &r.KillRate, &r.Survivors, &r.ProvenMissed, &r.Trees, &r.TS, &started,
			&r.TestWriterFailed, &r.PoolTestUnsound, &r.BaselineFailed); err != nil {
			return nil, err
		}
		if started.Valid {
			t := started.Time
			r.AuditedAt = &t
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UncoveredPaths queries corral_audits DIRECTLY, not the corral_seal view:
// the view's `kill_rate IS NOT NULL` filter is exactly what would hide an
// uncovered row (KillRate is nil BY CONSTRUCTION for one — see
// auditpush.Row.Uncovered's doc), so an uncovered file would otherwise be
// indistinguishable from one this warehouse has never seen at all.
//
// A pre-fix row (pushed before Task 1's F4) may carry `reason` naming
// reposcan.ReasonUncovered without the `uncovered` column set — the OR
// tolerates that at no cost, rather than silently missing every row a
// warehouse recorded before the column existed.
//
// "Truly latest" needs a DETERMINISTIC tiebreaker, not `ORDER BY ts DESC`
// alone: every file row in one PushBundle call shares the exact same `ts`
// (auditpush.pushBundleOnce computes `now` once per bundle, see bundle.go),
// and two separate pushes can in principle land the same timestamp too —
// coarse clock resolution, or a test driving two PushBundle calls back to
// back. scan_id is NOT that tiebreaker: it is the local ledger's row id and
// reads 0 for every run pushed without --record (push.go's own doc: "scan_id
// is always the ledger's row id, or 0 when --record was not given"), so
// several genuinely different pushes can tie on it too. What IS monotonic
// is insertion order itself: corral_audits is APPEND ONLY — inserted, never
// UPDATEd or DELETEd (push.go's own rule) — so DuckDB's `rowid`
// pseudocolumn, which tracks physical insertion order, is safe to rely on
// here for exactly the reason internal/scanstore/store.go's FilesForScan
// already relies on it for scan_files (see that doc comment): an ORDER BY
// that assumed rowid survives updates/deletes would not be safe, but that
// case never arises for an append-only table. `ORDER BY ts DESC, rowid
// DESC` is therefore not incidental: for any tie on ts, the row physically
// inserted LAST — i.e. pushed most recently — wins, which is the same
// answer "truly latest" is supposed to give.
func (r dbSealReader) UncoveredPaths(ctx context.Context, repo string) (map[string]bool, error) {
	q := `SELECT path FROM (
	        SELECT path, COALESCE(uncovered, false) AS uncovered, COALESCE(reason, '') AS reason,
	               row_number() OVER (PARTITION BY path ORDER BY ts DESC, rowid DESC) AS rn
	        FROM corral_audits WHERE repo = ?
	      ) WHERE rn = 1 AND (uncovered OR reason = ?)`
	rows, err := r.db.QueryContext(ctx, q, repo, reposcan.ReasonUncovered)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out[p] = true
	}
	return out, rows.Err()
}

// ImportOnlyPaths is UncoveredPaths with import_only/ReasonImportOnly in
// place of uncovered/ReasonUncovered — same query shape, same "truly
// latest" tiebreak (see UncoveredPaths' own doc for why `ORDER BY ts DESC,
// rowid DESC` is load-bearing), same OR-tolerance for a row pushed before
// this column existed but whose `reason` already named
// reposcan.ReasonImportOnly.
func (r dbSealReader) ImportOnlyPaths(ctx context.Context, repo string) (map[string]bool, error) {
	q := `SELECT path FROM (
	        SELECT path, COALESCE(import_only, false) AS import_only, COALESCE(reason, '') AS reason,
	               row_number() OVER (PARTITION BY path ORDER BY ts DESC, rowid DESC) AS rn
	        FROM corral_audits WHERE repo = ?
	      ) WHERE rn = 1 AND (import_only OR reason = ?)`
	rows, err := r.db.QueryContext(ctx, q, repo, reposcan.ReasonImportOnly)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out[p] = true
	}
	return out, rows.Err()
}

func (r dbSealReader) Close() error { return r.db.Close() }

// openSealDB attaches dsn as the warehouse `corral_seal` lives in, exactly
// the way pushBundleOnce (internal/auditpush/bundle.go) attaches its own
// target — ONE connection, `USE warehouse` so every unqualified statement
// resolves there, and `md:` gets the motherduck extension loaded first.
//
// Read-only wherever DuckDB allows it. That is not a preference: `corral
// seal` is a READER, and a reader that holds a writable handle on the
// operator's warehouse can corrupt it on a crash and blocks a concurrent
// push for no benefit. This reader has no INSERT/UPDATE/DELETE anywhere in
// it.
//
// The one thing it does write is the corral_seal VIEW, and only when the
// warehouse does not have one — a file whose corral_audits came from an
// older corral, or from another writer entirely. A read-only attach cannot
// run DDL, so that path closes the read-only handle, re-opens writable JUST
// long enough for the CREATE VIEW, and then re-opens read-only to actually
// read. `md:` skips the read-only attempt outright (motherduck's own access
// control, not this reader's, is what would gate a write there), so its
// handle can run the DDL where it stands.
//
// A `--db` naming a path that does not exist is a TYPO and is refused. DuckDB
// answers a missing file by CREATING it, so without this check seal would
// invent an empty warehouse, report it as empty, and leave the file behind
// for the next reader to be confused by.
func openSealDB(dsn string) (sealReader, error) {
	isMD := strings.HasPrefix(dsn, "md:")
	if !isMD {
		if _, err := os.Stat(dsn); err != nil {
			return nil, fmt.Errorf("corral seal: no warehouse at %s — nothing has pushed to it yet (run `certify --repo --push %s` first), or --db names the wrong path; a reader does not create one", dsn, dsn)
		}
	}

	db, err := attachWarehouse(dsn, !isMD)
	if err != nil {
		if isMD {
			return nil, fmt.Errorf("corral seal: opening %s: %w", dsn, err)
		}
		// The READ-ONLY attach itself failed on a file that exists — a
		// warehouse another process holds the lock on, a file that is not a
		// DuckDB database, a permissions problem. Report what actually
		// happened; do NOT retry writable, which would turn "someone else is
		// pushing" into a second writer fighting for the same lock.
		return nil, fmt.Errorf("corral seal: opening %s read-only: %w", dsn, err)
	}

	// Confirm the view is actually there to read.
	if _, verr := db.Exec("SELECT 1 FROM corral_seal LIMIT 0"); verr != nil {
		if isMD {
			// Already writable: create it where we stand.
			if _, derr := db.Exec(auditpush.SealViewDDL); derr != nil {
				db.Close()
				return nil, fmt.Errorf("corral seal: %s has no corral_seal view and creating it failed: %w (the read said: %v)", dsn, derr, verr)
			}
			return dbSealReader{db: db}, nil
		}
		// Read-only cannot run DDL. Close, create the view through a
		// writable handle, and come back read-only to read.
		db.Close()
		w, werr := attachWarehouse(dsn, false)
		if werr != nil {
			return nil, fmt.Errorf("corral seal: %s has no corral_seal view and could not be opened writable to create one: %w (the read said: %v)", dsn, werr, verr)
		}
		if _, derr := w.Exec(auditpush.SealViewDDL); derr != nil {
			w.Close()
			return nil, fmt.Errorf("corral seal: creating the corral_seal view in %s: %w", dsn, derr)
		}
		w.Close()
		db, err = attachWarehouse(dsn, true)
		if err != nil {
			return nil, fmt.Errorf("corral seal: reopening %s read-only after creating the corral_seal view: %w", dsn, err)
		}
	}
	return dbSealReader{db: db}, nil
}

// attachWarehouse opens exactly one DuckDB connection and ATTACHes dsn as
// "warehouse", mirroring pushBundleOnce's own attach sequence so a seal
// reader and a seal writer never disagree about how a target resolves.
func attachWarehouse(dsn string, readOnly bool) (*sql.DB, error) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if strings.HasPrefix(dsn, "md:") {
		if _, err := db.Exec("INSTALL motherduck; LOAD motherduck;"); err != nil {
			db.Close()
			return nil, fmt.Errorf("load motherduck extension: %w", err)
		}
	}
	stmt := fmt.Sprintf("ATTACH '%s' AS warehouse", strings.ReplaceAll(dsn, "'", "''"))
	if readOnly {
		stmt = fmt.Sprintf("ATTACH '%s' AS warehouse (READ_ONLY)", strings.ReplaceAll(dsn, "'", "''"))
	}
	if _, err := db.Exec(stmt); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec("USE warehouse"); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// runSeal implements `corral seal` — the reader that says what the
// warehouse's accumulated verdicts mean for the repo's CURRENT state, not
// just what any one scan measured.
func runSeal(args []string, open func(dsn string) (sealReader, error), stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("seal", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dsn := fs.String("db", "", "warehouse to read (default: $CORRALAI_SCANS_DB, else ~/.claude/corralai_scans.duckdb — the same resolution `corral scans` uses, since a single-operator setup ordinarily pushes to the same local file it records to)")
	repoDir := fs.String("repo", "", "a checkout to judge validity against: each hot file's seal row is marked live (bytes unchanged since the audit), stale (changed since), never audited, unreadable, or unknown when the row recorded no validity key to compare against. Only live counts toward coverage. Without this flag, seal prints the raw ledger with no such judgement")
	top := fs.Int("top", defaultSealTop, "how many of the repo's highest-ranked (churn x size) files count as \"hot\" for the coverage line — same ranking `certify --repo` uses to bound a scan")
	asJSON := fs.Bool("json", false, "emit the rows as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	target := strings.TrimSpace(*dsn)
	if target == "" {
		target = defaultScanDSN()
	}

	st, err := open(target)
	if err != nil {
		fmt.Fprintln(stderr, "corral seal:", err)
		return 1
	}
	defer st.Close()

	if strings.TrimSpace(*repoDir) == "" {
		return runSealWithoutRepo(st, *asJSON, stdout, stderr)
	}
	return runSealWithRepo(st, *repoDir, *top, *asJSON, stdout, stderr)
}

// runSealWithoutRepo prints every repo's latest verdict per path, with no
// live/stale judgement — there is no checkout here to judge it against.
func runSealWithoutRepo(st sealReader, asJSON bool, stdout, stderr io.Writer) int {
	rows, err := st.SealRows(context.Background(), "")
	if err != nil {
		fmt.Fprintln(stderr, "corral seal:", err)
		return 1
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Repo != rows[j].Repo {
			return rows[i].Repo < rows[j].Repo
		}
		return rows[i].Path < rows[j].Path
	})

	if asJSON {
		return emitSealJSON(rows, stdout)
	}
	if len(rows) == 0 {
		fmt.Fprintln(stdout, "corral_seal is empty — no scan has pushed a graded file to this warehouse yet")
		return 0
	}
	fmt.Fprintln(stdout, "no --repo given — printing the warehouse's latest verdict per path, with NO live/stale judgement (pass --repo <dir> to compare against a checkout)")
	tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "REPO\tKILL\tSURVIVORS\tPROVEN\tTREES\tAUDITED\tCAVEAT\tPATH\t")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%.2f\t%d\t%d\t%d\t%s\t%s\t%s\t\n",
			r.Repo, r.KillRate, r.Survivors, r.ProvenMissed, r.Trees,
			r.auditedLabel(), r.caveat(), r.Path)
	}
	tw.Flush()
	return 0
}

// sealState is one hot file's judged validity, plus the seal row it was
// judged against (nil for "never audited" — there is nothing to show).
type sealState struct {
	Path  string
	State string
	Row   *sealRow
}

// runSealWithRepo judges each of repoDir's churn x size top-N ("hot")
// files against the warehouse's latest verdict for it: LIVE (the checkout's
// bytes still hash to what was audited), STALE (they do not — the file
// changed since), or NEVER AUDITED (no seal row at all). The frame is the
// hot set, not the seal rows: a file corral has never touched must still
// appear, which is the entire point of a coverage claim.
func runSealWithRepo(st sealReader, repoDir string, top int, asJSON bool, stdout, stderr io.Writer) int {
	repo, _, err := auditSubject(repoDir, reposcan.RepoReport{})
	if err != nil {
		fmt.Fprintln(stderr, "corral seal:", err)
		return 1
	}

	cands, _, err := reposcan.Enumerate(repoDir)
	if err != nil {
		fmt.Fprintln(stderr, "corral seal: enumerating", repoDir, ":", err)
		return 1
	}
	ranked, _ := reposcan.Rank(repoDir, cands)
	hot, _ := reposcan.Select(ranked, top)

	rows, err := st.SealRows(context.Background(), repo)
	if err != nil {
		fmt.Fprintln(stderr, "corral seal:", err)
		return 1
	}
	byPath := make(map[string]sealRow, len(rows))
	for _, r := range rows {
		byPath[r.Path] = r
	}

	uncov, err := st.UncoveredPaths(context.Background(), repo)
	if err != nil {
		fmt.Fprintln(stderr, "corral seal:", err)
		return 1
	}
	impOnly, err := st.ImportOnlyPaths(context.Background(), repo)
	if err != nil {
		fmt.Fprintln(stderr, "corral seal:", err)
		return 1
	}

	states := make([]sealState, 0, len(hot))
	live, uncovered, importOnly := 0, 0, 0
	for _, c := range hot {
		if uncov[c.Path] {
			// Checked BEFORE the "never audited" default and before the
			// live/stale comparison below: uncovered is its own state,
			// distinct from both — the evidence ran and PROVED no test
			// executes this file, which is a stronger and different claim
			// than "corral has not looked yet". It is also, by
			// UncoveredPaths' own contract, the file's truly-latest verdict,
			// so it takes priority over anything corral_seal (graded-only)
			// might still hold from an OLDER row.
			//
			// impOnly is checked FIRST, inside this same branch (every
			// import-only path is ALSO an uncovered one — see
			// ImportOnlyPaths' own doc): a path in both maps was executed
			// at import time, never by a test directly, and calling that
			// plain "uncovered" is the exact false claim
			// reposcan.ReasonImportOnly exists to correct. Counted
			// separately from the genuinely-uncovered tally below, the same
			// split the console report already makes, so the coverage
			// line's own "%d uncovered" never folds the two together.
			if impOnly[c.Path] {
				states = append(states, sealState{Path: c.Path, State: reposcan.ReasonImportOnly})
				importOnly++
				continue
			}
			states = append(states, sealState{Path: c.Path, State: "uncovered"})
			uncovered++
			continue
		}
		r, ok := byPath[c.Path]
		if !ok {
			states = append(states, sealState{Path: c.Path, State: "never audited"})
			continue
		}
		rc := r
		sum, herr := fileSHA256(filepath.Join(repoDir, c.Path))
		switch {
		case herr != nil:
			// A file corral cannot READ is not a file that CHANGED. Calling
			// it stale tells the operator to re-audit a file whose bytes may
			// be byte-identical to the ones that were graded — a diagnosis
			// nothing here made. It does not count as live either: the
			// validity key could not be computed, so the honest state is
			// that the reader does not know.
			states = append(states, sealState{Path: c.Path, State: "unreadable", Row: &rc})
		case strings.TrimSpace(r.ParentSHA256) == "":
			// The ROW has no validity key: parent_sha256 was NULL (the
			// reader COALESCEs it to ""), because the verdict predates the
			// column, or because the file's own mutants disagreed about what
			// was audited and buildScanFileRows recorded nothing rather than
			// pick one. There is no hash to compare the checkout against, so
			// the comparison below cannot run at all — and it must not be
			// allowed to, because "" never equals a real sha256 and the
			// default branch would announce that the file CHANGED. That is a
			// claim about bytes nothing here ever saw.
			//
			// Not live, for the same reason as unreadable: a verdict whose
			// validity cannot be checked is not a verdict this reader can
			// call current.
			states = append(states, sealState{Path: c.Path, State: "unknown (no validity key recorded)", Row: &rc})
		case sum == r.ParentSHA256:
			states = append(states, sealState{Path: c.Path, State: "live", Row: &rc})
			live++
		default:
			states = append(states, sealState{Path: c.Path, State: fmt.Sprintf("stale (file changed since %s)", rc.auditedLabel()), Row: &rc})
		}
	}

	if asJSON {
		return emitSealStateJSON(states, stdout)
	}

	if len(states) == 0 {
		fmt.Fprintln(stdout, "no candidate files found under", repoDir)
		return 0
	}

	tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "STATE\tKILL\tSURVIVORS\tPROVEN\tTREES\tAUDITED\tCAVEAT\tPATH\t")
	for _, s := range states {
		if s.Row == nil {
			fmt.Fprintf(tw, "%s\t—\t—\t—\t—\t—\t\t%s\t\n", s.State, s.Path)
			continue
		}
		fmt.Fprintf(tw, "%s\t%.2f\t%d\t%d\t%d\t%s\t%s\t%s\t\n",
			s.State, s.Row.KillRate, s.Row.Survivors, s.Row.ProvenMissed, s.Row.Trees,
			s.Row.auditedLabel(), s.Row.caveat(), s.Path)
	}
	tw.Flush()

	pct := 0.0
	if len(states) > 0 {
		pct = 100 * float64(live) / float64(len(states))
	}
	line := fmt.Sprintf("coverage: %d of %d hot files carry a live verdict (%s%%)",
		live, len(states), trimPercent(pct))
	if uncovered > 0 {
		// Named explicitly rather than left to read as an ordinary "not yet
		// audited" gap: these files were LOOKED AT — the selection evidence
		// ran and found no test that executes them at all, which is a
		// finding, not an omission.
		line += fmt.Sprintf("; %d uncovered — no test executes them", uncovered)
	}
	if importOnly > 0 {
		// Reported separately from uncovered, for the same reason the
		// console report keeps two counts: these files WERE executed, at
		// import time, just never by a test directly — folding them into
		// "uncovered" would be the false claim reposcan.ReasonImportOnly
		// exists to correct.
		line += fmt.Sprintf("; %d %s", importOnly, reposcan.ReasonImportOnly)
	}
	fmt.Fprintln(stdout, line)
	return 0
}

// trimPercent renders a coverage percentage without a trailing ".0" for a
// whole number, matching the brief's own example line ("...(60%)") rather
// than printing "60.0%" for the common case a whole-number ratio produces.
func trimPercent(pct float64) string {
	if math.Trunc(pct) == pct {
		return fmt.Sprintf("%d", int(pct))
	}
	return fmt.Sprintf("%.1f", pct)
}

// emitSealJSON writes the rows as JSON. A nil slice is normalized to an
// EMPTY one first: `[]` is "this warehouse has no graded rows", and `null` is
// a shape a pipeline has to special-case (or crashes on) for a state that is
// perfectly ordinary.
func emitSealJSON(rows []sealRow, stdout io.Writer) int {
	if rows == nil {
		rows = []sealRow{}
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(rows)
	return 0
}

// sealStateJSON is the --repo --json shape: snake_case, and the row's own
// fields flattened in rather than nested, so a reader does not have to know
// this command's internal sealState/sealRow split.
type sealStateJSON struct {
	State        string   `json:"state"`
	Path         string   `json:"path"`
	KillRate     *float64 `json:"kill_rate"`
	Survivors    *int     `json:"survivors"`
	ProvenMissed *int     `json:"proven_missed"`
	Trees        *int     `json:"trees"`
	Audited      *string  `json:"audited"`
}

func emitSealStateJSON(states []sealState, stdout io.Writer) int {
	out := make([]sealStateJSON, 0, len(states))
	for _, s := range states {
		j := sealStateJSON{State: s.State, Path: s.Path}
		if s.Row != nil {
			kr, sv, pm, tr := s.Row.KillRate, s.Row.Survivors, s.Row.ProvenMissed, s.Row.Trees
			ts := s.Row.TS.Format(time.RFC3339)
			j.KillRate, j.Survivors, j.ProvenMissed, j.Trees, j.Audited = &kr, &sv, &pm, &tr, &ts
		}
		out = append(out, j)
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
	return 0
}

// auditedLabel is the scan's own time when recorded, else the push time
// marked as such — a reader must be able to tell the two apart.
func (r sealRow) auditedLabel() string {
	if r.AuditedAt != nil {
		return r.AuditedAt.Format("2006-01-02 15:04")
	}
	return r.TS.Format("2006-01-02 15:04") + " (push)"
}
