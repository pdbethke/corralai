// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/auditpush"
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
