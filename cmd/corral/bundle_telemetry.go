// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/reposcan"
)

// telemetryRows turns what a finished run measured about its seats into the
// bundle's rows, stamping each with the subject the run audited.
//
// The conversion is field by field, which AGENTS.md names as the shape that
// fails open on the field nobody remembered: a field added to an advpool
// producer and not here would vanish from the ledger without a sound. The
// three TestXRowCarriesEveryField tests in bundle_telemetry_test.go fill
// every producer field and fail when one does not arrive under its own name.
//
// A nil or empty input slice yields a nil slice, never an empty one, so a
// run that measured nothing of a kind adds no key to the entry (omitempty)
// rather than a claim of "measured none".
//
// path is the audited file for seats and findings. A MutantAttempt carries
// its own Path (the mutant's file), which is kept as measured.
func telemetryRows(repo, commit, path string, t advpool.RunTelemetry) ([]auditpush.SeatRow, []auditpush.MutantAttemptRow, []auditpush.CriticFindingRow) {
	var seats []auditpush.SeatRow
	for _, o := range t.Seats {
		seats = append(seats, auditpush.SeatRow{
			Repo: repo, Commit: commit, Path: path,
			Model: o.Model, Role: o.Role,
			Catches: o.Catches, Opportunities: o.Opportunities,
			SoundTests: o.SoundTests, AuthoredTests: o.AuthoredTests,
			CriticFlags: o.CriticFlags, MutantsPlanted: o.MutantsPlanted, MutantsSurvived: o.MutantsSurvived,
			Shard: o.Shard, Region: o.Region, RegionComplexity: o.RegionComplexity, RegionLines: o.RegionLines,
			TestComplexity: o.TestComplexity, ParseRetries: o.ParseRetries,
			Dropped: o.Dropped, Shadow: o.Shadow, Lang: o.Lang, ShadowDrawn: o.ShadowDrawn,
			SeatTool: o.SeatTool, ModelUnresolved: o.ModelUnresolved,
		})
	}
	var attempts []auditpush.MutantAttemptRow
	for _, a := range t.MutantAttempts {
		attempts = append(attempts, auditpush.MutantAttemptRow{
			Repo: repo, Commit: commit,
			Path: a.Path, MutantID: a.MutantID, Model: a.Model, Role: a.Role, Shadow: a.Shadow, Outcome: a.Outcome,
		})
	}
	var findings []auditpush.CriticFindingRow
	for _, f := range t.CriticFindings {
		findings = append(findings, auditpush.CriticFindingRow{
			Repo: repo, Commit: commit, Path: path,
			QueueFindingID: f.QueueFindingID, Model: f.Model,
			TargetTest: f.TargetTest, TestFile: f.TestFile, TestSelector: f.TestSelector,
			Scope: f.Scope, Evidence: f.Evidence, Severity: f.Severity,
			Adjudication: f.Adjudication, Source: f.Source,
			SeatTool: f.SeatTool, ModelUnresolved: f.ModelUnresolved,
		})
	}
	return seats, attempts, findings
}

// scanTelemetryRows is a repo scan's three telemetry slices: every file's own
// run's rows (FileResult.Telemetry), concatenated in results order, with each
// seat and critic-finding row stamped with the file that run audited (the
// stamping rule is telemetryRows', not a second one). A file that ran
// no audit this scan (a cache hit, a refusal) contributes nothing, and a scan
// in which no file measured anything returns nil slices, so the entry carries
// no telemetry keys rather than a claim of "none".
func scanTelemetryRows(repo, commit string, results []reposcan.FileResult) ([]auditpush.SeatRow, []auditpush.MutantAttemptRow, []auditpush.CriticFindingRow) {
	var seats []auditpush.SeatRow
	var attempts []auditpush.MutantAttemptRow
	var findings []auditpush.CriticFindingRow
	for _, r := range results {
		s, a, f := telemetryRows(repo, commit, r.Job.Path, r.Telemetry)
		seats, attempts, findings = append(seats, s...), append(attempts, a...), append(findings, f...)
	}
	return seats, attempts, findings
}
