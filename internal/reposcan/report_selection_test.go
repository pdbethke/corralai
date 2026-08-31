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

// THE RESIDUAL: ImportOnly must reach the report (WeakFile, RepoReport)
// exactly the way Uncovered does, distinguished on its own — a genuinely
// dead file and an import-only one are BOTH excluded from GradedFiles (the
// same withholding), but only ImportOnlyFiles counts the latter, so a
// printer can tell them apart without re-deriving the distinction.
func TestRepoReportCarriesImportOnlyDistinctFromUncovered(t *testing.T) {
	results := []FileResult{
		{Gradable: true, Verdict: advpool.Verdict{TestSelection: advpool.TestSelection{Method: "coverage-context"}, Uncovered: true}},
		{Gradable: true, Verdict: advpool.Verdict{TestSelection: advpool.TestSelection{Method: "coverage-context"}, Uncovered: true, ImportOnly: true}},
		{Gradable: true, Verdict: advpool.Verdict{DevKillRate: 1.0, TestSelection: advpool.TestSelection{Method: "coverage-context", Selected: 3, Of: 10}}},
	}
	results[0].Job.Path, results[1].Job.Path, results[2].Job.Path = "pkg/dead.py", "pkg/__init__.py", "pkg/a.py"
	r := Aggregate("o", "r", "c", 3, 3, results, nil)

	if r.UncoveredFiles != 2 {
		t.Errorf("UncoveredFiles = %d, want 2 (BOTH shapes withhold the rate)", r.UncoveredFiles)
	}
	if r.ImportOnlyFiles != 1 {
		t.Errorf("ImportOnlyFiles = %d, want 1 (a SUBSET of UncoveredFiles)", r.ImportOnlyFiles)
	}
	if r.GradedFiles != 1 || r.KillRate != 1.0 {
		t.Errorf("GradedFiles=%d KillRate=%v, want 1/1.0 — neither withheld shape enters the mean", r.GradedFiles, r.KillRate)
	}

	byPath := map[string]WeakFile{}
	for _, w := range r.Weakest {
		byPath[w.Path] = w
	}
	if w := byPath["pkg/dead.py"]; !w.Uncovered || w.ImportOnly {
		t.Errorf("pkg/dead.py = %+v, want Uncovered=true ImportOnly=false", w)
	}
	if w := byPath["pkg/__init__.py"]; !w.Uncovered || !w.ImportOnly {
		t.Errorf("pkg/__init__.py = %+v, want Uncovered=true ImportOnly=true", w)
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
	for _, w := range Aggregate("o", "r", "c", 3, 3, results, nil).Weakest {
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

// TestAggregateCarriesConcurrencyOntoTheReport pins the report's half of
// "every reader says how many trees scored the file, or why one" — the
// printer, the signer and the warehouse all read the report, never the
// verdict, so a Concurrency that stopped at the verdict would be invisible
// everywhere that matters.
func TestAggregateCarriesConcurrencyOntoTheReport(t *testing.T) {
	results := []FileResult{
		{Gradable: true, Verdict: advpool.Verdict{Concurrency: advpool.Concurrency{
			Trees: 6, Shared: []string{".venv"},
		}}},
		{Gradable: true, Verdict: advpool.Verdict{Concurrency: advpool.Concurrency{
			Trees: 1, Note: "suite is not concurrency-safe: baseline failed under 3",
		}}},
		// A verdict served from the ledger cache, recorded before this
		// column existed: it carries NO Concurrency at all. That must reach
		// the report as the "not recorded" 0 — the printer then prints no
		// line and the signer signs no key — and never be rounded to a 1
		// the cached run never measured.
		{Gradable: true, CacheHit: true, Verdict: advpool.Verdict{}},
	}
	results[0].Job.Path, results[1].Job.Path, results[2].Job.Path = "pkg/a.py", "pkg/b.py", "pkg/cached.py"
	byPath := map[string]WeakFile{}
	for _, w := range Aggregate("o", "r", "c", 3, 3, results, nil).Weakest {
		byPath[w.Path] = w
	}
	if got := byPath["pkg/a.py"]; got.Trees != 6 || got.ConcurrencyNote != "" {
		t.Errorf("got Trees=%d Note=%q, want Trees=6, no note", got.Trees, got.ConcurrencyNote)
	}
	// The dep dirs the trees SHARE are disclosure too: they are the channel
	// between trees the isolation argument otherwise rules out.
	if got := byPath["pkg/a.py"]; len(got.SharedDirs) != 1 || got.SharedDirs[0] != ".venv" {
		t.Errorf("got SharedDirs=%q, want [.venv] on the report", got.SharedDirs)
	}
	if got := byPath["pkg/b.py"]; got.Trees != 1 || got.ConcurrencyNote != "suite is not concurrency-safe: baseline failed under 3" {
		t.Errorf("got Trees=%d Note=%q, want the downgrade note preserved", got.Trees, got.ConcurrencyNote)
	}
	if got := byPath["pkg/cached.py"]; got.Trees != 0 || got.ConcurrencyNote != "" {
		t.Errorf("got Trees=%d Note=%q, want 0: a cache hit with no Concurrency measured nothing", got.Trees, got.ConcurrencyNote)
	}
}
