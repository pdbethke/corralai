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
	"time"

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
	// ModelCallsForScan backs the money half of `--timing`'s readout — the
	// per-role cost line printed beside each file's timing line. Part of the
	// interface for the same reason MutantsForScan is: a reader that cannot
	// answer it fails to compile, not quietly prints nothing.
	ModelCallsForScan(ctx context.Context, scanID int64) ([]scanstore.ModelCall, error)
	// ScanByID backs the facts that live at the SCAN grain rather than the
	// file grain — selection_ms above all, the one instrumented coverage run
	// a whole scan shares. ok is false for an unknown id, which is an answer,
	// not an error.
	ScanByID(ctx context.Context, scanID int64) (scanstore.ScanRow, bool, error)
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
// splitSharedDirs turns the ledger's comma-joined shared_dirs back into the
// list concurrencyDisclosure renders. NULL/"" is "nothing shared" and stays
// nil, so the disclosure gains no suffix rather than an empty one.
func splitSharedDirs(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return strings.Split(v, ",")
}

func runScans(args []string, open func(dsn string) (scansReader, error), stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: corral scans list [--db <path>] [--limit n] [--json]")
		fmt.Fprintln(stderr, "       corral scans show <scan-id> [--db <path>] [--json] [--evidence] [--timing]")
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
		fmt.Fprintln(stderr, "usage: corral scans show <scan-id> [--db <path>] [--json] [--evidence] [--timing]")
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
	timing := fs.Bool("timing", false, "also print where each audited file's wall clock went, phase by phase — with --json, adds top-level selection_ms, selection_reused_from and model_calls and wraps the file array in an object ({\"files\": [...], \"selection_ms\": ..., \"selection_reused_from\": ..., \"model_calls\": [...]}) instead of emitting it bare")
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
		if !*timing {
			// UNCHANGED shape: the bare file array every existing consumer of
			// `scans show --json` already parses. --timing is what adds the
			// scan-grain selection_ms and the model_calls rows below — asking
			// for neither must not change what this prints.
			enc := json.NewEncoder(stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(files)
			return 0
		}
		return runScansShowJSONWithTiming(st, id, files, stdout, stderr)
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
	mutants, merr := st.MutantsForScan(context.Background(), id)
	if merr != nil {
		fmt.Fprintln(stderr, "corral scans show: per-mutant rows unavailable:", merr)
	}
	spreads := mutantSpreads(mutants)

	tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "PATH\tDISPOSITION\tREASON\tKILL RATE\tSURVIVORS\tPROVEN\tSELECTION\tCONCURRENCY\tEVIDENCE\tNOTE\t")
	for _, f := range files {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%s\t%s\t%s\t%s\t\n",
			f.Path, f.Disposition, f.Reason, formatKillRate(f.KillRate),
			f.Survivors, f.ProvenMissed, scanFileSelectionWith(f, spreads[f.Path]),
			concurrencyDisclosure(f.Trees, f.ConcurrencyNote, splitSharedDirs(f.SharedDirs)),
			f.Evidence, scanFileNote(f))
	}
	tw.Flush()

	// THE --transparency RECEIPT, unconditionally — not gated behind --json
	// or --timing, since it is a scan-grain fact a reader should not have to
	// ask for specially. Silent when the scan was never uploaded to Rekor
	// (--transparency was not given, or the upload failed open): an em dash
	// here would announce a receipt that does not exist.
	if row, ok, serr := st.ScanByID(context.Background(), id); serr != nil {
		fmt.Fprintln(stderr, "corral scans show: scan header unavailable:", serr)
	} else if ok && row.RekorLogIndex != nil {
		fmt.Fprintf(stdout, "\nrekor: index %d (uuid %s)\n", *row.RekorLogIndex, row.RekorUUID)
	}

	// WHICH TEST WAS AWAKE, per killed mutant, when the runner said so.
	// Silent otherwise — see killedByListing.
	if lines := killedByListing(mutants); len(lines) > 0 {
		fmt.Fprintln(stdout, "\nkilled by:")
		kw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
		for _, l := range lines {
			fmt.Fprintln(kw, l)
		}
		kw.Flush()
	}

	// WHERE THE MINUTES WENT, one line per file, through the SAME helper the
	// report prints live — so a stored scan and the run that produced it read
	// identically. Opt-in: the table above is already wide, and every row
	// recorded before the clock existed has nothing to say here.
	if *timing {
		// The scan grain FIRST, and only what actually happened at it: the
		// instrumented coverage run is ONE run shared by every file below, so
		// it is announced once here. Every file's own line names it too (a
		// readout must be able to account for each phase of that file's
		// audit), and the per-file totals deliberately exclude it — which is
		// what makes summing them sound.
		//
		// A reader that cannot answer, or a scan that instrumented nothing,
		// prints nothing: an em dash here would announce a missing
		// measurement where there was no measurement to make.
		if row, ok, serr := st.ScanByID(context.Background(), id); serr != nil {
			fmt.Fprintln(stderr, "corral scans show: scan header unavailable:", serr)
		} else if ok && row.SelectionMillis != nil {
			sel := time.Duration(*row.SelectionMillis) * time.Millisecond
			fmt.Fprintf(stdout, "\nselection %s (once per scan)\n", durationText(sel))
		} else if ok && row.SelectionReusedFrom != nil {
			// This scan ran no selection pass of its own (SelectionMillis is
			// nil above), and this is the one column that tells "reused"
			// apart from "never ran" — see scanstore.Scan.SelectionReusedFrom.
			fmt.Fprintf(stdout, "\nselection: reused — tree unchanged since scan %d\n", *row.SelectionReusedFrom)
		}
		// The money half of the same readout, by the same per-file grouping:
		// best-effort, like the spread above — a ledger written before
		// scan_model_calls existed still prints every file's timing, with
		// this line simply absent.
		calls, cerr := st.ModelCallsForScan(context.Background(), id)
		if cerr != nil {
			fmt.Fprintln(stderr, "corral scans show: model calls unavailable:", cerr)
		}
		callsByPath := modelCallsByPath(calls)
		for _, f := range files {
			t, med, max := timingOf(f)
			if !t.Measured() {
				// Nothing was timed. Seven em dashes would look like a
				// measurement; silence is the honest rendering.
				continue
			}
			fmt.Fprintf(stdout, "\n%s\n%s\n", f.Path, timingLine(t, f.MutantsGraded, med, max))
			if line := costLine(callsByPath[f.Path]); line != "" {
				fmt.Fprintln(stdout, line)
			}
		}
	}

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

// scansShowJSON is the `--json --timing` shape of `corral scans show`: the
// same file array `--json` alone prints, plus the two things `--timing`
// prints to the terminal that the bare array had nowhere to carry —
// scan-grain selection_ms and the per-file, per-role model-call rows.
//
// It exists because a docs fixture had to hand-transcribe cost numbers out of
// the text `--timing` readout: everything that readout prints was already
// MEASURED and already reachable from this reader, it just never reached
// `--json`. Wrapping is opt-in to --timing precisely so the bare-array shape
// `--json` alone has always printed stays byte-identical — an existing
// consumer that never asked for timing sees no change at all.
type scansShowJSON struct {
	Files       []scanstore.File `json:"files"`
	SelectionMS *int64           `json:"selection_ms"`
	// SelectionReusedFrom is the id of the scan whose selection evidence
	// THIS scan reused — null when this scan ran its own selection pass
	// (SelectionMS is set instead) or ran none at all. See
	// scanstore.Scan.SelectionReusedFrom's own doc for why this is the only
	// column that tells those two cases apart; the text `--timing` readout
	// already prints it (see the "selection: reused" line above), and this
	// field is the same fact reaching --json.
	SelectionReusedFrom *int64               `json:"selection_reused_from"`
	ModelCalls          []scansShowModelCall `json:"model_calls"`
}

// scansShowModelCall is one scan_model_calls row, snake_case and with the
// same nullable-vs-zero discipline the rest of this ledger keeps: Retries and
// CachedInputTokens are NULL when the ledger never measured them (see
// scanstore.ModelCall.Retries and the doc below), never a stored 0 that a
// later query would average as a measured zero.
type scansShowModelCall struct {
	Path         string `json:"path"`
	Role         string `json:"role"`
	Model        string `json:"model"`
	Calls        int    `json:"calls"`
	Retries      *int   `json:"retries"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	// CachedInputTokens is how many of InputTokens the provider served from
	// its own prompt cache — null when this seat's calls reported nothing,
	// which is most of them (Ollama and any provider silent about caching).
	// Present, rather than omitted, so a consumer can tell "not measured"
	// from "field does not exist on this build" the same way the ledger's own
	// nullable columns do.
	CachedInputTokens *int64 `json:"cached_input_tokens"`
	// CacheWriteInputTokens is what filling that cache cost (Anthropic bills
	// a write at 1.25x an input token) — null wherever nothing reported one,
	// which is every provider but Anthropic.
	CacheWriteInputTokens *int64 `json:"cache_write_input_tokens"`
	WallMillis            int64  `json:"wall_ms"`
}

// runScansShowJSONWithTiming is the --json branch of `--timing`: the ledger
// reads that back the text readout above already makes (ScanByID for the
// scan-grain selection_ms, ModelCallsForScan for the cost rows), rendered as
// JSON instead of text. Best-effort like the text readout: a reader that
// cannot answer ModelCallsForScan still prints the file array and
// selection_ms, with model_calls simply empty.
func runScansShowJSONWithTiming(st scansReader, id int64, files []scanstore.File, stdout, stderr io.Writer) int {
	out := scansShowJSON{Files: files, ModelCalls: []scansShowModelCall{}}

	if row, ok, serr := st.ScanByID(context.Background(), id); serr != nil {
		fmt.Fprintln(stderr, "corral scans show: scan header unavailable:", serr)
	} else if ok {
		out.SelectionMS = row.SelectionMillis
		out.SelectionReusedFrom = row.SelectionReusedFrom
	}

	calls, cerr := st.ModelCallsForScan(context.Background(), id)
	if cerr != nil {
		fmt.Fprintln(stderr, "corral scans show: model calls unavailable:", cerr)
	}
	for _, c := range calls {
		out.ModelCalls = append(out.ModelCalls, scansShowModelCall{
			Path: c.Path, Role: c.Role, Model: c.Model, Calls: c.Calls,
			Retries: c.Retries, InputTokens: c.InputTokens, OutputTokens: c.OutputTokens,
			CachedInputTokens: c.CachedInputTokens, CacheWriteInputTokens: c.CacheWriteInputTokens,
			WallMillis: c.WallMillis,
		})
	}

	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
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
func mutantSpreads(ms []scanstore.Mutant) map[string]mutantSpread {
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
	return out
}

// killedByListing is the per-mutant answer to "which test was awake": one
// line per killed mutant whose grading run actually named its killer.
//
// Printed ONLY when at least one row has an id. A killed_by is best-effort —
// NULL for a language whose runner corral does not parse, for a
// timeout-kill, and for any output whose summary said nothing — and a block
// of em dashes would announce a missing measurement where no measurement was
// ever attempted. Rows are emitted in the ledger's own order so two readings
// of one scan agree.
func killedByListing(ms []scanstore.Mutant) []string {
	var lines []string
	for _, m := range ms {
		if m.Outcome != "killed" || m.KilledBy == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("  %s\t%s\t%s", m.Path, m.MutantID, m.KilledBy))
	}
	return lines
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
