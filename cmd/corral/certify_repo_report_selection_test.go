// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/reposcan"
)

func TestPrintWeakFileNamesTheMeasurement(t *testing.T) {
	var b bytes.Buffer
	printWeakFile(&b, reposcan.WeakFile{Path: "pkg/a.py", KillRate: 0.65, SelectionMethod: "coverage-context", SelectedTests: 14, SuiteTests: 1431})
	if !strings.Contains(b.String(), "graded by 14 of 1431 tests (coverage-context)") {
		t.Errorf("got %q", b.String())
	}
	b.Reset()
	printWeakFile(&b, reposcan.WeakFile{Path: "lib/a.rb", KillRate: 0.9, SelectionFallback: "no selector for ruby"})
	if !strings.Contains(b.String(), "graded by the whole suite (no selector for ruby)") {
		t.Errorf("got %q", b.String())
	}
	b.Reset()
	printWeakFile(&b, reposcan.WeakFile{Path: "pkg/u.py", Uncovered: true, ProvenMissed: 3, SelectionMethod: "coverage-context"})
	if !strings.Contains(b.String(), "[UNCOVERED — no test executes this file]") || strings.Contains(b.String(), "0.00") {
		t.Errorf("uncovered must withhold the rate: %q", b.String())
	}
	// The "which measurement is this" clause is printed on EVERY line — the
	// point of it is that a reader never has to infer the mode from an
	// absence. The uncovered case had no clause at all, so the one line
	// where the mode is most load-bearing was the one line that said nothing.
	if !strings.Contains(b.String(), "graded by the tests for this file — none execute it (coverage-context)") {
		t.Errorf("uncovered must still say which measurement it is: %q", b.String())
	}

	// A per-mutant run whose every mutant was rejected by the compile gate
	// carries PerMutant with a spread of {0,0,0} — a reachable state, and
	// "0 to 0 per mutant, median 0" would report a range nothing measured as
	// though it had been.
	b.Reset()
	printWeakFile(&b, reposcan.WeakFile{Path: "pkg/none.py", KillRate: 0, SelectionMethod: "coverage-lines", PerMutant: true})
	if strings.Contains(b.String(), "per mutant") {
		t.Errorf("an unmeasured spread must not be printed as a range: %q", b.String())
	}
	if !strings.Contains(b.String(), "(coverage-lines; no mutant graded)") {
		t.Errorf("a per-mutant run that graded nothing must say so: %q", b.String())
	}
}

func TestMinKillRateFailsAnUncoveredFile(t *testing.T) {
	min := 0.5
	// Audited: 1 so the Audited == 0 early return cannot answer this, and a
	// rate ABOVE the threshold so ONLY the Uncovered branch can fail it.
	r := reposcan.RepoReport{Audited: 1, GradedFiles: 1, Weakest: []reposcan.WeakFile{{Path: "pkg/u.py", Uncovered: true, KillRate: 0.9}}}
	if code := repoScanExitCode(r, false, &min, nil); code != 1 {
		t.Errorf("exit %d, want 1: an uncovered file under a --min-kill-rate gate is a failure, not a pass on a withheld number", code)
	}
}

// TestPrintWeakFileNamesThePerMutantMeasurement pins the disclosure at the
// grain the grading now happens: when each mutant was graded by the tests
// that reach ITS lines, "234 of 620 tests" is the file's UNION, not what any
// one mutant faced. A line that printed only the union would let a reader
// take 234 for the number every mutant survived.
func TestPrintWeakFileNamesThePerMutantMeasurement(t *testing.T) {
	var b bytes.Buffer
	printWeakFile(&b, reposcan.WeakFile{
		Path: "src/flask/cli.py", KillRate: 0.65,
		SelectionMethod: "coverage-lines", SelectedTests: 234, SuiteTests: 620,
		PerMutant: true, TestsPerMutant: &advpool.TestsPerMutantSpread{Min: 3, Median: 9, Max: 41},
	})
	want := "graded by 234 of 620 tests — 3 to 41 per mutant, median 9 (coverage-lines)"
	if !strings.Contains(b.String(), want) {
		t.Errorf("got %q, want it to contain %q", b.String(), want)
	}
	// The method comes from the verdict, never from a hardcoded string here:
	// a per-mutant run's selection IS "coverage-lines", and a printer that
	// stamped the label itself would keep saying so after the measurement
	// changed underneath it.
	if strings.Contains(b.String(), "coverage-context") {
		t.Errorf("the method must be the verdict's, not invented: %q", b.String())
	}

	// Without PerMutant the line is byte-identical to what it has always
	// been — the per-mutant clause is an ADDITION, not a rewrite.
	b.Reset()
	printWeakFile(&b, reposcan.WeakFile{Path: "pkg/a.py", KillRate: 0.65, SelectionMethod: "coverage-context", SelectedTests: 14, SuiteTests: 1431})
	if !strings.Contains(b.String(), "graded by 14 of 1431 tests (coverage-context)") {
		t.Errorf("got %q", b.String())
	}
	if strings.Contains(b.String(), "per mutant") {
		t.Errorf("a run that did NOT grade per mutant must not claim it did: %q", b.String())
	}
}
