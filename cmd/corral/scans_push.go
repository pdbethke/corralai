// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// scansPushReader is the read-only surface `corral scans push` needs beyond
// scansReader: EventsForScan, so the bundle it builds carries the same tape
// `scans show --timing` would print, not four of the five grains.
//
// A separate interface rather than an addition to scansReader: every
// existing scansReader fixture (scans list/show's tests) would otherwise
// have to grow a method it never uses, for a command that does not read it.
type scansPushReader interface {
	scansReader
	// EventsForScan backs the fifth warehouse grain (corral_events) — the
	// tape buildBundle maps 1:1 into EventRow. Without it a pushed scan
	// would carry four of the five tables the local ledger recorded.
	EventsForScan(ctx context.Context, scanID int64) ([]scanstore.Event, error)
}

// pushAllLimit stands in for "no limit" when asking the ledger for every
// scan header: Scans(ctx, limit) treats <= 0 as its own default of 20 (the
// right default for a human skimming `scans list`, the wrong one for a verb
// whose whole point is "all of them"). DuckDB has no trouble with a LIMIT
// this large against a ledger that will never hold anywhere near it.
const pushAllLimit = 1 << 30

// openScanStoreForPush is the production opener for `corral scans push`:
// the same store openScanStore opens, returned as the wider interface this
// command needs.
func openScanStoreForPush(dsn string) (scansPushReader, error) {
	st, err := scanstore.Open(dsn)
	if err != nil {
		return nil, fmt.Errorf("opening the scan ledger %s: %w (a `certify --repo --record` run in another process holds it open — DuckDB is single-writer)", dsn, err)
	}
	return st, nil
}

// scansPusher is the warehouse-writing half of `scans push`, factored out so
// tests can swap it for a no-op and pin --dry-run's "touches nothing"
// contract without a fake DuckDB target.
type scansPusher func(target string, b auditpush.Bundle) (auditpush.Counts, error)

// runScansPush implements `corral scans push` — the verb missing from a
// ledger full of `--record`ed scans that were never pushed to a warehouse at
// the time. Before this, the only way to get a recorded scan into a
// warehouse was to re-run the whole audit with --push, which pays for the
// mutants and the model calls all over again to move rows that already
// exist on disk.
//
// It builds each selected scan's bundle with the SAME mapping `certify
// --repo --push` uses (buildBundle in certify_repo_bundle.go) and hands it
// to the SAME writer (auditpush.PushBundle) — there is deliberately no
// second bundle builder and no hand-written SQL against the warehouse here.
// PushBundle enforces BlankUnpushedSource internally, and this command never
// sets Bundle.SourcePushed: `scans push` has no --push-source flag, so
// source bytes (the authored test, the mutant code, the verdict blob) never
// reach the warehouse through this verb, regardless of what the ORIGINAL
// `certify --repo` run recorded.
//
// REPEAT-PUSH CONTRACT: the five warehouse tables are append-only (see
// ScanRow's doc in internal/auditpush/bundle.go and PushBundle's own doc) —
// pushing the same scan twice does not overwrite, it duplicates every row a
// second time. This command does not attempt to detect or dedupe that; it
// says so in its own --help text, and --dry-run reports the same row counts
// a real push would ADD, whether or not this is the first time that scan has
// been pushed.
func runScansPush(args []string, open func(string) (scansPushReader, error), push scansPusher, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("scans push", flag.ContinueOnError)
	fs.SetOutput(stderr)
	targetDSN := fs.String("db", "", "the warehouse to push to — a DuckDB path, or md:<database> for MotherDuck (required)")
	scanIDFlag := fs.String("scan", "", "push only this one scan id (see corral scans list)")
	all := fs.Bool("all", false, "push every recorded scan (optionally narrowed by --since)")
	since := fs.String("since", "", "with --all, push only scans recorded on or after this date (YYYY-MM-DD)")
	dryRun := fs.Bool("dry-run", false, "print exactly what would be pushed and touch nothing — neither the ledger nor the target warehouse")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: corral scans push --db <dsn> [--scan <id> | --all] [--since YYYY-MM-DD] [--dry-run]")
		fmt.Fprintln(stderr, "")
		fmt.Fprintln(stderr, "Reads from the SAME local ledger `corral scans list` reads (default:")
		fmt.Fprintln(stderr, "$CORRALAI_SCANS_DB, else ~/.claude/corralai_scans.duckdb) — set that env var to")
		fmt.Fprintln(stderr, "push from a non-default ledger.")
		fmt.Fprintln(stderr, "")
		fmt.Fprintln(stderr, "Pushes scans ALREADY in the local ledger (`certify --repo --record`) to a")
		fmt.Fprintln(stderr, "warehouse — the verb for someone who recorded for weeks before deciding they")
		fmt.Fprintln(stderr, "wanted a warehouse, so they do not have to re-run every audit to get there.")
		fmt.Fprintln(stderr, "")
		fmt.Fprintln(stderr, "The warehouse tables are APPEND-ONLY: pushing the same scan id twice adds its")
		fmt.Fprintln(stderr, "rows a second time rather than overwriting the first. --dry-run reports the")
		fmt.Fprintln(stderr, "rows a push would ADD, which is the same count whether or not this scan has")
		fmt.Fprintln(stderr, "been pushed before.")
		fmt.Fprintln(stderr, "")
		fmt.Fprintln(stderr, "Never carries source (the authored test, mutant code, the verdict blob) —")
		fmt.Fprintln(stderr, "this command has no --push-source flag, so BlankUnpushedSource withholds it")
		fmt.Fprintln(stderr, "unconditionally, even for a scan whose original run pushed source itself.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if strings.TrimSpace(*scanIDFlag) != "" && *all {
		fmt.Fprintln(stderr, "corral scans push: --scan and --all are mutually exclusive")
		return 2
	}
	if strings.TrimSpace(*scanIDFlag) == "" && !*all && strings.TrimSpace(*since) == "" {
		fmt.Fprintln(stderr, "corral scans push: nothing to push — give --scan <id> or --all")
		fs.Usage()
		return 2
	}
	if strings.TrimSpace(*targetDSN) == "" {
		fmt.Fprintln(stderr, "corral scans push: --db is required (the warehouse to push to)")
		return 2
	}

	var sinceTime time.Time
	if strings.TrimSpace(*since) != "" {
		t, terr := time.Parse("2006-01-02", strings.TrimSpace(*since))
		if terr != nil {
			fmt.Fprintf(stderr, "corral scans push: --since %q is not a date (want YYYY-MM-DD)\n", *since)
			return 2
		}
		sinceTime = t
	}

	st, err := open(scansDSNOr(""))
	if err != nil {
		fmt.Fprintln(stderr, "corral scans push:", err)
		return 1
	}
	defer st.Close()

	ctx := context.Background()

	var targets []scanstore.ScanRow
	if strings.TrimSpace(*scanIDFlag) != "" {
		id, perr := strconv.ParseInt(strings.TrimSpace(*scanIDFlag), 10, 64)
		if perr != nil {
			fmt.Fprintf(stderr, "corral scans push: %q is not a scan id (see `corral scans list`)\n", *scanIDFlag)
			return 2
		}
		row, ok, serr := st.ScanByID(ctx, id)
		if serr != nil {
			fmt.Fprintln(stderr, "corral scans push:", serr)
			return 1
		}
		if !ok {
			fmt.Fprintf(stderr, "corral scans push: no scan %d in this ledger (see `corral scans list`)\n", id)
			return 2
		}
		targets = []scanstore.ScanRow{row}
	} else {
		rows, serr := st.Scans(ctx, pushAllLimit)
		if serr != nil {
			fmt.Fprintln(stderr, "corral scans push:", serr)
			return 1
		}
		for _, r := range rows {
			if !sinceTime.IsZero() && r.TS.Before(sinceTime) {
				continue
			}
			targets = append(targets, r)
		}
		if len(targets) == 0 {
			if len(rows) == 0 {
				fmt.Fprintln(stderr, "corral scans push: no scans recorded yet — run `corral certify --repo <dir> --record` first")
			} else {
				fmt.Fprintf(stderr, "corral scans push: no scans recorded on or after %s\n", *since)
			}
			return 2
		}
		// Scans returns newest-first; push oldest-first so a scan id N's
		// row lands in the warehouse before N+1's, matching the order a
		// reader scanning the ledger top to bottom would expect.
		for i, j := 0, len(targets)-1; i < j; i, j = i+1, j-1 {
			targets[i], targets[j] = targets[j], targets[i]
		}
	}

	var total auditpush.Counts
	for _, row := range targets {
		files, ferr := st.FilesForScan(ctx, row.ID)
		if ferr != nil {
			fmt.Fprintf(stderr, "corral scans push: scan %d: %v\n", row.ID, ferr)
			return 1
		}
		mutants, merr := st.MutantsForScan(ctx, row.ID)
		if merr != nil {
			fmt.Fprintf(stderr, "corral scans push: scan %d: %v\n", row.ID, merr)
			return 1
		}
		calls, cerr := st.ModelCallsForScan(ctx, row.ID)
		if cerr != nil {
			fmt.Fprintf(stderr, "corral scans push: scan %d: %v\n", row.ID, cerr)
			return 1
		}
		events, eerr := st.EventsForScan(ctx, row.ID)
		if eerr != nil {
			fmt.Fprintf(stderr, "corral scans push: scan %d: %v\n", row.ID, eerr)
			return 1
		}

		rosterJSON := scanRosterJSON(files)
		bundle := buildBundle(row.Scan, row.ID, files, mutants, calls, events,
			auditpush.Link{ScanID: row.ID, StatementSHA256: row.StatementSHA256},
			// SourcePushed is always false here: `scans push` has no
			// --push-source flag, so source never leaves the box through
			// this verb even when the ORIGINAL run pushed it.
			false,
			row.Repo, row.Commit, "",
			bundleMeta{
				ModelsByRole: rosterJSON,
				// Neither threshold is recoverable from the ledger — a
				// scan header records what was MEASURED, not the
				// --min-kill-rate/--max-proven-missed the run was held to
				// (see certify_repo.go's own flags). Left nil rather than
				// guessed, which makes Passed true honestly: with no
				// threshold set, there is no breach to report.
				Passed: true,
			})

		fmt.Fprintf(stdout, "scan %d · %s · %d file(s), %d mutant(s) → %s\n",
			row.ID, row.Repo, len(files), len(mutants), *targetDSN)

		if *dryRun {
			total.Scans++
			total.Files += len(bundle.Files)
			total.Mutants += len(bundle.Mutants)
			total.Calls += len(bundle.Calls)
			total.Events += len(bundle.Events)
			continue
		}

		c, perr := push(*targetDSN, bundle)
		if perr != nil {
			fmt.Fprintf(stderr, "corral scans push: scan %d: pushing to %s: %v\n", row.ID, *targetDSN, perr)
			return 1
		}
		total.Scans += c.Scans
		total.Files += c.Files
		total.Mutants += c.Mutants
		total.Calls += c.Calls
		total.Events += c.Events
	}

	verb := "pushed"
	if *dryRun {
		verb = "would push"
	}
	fmt.Fprintf(stdout, "%s %d scan(s), %d file(s), %d mutant(s), %d model-call row(s), %d event(s) to %s\n",
		verb, total.Scans, total.Files, total.Mutants, total.Calls, total.Events, *targetDSN)
	return 0
}

// scanRosterJSON derives a scan's ModelsByRole for the warehouse header from
// its own recorded files, rather than from a live `models()` roster — there
// is no live run here, only a ledger. buildBundle applies ONE value to every
// row of the scan (the same shape the certify --repo caller uses), so this
// picks the first non-empty per-file value it finds rather than inventing a
// merge across files that, in practice, always agree.
func scanRosterJSON(files []scanstore.File) string {
	for _, f := range files {
		if strings.TrimSpace(f.ModelsByRole) != "" {
			return f.ModelsByRole
		}
	}
	return ""
}
