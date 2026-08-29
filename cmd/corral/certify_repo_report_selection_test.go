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
	printWeakFile(&b, reposcan.WeakFile{Path: "pkg/u.py", Uncovered: true, ProvenMissed: 3})
	if !strings.Contains(b.String(), "[UNCOVERED — no test executes this file]") || strings.Contains(b.String(), "0.00") {
		t.Errorf("uncovered must withhold the rate: %q", b.String())
	}
}

func TestMinKillRateFailsAnUncoveredFile(t *testing.T) {
	min := 0.5
	r := reposcan.RepoReport{Weakest: []reposcan.WeakFile{{Path: "pkg/u.py", Uncovered: true, KillRate: 0}}}
	if code := repoScanExitCode(r, false, &min, nil); code != 1 {
		t.Errorf("exit %d, want 1: an uncovered file under a --min-kill-rate gate is a failure, not a pass on a withheld number", code)
	}
}
