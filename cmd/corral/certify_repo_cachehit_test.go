// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/reposcan"
)

// cacheHitResults is one FRESH file and one CACHE-HIT file, both carrying the
// identical spend and clock in their verdict — which is exactly the state the
// ledger cache leaves behind, because Verdict round-trips model_calls and
// timing through verdict_json and ledgerCache.Get restores them verbatim.
//
// The fresh file's numbers were paid for by THIS run. The reused file's were
// paid for by whichever run first earned them, and this run must not report
// them a second time.
func cacheHitResults() []reposcan.FileResult {
	spend := func(calls int) []advpool.ModelCall {
		return []advpool.ModelCall{
			{Role: advpool.RoleMutantGenerator, Model: "m-1", Calls: calls, InputTokens: int64(100 * calls), OutputTokens: int64(10 * calls), Wall: time.Duration(calls) * time.Second},
		}
	}
	clock := advpool.Timing{
		Selection: 30 * time.Second, Generation: time.Minute, Pool: 20 * time.Second,
		DevPass: 10 * time.Minute, AuthoredPass: time.Minute, Critic: 15 * time.Second,
		Total: 13 * time.Minute,
	}
	return []reposcan.FileResult{
		{
			Job: reposcan.Job{Path: "fresh.go", Lang: "go"}, Gradable: true,
			Verdict: advpool.Verdict{
				DevKillRate: 0.5, MutantsTotal: 4,
				ModelCalls: spend(3), Timing: clock,
				BaselineDuration:     45 * time.Second,
				MutantDurationMedian: 20 * time.Second, MutantDurationMax: 40 * time.Second,
			},
		},
		{
			Job: reposcan.Job{Path: "reused.go", Lang: "go"}, Gradable: true,
			CacheHit: true, ComputedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
			Verdict: advpool.Verdict{
				BaselineDuration: 45 * time.Second,
				DevKillRate:      0.9, MutantsTotal: 4,
				ModelCalls: spend(7), Timing: clock,
				MutantDurationMedian: 20 * time.Second, MutantDurationMax: 40 * time.Second,
			},
		},
	}
}

// TestACacheHitBuildsNoModelCallRows: scan_model_calls is a record of what
// THIS scan spent. A reused verdict spent nothing, so it contributes no row —
// not a row of zeros, and certainly not a copy of the spend the run that
// earned it already recorded under its own scan id.
func TestACacheHitBuildsNoModelCallRows(t *testing.T) {
	rows := buildScanModelCallRows(cacheHitResults())
	for _, r := range rows {
		if r.Path != "fresh.go" {
			t.Errorf("a reused verdict produced a scan_model_calls row: %+v", r)
		}
	}
	if len(rows) != 1 {
		t.Fatalf("got %d model-call row(s), want 1 (the fresh file's)", len(rows))
	}
	if rows[0].Calls != 3 || rows[0].InputTokens != 300 {
		t.Errorf("the fresh file's row = %+v, want 3 calls / 300 in", rows[0])
	}
}

// TestACacheHitIsNotInTheScanTotals: the header totals and the `cost:` line
// are both built from these, and a scan that reused a verdict must not report
// tokens it never bought.
func TestACacheHitIsNotInTheScanTotals(t *testing.T) {
	totals := scanModelCallTotals(cacheHitResults())
	if len(totals) != 1 {
		t.Fatalf("scanModelCallTotals = %+v, want one role", totals)
	}
	if totals[0].Calls != 3 || totals[0].InputTokens != 300 || totals[0].OutputTokens != 30 {
		t.Errorf("totals = %+v, want only the fresh file's 3 calls / 300 in / 30 out", totals[0])
	}
	line := costLine(totals)
	if !strings.Contains(line, "3 calls") {
		t.Errorf("cost line does not report the fresh spend: %q", line)
	}
	if strings.Contains(line, "10 calls") || strings.Contains(line, "1k") {
		t.Errorf("cost line re-billed the reused verdict: %q", line)
	}
}

// TestACacheHitStoresNoTiming: the ledger's timing columns say how long THIS
// run took. A reused verdict took no time at all this run, so every phase
// column is NULL — the same NULL a phase that did not run gets, which is the
// honest answer to "how long did this scan spend on this file".
func TestACacheHitStoresNoTiming(t *testing.T) {
	rows := buildScanFileRows(cacheHitResults(), nil, reposcan.CoverageMap{}, "", t.TempDir(), io.Discard)
	byPath := map[string]int{}
	for i, r := range rows {
		byPath[r.Path] = i
	}
	fresh, ok := byPath["fresh.go"]
	if !ok {
		t.Fatal("no row for the fresh file")
	}
	if rows[fresh].TotalMillis == nil || rows[fresh].DevPassMillis == nil {
		t.Error("the FRESH file lost its clock")
	}
	reused, ok := byPath["reused.go"]
	if !ok {
		t.Fatal("no row for the reused file")
	}
	r := rows[reused]
	for name, got := range map[string]*int64{
		"selection_ms":     r.SelectionMillis,
		"generation_ms":    r.GenerationMillis,
		"pool_ms":          r.PoolMillis,
		"dev_pass_ms":      r.DevPassMillis,
		"authored_pass_ms": r.AuthoredPassMillis,
		"critic_ms":        r.CriticMillis,
		"total_ms":         r.TotalMillis,
		"mutant_ms_median": r.MutantMillisMedian,
		"mutant_ms_max":    r.MutantMillisMax,
	} {
		if got != nil {
			t.Errorf("a reused verdict recorded %s = %d — time this scan never spent", name, *got)
		}
	}
}

// TestACacheHitPrintsNoTimeLine: the per-file report line for a reused
// verdict must not print a clock. The seven phases would be read as this
// run's minutes; they are another run's.
func TestACacheHitPrintsNoTimeLine(t *testing.T) {
	var b bytes.Buffer
	printWeakFile(&b, reposcan.WeakFile{
		Path: "reused.go", KillRate: 0.9, CacheHit: true,
		Timing:        advpool.Timing{DevPass: 10 * time.Minute, Total: 13 * time.Minute},
		MutantsGraded: 4,
	})
	if strings.Contains(b.String(), "time:") {
		t.Errorf("a reused verdict printed a clock: %q", b.String())
	}
	var fresh bytes.Buffer
	printWeakFile(&fresh, reposcan.WeakFile{
		Path: "fresh.go", KillRate: 0.5,
		Timing:        advpool.Timing{DevPass: 10 * time.Minute, Total: 13 * time.Minute},
		MutantsGraded: 4,
	})
	if !strings.Contains(fresh.String(), "time:") {
		t.Errorf("a fresh verdict lost its clock: %q", fresh.String())
	}
}

// The baseline-suite wall clock is a timing column like any other: a row
// whose verdict was reused must not record the ORIGINAL run's baseline as
// this scan's. Found by the fix wave's own re-review — every other timing
// column was gated and this one, which predates the timing work, was not.
func TestACacheHitStoresNoBaselineMillis(t *testing.T) {
	rows := buildScanFileRows(cacheHitResults(), nil, reposcan.CoverageMap{}, "", t.TempDir(), io.Discard)
	fresh, reused := -1, -1
	for i, r := range rows {
		switch r.Path {
		case "fresh.go":
			fresh = i
		case "reused.go":
			reused = i
		}
	}
	if fresh < 0 || reused < 0 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[fresh].SuiteBaselineMillis != 45000 {
		t.Errorf("fresh row baseline = %d, want 45000", rows[fresh].SuiteBaselineMillis)
	}
	if rows[reused].SuiteBaselineMillis != 0 {
		t.Errorf("reused row baseline = %d ms — another scan's measurement recorded as this one's; want 0 (stored NULL)", rows[reused].SuiteBaselineMillis)
	}
}
