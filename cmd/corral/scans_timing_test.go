// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/scanstore"
)

func ms(v int64) *int64 { return &v }

// TestScansShowTimingIsTheSameLine: the report prints a file's clock the
// moment the audit finishes, and this command prints it back out of the
// ledger months later. They go through ONE helper, so a reader comparing a
// stored scan with the run that produced it is comparing the same sentence —
// not two renderings that can drift.
func TestScansShowTimingIsTheSameLine(t *testing.T) {
	r := &fakeScansReader{files: []scanstore.File{{
		Path: "pkg/a.py", Disposition: "audited", Gradable: true,
		SelectionMillis: ms(92000), GenerationMillis: ms(250000), PoolMillis: ms(12000),
		DevPassMillis: ms(2104000), AuthoredPassMillis: ms(109000),
		TotalMillis:        ms(2501000),
		MutantsGraded:      39,
		MutantMillisMedian: ms(54000), MutantMillisMax: ms(192000),
	}}}
	r.scan = scanstore.ScanRow{ID: 1, Scan: scanstore.Scan{SelectionMillis: ms(92000)}}
	var out, errOut bytes.Buffer
	if code := runScans([]string{"show", "1", "--timing"}, openFake(r), &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), wantTimingLine) {
		t.Errorf("`scans show --timing` did not print the report's own line:\n%s", out.String())
	}
	// And the scan grain says it ONCE, where the run happened. Without this
	// the only selection number a reader sees is the per-file copy, which
	// they will sum.
	if !strings.Contains(out.String(), "selection 1m32s (once per scan)") {
		t.Errorf("the scan header did not name the one instrumented run:\n%s", out.String())
	}
}

// TestScansShowTimingSaysNothingAboutAnUnrunSelection: a --whole-suite scan
// instrumented nothing, so the header has nothing to report — and must not
// print "selection —  (once per scan)" as though something were missing.
func TestScansShowTimingSaysNothingAboutAnUnrunSelection(t *testing.T) {
	r := &fakeScansReader{files: []scanstore.File{{
		Path: "pkg/a.py", Disposition: "audited", Gradable: true, DevPassMillis: ms(2104000),
	}}, scan: scanstore.ScanRow{ID: 1}}
	var out, errOut bytes.Buffer
	if code := runScans([]string{"show", "1", "--timing"}, openFake(r), &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut.String())
	}
	if strings.Contains(out.String(), "once per scan") {
		t.Errorf("a scan that instrumented nothing announced a selection run:\n%s", out.String())
	}
}

// TestScansShowWithoutTimingSaysNothing: --timing is opt-in, and the table is
// already wide.
func TestScansShowWithoutTimingSaysNothing(t *testing.T) {
	r := &fakeScansReader{files: []scanstore.File{{
		Path: "pkg/a.py", Disposition: "audited", Gradable: true, DevPassMillis: ms(2104000),
	}}}
	var out, errOut bytes.Buffer
	if code := runScans([]string{"show", "1"}, openFake(r), &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut.String())
	}
	if strings.Contains(out.String(), "time:") {
		t.Errorf("`scans show` printed the timing line without being asked:\n%s", out.String())
	}
}

// TestScansShowTimingPrintsTheCostLineBesideTheTimingLine: the money half of
// the same readout — one costLine per file, built from that file's own
// scan_model_calls rows, right under its timing line.
func TestScansShowTimingPrintsTheCostLineBesideTheTimingLine(t *testing.T) {
	r := &fakeScansReader{files: []scanstore.File{{
		Path: "pkg/a.py", Disposition: "audited", Gradable: true, DevPassMillis: ms(2104000),
	}}}
	r.modelCalls = []scanstore.ModelCall{
		{ScanID: 1, Path: "pkg/a.py", Role: "mutant-generator", Model: "m-1", Calls: 24, InputTokens: 900_000, OutputTokens: 31_000},
		{ScanID: 1, Path: "pkg/a.py", Role: "test-writer", Model: "w-1", Calls: 5, InputTokens: 300_000, OutputTokens: 17_000},
	}
	var out, errOut bytes.Buffer
	if code := runScans([]string{"show", "1", "--timing"}, openFake(r), &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut.String())
	}
	want := "  cost: 1.2M tokens in / 48k out across 29 calls — mutant-generator 0.9M/31k (24 calls), test-writer 0.3M/17k (5 calls)"
	if !strings.Contains(out.String(), want) {
		t.Errorf("`scans show --timing` did not print the cost line:\n%s\nwant it to contain:\n%s", out.String(), want)
	}
}

// TestScansShowTimingSaysNothingAboutCostWhenNoCallsWereRecorded: a scan
// ledger written before scan_model_calls existed (or a run that made no
// calls at all) must not print an empty "  cost: " line.
func TestScansShowTimingSaysNothingAboutCostWhenNoCallsWereRecorded(t *testing.T) {
	r := &fakeScansReader{files: []scanstore.File{{
		Path: "pkg/a.py", Disposition: "audited", Gradable: true, DevPassMillis: ms(2104000),
	}}}
	var out, errOut bytes.Buffer
	if code := runScans([]string{"show", "1", "--timing"}, openFake(r), &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut.String())
	}
	if strings.Contains(out.String(), "cost:") {
		t.Errorf("a scan with no recorded model calls printed a cost line:\n%s", out.String())
	}
}

// TestScansShowTimingSaysNothingForAnUntimedFile: every row corral recorded
// before this change has NULL in all seven columns, and an em-dashed line for
// a file nothing timed would read as a measurement of nothing.
func TestScansShowTimingSaysNothingForAnUntimedFile(t *testing.T) {
	r := &fakeScansReader{files: []scanstore.File{{Path: "old.py", Disposition: "audited", Gradable: true}}}
	var out, errOut bytes.Buffer
	if code := runScans([]string{"show", "1", "--timing"}, openFake(r), &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut.String())
	}
	if strings.Contains(out.String(), "time:") {
		t.Errorf("an untimed ledger row printed a clock:\n%s", out.String())
	}
}
