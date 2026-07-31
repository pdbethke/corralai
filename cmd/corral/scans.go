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
	fmt.Fprintln(tw, "ID\tWHEN\tREPO\tCOMMIT\tSUBSTRATE\tAUDITED\tCANDIDATES\tKILL RATE\t")
	for _, r := range rows {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%d\t%d\t%s\t\n",
			r.ID, r.TS.Format("2006-01-02 15:04"), r.Repo, shortCommit(r.Commit),
			r.Substrate, r.Audited, r.Candidates, formatKillRate(r.KillRate))
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

	tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "PATH\tDISPOSITION\tREASON\tKILL RATE\tSURVIVORS\tPROVEN\tEVIDENCE\tNOTE\t")
	for _, f := range files {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%s\t%s\t\n",
			f.Path, f.Disposition, f.Reason, formatKillRate(f.KillRate),
			f.Survivors, f.ProvenMissed, f.Evidence, scanFileNote(f))
	}
	tw.Flush()

	if *evidence {
		for _, f := range files {
			if strings.TrimSpace(f.AuthoredTest) == "" {
				continue
			}
			fmt.Fprintf(stdout, "\n--- %s: the pool's authored test ---\n", f.Path)
			if f.ProvenMutantIDs != "" {
				fmt.Fprintf(stdout, "killed: %s\n", f.ProvenMutantIDs)
			} else {
				fmt.Fprintf(stdout, "killed: nothing — this test graded soundly against %d survivor(s) and proved none of them\n", f.Survivors)
			}
			fmt.Fprintln(stdout, f.AuthoredTest)
		}
	}
	return 0
}

// scanFileNote renders the honesty flags a bare proven_missed number cannot
// carry. A 0 in the PROVEN column means one of several very different things,
// and the whole point of storing these flags was that a later reader must not
// have to re-derive which — so the reader must actually say it.
func scanFileNote(f scanstore.File) string {
	switch {
	case f.TimedOut:
		return "TIMED OUT — pool did not converge"
	case f.TestWriterFailed:
		return "WRITER FAILED — survivor(s) not proven-killed"
	case f.PoolTestUnsound:
		return "TEST UNSOUND — authored test did not genuinely grade"
	case f.Disposition == "audited" && f.Survivors > 0 && f.ProvenMissed == 0:
		return "tried and missed — authored test graded, proved nothing"
	default:
		return ""
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
