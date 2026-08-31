// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// wantTimingLine is the report's per-file clock, exactly. It is a golden
// string rather than a set of substring checks because this ONE line is what
// an operator reads to decide where an audit's minutes went, and every phase
// has to be named on it — including the ones that did not run, which say so
// with an em dash rather than a 0s nobody measured.
// The total is the FILE's own work — pool + generation + dev pass + authored
// + critic, plus the residual between them — and deliberately NOT the 1m32s
// selection, which is one instrumented run shared by every file of the scan
// and is recorded once on the scan header. Selection is still NAMED on the
// line: a readout that skipped it could not account for a phase the file's
// audit really waited on.
const wantTimingLine = "   time: selection 1m32s · generation 4m10s · pool 12s · dev pass 35m04s (39 mutants, median 54s, max 3m12s) · authored 1m49s · critic — · total 41m41s"

func TestTimingLineNamesEveryPhaseOrDash(t *testing.T) {
	got := timingLine(advpool.Timing{
		Selection:    92 * time.Second,
		Generation:   4*time.Minute + 10*time.Second,
		Pool:         12 * time.Second,
		DevPass:      35*time.Minute + 4*time.Second,
		AuthoredPass: 109 * time.Second,
		Total:        41*time.Minute + 41*time.Second,
	}, 39, 54*time.Second, 3*time.Minute+12*time.Second)
	if got != wantTimingLine {
		t.Errorf("timing line:\n got %q\nwant %q", got, wantTimingLine)
	}
}

// TestTimingLineDashesWhatDidNotRun: the jail substrate builds no trees and
// `--critic-model off` runs no critic. Neither spent zero seconds; neither
// happened at all, and the line has to tell them apart from a phase that
// really was instant.
func TestTimingLineDashesWhatDidNotRun(t *testing.T) {
	got := timingLine(advpool.Timing{
		Selection:  92 * time.Second,
		Generation: 4*time.Minute + 10*time.Second,
		DevPass:    35*time.Minute + 4*time.Second,
		Total:      41*time.Minute + 6*time.Second,
	}, 0, 0, 0)
	const want = "   time: selection 1m32s · generation 4m10s · pool — · dev pass 35m04s · authored — · critic — · total 41m06s"
	if got != want {
		t.Errorf("timing line:\n got %q\nwant %q", got, want)
	}
}

// TestReportPrintsTheTimingLine wires the helper to the per-file report line
// the Action copies verbatim into its job summary.
func TestReportPrintsTheTimingLine(t *testing.T) {
	var b bytes.Buffer
	printWeakFile(&b, reposcan.WeakFile{
		Path: "pkg/a.py", KillRate: 0.65,
		Timing: advpool.Timing{
			Selection: 92 * time.Second, Generation: 4*time.Minute + 10*time.Second,
			Pool: 12 * time.Second, DevPass: 35*time.Minute + 4*time.Second,
			AuthoredPass: 109 * time.Second, Total: 41*time.Minute + 41*time.Second,
		},
		MutantsGraded:      39,
		MutantMillisMedian: 54000,
		MutantMillisMax:    192000,
	})
	if !strings.Contains(b.String(), wantTimingLine+"\n") {
		t.Errorf("the report's file line carries no clock:\n%s", b.String())
	}
}

// TestReportSaysNothingWhenNothingWasTimed: a verdict served from a cache row
// written before any of this existed measured no phase at all, and a line of
// seven em dashes is noise, not disclosure.
func TestReportSaysNothingWhenNothingWasTimed(t *testing.T) {
	var b bytes.Buffer
	printWeakFile(&b, reposcan.WeakFile{Path: "pkg/a.py", KillRate: 0.65})
	if strings.Contains(b.String(), "time:") {
		t.Errorf("an untimed file printed a clock:\n%s", b.String())
	}
}

// TestLedgerStoresTheClockAndNullsWhatItDidNotMeasure: the ledger's timing
// columns are *int64 for one reason — a phase nothing timed must read back as
// unknown, not as a phase that cost nothing. Averaged into the cost model, a
// stored 0 says "the pool is free".
func TestLedgerStoresTheClockAndNullsWhatItDidNotMeasure(t *testing.T) {
	results := []reposcan.FileResult{{
		Job:      reposcan.Job{Path: "a.go", Lang: "go"},
		Gradable: true,
		Verdict: advpool.Verdict{
			DevKillRate: 0.5, MutantsTotal: 39,
			Timing: advpool.Timing{
				Selection: 92 * time.Second, Generation: 4*time.Minute + 10*time.Second,
				DevPass: 35*time.Minute + 4*time.Second, Total: 41*time.Minute + 6*time.Second,
			},
			MutantDurationMedian: 54 * time.Second,
			MutantDurationMax:    3*time.Minute + 12*time.Second,
			DevKilledMutants: []advpool.MutantRef{
				{ID: "m1", Duration: 54 * time.Second},
			},
			DevSurvivedMutants: []advpool.MutantRef{
				{ID: "m2"}, // never timed: the ledger must store NULL
			},
		},
	}}
	rows := buildScanFileRows(results, nil, reposcan.CoverageMap{}, "", t.TempDir(), io.Discard)
	if len(rows) != 1 {
		t.Fatalf("got %d file row(s), want 1", len(rows))
	}
	f := rows[0]
	for name, got := range map[string]*int64{
		"selection_ms":  f.SelectionMillis,
		"generation_ms": f.GenerationMillis,
		"dev_pass_ms":   f.DevPassMillis,
		"total_ms":      f.TotalMillis,
	} {
		if got == nil {
			t.Errorf("%s is NULL for a phase that WAS measured", name)
		}
	}
	if f.SelectionMillis != nil && *f.SelectionMillis != 92000 {
		t.Errorf("selection_ms = %d, want 92000", *f.SelectionMillis)
	}
	if f.PoolMillis != nil || f.AuthoredPassMillis != nil || f.CriticMillis != nil {
		t.Errorf("a phase that did not run was stored as a number: pool=%v authored=%v critic=%v",
			f.PoolMillis, f.AuthoredPassMillis, f.CriticMillis)
	}
	if f.MutantMillisMedian == nil || *f.MutantMillisMedian != 54000 {
		t.Errorf("mutant_ms_median = %v, want 54000", f.MutantMillisMedian)
	}
	if f.MutantMillisMax == nil || *f.MutantMillisMax != 192000 {
		t.Errorf("mutant_ms_max = %v, want 192000", f.MutantMillisMax)
	}

	mutants := buildScanMutantRows(1, results)
	if len(mutants) != 2 {
		t.Fatalf("got %d mutant row(s), want 2", len(mutants))
	}
	byID := map[string]scanstore.Mutant{}
	for _, m := range mutants {
		byID[m.MutantID] = m
	}
	if d := byID["m1"].DurationMillis; d == nil || *d != 54000 {
		t.Errorf("m1 duration_ms = %v, want 54000", d)
	}
	if d := byID["m2"].DurationMillis; d != nil {
		t.Errorf("m2 was never timed and stored %v — NULL is the only honest value", *d)
	}
}

// TestRunSpecCarriesTheTwoPhasesTheDriverCannotTime is the hop this codebase
// keeps dropping: a value measured in one layer and silently discarded on the
// way to the next. The scan times its own instrumented selection run and the
// pool discloses its copy + probe; neither number exists anywhere the driver
// can reach it unless the RunSpec carries it.
func TestRunSpecCarriesTheTwoPhasesTheDriverCannotTime(t *testing.T) {
	rs := newAuditRunSpec(localAuditInput{
		selectionDuration: 92 * time.Second,
		concurrency:       &adequacy.Disclosure{Trees: 6, CopyDuration: 8 * time.Second, ProbeDuration: 4 * time.Second},
	}, auditRoles{}, runSubject{})
	if rs.SelectionDuration != 92*time.Second {
		t.Errorf("RunSpec.SelectionDuration = %v, want the scan's measured 1m32s", rs.SelectionDuration)
	}
	if rs.PoolDuration != 12*time.Second {
		t.Errorf("RunSpec.PoolDuration = %v, want copy 8s + probe 4s", rs.PoolDuration)
	}
}

// TestJailSubstrateReportsNoPoolTime: the jail builds no trees, discloses
// nothing, and must not be charged for a phase it never had.
func TestJailSubstrateReportsNoPoolTime(t *testing.T) {
	if d := poolDuration(nil); d != 0 {
		t.Errorf("poolDuration(nil) = %v, want 0 — the jail substrate has no pool", d)
	}
}

// TestSelectionTimeIsRecordedOnlyWhenTheSuiteActuallyRan.
//
// `--whole-suite` instruments NOTHING: collectSelection returns before it
// builds a runner. A clock started around the call regardless recorded a few
// microseconds, which millisOrNil rounded up to a stored 1 and the report
// rendered as `selection 0s` — a scan that ran no selection pass claiming, in
// a signed and pushed record, that it ran one for free. The clock has to be
// gated on the same condition the RUN is.
func TestSelectionTimeIsRecordedOnlyWhenTheSuiteActuallyRan(t *testing.T) {
	orig := collectSelectionEvidence
	t.Cleanup(func() { collectSelectionEvidence = orig })
	collectSelectionEvidence = func(ctx context.Context, runner coverageRunner, files map[string]string, p lang.Plugin, testCmd []string, sourcePaths []string) reposcan.SelectionEvidence {
		time.Sleep(5 * time.Millisecond)
		return reposcan.SelectionEvidence{Ran: true}
	}

	ws := &localExecutor{wholeSuite: true, substrate: substrateWorkspace, repoDir: t.TempDir()}
	ws.selection = ws.collectSelection(context.Background(), []string{"a.py"})
	if ws.selectionDuration != 0 {
		t.Errorf("--whole-suite recorded %v of selection time for a pass that never ran", ws.selectionDuration)
	}

	ran := &localExecutor{substrate: substrateWorkspace, repoDir: t.TempDir()}
	ran.selection = ran.collectSelection(context.Background(), []string{"a.py"})
	if !ran.selection.Ran {
		t.Fatalf("the fixture did not reach the instrumented run: %+v", ran.selection)
	}
	if ran.selectionDuration < 5*time.Millisecond {
		t.Errorf("the instrumented run took at least 5ms and was recorded as %v", ran.selectionDuration)
	}
}

// TestUnmeasuredSelectionIsNullAndDashed is the other half of the same
// ruling, at the two places the number surfaces.
func TestUnmeasuredSelectionIsNullAndDashed(t *testing.T) {
	rows := buildScanFileRows([]reposcan.FileResult{{
		Job: reposcan.Job{Path: "a.go", Lang: "go"}, Gradable: true,
		Verdict: advpool.Verdict{
			DevKillRate: 0.5,
			Timing:      advpool.Timing{DevPass: 35 * time.Minute, Total: 36 * time.Minute},
		},
	}}, nil, reposcan.CoverageMap{}, "", t.TempDir(), io.Discard)
	if len(rows) != 1 || rows[0].SelectionMillis != nil {
		t.Errorf("a scan that ran no selection pass stored %v; NULL is the only honest value", rows[0].SelectionMillis)
	}
	line := timingLine(advpool.Timing{DevPass: 35 * time.Minute, Total: 36 * time.Minute}, 0, 0, 0)
	if !strings.Contains(line, "selection —") {
		t.Errorf("line %q must say selection —, never 0s", line)
	}
}

// TestSubSecondPhasesNeverPrintZero: a phase that ran and took 1ms is not a
// phase that did not run. Rounding it to `0s` is the same lie as storing 0
// for an unmeasured phase, in the other direction — and `—` would be a
// second one, because the phase demonstrably happened.
func TestSubSecondPhasesNeverPrintZero(t *testing.T) {
	if got := durationText(time.Millisecond); got != "<1s" {
		t.Errorf("durationText(1ms) = %q, want %q", got, "<1s")
	}
	if got := durationText(999 * time.Millisecond); got != "<1s" {
		t.Errorf("durationText(999ms) = %q, want %q", got, "<1s")
	}
	if got := durationText(0); got != unmeasured {
		t.Errorf("durationText(0) = %q, want %q", got, unmeasured)
	}
	if got := durationText(time.Second); got != "1s" {
		t.Errorf("durationText(1s) = %q, want %q", got, "1s")
	}
}

// TestSelectionIsRecordedAtTheScanGrain. The instrumented pass runs ONCE for
// a scan, so `sum(total_ms)` over a scan's files must not include it — it
// lives on the scan header, and the bundle carries it there unchanged.
func TestSelectionIsRecordedAtTheScanGrain(t *testing.T) {
	scan := scanstore.Scan{Repo: "o/r", Commit: "deadbeef", SelectionMillis: ms64(92000)}
	b := buildBundle(scan, 11, nil, nil, nil, nil, auditpush.Link{}, false,
		"o/r", "deadbeef", "", bundleMeta{})
	if b.Scan.SelectionMillis == nil || *b.Scan.SelectionMillis != 92000 {
		t.Fatalf("the bundle's scan row carries selection_ms %v, want 92000", b.Scan.SelectionMillis)
	}

	whole := scanstore.Scan{Repo: "o/r", Commit: "deadbeef"}
	wb := buildBundle(whole, 11, nil, nil, nil, nil, auditpush.Link{}, false,
		"o/r", "deadbeef", "", bundleMeta{})
	if wb.Scan.SelectionMillis != nil {
		t.Errorf("a --whole-suite scan pushed selection_ms %d; NULL is the only honest value", *wb.Scan.SelectionMillis)
	}
}

func ms64(v int64) *int64 { return &v }
