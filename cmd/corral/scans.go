// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/pdbethke/corralai/internal/scanstore"
)

// scansReader is the read-only surface `corral scans` needs, kept as an
// interface so the command is unit-testable without a real DuckDB file.
type scansReader interface {
	Scans(ctx context.Context, limit int) ([]scanstore.ScanRow, error)
	FilesForScan(ctx context.Context, scanID int64) ([]scanstore.File, error)
	// MutantsForScan backs the per-mutant half of the SELECTION column: once
	// each mutant is graded by the tests that reach its own lines, the
	// file-grain counts are a union and the spread lives only at the mutant
	// grain. Part of the interface rather than an optional type assertion so
	// a reader that cannot answer it fails to compile instead of quietly
	// printing the narrower claim.
	MutantsForScan(ctx context.Context, scanID int64) ([]scanstore.Mutant, error)
	Close() error
}

// defaultScansDSN resolves the ledger path the same way `certify --repo
// --record-db` documents its own default, so the reader and the writer can
// never disagree about which file is "the ledger".
func defaultScansDSN() string {
	if dsn := strings.TrimSpace(os.Getenv("CORRALAI_SCANS_DB")); dsn != "" {
		return dsn
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "corralai_scans.duckdb"
	}
	return filepath.Join(home, ".claude", "corralai_scans.duckdb")
}

// runScans implements `corral scans list|show` — the read side of the scan
// ledger `certify --repo --record` writes.
//
// It exists because that ledger was write-only in practice: every scan and
// every per-file disposition was recorded, and nothing shipped could read them
// back. Inspecting one meant installing a duckdb CLI (absent from the
// production host) or writing a Go program. The gap became acute the day the
// ledger started carrying the authored test and the ids it proved — evidence
// specifically kept so a "tried and missed" would not require paying for
// another audit to interrogate. Evidence nobody can query is not evidence.
//
// Read-only by design, like `corral matrix list`: a scan ledger is a record of
// what happened, and nothing a human adjudicates after the fact (contrast
// `corral criticscore confirm/refute`, which records a human verdict).
func runScans(args []string, open func(dsn string) (scansReader, error), stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: corral scans list [--db <path>] [--limit n] [--json]")
		fmt.Fprintln(stderr, "       corral scans show <scan-id> [--db <path>] [--json] [--evidence]")
		return 2
	}

	switch args[0] {
	case "list":
		return runScansList(args[1:], open, stdout, stderr)
	case "show":
		return runScansShow(args[1:], open, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "corral scans: unknown subcommand %q — want list or show\n", args[0])
		return 2
	}
}

func runScansList(args []string, open func(string) (scansReader, error), stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("scans list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dsn := fs.String("db", "", "path to the scan ledger (default: $CORRALAI_SCANS_DB, else ~/.claude/corralai_scans.duckdb)")
	limit := fs.Int("limit", 20, "how many scans to show, newest first")
	asJSON := fs.Bool("json", false, "emit the raw rows as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	st, err := open(scansDSNOr(*dsn))
	if err != nil {
		fmt.Fprintln(stderr, "corral scans list:", err)
		return 1
	}
	defer st.Close()

	rows, err := st.Scans(context.Background(), *limit)
	if err != nil {
		fmt.Fprintln(stderr, "corral scans list:", err)
		return 1
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rows)
		return 0
	}
	if len(rows) == 0 {
		fmt.Fprintln(stdout, "no scans recorded yet — run `corral certify --repo <dir> --record` (note: --record is a BOOL that turns recording ON; --record-db only says where)")
		return 0
	}

	tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
	// REUSED sits next to AUDITED deliberately: those two numbers only mean
	// something together. cache_hits was always 0 before the verdict cache
	// shipped, so leaving it unprinted was harmless — now a scan can be 24 of
	// 25 files reused from three weeks ago, and a reader shown only a kill
	// rate would take it for a fresh measurement of today's code.
	fmt.Fprintln(tw, "ID\tWHEN\tREPO\tCOMMIT\tSUBSTRATE\tAUDITED\tREUSED\tCANDIDATES\tKILL RATE\t")
	for _, r := range rows {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%d\t%d\t%d\t%s\t\n",
			r.ID, r.TS.Format("2006-01-02 15:04"), r.Repo, shortCommit(r.Commit),
			r.Substrate, r.Audited, r.CacheHits, r.Candidates, formatKillRate(r.KillRate))
	}
	tw.Flush()
	return 0
}

func runScansShow(args []string, open func(string) (scansReader, error), stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: corral scans show <scan-id> [--db <path>] [--json] [--evidence]")
		return 2
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		fmt.Fprintf(stderr, "corral scans show: %q is not a scan id (see `corral scans list`)\n", args[0])
		return 2
	}
	fs := flag.NewFlagSet("scans show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dsn := fs.String("db", "", "path to the scan ledger (default: $CORRALAI_SCANS_DB, else ~/.claude/corralai_scans.duckdb)")
	asJSON := fs.Bool("json", false, "emit the raw rows as JSON")
	evidence := fs.Bool("evidence", false, "also print the pool's authored test source for each audited file")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	st, oerr := open(scansDSNOr(*dsn))
	if oerr != nil {
		fmt.Fprintln(stderr, "corral scans show:", oerr)
		return 1
	}
	defer st.Close()

	files, ferr := st.FilesForScan(context.Background(), id)
	if ferr != nil {
		fmt.Fprintln(stderr, "corral scans show:", ferr)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(files)
		return 0
	}
	if len(files) == 0 {
		fmt.Fprintf(stdout, "scan %d has no recorded files (unknown id? see `corral scans list`)\n", id)
		return 0
	}

	// The per-mutant spread is derived from the mutant rows rather than
	// stored again at the file grain: scan_mutants already records what each
	// mutant was graded by, and a second copy of the same fact is a second
	// thing that can disagree. Best-effort — a ledger too old to answer
	// still prints every file row, with the column saying only what it can.
	spreads, merr := mutantSpreads(context.Background(), st, id)
	if merr != nil {
		fmt.Fprintln(stderr, "corral scans show: per-mutant spread unavailable:", merr)
	}

	tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "PATH\tDISPOSITION\tREASON\tKILL RATE\tSURVIVORS\tPROVEN\tSELECTION\tCONCURRENCY\tEVIDENCE\tNOTE\t")
	for _, f := range files {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%s\t%s\t%s\t%s\t\n",
			f.Path, f.Disposition, f.Reason, formatKillRate(f.KillRate),
			f.Survivors, f.ProvenMissed, scanFileSelectionWith(f, spreads[f.Path]),
			concurrencyDisclosure(f.Trees, f.ConcurrencyNote), f.Evidence, scanFileNote(f))
	}
	tw.Flush()

	if *evidence {
		for _, f := range files {
			if strings.TrimSpace(f.AuthoredTest) == "" {
				continue
			}
			fmt.Fprintf(stdout, "\n--- %s: the pool's authored test ---\n", f.Path)
			fmt.Fprintln(stdout, authoredTestOutcome(f))
			fmt.Fprintln(stdout, f.AuthoredTest)
		}
	}
	return 0
}

// authoredTestOutcome states what actually became of the printed test. It
// consults the SAME diagnosis flags scanFileNote does, because inferring the
// outcome from an empty proven-id list alone gets it exactly backwards: a run
// that failed on unmutated code and scored NOTHING has an empty list too, and
// this used to report that as "graded soundly against N survivors and proved
// none of them" — the opposite of what happened, printed directly beneath a
// NOTE column already saying TEST UNSOUND. Caught on this command's first real
// use, against a gemini-3.6-flash run of pallets/flask whose authored test
// failed on clean code and was described as having soundly graded 17 survivors
// it never touched.
func authoredTestOutcome(f scanstore.File) string {
	switch {
	case f.TestWriterFailed:
		return "killed: nothing — this test did not compile, so it never ran against any survivor"
	case f.PoolTestUnsound:
		return fmt.Sprintf("killed: nothing — this test never genuinely graded (it failed on the unmutated code, or never read the file under audit), so the %d survivor(s) were never actually tested against it", f.Survivors)
	case f.TimedOut:
		return "killed: unknown — the run hit its deadline before the pool converged"
	case f.ProvenMutantIDs != "":
		return fmt.Sprintf("killed: %s", f.ProvenMutantIDs)
	default:
		return fmt.Sprintf("killed: nothing — this test graded soundly against %d survivor(s) and proved none of them", f.Survivors)
	}
}

// scanFileNote renders the honesty flags a bare proven_missed number cannot
// carry. A 0 in the PROVEN column means one of several very different things,
// and the whole point of storing these flags was that a later reader must not
// have to re-derive which — so the reader must actually say it.
func scanFileNote(f scanstore.File) string {
	note := baseScanFileNote(f)
	// Reuse is disclosed ALONGSIDE the diagnosis, never instead of it: this
	// row's numbers were not measured by the scan being read, they were served
	// from a prior scan's cache_key match, and a reader comparing two scans
	// must not mistake a repeat for a re-measurement. It leads the note
	// because it qualifies everything after it.
	if f.CacheHit {
		if note == "" {
			return "REUSED — verdict served from an earlier scan"
		}
		return "REUSED — verdict served from an earlier scan; " + note
	}
	return note
}

func baseScanFileNote(f scanstore.File) string {
	switch {
	case f.TimedOut:
		return "TIMED OUT — pool did not converge"
	case f.TestWriterFailed:
		return "WRITER FAILED — survivor(s) not proven-killed"
	case f.PoolTestUnsound:
		return "TEST UNSOUND — authored test did not genuinely grade"
	case f.Uncovered:
		// BEFORE the tried-and-missed clause, and never merged into it. An
		// uncovered file has survivors and, usually, proven_missed 0 for a
		// reason that has nothing to do with the writer: NO TEST EXECUTES
		// THE FILE. Reading that as "the authored test graded and proved
		// nothing" accuses the writer of failing at work the run never asked
		// it to do, and hides the actual finding. The proven count still
		// prints in its own column when the writer did prove something —
		// being uncovered does not erase that.
		return "uncovered — no test executes it; rate withheld"
	case f.Disposition == "audited" && f.Survivors > 0 && f.ProvenMissed == 0:
		return "tried and missed — authored test graded, proved nothing"
	default:
		return ""
	}
}

// mutantSpread is how many tests the file's mutants each actually ran, over
// the mutants whose grading recorded a count. ok is false when the ledger
// recorded no per-mutant grading for the file at all — distinct from a
// recorded spread of 0, and the reason the column cannot simply test the
// numbers for zero.
type mutantSpread struct {
	min, max int
	ok       bool
}

// mutantSpreads folds a scan's mutant rows into one spread per path.
func mutantSpreads(ctx context.Context, st scansReader, scanID int64) (map[string]mutantSpread, error) {
	ms, err := st.MutantsForScan(ctx, scanID)
	if err != nil {
		return nil, err
	}
	out := map[string]mutantSpread{}
	for _, m := range ms {
		// A mutant graded by the file's shared command records no rule, and
		// counting it as a 0 would invent a "0–41/mutant" spread nothing
		// measured.
		if m.SelectionRule == "" {
			continue
		}
		sp, seen := out[m.Path]
		if !seen || m.TestsRun < sp.min {
			sp.min = m.TestsRun
		}
		if !seen || m.TestsRun > sp.max {
			sp.max = m.TestsRun
		}
		sp.ok = true
		out[m.Path] = sp
	}
	return out, nil
}

// scanFileSelectionWith renders WHICH MEASUREMENT a row's kill rate is, in one
// column. A ledger reader comparing two rows is comparing two answers, and
// they are only comparable if they answered the same question: "0.65 over 14
// of 1431 tests" and "0.65 over the whole suite" are different claims. "—"
// means the ledger does not say — a row written before these columns existed,
// or a rejected file that was never graded at all — never a positive
// whole-suite claim, which such a row cannot make.
//
// It carries the per-mutant spread when the scan recorded one:
// "coverage-lines 234/620" alone reads as though every mutant faced 234
// tests; ", 3–41/mutant" is what says otherwise. A zero mutantSpread is the
// ordinary shared-command run, and prints exactly the column that always was.
func scanFileSelectionWith(f scanstore.File, sp mutantSpread) string {
	switch {
	case f.Uncovered:
		return "UNCOVERED"
	case f.TestSelection != "" && sp.ok:
		return fmt.Sprintf("%s %d/%d, %d–%d/mutant", f.TestSelection, f.SelectedTests, f.SuiteTests, sp.min, sp.max)
	case f.TestSelection != "":
		return fmt.Sprintf("%s %d/%d", f.TestSelection, f.SelectedTests, f.SuiteTests)
	case f.SelectionFallback != "":
		return fmt.Sprintf("whole-suite (%s)", f.SelectionFallback)
	default:
		return "—"
	}
}

// formatKillRate renders a *float64 kill rate, keeping NULL distinct from
// 0.00. A never-measured scan printed as "0.00" would read as "your tests
// caught nothing" about something corral never graded — the same false
// accusation the *float64 column type exists to prevent.
func formatKillRate(k *float64) string {
	if k == nil {
		return "—"
	}
	return fmt.Sprintf("%.2f", *k)
}

func scansDSNOr(flagVal string) string {
	if strings.TrimSpace(flagVal) != "" {
		return flagVal
	}
	return defaultScansDSN()
}

// openScanStore is the production opener wired into main's dispatch.
func openScanStore(dsn string) (scansReader, error) {
	st, err := scanstore.Open(dsn)
	if err != nil {
		// DuckDB holds a single-writer lock on its file, so a scan running
		// concurrently in another process owns it. Say so plainly rather than
		// surfacing a bare driver error.
		return nil, fmt.Errorf("opening the scan ledger %s: %w (a `certify --repo --record` run in another process holds it open — DuckDB is single-writer)", dsn, err)
	}
	return st, nil
}
