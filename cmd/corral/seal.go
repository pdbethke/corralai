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
	Repo         string    `json:"repo"`
	Path         string    `json:"path"`
	ParentSHA256 string    `json:"parent_sha256"`
	KillRate     float64   `json:"kill_rate"`
	Survivors    int       `json:"survivors"`
	ProvenMissed int       `json:"proven_missed"`
	Trees        int       `json:"trees"`
	TS           time.Time `json:"ts"`
}

// sealReader is the read-only surface `corral seal` needs, kept as an
// interface (mirroring scansReader) so the command is unit-testable without
// a real DuckDB warehouse.
type sealReader interface {
	// SealRows returns corral_seal's rows, newest write order irrelevant —
	// callers sort what they need. repo == "" means every repo the warehouse
	// has ever seen a verdict for; a non-empty repo filters to it.
	SealRows(ctx context.Context, repo string) ([]sealRow, error)
	Close() error
}

// dbSealReader is the production sealReader: a single DuckDB connection
// ATTACHed to the operator's warehouse, opened read-only wherever DuckDB
// allows it. See openSealDB.
type dbSealReader struct{ db *sql.DB }

func (r dbSealReader) SealRows(ctx context.Context, repo string) ([]sealRow, error) {
	q := `SELECT repo, path, COALESCE(parent_sha256, ''), kill_rate,
	      COALESCE(survivors, 0), COALESCE(proven_missed, 0), COALESCE(trees, 0), ts
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
		if err := rows.Scan(&r.Repo, &r.Path, &r.ParentSHA256, &r.KillRate, &r.Survivors, &r.ProvenMissed, &r.Trees, &r.TS); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (r dbSealReader) Close() error { return r.db.Close() }

// openSealDB attaches dsn as the warehouse `corral_seal` lives in, exactly
// the way pushBundleOnce (internal/auditpush/bundle.go) attaches its own
// target — ONE connection, `USE warehouse` so every unqualified statement
// resolves there, and `md:` gets the motherduck extension loaded first.
//
// Read-only wherever DuckDB allows it: a local file is ATTACHed
// `(READ_ONLY)` first, which is also why the view has to be created OUTSIDE
// that attempt when it is missing — a read-only attach cannot run DDL. `md:`
// databases skip the read-only attempt outright (motherduck's own access
// control, not this reader's, is what would gate a write there) and a local
// file that fails the read-only attach (most commonly: the view does not
// exist yet, or the file does not exist yet at all) falls back to a normal
// attach for exactly long enough to create the view, never to write a row —
// this reader has no INSERT/UPDATE/DELETE anywhere in it.
func openSealDB(dsn string) (sealReader, error) {
	isMD := strings.HasPrefix(dsn, "md:")
	db, err := attachWarehouse(dsn, !isMD)
	if err != nil && !isMD {
		// The read-only attempt failed — almost always because the view is
		// not there yet to read. Reopen writable, JUST to create it.
		db, err = attachWarehouse(dsn, false)
		if err != nil {
			return nil, fmt.Errorf("corral seal: opening %s: %w", dsn, err)
		}
		if _, verr := db.Exec(auditpush.SealViewDDL); verr != nil {
			db.Close()
			return nil, fmt.Errorf("corral seal: creating corral_seal view: %w", verr)
		}
		return dbSealReader{db: db}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("corral seal: opening %s: %w", dsn, err)
	}
	// Confirm the view is actually there to read: a read-only attach on a
	// warehouse with corral_audits but no corral_seal view (or nothing at
	// all yet) cannot create it, and the honest response is to say so.
	if _, verr := db.Exec("SELECT 1 FROM corral_seal LIMIT 0"); verr != nil {
		db.Close()
		return nil, fmt.Errorf("corral seal: %s has no corral_seal view and is not writable to create one (run `certify --repo --push` against it first, or open it directly with duckdb to create the view): %w", dsn, verr)
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
	repoDir := fs.String("repo", "", "a checkout to judge validity against: each hot file's seal row is marked live (bytes unchanged since the audit), stale (changed since), or never audited. Without this flag, seal prints the raw ledger with no such judgement")
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
	fmt.Fprintln(tw, "REPO\tKILL\tSURVIVORS\tPROVEN\tTREES\tTS\tPATH\t")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%.2f\t%d\t%d\t%d\t%s\t%s\t\n",
			r.Repo, r.KillRate, r.Survivors, r.ProvenMissed, r.Trees,
			r.TS.Format("2006-01-02 15:04"), r.Path)
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

	states := make([]sealState, 0, len(hot))
	live := 0
	for _, c := range hot {
		r, ok := byPath[c.Path]
		if !ok {
			states = append(states, sealState{Path: c.Path, State: "never audited"})
			continue
		}
		rc := r
		sum, herr := fileSHA256(filepath.Join(repoDir, c.Path))
		switch {
		case herr != nil:
			// The file the audit graded no longer exists (or cannot be
			// read) in this checkout — that IS staleness, just from the
			// other direction. Say so rather than reporting a hash error.
			states = append(states, sealState{Path: c.Path, State: fmt.Sprintf("stale (file changed since %s)", r.TS.Format("2006-01-02 15:04")), Row: &rc})
		case sum == r.ParentSHA256:
			states = append(states, sealState{Path: c.Path, State: "live", Row: &rc})
			live++
		default:
			states = append(states, sealState{Path: c.Path, State: fmt.Sprintf("stale (file changed since %s)", r.TS.Format("2006-01-02 15:04")), Row: &rc})
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
	fmt.Fprintln(tw, "STATE\tKILL\tSURVIVORS\tPROVEN\tTREES\tAUDITED\tPATH\t")
	for _, s := range states {
		if s.Row == nil {
			fmt.Fprintf(tw, "%s\t—\t—\t—\t—\t—\t%s\t\n", s.State, s.Path)
			continue
		}
		fmt.Fprintf(tw, "%s\t%.2f\t%d\t%d\t%d\t%s\t%s\t\n",
			s.State, s.Row.KillRate, s.Row.Survivors, s.Row.ProvenMissed, s.Row.Trees,
			s.Row.TS.Format("2006-01-02 15:04"), s.Path)
	}
	tw.Flush()

	pct := 0.0
	if len(states) > 0 {
		pct = 100 * float64(live) / float64(len(states))
	}
	fmt.Fprintf(stdout, "coverage: %d of %d hot files carry a live verdict (%s%%)\n",
		live, len(states), trimPercent(pct))
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

func emitSealJSON(rows []sealRow, stdout io.Writer) int {
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
