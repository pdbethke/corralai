// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"math"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// defaultScanDSN resolves the DuckDB scan ledger path exactly the way
// localBugCatchDBPath (cmd/corral/certify_local_bugcatch.go) resolves its
// own store: the CORRALAI_SCANS_DB env var, else os.UserHomeDir() falling
// back to os/user.Current(), else ~/.claude/corralai_scans.duckdb. Kept as
// the same resolution order deliberately — an operator who already knows
// that pattern from the bugcatch store should not have to learn a second
// one for this store.
func defaultScanDSN() string {
	if p := strings.TrimSpace(os.Getenv("CORRALAI_SCANS_DB")); p != "" {
		return p
	}
	home := ""
	if u, err := os.UserHomeDir(); err == nil {
		home = u
	} else if usr, err := user.Current(); err == nil {
		home = usr.HomeDir
	}
	return filepath.Join(home, ".claude", "corralai_scans.duckdb")
}

// killRatePtr converts a possibly-NaN kill rate into the *float64 form
// scanstore.Scan.KillRate expects, nil-ing out NaN explicitly at the
// source. reposcan.RepoReport.KillRate is math.NaN() when nothing was
// audited (see internal/reposcan/report.go) — a deliberate choice so a
// stored 0.0 never misrepresents "no measurement was made" as "terrible
// tests". scanstore.sanitizeKillRate already nils a NaN defensively at the
// store boundary too; this call does the same conversion at the source, so
// the intent is visible here rather than relying solely on that backstop.
func killRatePtr(v float64) *float64 {
	if math.IsNaN(v) {
		return nil
	}
	return &v
}

// buildScanFileRows turns one repo report into the rows scanstore.Record
// wants — one per file the scan judged, audited or rejected. It reads from
// TWO sources, deliberately, not one:
//
//   - results (reposcan.Scan's own return value, still in scope at the
//     call site) — one FileResult per JOB actually run. A gradable result
//     becomes an "audited" row (KillRate + Survivors, evidence "proven").
//     An UNgradable result is still a row: prep-failed, baseline-failed,
//     flaky-baseline, suite-ignores-file, executor-error and cancelled are
//     the scan's MOST EXPENSIVE rejections — corral selected most of these
//     files, emitted jobs for them, prepped a jail, and ran the check
//     command at least once — and before this fix they were tallied only
//     in rep.Ungradable (a map[reason]int with no per-file paths) and had
//     NO row here at all: a file the scan visibly worked on was invisible
//     to "why did file X get skipped on scan N", the exact question this
//     store exists to answer (see internal/scanstore/store.go's package
//     doc). Evidence for these rows is NOT a blanket "proven" — see
//     ungradableEvidence below, which is where "proven" earns its keep as
//     the label this table's defensibility rests on.
//   - rep.Excluded — every file that never became a job at all: the
//     enumerate-level reasons (no-language, is-test, no-paired-test,
//     ambiguous-test, not-regular-file) and the candidate-level ones added
//     before job emission (ungoaled, source-too-large, derive-failed,
//     not-selected). These are decided by filename pairing or bookkeeping,
//     never by running anything, so evidence defaults to "paired" — unless
//     the coverage pre-flight (a SEPARATE, independent inventory over the
//     same enumerated source set — see runPreflight) also measured this
//     path, in which case evidence is promoted to "coverage": a row that
//     carries a preflight_state IS carrying coverage-derived evidence, and
//     Task 1's own TestRecordRoundTripsEveryDisposition models exactly
//     that shape (reason "not-selected", preflight_state "not-executed",
//     evidence "coverage") — leaving it "paired" here would silently
//     contradict that fixture and make `WHERE evidence='coverage'` return
//     nothing for a row that plainly has coverage evidence attached.
//
// A path cannot legitimately appear in both sources (results only holds
// paths that became jobs; rep.Excluded only holds paths that never did),
// but a `seen` set skips a second row for any path already written rather
// than assume that invariant holds forever.
//
// preflightState overlays preflight_state onto every row, but ONLY when
// the coverage pre-flight actually ran — see preflightState's own doc
// comment for why a path absent from the map must stay empty rather than
// being recorded as "not-executed".
func buildScanFileRows(results []reposcan.FileResult, excluded []reposcan.Exclusion, preflight reposcan.CoverageMap) []scanstore.File {
	rows := make([]scanstore.File, 0, len(results)+len(excluded))
	seen := make(map[string]bool, len(results)+len(excluded))

	for _, r := range results {
		path := r.Job.Path
		if seen[path] {
			continue
		}
		seen[path] = true

		if r.Gradable {
			kr := r.Verdict.DevKillRate
			rows = append(rows, scanstore.File{
				Path: path, Lang: r.Job.Lang, Disposition: "audited",
				KillRate: &kr, Survivors: r.Verdict.Survivors, Gradable: true,
				Evidence: "proven", PreflightState: preflightState(preflight, path),
				// TimedOut rides straight through from the verdict: a claim
				// carries how it was earned, and a query over this ledger
				// must be able to tell a banked, unconverged score apart
				// from a clean audited row.
				TimedOut: r.Verdict.TimedOut,
				// TestWriterFailed rides through the same way: without this
				// a row with survivors > 0 and a clean-looking kill rate
				// would read as "no real gaps" when it actually means "gaps
				// found, no killing test could be authored to prove them."
				TestWriterFailed: r.Verdict.TestWriterFailed,
				// ProvenMissed rides through too — corral's strongest claim
				// (a survivor its own authored test killed BY EXECUTION),
				// now stored per-row so a later query can filter on
				// `proven_missed > 0` instead of losing the distinction the
				// moment this row is written.
				ProvenMissed: r.Verdict.ProvenMissed,
			})
			continue
		}

		// Mirrors Aggregate's own fallback (internal/reposcan/report.go) so
		// this row's reason always matches what rep.Ungradable[reason]
		// counted for the same file — an empty Reason must not become an
		// empty (and therefore useless) reason column.
		reason := r.Reason
		if reason == "" {
			reason = reposcan.ReasonExecutorError
		}
		rows = append(rows, scanstore.File{
			Path: path, Lang: r.Job.Lang, Disposition: "rejected", Reason: reason,
			Gradable: false, Evidence: ungradableEvidence(reason), PreflightState: preflightState(preflight, path),
			Detail: r.Detail,
		})
	}

	for _, e := range excluded {
		if seen[e.Path] {
			continue
		}
		seen[e.Path] = true

		pfState := preflightState(preflight, e.Path)
		rows = append(rows, scanstore.File{
			Path: e.Path, Lang: detectLang(e.Path), Disposition: "rejected", Reason: e.Reason,
			Gradable: false, Evidence: exclusionEvidence(pfState), PreflightState: pfState,
		})
	}
	return rows
}

// ungradableEvidence decides the evidence label for a job that ran (or
// tried to) and came back ungradable — see buildScanFileRows. "proven" is
// the single label this whole table's defensibility rests on: a later
// grading query treats it as "corral actually executed something against
// this file", and that claim must be TRUE, not merely "corral meant to".
//
// Two of the six ungradable reasons never reach that bar:
//   - reposcan.ReasonCancelled is written by reposcan.Scan itself, BEFORE
//     ex.Execute is ever called (see internal/reposcan/scan.go) — the
//     check command never ran, full stop. This function's own earlier
//     comment used to concede this and stamp "proven" anyway; that was
//     the overclaim, not a defensible choice.
//   - reposcan.ReasonPrepFailed is returned at the very top of
//     localExecutor.Execute (cmd/corral/certify_repo.go), when the
//     language-wide jail seed fails to build — BEFORE l.newBaseline is
//     ever reached, so the check command never ran for this file either.
//
// Both get "" (no evidence claim), the same value an excluded file that
// was never even a candidate gets — deliberately not a fifth enum value:
// "no evidence" and "" already mean the same thing everywhere else in
// this table (see exclusionEvidence), and reusing it means one fewer
// distinct string a query has to know about, not one more.
//
// The other four reasons (baseline-failed, flaky-baseline,
// suite-ignores-file, executor-error) DO clear the bar: baseline-failed
// and flaky-baseline are decided FROM the check command's own baseline
// runs (it ran — possibly more than once — and either failed
// consistently or disagreed with itself); suite-ignores-file is decided
// from a completed audit run (the canary survived); executor-error covers
// every other failure inside Execute, all of which are reached only AFTER
// at least one real baseline run has already happened (CheckBaselineStable
// erroring mid-run, or the audit step itself failing once baseline passed
// — see Execute's own ordering). All four keep "proven".
func ungradableEvidence(reason string) string {
	switch reason {
	case reposcan.ReasonCancelled, reposcan.ReasonPrepFailed:
		return ""
	default:
		return "proven"
	}
}

// exclusionEvidence decides the evidence label for a file that was
// excluded WITHOUT ever running (see buildScanFileRows). A non-empty
// preflightState means the coverage pre-flight's own instrumented run
// measured this exact path, which outranks a bare filename-pairing guess.
func exclusionEvidence(preflightState string) string {
	if preflightState != "" {
		return "coverage"
	}
	return "paired"
}

// detectLang resolves a language name for a file the scan never ran (so
// there is no Job.Lang to read) using the same lang.Detect the enumerator
// itself uses. Empty when no plugin recognizes the path (e.g. README.md) —
// never guessed.
func detectLang(path string) string {
	if plug, ok := lang.Detect(path); ok {
		return plug.Name()
	}
	return ""
}

// preflightState answers, for one path, what the coverage pre-flight
// learned about it — "executed", "not-executed", or "" (empty). Empty
// covers TWO cases that must not be conflated with a claim: the pre-flight
// never ran at all (preflight.Ran is false — --preflight was not given, or
// it declined/failed), and the pre-flight DID run but this path is absent
// from its Executed map, meaning its own instrumentation never measured
// this path at all (e.g. outside coverage.py's own source scope, or a
// different language than the one instrumented). Recording either case as
// a state would store a claim corral did not make.
func preflightState(cm reposcan.CoverageMap, path string) string {
	if !cm.Ran {
		return ""
	}
	executed, measured := cm.Executed[path]
	if !measured {
		return ""
	}
	if executed {
		return "executed"
	}
	return "not-executed"
}

// recordCertifyRepoScan opens the ledger at dsn, writes scan and files in
// one transaction, and closes it again. Every error case (an unopenable
// DSN, a failed write) is returned unchanged to the caller, which is
// responsible for the fail-open handling — this function does not print
// anything itself, so it stays testable as a pure function of its inputs.
func recordCertifyRepoScan(dsn string, scan scanstore.Scan, files []scanstore.File) error {
	st, err := scanstore.Open(dsn)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	if _, err := st.Record(context.Background(), scan, files); err != nil {
		return err
	}
	return nil
}
