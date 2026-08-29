// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"strings"
	"testing"

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
