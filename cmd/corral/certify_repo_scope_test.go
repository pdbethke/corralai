// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"reflect"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/reposcan"
)

// pyEvidence is one instrumented run's answer for a repo where
// tests/test_target.py (and nothing else) executes src/pkg/target.py.
const pyEvidence = `{"format":"corral-selection-3","tests":2,"files":{` +
	`"src/pkg/target.py":{"tests":["tests/test_target.py::test_t"],"lines":{},"static":[]},` +
	`"tests/test_target.py":{"tests":["tests/test_target.py::test_t"],"lines":{},"static":[]},` +
	`"src/pkg/other.py":{"tests":["tests/test_other.py::test_o"],"lines":{},"static":[]},` +
	`"tests/test_other.py":{"tests":["tests/test_other.py::test_o"],"lines":{},"static":[]}}}`

// TestSelectionResolvesThePerJobCommand covers the one change that decides
// whether corral is practical. Scoring runs the target's whole suite once per
// mutant, so an audit costs O(mutants × suite runtime): 1.46s/suite on flask
// but 77s on psf/requests, where the suite is ~96% of a file's audit.
// Narrowing to the tests that DEMONSTRABLY EXECUTE the file collapses that
// multiplier.
//
// It replaces --scope-tests, which narrowed by FILENAME convention and
// inverted verdicts (requests/adapters.py 1.00 -> 0.00) because the file's
// real coverage lived in a test that convention never paired it with. Only
// execution evidence is acceptable here.
//
// Applied at testCmd() deliberately — that one function's result becomes BOTH
// the baseline command and the executor's scoring command, so the two cannot
// disagree about what "the suite" means. A narrowed scoring run graded against
// an unnarrowed baseline would compare different things and silently corrupt
// every kill rate.
func TestSelectionResolvesThePerJobCommand(t *testing.T) {
	job := reposcan.Job{Path: "src/pkg/target.py", TestPath: "tests/test_target.py", Lang: "python"}
	evidence := reposcan.SelectionEvidence{Ran: true, Raw: []byte(pyEvidence)}

	t.Run("no evidence: the whole suite, disclosed", func(t *testing.T) {
		ex := newLocalExecutor(t.TempDir(), nil, substrateWorkspace, 0, nil)
		ex.selection = reposcan.SelectionEvidence{Note: "no selector for python"}
		sel := ex.selectionFor(job)
		if sel.Method != "" || sel.Fallback == "" {
			t.Fatalf("selection = %+v — with no evidence the whole suite runs, and the record must say why", sel)
		}
		got := ex.testCmd(job, sel)
		if last := got[len(got)-1]; last == job.TestPath {
			t.Fatalf("testCmd = %v — with no evidence there is nothing to narrow to", got)
		}
	})

	t.Run("evidence: only the tests that executed the file", func(t *testing.T) {
		ex := newLocalExecutor(t.TempDir(), nil, substrateWorkspace, 0, nil)
		ex.selection = evidence
		sel := ex.selectionFor(job)
		if sel.Method != "coverage-context" {
			t.Fatalf("selection = %+v, want a coverage-context method", sel)
		}
		if len(sel.Tests) != 1 || sel.Tests[0] != "tests/test_target.py::test_t" {
			t.Fatalf("selection tests = %v, want only the test that executed the file", sel.Tests)
		}
		got := ex.testCmd(job, sel)
		if len(got) == 0 || got[len(got)-1] != "tests/test_target.py::test_t" {
			t.Fatalf("testCmd = %v, want the selected node id as the final argument", got)
		}
	})

	t.Run("--whole-suite opts out, and discloses itself", func(t *testing.T) {
		ex := newLocalExecutor(t.TempDir(), nil, substrateWorkspace, 0, nil)
		ex.selection = evidence
		ex.wholeSuite = true
		sel := ex.selectionFor(job)
		if sel.Method != "" || sel.Fallback != "--whole-suite" {
			t.Fatalf("selection = %+v — --whole-suite is a different MEASUREMENT and the verdict must record it", sel)
		}
		got := ex.testCmd(job, sel)
		if last := got[len(got)-1]; last == "tests/test_target.py::test_t" {
			t.Fatalf("testCmd = %v — --whole-suite must not narrow", got)
		}
	})

	t.Run("an explicit -- command is what gets narrowed, never replaced", func(t *testing.T) {
		// The operator naming a command has already chosen the surface; the
		// selection may only narrow within it, and with no evidence it must
		// come through untouched.
		explicit := []string{"make", "test"}
		ex := newLocalExecutor(t.TempDir(), explicit, substrateWorkspace, 0, nil)
		got := ex.testCmd(job, lang.Selection{Fallback: "no evidence"})
		if len(got) != 2 || got[0] != "make" {
			t.Fatalf("testCmd = %v, want the operator's explicit command untouched", got)
		}
		if base := ex.baseCmd(job); len(base) != 2 || base[0] != "make" {
			t.Fatalf("baseCmd = %v, want the operator's explicit command", base)
		}
	})

	t.Run("a file the evidence never saw falls back to the full suite", func(t *testing.T) {
		ex := newLocalExecutor(t.TempDir(), nil, substrateWorkspace, 0, nil)
		ex.selection = evidence
		sel := ex.selectionFor(reposcan.Job{Path: "a.py", TestPath: "tests/test_a.py", Lang: "python"})
		if sel.Fallback == "" {
			t.Fatalf("selection = %+v — a file whose paired test the suite never ran must not be accused of being uncovered", sel)
		}
		got := ex.testCmd(reposcan.Job{Path: "a.py", Lang: "python"}, sel)
		if len(got) == 0 {
			t.Fatal("testCmd returned nothing")
		}
		if last := got[len(got)-1]; last == "" {
			t.Fatalf("testCmd = %v — with nothing to narrow to it must fall back, never emit a dangling path", got)
		}
	})

	t.Run("a language with no selector is unchanged", func(t *testing.T) {
		ex := newLocalExecutor(t.TempDir(), nil, substrateWorkspace, 0, nil)
		ex.selection = evidence
		goJob := reposcan.Job{Path: "a.go", TestPath: "a_test.go", Lang: "go"}
		sel := ex.selectionFor(goJob)
		if sel.Method != "" || sel.Fallback == "" {
			t.Fatalf("selection = %+v — go implements no TestSelector, so it grades whole-suite and says so", sel)
		}
		got := ex.testCmd(goJob, sel)
		if len(got) > 0 && got[len(got)-1] == goJob.TestPath {
			t.Fatalf("testCmd = %v — go must keep its stock command", got)
		}
	})
}

// I2. The executor's own baseline command and the advpool dev pass's command
// must be the SAME command for the same job — a narrowed scoring run graded
// against an unnarrowed baseline compares different things. They are two
// separate resolutions in two packages, so pin them against each other rather
// than trusting that both were remembered.
//
// The UNCOVERED case is the one that had drifted: the dev pass ran the paired
// test file alone (the measurement Uncovered means), while the executor's
// baseline still ran the operator's whole command.
func TestExecutorBaselineMatchesTheDevPassCommand(t *testing.T) {
	job := reposcan.Job{Path: "src/pkg/target.py", TestPath: "tests/test_target.py", Lang: "python"}
	for _, tc := range []struct {
		name string
		sel  lang.Selection
	}{
		{"selected", lang.Selection{
			Base: []string{"pytest"}, Cmd: []string{"pytest", "tests/test_target.py::test_t"},
			Tests: []string{"tests/test_target.py::test_t"}, Method: "coverage-context",
		}},
		{"uncovered", lang.Selection{Base: []string{"pytest"}, Method: "coverage-context"}},
		{"whole suite", lang.Selection{Fallback: "--whole-suite"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ex := newLocalExecutor(t.TempDir(), []string{"pytest", "tests/"}, substrateWorkspace, 0, nil)
			got := ex.testCmd(job, tc.sel)
			want := advpool.DevCommandArgv(tc.sel, job.Lang, ex.baseCmd(job), job.TestPath)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("executor baseline = %v, dev pass = %v — they must be one command", got, want)
			}
		})
	}
}
