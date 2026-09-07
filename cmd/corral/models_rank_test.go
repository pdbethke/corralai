// SPDX-License-Identifier: Elastic-2.0

package main

// SURFACE CLAIMS — threaded into runRank as a value; the test calls runRank.
//
// testdata/executed-surfaces.tsv names this file as the receipt for the
// flag(s) below, and TestDocsClassifiedSurfacesCarryAReceipt requires either
// the literal or an explicit claim. This is the explicit claim: a receipt a
// reader cannot check is not a receipt.
//surface: --min-runs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/pdbethke/corralai/internal/review"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/brain"
	"github.com/pdbethke/corralai/internal/bugcatch"
	"github.com/pdbethke/corralai/internal/modelrank"
)

func rankLoaderFor(obs []modelrank.Observation) rankLoader {
	return func(dsn string) (rankEvidence, error) {
		return rankEvidence{Obs: obs, Source: "a seeded store"}, nil
	}
}

func writerRuns(model, lang string, n, catches, opps int) []modelrank.Observation {
	var out []modelrank.Observation
	for i := 0; i < n; i++ {
		out = append(out, modelrank.Observation{
			Model: model, Role: modelrank.SeatTestWriter, Lang: lang,
			Run:     fmt.Sprintf("%s-%s-%d", model, lang, i),
			Catches: catches, Opportunities: opps,
		})
	}
	return out
}

func runRank(t *testing.T, args []string, obs []modelrank.Observation) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	rc := runModels(append([]string{"rank"}, args...), t.TempDir(), rankLoaderFor(obs), &out, &errb)
	return rc, out.String(), errb.String()
}

// The whole command's reason for existing, in one assertion: the ranking is
// evidence a human reads, and it says so on its face.
func TestRankSaysItIsDisclosureNotSelection(t *testing.T) {
	rc, out, _ := runRank(t, nil, writerRuns("m", "", 6, 5, 10))
	if rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if !strings.Contains(out, "DISCLOSURE, NOT SELECTION") {
		t.Fatalf("the table must say it sets no default:\n%s", out)
	}
	if !strings.Contains(out, "min-runs: 5") {
		t.Fatalf("the default evidence floor must be stated:\n%s", out)
	}
}

// The live scorecard's `claude-sonnet-5 3/3 100%` row: printed, marked, and
// kept out of the prefer line.
func TestRankPrintsThinRowsButNeverPrefersThem(t *testing.T) {
	// The live scorecard's real row: 22 runs, 3 survivors ever attempted.
	obs := writerRuns("claude-sonnet-5", "", 22, 0, 0)
	obs = append(obs, modelrank.Observation{Model: "claude-sonnet-5", Role: modelrank.SeatTestWriter,
		Run: "sonnet-attempt", Catches: 3, Opportunities: 3})
	obs = append(obs, modelrank.Observation{Model: "gemini-3.6-flash", Role: modelrank.SeatTestWriter,
		Run: "g", Catches: 64, Opportunities: 79})
	rc, out, _ := runRank(t, nil, obs)
	if rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if !strings.Contains(out, "claude-sonnet-5") {
		t.Fatalf("the thin row must still be disclosed:\n%s", out)
	}
	if !strings.Contains(out, "insufficient evidence (n=3)") { // 3 ATTEMPTS, not 22 runs
		t.Fatalf("the thin row must be marked:\n%s", out)
	}
	if !strings.Contains(out, "prefer: gemini-3.6-flash") {
		t.Fatalf("prefer must name the well-evidenced model:\n%s", out)
	}
	if strings.Contains(out, "prefer: claude-sonnet-5") {
		t.Fatalf("a 3/3 row must never be promoted:\n%s", out)
	}
}

func TestRankRegistryModeVsEvidenceMode(t *testing.T) {
	obs := writerRuns("gemini-3.6-flash", "", 6, 5, 10)

	rc, out, _ := runRank(t, nil, obs)
	if rc != 0 || !strings.Contains(out, "mode: evidence") {
		t.Fatalf("rc=%d; want evidence mode:\n%s", rc, out)
	}

	t.Setenv("CORRALAI_MODELS", `{"fast": {"provider": "google", "model": "gemini-3.6-flash"}}`)
	rc, out, _ = runRank(t, nil, obs)
	if rc != 0 || !strings.Contains(out, "mode: registry") {
		t.Fatalf("rc=%d; want registry mode:\n%s", rc, out)
	}
	if !strings.Contains(out, "fast") {
		t.Fatalf("the declared alias must be shown:\n%s", out)
	}
	if !strings.Contains(out, "prefer: fast") {
		t.Fatalf("registry mode prefers by alias:\n%s", out)
	}
}

func TestRankLangSegmentationAndFilter(t *testing.T) {
	obs := append(writerRuns("m", "python", 6, 9, 10), writerRuns("m", "go", 6, 1, 10)...)
	rc, out, _ := runRank(t, nil, obs)
	if rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if !strings.Contains(out, "test-writer · python") || !strings.Contains(out, "test-writer · go") {
		t.Fatalf("want both language groups:\n%s", out)
	}
	rc, out, _ = runRank(t, []string{"--lang", "go"}, obs)
	if rc != 0 || strings.Contains(out, "· python") {
		t.Fatalf("--lang go leaked python (rc=%d):\n%s", rc, out)
	}
}

// --lang against evidence with no language dimension must refuse, not quietly
// return an empty table that reads as "no models are good at go".
func TestRankLangRefusesWhenEvidenceRecordsNoLanguage(t *testing.T) {
	rc, _, errOut := runRank(t, []string{"--lang", "go"}, writerRuns("m", "", 6, 5, 10))
	if rc != 2 {
		t.Fatalf("rc=%d, want 2", rc)
	}
	if !strings.Contains(errOut, "records no language") {
		t.Fatalf("refusal must explain itself: %s", errOut)
	}
}

func TestRankJSONShape(t *testing.T) {
	rc, out, _ := runRank(t, []string{"--json"}, writerRuns("m", "", 6, 5, 10))
	if rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	var rep modelrank.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("--json is not valid JSON: %v\n%s", err, out)
	}
	if rep.Mode != modelrank.ModeEvidence || rep.MinRuns != 5 || len(rep.Groups) != 1 {
		t.Fatalf("unexpected report: %+v", rep)
	}
	g := rep.Groups[0]
	if g.Seat != modelrank.SeatTestWriter || g.Prefer != "m" || len(g.Rows) != 1 {
		t.Fatalf("unexpected group: %+v", g)
	}
	r := g.Rows[0]
	if r.Metric == nil || *r.Metric != 0.5 || r.N != 60 || r.NUnit != "survivors attempted" || !r.Sufficient || r.MetricLabel == "" {
		t.Fatalf("unexpected row: %+v", r)
	}
}

// An unreachable --db is a refusal with exit 2 — it must never fall back to
// the local ledger, which would answer a different question than the one the
// operator asked.
func TestRankUnreachableDBRefusesWithExitTwo(t *testing.T) {
	var out, errb bytes.Buffer
	load := func(dsn string) (rankEvidence, error) {
		if dsn == "" {
			t.Fatal("a named --db must never fall back to the default store")
		}
		return rankEvidence{}, errors.New("cannot read the warehouse: no such file")
	}
	rc := runModels([]string{"rank", "--db", "/nope/missing.duckdb"}, t.TempDir(), load, &out, &errb)
	if rc != 2 {
		t.Fatalf("rc=%d, want 2", rc)
	}
	if out.Len() != 0 {
		t.Fatalf("a refusal must print no ranking:\n%s", out.String())
	}
	if !strings.Contains(errb.String(), "cannot read the warehouse") {
		t.Fatalf("refusal must name the cause: %s", errb.String())
	}
}

func TestRankUnknownSeatRefuses(t *testing.T) {
	rc, _, errOut := runRank(t, []string{"--seat", "typo"}, writerRuns("m", "", 6, 5, 10))
	if rc != 2 || !strings.Contains(errOut, "is not a seat") {
		t.Fatalf("rc=%d err=%s", rc, errOut)
	}
}

func TestRankGoalDeriverIsReportedNotScored(t *testing.T) {
	obs := []modelrank.Observation{{Model: "g", Role: modelrank.SeatGoalDeriver, Run: "r1"}}
	rc, out, _ := runRank(t, nil, obs)
	if rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if !strings.Contains(out, "not scored (goal quality is only visible downstream, via mutant yield)") {
		t.Fatalf("goal-deriver must be reported as unscored:\n%s", out)
	}
	if strings.Contains(out, "prefer:") {
		t.Fatalf("an unscored seat must carry no prefer line:\n%s", out)
	}
}

// The bugcatch adapter reads the SAME ledger `corral scorecard` reads, and
// must count converged RUNS (not the per-shard rows the generator fans out
// into) and skip the shadow seats.
func TestBugcatchRankEvidenceCountsRunsAndSkipsShadow(t *testing.T) {
	store, err := bugcatch.Open(filepath.Join(t.TempDir(), "bc.duckdb"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()
	ts := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	var obs []bugcatch.Observation
	for shard := 0; shard < 4; shard++ {
		obs = append(obs, bugcatch.Observation{
			TS: ts, RecordID: 1, Model: "gen", Role: modelrank.SeatMutantGenerator, Source: "pool",
			Shard: shard, MutantsPlanted: 10, MutantsSurvived: 2,
		})
	}
	obs = append(obs,
		bugcatch.Observation{TS: ts, RecordID: 1, Model: "w", Role: modelrank.SeatTestWriter, Catches: 3, Opportunities: 8},
		bugcatch.Observation{TS: ts, RecordID: 1, Model: "shadow-w", Role: modelrank.SeatTestWriter, Catches: 9, Opportunities: 9, Shadow: true},
	)
	if err := store.Record(context.Background(), obs); err != nil {
		t.Fatalf("record: %v", err)
	}
	ev, err := bugcatchRankEvidence(context.Background(), store, nil)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	rep := modelrank.Rank(ev.Obs, modelrank.Options{MinRuns: 1})
	for _, g := range rep.Groups {
		for _, r := range g.Rows {
			if r.Model == "shadow-w" {
				t.Fatal("a shadow seat's outcome is not evidence about a seat's work")
			}
			if r.Model == "gen" {
				if r.N != 1 {
					t.Fatalf("4 shards of ONE run must count as 1 run, got %d", r.N)
				}
				if r.Metric == nil || *r.Metric != 8 {
					t.Fatalf("want 8 unkilled mutants for the run, got %v", r.Metric)
				}
				if r.Valid != nil {
					t.Fatal("this ledger records no graded count — the valid share must be absent, not 0")
				}
			}
		}
	}
	if !rep.LangDimension {
		return // expected: the bugcatch ledger records no language
	}
	t.Fatal("the bug-catching ledger records no language; the report must not claim one")
}

// The --db path, end to end against a real pushed warehouse: this is the
// evidence that carries a LANGUAGE, and the scan (not the file) is the run.
func TestWarehouseRankEvidenceSegmentsByLanguage(t *testing.T) {
	target := filepath.Join(t.TempDir(), "w.duckdb")
	byRole := `{"test-writer":"w1","mutant-generator":"g1"}`
	var files []auditpush.Row
	for i := 0; i < 6; i++ {
		files = append(files,
			auditpush.Row{Repo: "o/r", Commit: "c", Path: fmt.Sprintf("p%d.py", i), Lang: "python",
				Disposition: "audited", ModelsByRole: byRole, ScanID: int64(i + 1),
				Survivors: 10, ProvenMissed: 9, MutantsPlanted: 20, MutantsGraded: 18, MutantsInvalid: 2},
			auditpush.Row{Repo: "o/r", Commit: "c", Path: fmt.Sprintf("p%d.go", i), Lang: "go",
				Disposition: "audited", ModelsByRole: byRole, ScanID: int64(i + 1),
				Survivors: 10, ProvenMissed: 1, MutantsPlanted: 20, MutantsGraded: 18, MutantsInvalid: 2},
		)
	}
	if _, err := auditpush.PushBundle(target, auditpush.Bundle{Files: files}); err != nil {
		t.Fatalf("push: %v", err)
	}
	db, err := attachWarehouse(target, true)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	defer db.Close()
	ev, err := warehouseRankEvidence(db, target)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	rep := modelrank.Rank(ev.Obs, modelrank.Options{MinRuns: 5})
	if !rep.LangDimension {
		t.Fatal("a warehouse records a language — the report must use it")
	}
	var py, gol *modelrank.Row
	for i := range rep.Groups {
		g := &rep.Groups[i]
		if g.Seat != modelrank.SeatTestWriter {
			continue
		}
		for j := range g.Rows {
			if g.Rows[j].Model != "w1" {
				continue
			}
			switch g.Lang {
			case "python":
				py = &g.Rows[j]
			case "go":
				gol = &g.Rows[j]
			}
		}
	}
	if py == nil || gol == nil {
		t.Fatalf("want a python and a go writer row, got %+v", rep.Groups)
	}
	// A writer good at Python and bad at Go must not be averaged into one number.
	if *py.Metric != 0.9 || *gol.Metric != 0.1 {
		t.Fatalf("python=%v go=%v", *py.Metric, *gol.Metric)
	}
	// Six scans x 10 survivors: the writer's n is the attempts its rate is
	// made of, and the run key behind them is the SCAN, not the file (twelve
	// file rows, six scans).
	if py.N != 60 {
		t.Fatalf("n = %d, want 60 survivors attempted", py.N)
	}
	// The generator's valid share comes off the warehouse's own denominators.
	for _, g := range rep.Groups {
		for _, r := range g.Rows {
			if r.Model == "g1" && (r.Valid == nil || *r.Valid < 0.89 || *r.Valid > 0.91) {
				t.Fatalf("want a 90%% valid share for the generator, got %v", r.Valid)
			}
		}
	}
}

// READ-ONLY, proven rather than asserted: ranking a ledger must leave it byte
// for byte as it found it, and must not change what `corral scorecard` says
// about the same rows. This command reports on runs that already happened; it
// must not be able to touch one.
func TestRankChangesNothingItReads(t *testing.T) {
	dir := t.TempDir()
	bcPath := filepath.Join(dir, "bc.duckdb")
	store, err := bugcatch.Open(bcPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ts := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	var seed []bugcatch.Observation
	for i := 1; i <= 6; i++ {
		seed = append(seed, bugcatch.Observation{TS: ts, RecordID: int64(i), Model: "w",
			Role: modelrank.SeatTestWriter, Source: "pool", Catches: 1, Opportunities: 2})
	}
	if err := store.Record(context.Background(), seed); err != nil {
		t.Fatalf("record: %v", err)
	}
	before, err := brain.BuildBugCatchScorecard(store, nil)
	if err != nil {
		t.Fatalf("scorecard: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	digest := func() [32]byte {
		b, rerr := os.ReadFile(bcPath)
		if rerr != nil {
			t.Fatalf("read ledger: %v", rerr)
		}
		return sha256.Sum256(b)
	}
	sumBefore := digest()

	t.Setenv("CORRALAI_BUGCATCH_DB", bcPath)
	t.Setenv("CORRALAI_CRITICSCORE_DB", filepath.Join(dir, "cs.duckdb"))
	var out, errb bytes.Buffer
	if rc := runModels([]string{"rank"}, dir, defaultRankLoader, &out, &errb); rc != 0 {
		t.Fatalf("rc=%d err=%s", rc, errb.String())
	}
	if !strings.Contains(out.String(), "prefer: w") {
		t.Fatalf("expected a ranking off the seeded ledger:\n%s", out.String())
	}
	if digest() != sumBefore {
		t.Fatal("ranking altered the ledger it read — this command must be read-only")
	}

	reopened, err := bugcatch.Open(bcPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	after, err := brain.BuildBugCatchScorecard(reopened, nil)
	if err != nil {
		t.Fatalf("scorecard: %v", err)
	}
	b1, _ := json.Marshal(before)
	b2, _ := json.Marshal(after)
	if string(b1) != string(b2) {
		t.Fatalf("the scorecard changed after a rank:\nbefore %s\nafter  %s", b1, b2)
	}
}

// TestWarehouseRankEvidenceChargesOnlyMeasuredRuns pins the review's four
// rows: a writer-failed file (10 survivors), a graded file at 3/4, that
// same file re-served from the verdict cache in a later scan, and a
// timed-out file (7 survivors). The measured record is 3/4 over ONE run;
// the warehouse path used to report 6/25 over three, charging pipeline
// failures to the writer and counting the cache hit — a run in which the
// writer did nothing — twice.
func TestWarehouseRankEvidenceChargesOnlyMeasuredRuns(t *testing.T) {
	target := filepath.Join(t.TempDir(), "w.duckdb")
	byRole := `{"test-writer":"w1","mutant-generator":"g1"}`
	reused := int64(1)
	files := []auditpush.Row{
		{Repo: "o/r", Commit: "c", Path: "a.go", Lang: "go", Disposition: "audited", ModelsByRole: byRole, ScanID: 1,
			Survivors: 10, ProvenMissed: 0, TestWriterFailed: true, MutantsPlanted: 12, MutantsGraded: 12},
		{Repo: "o/r", Commit: "c", Path: "b.go", Lang: "go", Disposition: "audited", ModelsByRole: byRole, ScanID: 1,
			Survivors: 4, ProvenMissed: 3, MutantsPlanted: 8, MutantsGraded: 8},
		{Repo: "o/r", Commit: "c", Path: "b.go", Lang: "go", Disposition: "audited", ModelsByRole: byRole, ScanID: 2,
			Survivors: 4, ProvenMissed: 3, MutantsPlanted: 8, MutantsGraded: 8, CacheHit: true, ReusedFromScanID: &reused},
		{Repo: "o/r", Commit: "c", Path: "c.go", Lang: "go", Disposition: "audited", ModelsByRole: byRole, ScanID: 3,
			Survivors: 7, ProvenMissed: 0, TimedOut: true, MutantsPlanted: 9, MutantsGraded: 9},
	}
	if _, err := auditpush.PushBundle(target, auditpush.Bundle{Files: files}); err != nil {
		t.Fatalf("push: %v", err)
	}
	db, err := attachWarehouse(target, true)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	defer db.Close()
	ev, err := warehouseRankEvidence(db, target)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	catches, opps, writerRuns, genRuns := 0, 0, map[string]bool{}, map[string]bool{}
	for _, o := range ev.Obs {
		switch o.Role {
		case modelrank.SeatTestWriter:
			catches += o.Catches
			opps += o.Opportunities
			writerRuns[o.Run] = true
		case modelrank.SeatMutantGenerator:
			genRuns[o.Run] = true
		}
	}
	if catches != 3 || opps != 4 || len(writerRuns) != 1 {
		t.Errorf("writer = %d/%d over %d run(s), want 3/4 over 1 — the measured record only", catches, opps, len(writerRuns))
	}
	// The writer-failed file still measured the GENERATOR (its mutants were
	// planted and graded); the cache hit and the timed-out file measured
	// nothing this scan.
	if len(genRuns) != 1 {
		t.Errorf("generator runs = %v, want scan 1 only", genRuns)
	}
	if !strings.Contains(ev.Source, "a run is one SCAN") {
		t.Errorf("Source = %q, want it to say what a run is here", ev.Source)
	}
}

// TestBugcatchRankEvidenceReadsPastTheDebugCap: Observations() keeps the
// most recent 10 000 rows, and `models rank` used to read through it, so
// the OLDEST writer's record — here, the only one at 50/50 — vanished from
// the ranking once the ledger passed the cap, while `corral scorecard`
// still summed it.
func TestBugcatchRankEvidenceReadsPastTheDebugCap(t *testing.T) {
	store, err := bugcatch.Open(filepath.Join(t.TempDir(), "bc.duckdb"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()
	ts := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	obs := []bugcatch.Observation{{TS: ts, RecordID: 1, Model: "veteran", Role: modelrank.SeatTestWriter, Catches: 50, Opportunities: 50}}
	for i := 2; i <= 10_001; i++ {
		obs = append(obs, bugcatch.Observation{TS: ts, RecordID: int64(i), Model: "noise", Role: modelrank.SeatMutantGenerator, MutantsPlanted: 1})
	}
	if err := store.Record(context.Background(), obs); err != nil {
		t.Fatalf("record: %v", err)
	}
	ev, err := bugcatchRankEvidence(context.Background(), store, nil)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	seen := false
	for _, o := range ev.Obs {
		if o.Model == "veteran" && o.Catches == 50 {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("the oldest writer's 50/50 record is missing from %d observation(s) — the ranking read a truncated ledger", len(ev.Obs))
	}
}

// TestRankGradesTheReviewerAndTheVerifierFromTheLedger: the review loop's
// seats become rows, from the entries. One review with three findings —
// one held by execution and the verifier stood (both right), one refuted
// by a reproduced refutation (reviewer wrong, verifier right), one the
// person overruled (execution said held, the person refuted: reviewer
// wrong, the verifier's STANDS wrong) — plus a code-read claim nobody
// checked, which grades nobody.
func TestRankGradesTheReviewerAndTheVerifierFromTheLedger(t *testing.T) {
	t.Setenv("CORRALAI_CERTIFY_KEY_FILE", filepath.Join(t.TempDir(), "certify_key"))
	dir := filepath.Join(t.TempDir(), "ledger")
	writeCacheTestEntry(t, dir, nil) // a scan, so the directory is a ledger the warehouse reader accepts
	signer, _ := ledgerSignerFromLocalKey()
	code := 0
	r := review.Review{Repo: "acme/r", Commit: "abc", Scope: "pkg", ReviewerModel: "rev-m", VerifierModel: "ver-m", Lang: "go", FilesShown: []string{"pkg/a.go", "pkg/b.go"},
		Findings: []review.Finding{
			{ID: "R1", Declared: review.TierReproduced, Tier: review.TierReproduced, ExitCode: &code, Refutation: &review.Refutation{Model: "ver-m", Verdict: review.VerdictStands}},
			{ID: "R2", Declared: review.TierReproduced, Tier: review.TierCodeRead, Demoted: "refuted by ver-m, reproduced", Refutation: &review.Refutation{Model: "ver-m", Verdict: review.VerdictRefuted, Tier: review.TierReproduced}},
			{ID: "R3", Declared: review.TierReproduced, Tier: review.TierReproduced, ExitCode: &code, Refutation: &review.Refutation{Model: "ver-m", Verdict: review.VerdictStands}},
			{ID: "R4", Declared: review.TierCodeRead, Tier: review.TierCodeRead, Refutation: &review.Refutation{Model: "ver-m", Verdict: review.VerdictRefuted, Tier: review.TierCodeRead}},
		}}
	if _, err := auditpush.WriteReview(dir, r, signer); err != nil {
		t.Fatal(err)
	}
	entries, _ := auditpush.ReadLedgerDir(dir)
	if _, err := auditpush.WriteAdjudication(dir, entries[1].Hash+"#R3", auditpush.VerdictRefuted, "pdb", "narrower than claimed", signer); err != nil {
		t.Fatal(err)
	}
	// Through the VIEW: the directory loads as the same tables a warehouse
	// holds, so the grains are what the rank reads either way.
	vdb, err := auditpush.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer vdb.Close()
	obs, err := reviewRankEvidence(vdb)
	if err != nil {
		t.Fatal(err)
	}
	var rc, rh, vc, vok int
	for _, o := range obs {
		if o.Lang != "go" {
			t.Errorf("the scope's language must ride on the observation: %+v", o)
		}
		rc += o.ReviewClaimsChecked
		rh += o.ReviewClaimsHeld
		vc += o.VerifierCalls
		vok += o.VerifierCorrect
	}
	if rc != 3 || rh != 1 || vc != 3 || vok != 2 {
		t.Fatalf("reviewer %d/%d checked/held, verifier %d/%d calls/correct; want 3/1 and 3/2 (R4 grades nobody)", rh, rc, vok, vc)
	}
	var out, errb bytes.Buffer
	if rcode := runModels([]string{"rank", "--db", dir, "--min-runs", "1"}, t.TempDir(), defaultRankLoader, &out, &errb); rcode != 0 {
		t.Fatalf("rank: %d %s", rcode, errb.String())
	}
	for _, want := range []string{"reviewer · go", "1/3 claims held over 1 reviews", "verifier · go", "2/3 verdicts agreed over 1 reviews", "reviewer and verifier seats from corral_findings joined to corral_adjudications"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("rank output lacks %q:\n%s", want, out.String())
		}
	}
}

// TestRankReadsTheReviewSeatsFromAPushedWarehouse: the grains pushed to
// a warehouse FILE — a review, then a person's verdict from another
// process — rank the same as the directory's view does; the file holds
// no scripts unless the run said --push-source.
func TestRankReadsTheReviewSeatsFromAPushedWarehouse(t *testing.T) {
	t.Setenv("CORRALAI_CERTIFY_KEY_FILE", filepath.Join(t.TempDir(), "certify_key"))
	dir := filepath.Join(t.TempDir(), "ledger")
	signer, _ := ledgerSignerFromLocalKey()
	code := 0
	r := review.Review{Repo: "acme/r", Commit: "abc", Scope: "pkg", ReviewerModel: "rev-m", VerifierModel: "ver-m", Lang: "go",
		Findings: []review.Finding{
			{ID: "R1", Claim: "x", Declared: review.TierReproduced, Tier: review.TierReproduced, Script: "grep secret", Stdout: "the secret", ExitCode: &code, Refutation: &review.Refutation{Model: "ver-m", Verdict: review.VerdictStands}},
		}}
	if _, err := auditpush.WriteReview(dir, r, signer); err != nil {
		t.Fatal(err)
	}
	entries, _ := auditpush.ReadLedgerDir(dir)
	wh := filepath.Join(t.TempDir(), "wh.duckdb")
	if c, err := auditpush.PushReviewEntry(wh, entries[0], false); err != nil || c.Reviews != 1 || c.Findings != 1 {
		t.Fatalf("push review: %+v %v", c, err)
	}
	if _, err := auditpush.WriteAdjudication(dir, entries[0].Hash+"#R1", auditpush.VerdictRefuted, "pdb", "no", signer); err != nil {
		t.Fatal(err)
	}
	entries, _ = auditpush.ReadLedgerDir(dir)
	if c, err := auditpush.PushReviewEntry(wh, entries[1], false); err != nil || c.Adjudications != 1 {
		t.Fatalf("push adjudication: %+v %v", c, err)
	}
	db, err := attachWarehouse(wh, true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var script sql.NullString
	var scriptSHA string
	if err := db.QueryRow(`SELECT script, script_sha256 FROM corral_findings`).Scan(&script, &scriptSHA); err != nil {
		t.Fatal(err)
	}
	if script.Valid || scriptSHA == "" {
		t.Errorf("without --push-source the script must be withheld and its hash kept: script=%v sha=%q", script, scriptSHA)
	}
	obs, err := reviewRankEvidence(db)
	if err != nil {
		t.Fatal(err)
	}
	// The person refuted a claim execution held: reviewer wrong, verifier's STANDS wrong.
	var rh, rc, vok, vc int
	for _, o := range obs {
		rh, rc, vok, vc = rh+o.ReviewClaimsHeld, rc+o.ReviewClaimsChecked, vok+o.VerifierCorrect, vc+o.VerifierCalls
	}
	if rc != 1 || rh != 0 || vc != 1 || vok != 0 {
		t.Errorf("reviewer %d/%d, verifier %d/%d; want 0/1 and 0/1", rh, rc, vok, vc)
	}
}

// The audited party has a seat. One observation per audit of a change
// that reached a verdict — a scan with a gate, a review with a checked
// claim — for the author and for each co-author (an agent, in code an
// agent helped write); the change held when the gate passed, or when no
// checked claim did. Under the same evidence floor as every seat, and
// nothing in the row says what to do with it.
func TestRankGradesTheAuditedPartyFromTheLedger(t *testing.T) {
	t.Setenv("CORRALAI_CERTIFY_KEY_FILE", filepath.Join(t.TempDir(), "certify_key"))
	dir := filepath.Join(t.TempDir(), "ledger")
	signer, _ := ledgerSignerFromLocalKey()
	passed, failed := true, false
	party := auditpush.Identity{Author: "Ada Writer", Committer: "Forge Bot", CoAuthors: "Claude Code"}
	// Two scans with a gate (one passed, one failed), one with no gate.
	for i, p := range []*bool{&passed, &failed, nil} {
		b := auditpush.Bundle{Scan: auditpush.ScanRow{Repo: "acme/r", ScanID: int64(i + 1), Commit: fmt.Sprintf("c%d", i), Host: "h", Passed: p, Identity: party}}
		if _, err := auditpush.PushBundle(dir+"/", b); err != nil {
			t.Fatal(err)
		}
	}
	code := 0
	// A review whose one checked claim held (a real defect): the change did not.
	r := review.Review{Repo: "acme/r", Commit: "c3", Author: party.Author, Committer: party.Committer, CoAuthors: party.CoAuthors, Scope: "pkg", ReviewerModel: "rev-m", Lang: "go",
		Findings: []review.Finding{{ID: "R1", Declared: review.TierReproduced, Tier: review.TierReproduced, ExitCode: &code}}}
	if _, err := auditpush.WriteReview(dir, r, signer); err != nil {
		t.Fatal(err)
	}
	// A review with nothing checked: no evidence about anyone.
	r2 := review.Review{Repo: "acme/r", Commit: "c4", Author: party.Author, Scope: "pkg", ReviewerModel: "rev-m", Lang: "go",
		Findings: []review.Finding{{ID: "R1", Declared: review.TierHypothesis, Tier: review.TierHypothesis}}}
	if _, err := auditpush.WriteReview(dir, r2, signer); err != nil {
		t.Fatal(err)
	}
	vdb, err := auditpush.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer vdb.Close()
	obs, err := committerRankEvidence(vdb)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][2]int{}
	for _, o := range obs {
		if o.Role != modelrank.SeatCommitter {
			t.Fatalf("wrong seat: %+v", o)
		}
		g := got[o.Model]
		g[0] += o.ChangesAudited
		g[1] += o.ChangesHeld
		got[o.Model] = g
	}
	// Ada: 2 gated scans (1 held) + 1 checked review (0 held) = 3 audits, 1 held. Claude Code the same (co-author of every scan and the checked review).
	if got["Ada Writer"] != [2]int{3, 1} || got["Claude Code"] != [2]int{3, 1} {
		t.Fatalf("audited/held per party: %v", got)
	}
	if _, ok := got["Forge Bot"]; ok {
		t.Fatal("the committer of record is not the party who wrote the change")
	}
	var out, errb bytes.Buffer
	if rcode := runModels([]string{"rank", "--db", dir, "--seat", "committer", "--min-runs", "1"}, t.TempDir(), defaultRankLoader, &out, &errb); rcode != 0 {
		t.Fatalf("rank: %d %s", rcode, errb.String())
	}
	// Segmented by language like every seat: a scan spans the project
	// (no language), a review carries its scope's.
	for _, want := range []string{"Ada Writer", "Claude Code", "1/2 changes held over 2 commits", "committer · go", "0/1 changes held over 1 commits", "the committer seat from corral_scans.author/co_authors"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("rank output lacks %q:\n%s", want, out.String())
		}
	}
	// A warehouse written before the party was recorded is evidence about nobody.
	old := filepath.Join(t.TempDir(), "old.duckdb")
	if _, err := auditpush.PushBundle(old, auditpush.Bundle{Scan: auditpush.ScanRow{Repo: "acme/r", ScanID: 1, Commit: "c", Host: "h", Passed: &passed}}); err != nil {
		t.Fatal(err)
	}
	odb, err := auditpush.LoadDir(dir) // any db with the columns and no party
	if err != nil {
		t.Fatal(err)
	}
	defer odb.Close()
	if _, err := odb.Exec(`UPDATE corral_scans SET author = NULL; UPDATE corral_reviews SET author = NULL`); err != nil {
		t.Fatal(err)
	}
	if obs, err := committerRankEvidence(odb); err != nil || len(obs) != 0 {
		t.Fatalf("rows with no party recorded must grade nobody: %d %v", len(obs), err)
	}
}
