// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"fmt"
	"math"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
)

func TestRepoReportCountsSelectionModes(t *testing.T) {
	results := []FileResult{
		{Gradable: true, Verdict: advpool.Verdict{TestSelection: advpool.TestSelection{Method: "coverage-context", Selected: 3, Of: 10}}},
		{Gradable: true, Verdict: advpool.Verdict{TestSelection: advpool.TestSelection{Fallback: "no selector for ruby"}}},
		{Gradable: true, Verdict: advpool.Verdict{TestSelection: advpool.TestSelection{Method: "coverage-context"}, Uncovered: true}},
	}
	for i := range results {
		results[i].Job.Path = fmt.Sprintf("pkg/f%d.py", i)
	}
	r := Aggregate("o", "r", "c", 3, 3, results, nil) // report.go:178
	if r.SelectedFiles != 2 || r.WholeSuiteFiles != 1 || r.UncoveredFiles != 1 {
		t.Errorf("got selected=%d whole=%d uncovered=%d", r.SelectedFiles, r.WholeSuiteFiles, r.UncoveredFiles)
	}
	var w *WeakFile
	for i := range r.Weakest {
		if r.Weakest[i].Path == "pkg/f2.py" {
			w = &r.Weakest[i]
		}
	}
	if w == nil || !w.Uncovered {
		t.Errorf("uncovered weak file must be marked: %+v", w)
	}
}

// TestUncoveredFileIsExcludedFromTheHeadlineMean pins the leak the per-file
// line already refused: an uncovered file's rate is not a measurement, so
// averaging its 0.0 into the repo headline publishes a number nothing
// measured — and that number is what the scan header persists and the report
// prints first.
func TestUncoveredFileIsExcludedFromTheHeadlineMean(t *testing.T) {
	results := []FileResult{
		{Gradable: true, Verdict: advpool.Verdict{DevKillRate: 1.0, TestSelection: advpool.TestSelection{Method: "coverage-context", Selected: 3, Of: 10}}},
		{Gradable: true, Verdict: advpool.Verdict{DevKillRate: 0, TestSelection: advpool.TestSelection{Method: "coverage-context"}, Uncovered: true}},
	}
	results[0].Job.Path, results[1].Job.Path = "pkg/a.py", "pkg/u.py"
	r := Aggregate("o", "r", "c", 2, 2, results, nil)
	if r.KillRate != 1.0 {
		t.Errorf("headline kill rate = %v, want 1.00: the uncovered file was never graded, so its 0 must not be averaged in", r.KillRate)
	}
	if r.GradedFiles != 1 || r.Audited != 2 || r.UncoveredFiles != 1 {
		t.Errorf("graded=%d audited=%d uncovered=%d: the mean's denominator must be visible and must exclude the uncovered file",
			r.GradedFiles, r.Audited, r.UncoveredFiles)
	}

	// Every audited file uncovered: no mean exists, and a 0.0 would read as
	// "terrible tests" about a measurement nobody made.
	only := []FileResult{{Gradable: true, Verdict: advpool.Verdict{Uncovered: true}}}
	only[0].Job.Path = "pkg/u.py"
	r = Aggregate("o", "r", "c", 1, 1, only, nil)
	if !math.IsNaN(r.KillRate) || r.GradedFiles != 0 {
		t.Errorf("all-uncovered scan: kill rate = %v, graded = %d; want NaN over 0", r.KillRate, r.GradedFiles)
	}
}

// The report is what the printer, the signer and the warehouse read; the
// verdict is not. Whatever the run disclosed about the per-mutant grain has
// to survive the copy — the spread AND the rule counts, and the absence of a
// spread just as faithfully as a measured one.
func TestAggregateCarriesThePerMutantGrainOntoTheReport(t *testing.T) {
	results := []FileResult{
		{Gradable: true, Verdict: advpool.Verdict{TestSelection: advpool.TestSelection{
			Method: advpool.MethodCoverageLines, Selected: 234, Of: 620, PerMutant: true,
			TestsPerMutant: &advpool.TestsPerMutantSpread{Min: 3, Median: 9, Max: 41},
			Rules:          map[string]int{"lines": 30, "static": 4},
		}}},
		// Per-mutant, but the compile gate left nothing graded: no spread was
		// measured, and the report must carry that absence, not three zeros.
		{Gradable: true, Verdict: advpool.Verdict{TestSelection: advpool.TestSelection{
			Method: advpool.MethodCoverageLines, PerMutant: true,
		}}},
	}
	for i := range results {
		results[i].Job.Path = fmt.Sprintf("pkg/f%d.py", i)
	}
	byPath := map[string]WeakFile{}
	for _, w := range Aggregate("o", "r", "c", 2, 2, results, nil).Weakest {
		byPath[w.Path] = w
	}
	got := byPath["pkg/f0.py"]
	if !got.PerMutant || !got.MeasuredSpread() {
		t.Fatalf("the per-mutant grain did not reach the report: %+v", got)
	}
	if *got.TestsPerMutant != (advpool.TestsPerMutantSpread{Min: 3, Median: 9, Max: 41}) {
		t.Errorf("spread = %+v", got.TestsPerMutant)
	}
	if got.Rules["static"] != 4 || got.Rules["lines"] != 30 {
		t.Errorf("rule counts = %v", got.Rules)
	}
	if none := byPath["pkg/f1.py"]; !none.PerMutant || none.MeasuredSpread() {
		t.Errorf("a run that measured no spread must carry none: %+v", none)
	}
}
