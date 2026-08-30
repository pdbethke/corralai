// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"fmt"
	"os"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// githubRunURL is the run this scan happened in, so a row in the warehouse
// leads back to the logs that produced it. Empty — never fabricated — when
// the scan did not run in GitHub Actions.
func githubRunURL() string {
	srv, repoEnv, id := os.Getenv("GITHUB_SERVER_URL"), os.Getenv("GITHUB_REPOSITORY"), os.Getenv("GITHUB_RUN_ID")
	if srv == "" || repoEnv == "" || id == "" {
		return ""
	}
	return fmt.Sprintf("%s/%s/actions/runs/%s", srv, repoEnv, id)
}

// This file is THE mapping from the local ledger to the warehouse, and there
// is deliberately only one.
//
// The warehouse is not a second, parallel record of the scan — it is the
// ledger, pushed. Every column corral_audits carries has a scan_files column
// behind it; every corral_mutants row is a scan_mutants row. Building the
// pushed rows from the report a second time (which is what the code did
// before this change) is how the two records start to disagree: the report
// path carried only the AUDITED files, so the warehouse had no row for the
// files corral refused — the exact files an operator asking "is this repo
// covered?" needs to see.
//
// So: the ledger rows are built once (buildScanFileRows /
// buildScanMutantRows), recorded, and then mapped here. If a warehouse
// column has no ledger column to come from, the fix is to add the ledger
// column, not to reach back into the report.

// bundleMeta is the scan-wide context every warehouse row carries,
// denormalized onto each row on purpose: a query that reads one row must be
// able to see how much of the repo was looked at, and which gates the run
// was held to, without a join.
type bundleMeta struct {
	Repo   string
	Commit string
	RunURL string
	// ModelsByRole is the roster as JSON, so a new role does not need a
	// migration.
	ModelsByRole    string
	MinKillRate     *float64
	MaxProvenMissed *int
	Passed          bool
	// Audited and Candidates are the scan's scope: "3 files clean" reads
	// very differently out of 4 than out of 400, and a join to find that out
	// is a join people skip.
	Audited    int
	Candidates int
}

// buildBundle maps one recorded scan — its header and all four row grains —
// into the bundle a single PushBundle transaction writes.
//
// scanID is threaded in rather than read off the rows so a bundle built for a
// run with no --record still names 0 consistently everywhere, which is the
// honest value: there is no ledger row to join back to.
//
// StatementSHA256 is left EMPTY on every row here. PushBundle stamps it from
// the Link, and writeAuditStatement hashes this bundle before the statement
// exists — the statement's own hash cannot depend on itself.
func buildBundle(
	scan scanstore.Scan,
	scanID int64,
	files []scanstore.File,
	mutants []scanstore.Mutant,
	calls []scanstore.ModelCall,
	events []scanstore.Event,
	link auditpush.Link,
	sourcePushed bool,
	repo, commit, runURL string,
	meta bundleMeta,
) auditpush.Bundle {
	meta.Repo, meta.Commit, meta.RunURL = repo, commit, runURL
	// The scope every row carries comes from the ledger header, not from a
	// second count, so a row and the scan row it belongs to can never
	// disagree about how much of the repo was looked at.
	meta.Audited, meta.Candidates = scan.Audited, scan.Candidates

	b := auditpush.Bundle{
		Scan: auditpush.ScanRow{
			Repo: repo, RunURL: runURL, ScanID: scanID, Commit: commit,
			CorralVersion: scan.CorralVersion, Substrate: scan.Substrate,
			Host: scan.Host, Cores: scan.Cores, TreesRequested: scan.TreesRequested,
			DiffBase: scan.DiffBase, Candidates: scan.Candidates, Audited: scan.Audited,
			Passed:      meta.Passed,
			TotalMillis: nilIfZeroMillis(scan.TotalMillis),
			// Already nullable in the ledger, so it rides through as-is: the
			// scan grain is where the one instrumented coverage run belongs,
			// and NULL there means the run never happened.
			SelectionMillis: scan.SelectionMillis,
			InputTokens:     scan.InputTokens, OutputTokens: scan.OutputTokens,
			ModelCalls:   scan.ModelCalls,
			SourcePushed: sourcePushed,
		},
		Files:        buildAuditRows(files, scanID, meta),
		Mutants:      buildMutantRows(mutants, scanID, meta),
		Calls:        buildModelCallRows(calls, scanID, meta),
		Events:       buildEventRows(events, scanID, meta),
		Link:         link,
		SourcePushed: sourcePushed,
	}
	return b
}

// buildAuditRows maps ledger file rows to warehouse rows, field for field.
// Deterministic by construction: it reads nothing but its arguments, so two
// calls on the same ledger rows produce byte-identical JSON — which is what
// makes the statement's warehouseRowsSha256 verifiable by a third party who
// rebuilds the rows.
func buildAuditRows(files []scanstore.File, scanID int64, meta bundleMeta) []auditpush.Row {
	rows := make([]auditpush.Row, 0, len(files))
	for _, f := range files {
		rows = append(rows, auditpush.Row{
			Repo: meta.Repo, Commit: meta.Commit, RunURL: meta.RunURL, ScanID: scanID,
			Path: f.Path, Lang: f.Lang,
			KillRate: f.KillRate, Survivors: f.Survivors, ProvenMissed: f.ProvenMissed,
			TimedOut: f.TimedOut, TestWriterFailed: f.TestWriterFailed,
			PoolTestUnsound: f.PoolTestUnsound,
			// Scope, gates and roster: the same values on every row, so a
			// reader of one row can see what the run was held to.
			Audited: meta.Audited, Candidates: meta.Candidates,
			// PLANTED, not graded: advpool's MutantsTotal counts the mutants
			// that reached grading, with the compile-gate rejects already
			// removed. Filing it under mutants_planted understated every run
			// that produced an invalid mutant, and made the column an exact
			// duplicate of mutants_graded.
			// A timed-out mutant is planted too, but nothing counts them yet
			// (MutantsTimedOut is nil on every row), so adding it would be
			// adding a phantom. Revisit when there is a producer.
			MutantsPlanted: f.MutantsGraded + f.MutantsInvalid,
			ModelsByRole:   meta.ModelsByRole,
			MinKillRate:    meta.MinKillRate, MaxProvenMissed: meta.MaxProvenMissed,
			Passed: meta.Passed,
			// WHICH measurement the rate is, and at which grain.
			TestSelection: f.TestSelection, SelectedTests: f.SelectedTests,
			SuiteTests: f.SuiteTests, SelectionFallback: f.SelectionFallback,
			Uncovered:      f.Uncovered,
			PerMutant:      f.PerMutant,
			TestsPerMutant: ledgerSpread(f),
			Trees:          f.Trees, ConcurrencyNote: f.ConcurrencyNote, SharedDirs: f.SharedDirs,
			// Every file, at every disposition — the reason this mapping
			// starts from the ledger and not from the report.
			Disposition: f.Disposition, Reason: f.Reason,
			PreflightState: f.PreflightState, Evidence: f.Evidence,
			Detail: f.Detail, Status: f.Status,
			CacheHit: f.CacheHit, ReusedFromScanID: f.ReusedFromScanID,
			CacheKey: f.CacheKey, ParentSHA256: f.ParentSHA256,
			MutantsGraded: f.MutantsGraded, MutantsInvalid: f.MutantsInvalid,
			MutantsTimedOut: f.MutantsTimedOut,
			RegionsTotal:    f.RegionsTotal, RegionsProbed: f.RegionsProbed,
			DroppedRegions: f.DroppedRegions, VacuousFindings: f.VacuousFindings,
			AuthoredTestNotCollected: f.AuthoredTestNotCollected,
			BaselineFailed:           f.BaselineFailed,
			// The ledger stores this as a plain int64 (it predates the
			// NULL-not-zero rule); the warehouse column is nullable, and a
			// baseline of 0ms is a measurement nobody made.
			SuiteBaselineMillis:  nilIfZeroMillis(f.SuiteBaselineMillis),
			ProvenMutantIDs:      f.ProvenMutantIDs,
			ChallengerJaccard:    f.ChallengerJaccard,
			ChallengerKappa:      f.ChallengerKappa,
			ChallengerSufficient: f.ChallengerSufficient,
			GoalsDerived:         f.GoalsDerived,
			SelectionMillis:      f.SelectionMillis,
			GenerationMillis:     f.GenerationMillis,
			PoolMillis:           f.PoolMillis,
			DevPassMillis:        f.DevPassMillis,
			AuthoredPassMillis:   f.AuthoredPassMillis,
			CriticMillis:         f.CriticMillis,
			TotalMillis:          f.TotalMillis,
			MutantMillisMedian:   f.MutantMillisMedian,
			MutantMillisMax:      f.MutantMillisMax,
			// Source. Carried on the row and then WITHHELD by the writer
			// unless --push-source was given (auditpush.PushBundle), so a
			// forgotten blanking here cannot leak code.
			AuthoredTest: f.AuthoredTest,
			VerdictJSON:  f.VerdictJSON,
		})
	}
	return rows
}

func buildMutantRows(mutants []scanstore.Mutant, scanID int64, meta bundleMeta) []auditpush.MutantRow {
	rows := make([]auditpush.MutantRow, 0, len(mutants))
	for _, m := range mutants {
		rows = append(rows, auditpush.MutantRow{
			Repo: meta.Repo, RunURL: meta.RunURL, ScanID: scanID,
			Path: m.Path, MutantID: m.MutantID, ParentSHA256: m.ParentSHA256,
			Outcome: m.Outcome,
			Proven:  m.Proven, ProvenByAuthoredAlone: m.ProvenByAuthoredAlone,
			TestsRun: m.TestsRun, SelectionRule: m.SelectionRule,
			DurationMillis: m.DurationMillis, KilledBy: m.KilledBy,
			SpanStart: m.SpanStart, SpanEnd: m.SpanEnd,
			// InvalidReason and Code have no ledger column: the local
			// scan_mutants table admits only killed|survived (its CHECK
			// predates this schema and DuckDB cannot alter one in place), and
			// the mutant SOURCE is deliberately not kept at rest locally.
			// Left empty rather than guessed — a disclosed asymmetry, not an
			// oversight.
		})
	}
	return rows
}

func buildModelCallRows(calls []scanstore.ModelCall, scanID int64, meta bundleMeta) []auditpush.ModelCallRow {
	rows := make([]auditpush.ModelCallRow, 0, len(calls))
	for _, c := range calls {
		rows = append(rows, auditpush.ModelCallRow{
			Repo: meta.Repo, RunURL: meta.RunURL, ScanID: scanID,
			Path: c.Path, Role: c.Role, Model: c.Model,
			Calls: c.Calls, Retries: c.Retries,
			InputTokens: c.InputTokens, OutputTokens: c.OutputTokens,
			WallMillis: c.WallMillis,
		})
	}
	return rows
}

func buildEventRows(events []scanstore.Event, scanID int64, meta bundleMeta) []auditpush.EventRow {
	rows := make([]auditpush.EventRow, 0, len(events))
	for _, e := range events {
		rows = append(rows, auditpush.EventRow{
			Repo: meta.Repo, RunURL: meta.RunURL, ScanID: scanID,
			Path: e.Path, TS: e.TS, Seq: e.Seq, Kind: e.Kind, Actor: e.Actor,
			Subject: e.Subject, Model: e.Model,
			DurationMillis: e.DurationMillis, Detail: e.Detail,
		})
	}
	return rows
}

// ledgerSpread lifts the ledger's three nullable per-mutant counts into the
// warehouse's one pointer-to-struct. All three present or the spread is
// absent: a half-measured spread is not a spread.
func ledgerSpread(f scanstore.File) *auditpush.TestsPerMutantSpread {
	if f.TestsPerMutantMin == nil || f.TestsPerMutantMedian == nil || f.TestsPerMutantMax == nil {
		return nil
	}
	return &auditpush.TestsPerMutantSpread{
		Min: *f.TestsPerMutantMin, Median: *f.TestsPerMutantMedian, Max: *f.TestsPerMutantMax,
	}
}

// nilIfZeroMillis converts a ledger column that predates the NULL-not-zero
// rule into the warehouse's nullable one. Zero milliseconds is not a
// measurement anything makes — a suite that runs, runs for some time — so a
// stored 0 is "nobody timed it", and it must reach the warehouse as NULL
// rather than be averaged into the cost model as free.
func nilIfZeroMillis(v int64) *int64 {
	if v == 0 {
		return nil
	}
	ms := v
	return &ms
}
