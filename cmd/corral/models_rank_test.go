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
	"encoding/json"
	"errors"
	"fmt"
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
