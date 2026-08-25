// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"testing"

	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/reposcan"
)

// TestScanSharesOneMeterAcrossFiles pins the fix for a whole-repo scan being
// unable to say what it spent.
//
// Each file's audit built its own UsageMeter and discarded it, so `certify
// --repo` — the mode that runs a full herd per file and actually costs money —
// reported no usage at all. The executor now holds ONE meter and threads it
// into every job's input; without that, totals die with each file.
func TestScanSharesOneMeterAcrossFiles(t *testing.T) {
	ex := newLocalExecutor(t.TempDir(), []string{"go", "test", "./..."}, "jail", 0, nil)
	if ex.meter == nil {
		t.Fatal("executor has no meter: a scan cannot report what it spent")
	}

	a := ex.auditInputFor(reposcan.Job{Path: "a.go", TestPath: "a_test.go", Lang: "go"})
	b := ex.auditInputFor(reposcan.Job{Path: "b.go", TestPath: "b_test.go", Lang: "go"})

	if a.meter == nil || b.meter == nil {
		t.Fatal("per-file audit input carries no meter")
	}
	if a.meter != b.meter {
		t.Fatal("two files got DIFFERENT meters: per-file totals cannot sum to a run total")
	}
	if a.meter != ex.meter {
		t.Fatal("file meter is not the executor's meter")
	}
}

// The totals must reflect what the shared meter observed, and a nil meter must
// report zero rather than panicking.
func TestMeterTotalsReportsAndToleratesNil(t *testing.T) {
	ex := newLocalExecutor(t.TempDir(), nil, "jail", 0, nil)
	ex.meter.Add(agentbackend.Usage{InputTokens: 120, OutputTokens: 7})
	ex.meter.Add(agentbackend.Usage{InputTokens: 30, OutputTokens: 3})

	in, out, calls := ex.meterTotals()
	if in != 150 || out != 10 || calls != 2 {
		t.Fatalf("meterTotals() = %d in / %d out / %d calls, want 150/10/2", in, out, calls)
	}

	var empty localExecutor
	if in, out, calls := empty.meterTotals(); in != 0 || out != 0 || calls != 0 {
		t.Fatalf("nil-meter totals = %d/%d/%d, want zeros", in, out, calls)
	}
}
