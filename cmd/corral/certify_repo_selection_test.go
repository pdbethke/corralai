// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/reposcan"
)

func TestScopeTestsIsRemovedWithAPointer(t *testing.T) {
	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{"--repo", t.TempDir(), "--dry-run", "--scope-tests"}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "--scope-tests was removed") || !strings.Contains(errb.String(), "--whole-suite") {
		t.Errorf("stderr must name the removal and the replacement: %s", errb.String())
	}
}

func TestWholeSuiteFlagParses(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runCertifyRepo([]string{"--repo", t.TempDir(), "--dry-run", "--whole-suite"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
}

func TestAuditConfigKeyNamesTheSelectionMethod(t *testing.T) {
	a := auditConfigKey(false, "coverage-context", nil, "", "")
	b := auditConfigKey(false, "", nil, "", "")
	c := auditConfigKey(true, "", nil, "", "")
	if a == b || b == c || a == c {
		t.Errorf("selection method must move the key: %q %q %q", a, b, c)
	}
	if !strings.Contains(a, "test-selection=coverage-context") || !strings.Contains(c, "whole-suite=true") {
		t.Errorf("keys must spell the mode: %q %q", a, c)
	}
}

// The executor's per-job command is the selection's, and the job's
// RunSpec/JailScorer carry it — pinned through auditInputFor so a future
// refactor cannot resolve the command in one place and forget the other.
func TestAuditInputCarriesTheSelection(t *testing.T) {
	ex := &localExecutor{repoDir: t.TempDir(), substrate: substrateWorkspace}
	py, _ := lang.ByName("python")
	ex.selection = reposcan.NewSelectionEvidence(py, py.TestCmd(), []byte(`{"format":"corral-selection-3","tests":1,"files":{`+
		`"pkg/a.py":{"tests":["tests/test_a.py::test_x"],"lines":{},"static":[]},`+
		`"tests/test_a.py":{"tests":["tests/test_a.py::test_x"],"lines":{},"static":[]}}}`))
	in := ex.auditInputFor(reposcan.Job{Path: "pkg/a.py", TestPath: "tests/test_a.py", Lang: "python"})
	if in.selection.Method != "coverage-context" || len(in.selection.Tests) != 1 {
		t.Errorf("selection not carried: %+v", in.selection)
	}
	if got := strings.Join(in.checkArgv, " "); !strings.HasSuffix(got, "tests/test_a.py::test_x") {
		t.Errorf("checkArgv must be the narrowed command, got %q", got)
	}
	ex.wholeSuite = true
	in = ex.auditInputFor(reposcan.Job{Path: "pkg/a.py", TestPath: "tests/test_a.py", Lang: "python"})
	if in.selection.Method != "" || in.selection.Fallback != "--whole-suite" {
		t.Errorf("--whole-suite must disclose itself: %+v", in.selection)
	}
}

// A pytest node id for a PARAMETRIZED test contains a space, so an argv
// carrying one cannot be re-split with strings.Fields. The baseline runner
// joins and the scorer splits, so the pair must round-trip exactly — a
// baseline that fails on a mangled node id reads as COULD-NOT-GRADE.
func TestSelectionCommandSurvivesShellRoundTrip(t *testing.T) {
	cmd := []string{"pytest", "-q", "tests/test_a.py::test_x[hello world]"}
	got := adequacy.ShellSplit(adequacy.ShellJoin(cmd))
	if len(got) != len(cmd) {
		t.Fatalf("round trip changed the argv length: %q -> %q", cmd, got)
	}
	for i := range cmd {
		if got[i] != cmd[i] {
			t.Fatalf("round trip mangled %q -> %q", cmd, got)
		}
	}
}

// I4. The scan-level AuditConfig names the MODE; the per-file component has
// to name the actual measurement, because WHICH tests were selected can move
// without any digest in the key moving with it. The selection comes from
// coverage EVIDENCE, so an ordinary source change elsewhere in the repo — a
// new import, a changed branch — can route a test through this file or away
// from it while every test file, the argv and the file itself are byte-
// identical. Without the ids in the key, the next scan serves a verdict
// measured by a set of tests that no longer grade this file.
func TestFileSelectionKeyCoversTheSelectedTests(t *testing.T) {
	a := fileSelectionKey(lang.Selection{Method: "coverage-context", Tests: []string{"t/a.py::x", "t/b.py::y"}})
	b := fileSelectionKey(lang.Selection{Method: "coverage-context", Tests: []string{"t/a.py::x", "t/b.py::z"}})
	if a == b {
		t.Fatalf("two selections differing in one id key identically: %q", a)
	}
	if !strings.Contains(a, "file-selection=coverage-context") || !strings.Contains(a, "selected-tests=") {
		t.Fatalf("key must name both the mode and the digest: %q", a)
	}
	// Order is not a measurement: the same set keys the same either way.
	rev := fileSelectionKey(lang.Selection{Method: "coverage-context", Tests: []string{"t/b.py::y", "t/a.py::x"}})
	if rev != a {
		t.Fatalf("id ORDER moved the key: %q vs %q", a, rev)
	}
	// The two no-selection modes carry a mode and no digest — there is no
	// selected set to digest, and inventing one would be a fabricated number.
	for _, sel := range []lang.Selection{{}, {Method: "coverage-context"}} {
		if k := fileSelectionKey(sel); strings.Contains(k, "selected-tests=") {
			t.Errorf("%+v keyed a digest of nothing: %q", sel, k)
		}
	}
	if k := fileSelectionKey(lang.Selection{}); k != "file-selection=whole-suite" {
		t.Errorf("whole-suite key = %q", k)
	}
	if k := fileSelectionKey(lang.Selection{Method: "coverage-context"}); k != "file-selection=uncovered" {
		t.Errorf("uncovered key = %q", k)
	}
}

// TestResolveSelectionPluginMixedGoPythonReachesPython pins a consequence of
// goPlugin.CoverageCmd claiming only `go test`-shaped commands: the language
// choice it feeds is also TEST SELECTION's (resolveSelectionPlugin drives
// collectSelection, selectionCachePeek and the evidence-based candidacy
// widening). A repository with Go and Python sources given `-- pytest -q`
// used to resolve no plugin — the scan graded against the whole suite — and
// now resolves python, whose instrumented selection command is what runs.
// Asserted at the two seams that need no substrate: the resolution itself,
// and reposcan.InstrumentedCommand, THE command collectSelectionEvidence runs.
func TestResolveSelectionPluginMixedGoPythonReachesPython(t *testing.T) {
	ex := &localExecutor{checkArgv: []string{"pytest", "-q"}}
	sources := []string{"cmd/tool/main.go", "pkg/lib.py"}
	plug, langName, testCmd, note := ex.resolveSelectionPlugin(sources)
	if plug == nil || langName != "python" {
		t.Fatalf("resolveSelectionPlugin = (%v, %q, %q, %q); want the python plugin", plug, langName, testCmd, note)
	}
	if strings.Join(testCmd, " ") != "pytest -q" {
		t.Errorf("testCmd = %q, want the operator's own `pytest -q`", testCmd)
	}
	if _, ok := reposcan.InstrumentedCommand(plug, testCmd, sources); !ok {
		t.Errorf("python refused to instrument `pytest -q` for selection; the selection run would not happen")
	}
}

// The baseline retry for per-test evidence: a test that FAILED in the
// instrumented run is in the job's selection (and its every mutant's
// command), so a baseline that is red for its sake is retried without it,
// graded green, and the left-out test is named — the same retry a red unit
// gets.
func TestARedPythonBaselineIsRetriedWithoutTheTestThatFailedInTheEvidenceRun(t *testing.T) {
	ex := &localExecutor{repoDir: t.TempDir(), substrate: substrateWorkspace, baselineRuns: 2}
	py, _ := lang.ByName("python")
	ex.selection = reposcan.NewSelectionEvidence(py, py.TestCmd(), []byte(`{"format":"corral-selection-3","tests":2,`+
		`"failed":["tests/test_a.py::test_red"],"files":{`+
		`"pkg/a.py":{"tests":["tests/test_a.py::test_x","tests/test_a.py::test_red"],"lines":{"0":[[1,1]],"1":[[2,2]]},"static":[],"statements":2}}}`))
	var out strings.Builder
	ex.progress = &out
	var cmds [][]string
	ex.newBaseline = func(_ context.Context, in localAuditInput) (reposcan.BaselineRunner, func(), error) {
		cmds = append(cmds, in.checkArgv)
		green := !slices.Contains(in.checkArgv, "tests/test_a.py::test_red")
		return &argvBaseline{green: green}, func() {}, nil
	}
	var audited localAuditInput
	ex.audit = func(_ context.Context, in localAuditInput) (advpool.Verdict, error) {
		audited = in
		return advpool.Verdict{DevScored: true, DevKillRate: 1}, nil
	}
	res, err := ex.Execute(context.Background(), reposcan.Job{Path: "pkg/a.py", TestPath: "tests/test_a.py", Lang: "python"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Gradable || len(cmds) != 2 {
		t.Fatalf("result %+v after baselines %v:\n%s", res, cmds, out.String())
	}
	if !reflect.DeepEqual(audited.selection.Excluded, []string{"tests/test_a.py::test_red"}) || audited.selection.Always != nil ||
		!reflect.DeepEqual(audited.selection.Tests, []string{"tests/test_a.py::test_x"}) {
		t.Errorf("audited selection %+v, want the green test alone with the red one named as left out", audited.selection)
	}
	if !strings.Contains(out.String(), "retried without it: green") {
		t.Errorf("the run does not say what it did:\n%s", out.String())
	}
}

// The scan says once that tests failed in the instrumented run and what
// became of them; a green run says nothing.
func TestFailedInEvidenceLine(t *testing.T) {
	py, _ := lang.ByName("python")
	ev := reposcan.NewSelectionEvidence(py, py.TestCmd(), []byte(`{"format":"corral-selection-3","tests":3,`+
		`"failed":["tests/test_a.py::test_red","tests/test_b.py::test_red2"],"files":{`+
		`"pkg/a.py":{"tests":["tests/test_a.py::test_x"],"lines":{"0":[[1,1]]},"static":[],"statements":2}}}`))
	want := "2 tests failed in the instrumented run; their coverage is not used, and they run with every file's selection and every mutant (tests/test_a.py::test_red, tests/test_b.py::test_red2)"
	if got := failedInEvidenceLine(ev.Map); got != want {
		t.Errorf("line = %q, want %q", got, want)
	}
	green := reposcan.NewSelectionEvidence(py, py.TestCmd(), []byte(`{"format":"corral-selection-3","tests":1,"files":{`+
		`"pkg/a.py":{"tests":["tests/test_a.py::test_x"],"lines":{"0":[[1,1]]},"static":[],"statements":2}}}`))
	if got := failedInEvidenceLine(green.Map); got != "" {
		t.Errorf("a green run printed %q", got)
	}
}
