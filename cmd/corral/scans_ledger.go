// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// ledgerScans is scansReader over a ledger DIRECTORY: the entries, read
// once, presented in the row shapes `corral scans` prints. It is the
// inverse of buildBundle (certify_repo_bundle.go), field for field, so the
// record a scan wrote is what its reader shows — TestScansLedgerReaderIsTheInverseOfBuildBundle
// holds the two to each other.
//
// A scan's id is its position in the chain, 1 = the oldest entry,
// counting every entry — a retraction or a checkpoint holds its position
// too, so `scans show 3` a month from now is the same entry. Retraction
// and checkpoint entries are not scans: list leaves them out, show names
// what they are, and a retracted scan is listed and shown MARKED, since
// it happened and is no longer the record.
type ledgerScans struct {
	entries   []auditpush.LedgerEntry
	retracted map[string]auditpush.LedgerEntry
}

func openLedgerScans(dir string) (scansReader, error) {
	entries, err := auditpush.ReadLedgerDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading the ledger %s: %w", dir, err)
	}
	return &ledgerScans{entries: entries, retracted: auditpush.Retracted(entries)}, nil
}

func (l *ledgerScans) Close() error { return nil }

// entry returns the scan entry at a chain position; ok is false for an
// unknown position or for an entry that is not a scan.
func (l *ledgerScans) entry(scanID int64) (auditpush.LedgerEntry, bool) {
	if scanID < 1 || scanID > int64(len(l.entries)) || !l.entries[scanID-1].IsScan() {
		return auditpush.LedgerEntry{}, false
	}
	return l.entries[scanID-1], true
}

// EntryKind says what sits at a chain position, for a reader asked to show
// something that is not a scan: "" for a scan or an unknown position, else
// the kind and, for a retraction, what it retracts and why.
func (l *ledgerScans) EntryKind(scanID int64) string {
	if scanID < 1 || scanID > int64(len(l.entries)) {
		return ""
	}
	e := l.entries[scanID-1]
	switch e.Kind {
	case auditpush.KindRetract:
		return fmt.Sprintf("a retraction of entry %.12s: %s", e.Retracts, e.Reason)
	case auditpush.KindReview:
		if e.Review != nil {
			rep, cr, hy := e.Review.Counts()
			return fmt.Sprintf("a review of %s by %s (%d reproduced, %d code-read, %d hypothesis) — `corral review show <dir> %.12s`", e.Review.Scope, e.Review.ReviewerModel, rep, cr, hy, e.Hash)
		}
		return "a review"
	case auditpush.KindAdjudication:
		if e.Adjudication != nil {
			return fmt.Sprintf("an adjudication: %s %s by %s — %s", e.Adjudication.Verdict, e.Adjudication.Adjudicates, e.Adjudication.By, e.Adjudication.Reason)
		}
		return "an adjudication"
	case auditpush.KindCheckpoint:
		if e.Checkpoint != nil {
			return fmt.Sprintf("a checkpoint: %d earlier entries (through %s, head %.12s) were pruned before it", e.Checkpoint.Entries, e.Checkpoint.Through.UTC().Format("2006-01-02"), e.Checkpoint.Head)
		}
		return "a checkpoint"
	}
	return ""
}

// Scans lists the scan entries newest first, retracted ones marked.
func (l *ledgerScans) Scans(_ context.Context, limit int) ([]scanstore.ScanRow, error) {
	var out []scanstore.ScanRow
	for i := len(l.entries) - 1; i >= 0 && (limit <= 0 || len(out) < limit); i-- {
		if !l.entries[i].IsScan() {
			continue
		}
		out = append(out, l.scanRow(l.entries[i], int64(i+1)))
	}
	return out, nil
}

func (l *ledgerScans) ScanByID(_ context.Context, scanID int64) (scanstore.ScanRow, bool, error) {
	e, ok := l.entry(scanID)
	if !ok {
		return scanstore.ScanRow{}, false, nil
	}
	return l.scanRow(e, scanID), true, nil
}

func (l *ledgerScans) scanRow(e auditpush.LedgerEntry, id int64) scanstore.ScanRow {
	row := scanRowFromEntry(e, id)
	if r, gone := l.retracted[e.Hash]; gone {
		row.Retracted, row.RetractedReason = true, r.Reason
	}
	return row
}

func (l *ledgerScans) FilesForScan(_ context.Context, scanID int64) ([]scanstore.File, error) {
	e, ok := l.entry(scanID)
	if !ok {
		return nil, nil
	}
	out := make([]scanstore.File, 0, len(e.Bundle.Files))
	for _, r := range e.Bundle.Files {
		out = append(out, fileFromRow(r))
	}
	return out, nil
}

func (l *ledgerScans) MutantsForScan(_ context.Context, scanID int64) ([]scanstore.Mutant, error) {
	e, ok := l.entry(scanID)
	if !ok {
		return nil, nil
	}
	out := make([]scanstore.Mutant, 0, len(e.Bundle.Mutants))
	for _, m := range e.Bundle.Mutants {
		out = append(out, scanstore.Mutant{
			ScanID: scanID, Path: m.Path, MutantID: m.MutantID, Outcome: m.Outcome,
			ParentSHA256: m.ParentSHA256, Proven: m.Proven, ProvenByAuthoredAlone: m.ProvenByAuthoredAlone,
			TestsRun: m.TestsRun, SelectionRule: m.SelectionRule, DurationMillis: m.DurationMillis,
			KilledBy: m.KilledBy, SpanStart: m.SpanStart, SpanEnd: m.SpanEnd,
			Shape: m.Shape, GeneratorModel: m.GeneratorModel,
		})
	}
	return out, nil
}

func (l *ledgerScans) ModelCallsForScan(_ context.Context, scanID int64) ([]scanstore.ModelCall, error) {
	e, ok := l.entry(scanID)
	if !ok {
		return nil, nil
	}
	out := make([]scanstore.ModelCall, 0, len(e.Bundle.Calls))
	for _, c := range e.Bundle.Calls {
		out = append(out, scanstore.ModelCall{
			ScanID: scanID, Path: c.Path, Role: c.Role, Model: c.Model,
			Calls: c.Calls, Retries: c.Retries,
			InputTokens: c.InputTokens, OutputTokens: c.OutputTokens,
			CachedInputTokens: c.CachedInputTokens, CacheWriteInputTokens: c.CacheWriteInputTokens,
			WallMillis: c.WallMillis,
		})
	}
	return out, nil
}

// scanRowFromEntry is the scan grain. The two facts the retired store
// computed at write time — the scan's kill rate and its cache-hit count —
// are derived here from the file rows the same way reposcan.Aggregate
// derives them: the mean over files that were graded (audited, measured,
// and not timed out), NaN → nil when none was.
func scanRowFromEntry(e auditpush.LedgerEntry, id int64) scanstore.ScanRow {
	sc := e.Bundle.Scan
	owner, repo := splitRepo(sc.Repo)
	row := scanstore.ScanRow{ID: id, TS: e.Pushed}
	row.Scan = scanstore.Scan{
		Owner: owner, Repo: repo, Commit: sc.Commit, Substrate: sc.Substrate,
		EngineVersion: sc.EngineVersion, ModelSet: sc.ModelSet,
		Top: sc.Top, AllCandidates: sc.AllCandidates, DiffBase: sc.DiffBase,
		TotalFiles: sc.TotalFiles, Candidates: sc.Candidates, Audited: sc.Audited,
		PreflightRan: sc.PreflightRan, PreflightNote: sc.PreflightNote,
		CorralVersion: sc.CorralVersion, Host: sc.Host, Cores: sc.Cores, TreesRequested: sc.TreesRequested,
		SelectionMillis: sc.SelectionMillis, SelectionReused: sc.SelectionReused,
		InputTokens: sc.InputTokens, OutputTokens: sc.OutputTokens, ModelCalls: sc.ModelCalls,
		SourcePushed: sc.SourcePushed, StatementSHA256: sc.StatementSHA256,
		RekorLogIndex: sc.RekorLogIndex, RekorUUID: sc.RekorUUID,
	}
	if sc.StartedAt != nil {
		row.StartedAt = *sc.StartedAt
	}
	if sc.FinishedAt != nil {
		row.FinishedAt = *sc.FinishedAt
	}
	if sc.TotalMillis != nil {
		row.TotalMillis = *sc.TotalMillis
	}
	var sum float64
	graded := 0
	for _, f := range e.Bundle.Files {
		if f.CacheHit {
			row.CacheHits++
		}
		if f.Disposition != "audited" || f.KillRate == nil || f.TimedOut || math.IsNaN(*f.KillRate) {
			continue
		}
		sum += *f.KillRate
		graded++
	}
	if graded > 0 {
		row.KillRate = killRatePtr(sum / float64(graded))
	}
	return row
}

// splitRepo undoes the "owner/repo" the bundle carries.
func splitRepo(full string) (owner, repo string) {
	if i := strings.IndexByte(full, '/'); i >= 0 {
		return full[:i], full[i+1:]
	}
	return "", full
}

func fileFromRow(r auditpush.Row) scanstore.File {
	f := scanstore.File{
		Path: r.Path, Lang: r.Lang, Disposition: r.Disposition, Reason: r.Reason,
		KillRate: r.KillRate, Survivors: r.Survivors,
		Gradable:       r.Disposition == "audited",
		PreflightState: r.PreflightState, Evidence: r.Evidence, Detail: r.Detail,
		TimedOut: r.TimedOut, TestWriterFailed: r.TestWriterFailed, PoolTestUnsound: r.PoolTestUnsound,
		ProvenMissed: r.ProvenMissed, ProvenMutantIDs: r.ProvenMutantIDs, AuthoredTest: r.AuthoredTest,
		TestSelection: r.TestSelection, SelectedTests: r.SelectedTests, SuiteTests: r.SuiteTests,
		SelectionFallback: r.SelectionFallback, WriterMode: r.WriterMode, Uncovered: r.Uncovered,
		ImportOnly: r.ImportOnly, CoveringTests: r.CoveringTests, MutantsFrom: r.MutantsFrom,
		Trees: r.Trees, ConcurrencyNote: r.ConcurrencyNote, SharedDirs: r.SharedDirs,
		CacheKey: r.CacheKey, VerdictJSON: r.VerdictJSON, ModelsByRole: r.ModelsByRole,
		MutantsTotal: r.MutantsPlanted,
		RegionsTotal: r.RegionsTotal, RegionsProbed: r.RegionsProbed, DroppedRegions: r.DroppedRegions,
		VacuousFindings: r.VacuousFindings, Status: r.Status, PromptShape: r.PromptShape,
		MutantBudget: r.MutantBudget, MutantBudgetRule: r.MutantBudgetRule, Complexity: r.Complexity,
		Symbols: r.Symbols, SymbolsProbed: r.SymbolsProbed, Decisions: r.Decisions, DecisionsProbed: r.DecisionsProbed,
		AuthoredTestNotCollected: r.AuthoredTestNotCollected, BaselineFailed: r.BaselineFailed,
		CacheHit: r.CacheHit, ReusedFromScanID: r.ReusedFromScanID, ParentSHA256: r.ParentSHA256,
		MutantsGraded: r.MutantsGraded, MutantsInvalid: r.MutantsInvalid, MutantsTimedOut: r.MutantsTimedOut,
		SelectionMillis: r.SelectionMillis, GenerationMillis: r.GenerationMillis, PoolMillis: r.PoolMillis,
		DevPassMillis: r.DevPassMillis, AuthoredPassMillis: r.AuthoredPassMillis, CriticMillis: r.CriticMillis,
		TotalMillis: r.TotalMillis, MutantMillisMedian: r.MutantMillisMedian, MutantMillisMax: r.MutantMillisMax,
		ChallengerJaccard: r.ChallengerJaccard, ChallengerKappa: r.ChallengerKappa, ChallengerSufficient: r.ChallengerSufficient,
		ChallengerMutants: r.ChallengerMutants, ChallengerSurvivedWriter: r.ChallengerSurvivedWriter,
		ChallengerSurvivedShadow: r.ChallengerSurvivedShadow, ChallengerUnion: r.ChallengerUnion, ChallengerShared: r.ChallengerShared,
		PriorsApplied: r.PriorsApplied, PriorDigest: r.PriorDigest,
		GoalsDerived: r.GoalsDerived, GoalReused: r.GoalReused, PerMutant: r.PerMutant,
	}
	if r.SuiteBaselineMillis != nil {
		f.SuiteBaselineMillis = *r.SuiteBaselineMillis
	}
	if r.ComputedAt != nil {
		f.ComputedAt = *r.ComputedAt
	}
	if s := r.TestsPerMutant; s != nil {
		mn, md, mx := s.Min, s.Median, s.Max
		f.TestsPerMutantMin, f.TestsPerMutantMedian, f.TestsPerMutantMax = &mn, &md, &mx
	}
	return f
}
