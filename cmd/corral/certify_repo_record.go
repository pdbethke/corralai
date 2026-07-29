// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"math"
	"os"
	"os/user"
	"path/filepath"
	"strings"

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
// wants — one per file the scan judged, audited or rejected. Both slices
// are already complete and uncapped on rep (the printed report's top-10
// "weakest files" list and 20-line exclusion listing are presentation
// caps only; rep.Weakest and rep.Excluded themselves hold every file):
//
//   - rep.Weakest holds every GRADABLE file, disposition "audited",
//     evidence "proven" (execution, not a guess).
//   - rep.Excluded holds every rejected file, both the enumerate-level
//     reasons (no-language, is-test, no-paired-test, ambiguous-test,
//     not-regular-file) and the candidate-level ones added later
//     (ungoaled, source-too-large, derive-failed, not-selected),
//     disposition "rejected", evidence "paired" (a filename-pairing
//     decision, not a measurement).
//
// preflightState overlays preflight_state onto both, but ONLY when the
// coverage pre-flight actually ran — see preflightState's own doc comment
// for why a path absent from the map must stay empty rather than being
// recorded as "not-executed".
func buildScanFileRows(rep reposcan.RepoReport, preflight reposcan.CoverageMap) []scanstore.File {
	rows := make([]scanstore.File, 0, len(rep.Weakest)+len(rep.Excluded))
	for _, f := range rep.Weakest {
		kr := f.KillRate
		rows = append(rows, scanstore.File{
			Path:           f.Path,
			Disposition:    "audited",
			KillRate:       &kr,
			Survivors:      f.Survivors,
			Gradable:       true,
			Evidence:       "proven",
			PreflightState: preflightState(preflight, f.Path),
		})
	}
	for _, e := range rep.Excluded {
		rows = append(rows, scanstore.File{
			Path:           e.Path,
			Disposition:    "rejected",
			Reason:         e.Reason,
			Gradable:       false,
			Evidence:       "paired",
			PreflightState: preflightState(preflight, e.Path),
		})
	}
	return rows
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
