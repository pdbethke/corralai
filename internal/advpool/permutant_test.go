// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"reflect"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/lang"
)

func lineSelection() lang.Selection {
	return lang.Selection{
		Method: "coverage-context", Of: 10,
		Base:   []string{"python3", "-m", "pytest", "-q"},
		Cmd:    []string{"python3", "-m", "pytest", "-q", "tests/test_a.py::t1", "tests/test_a.py::t2"},
		Tests:  []string{"tests/test_a.py::t1", "tests/test_a.py::t2"},
		Lines:  map[string][]lang.LineRange{"tests/test_a.py::t1": {{Start: 10, End: 20}}, "tests/test_a.py::t2": {{Start: 40, End: 45}}},
		Static: []lang.LineRange{{Start: 1, End: 3}},
	}
}

func TestDevCommandForNarrowsPerMutant(t *testing.T) {
	rs := RunSpec{Lang: "python", TestCmd: "python3 -m pytest -q", Selection: lineSelection()}
	f := DevCommandFor(rs)
	if f == nil {
		t.Fatal("line evidence present: DevCommandFor must not be nil")
	}
	mc := f(adequacy.Mutant{ID: "m1", Span: lang.LineRange{Start: 41, End: 41}})
	if mc.Rule != lang.SpanRuleLines || mc.Tests != 1 || !reflect.DeepEqual(mc.Cmd, []string{"python3", "-m", "pytest", "-q", "tests/test_a.py::t2"}) {
		t.Errorf("got %+v", mc)
	}
	if mc := f(adequacy.Mutant{ID: "m2", Span: lang.LineRange{Start: 2, End: 2}}); mc.Rule != lang.SpanRuleStatic || mc.Tests != 2 {
		t.Errorf("static: %+v", mc)
	}
	if mc := f(adequacy.Mutant{ID: "m3"}); mc.Rule != lang.SpanRuleFile || mc.Tests != 2 {
		t.Errorf("zero span: %+v", mc)
	}
}

func TestDevCommandForIsNilWithoutLineEvidence(t *testing.T) {
	sel := lineSelection()
	sel.Lines = nil
	if DevCommandFor(RunSpec{Lang: "python", Selection: sel}) != nil {
		t.Error("v1-shaped evidence must mean today's behaviour")
	}
	if DevCommandFor(RunSpec{Lang: "python"}) != nil {
		t.Error("whole suite must mean today's behaviour")
	}
	sel = lineSelection()
	sel.Tests = nil
	if DevCommandFor(RunSpec{Lang: "python", Selection: sel}) != nil {
		t.Error("an uncovered file has no tests to narrow")
	}
}

// The driver prefers PerMutantScorer when the run carries line evidence,
// and the per-mutant grading reaches the Verdict's mutant refs and stats.
type recordingPerMutantScorer struct {
	fakeScorer // the existing test double in driver_test.go — embed it
	gotCmdFor  adequacy.CommandFor
}

func (r *recordingPerMutantScorer) ScoreReportFor(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string, cmdFor adequacy.CommandFor) (adequacy.Report, error) {
	r.gotCmdFor = cmdFor
	rep, err := r.fakeScorer.ScoreReport(ctx, codePath, code, test, mutants, testCmd)
	rep.PerMutant = map[string]adequacy.MutantGrading{}
	for _, m := range mutants {
		mc := cmdFor(m)
		rep.PerMutant[m.ID] = adequacy.MutantGrading{TestsRun: mc.Tests, Rule: mc.Rule}
	}
	return rep, err
}

func (r *recordingPerMutantScorer) ScoreFor(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string, cmdFor adequacy.CommandFor) (float64, []adequacy.Mutant, error) {
	r.gotCmdFor = cmdFor
	return r.fakeScorer.Score(ctx, codePath, code, test, mutants, testCmd)
}

// perMutantRunSpec is a python run carrying v2 (per-test line) evidence — the
// only shape DevCommandFor narrows for.
func perMutantRunSpec() RunSpec {
	rs := testRunSpec()
	rs.Lang = "python"
	rs.CodePath = "target.py"
	rs.Code = "def validate(pw):\n    return True\n"
	rs.DevTestPath = "tests/test_a.py"
	rs.DevTestCode = "def t1():\n    pass\n"
	rs.TestCmd = "python3 -m pytest -q"
	rs.Selection = lineSelection()
	return rs
}

func TestVerdictCarriesPerMutantGrading(t *testing.T) {
	// One mutant per SpanRule the closure can choose: {41,41} is reached by
	// exactly one test (lines), {2,2} touches an import-time line (static),
	// and a zero span has no evidence to narrow by (file).
	mutants := []adequacy.Mutant{
		{ID: "m1", Code: "c1", ParentSHA256: "p1", Span: lang.LineRange{Start: 41, End: 41}},
		{ID: "m2", Code: "c2", ParentSHA256: "p2", Span: lang.LineRange{Start: 2, End: 2}},
		{ID: "m3", Code: "c3", ParentSHA256: "p3"},
	}
	scorer := &recordingPerMutantScorer{fakeScorer: fakeScorer{devKillRate: 0.5, devSurvivors: mutants}}
	validator := &fakeValidator{mutants: mutants}
	d := newTestDriverWithSpec(t, 91, scorer, validator, 0.1, perMutantRunSpec())

	v := drivePoolToConvergence(t, d, 91)

	if scorer.gotCmdFor == nil {
		t.Fatal("the driver graded the dev pass through ScoreReport, not ScoreReportFor — the per-mutant closure never reached a scorer that can take it")
	}
	refs := append(append([]MutantRef{}, v.DevKilledMutants...), v.DevSurvivedMutants...)
	if len(refs) != len(mutants) {
		t.Fatalf("verdict names %d mutant(s), want %d", len(refs), len(mutants))
	}
	byID := map[string]MutantRef{}
	for _, r := range refs {
		if r.TestsRun <= 0 {
			t.Errorf("mutant %q: TestsRun = %d, want > 0 — the grading did not reach the ref", r.ID, r.TestsRun)
		}
		if r.Rule == "" {
			t.Errorf("mutant %q: no Rule — the ref does not say what graded it", r.ID)
		}
		byID[r.ID] = r
	}
	want := map[string]MutantRef{
		"m1": {ID: "m1", ParentSHA256: "p1", TestsRun: 1, Rule: lang.SpanRuleLines},
		"m2": {ID: "m2", ParentSHA256: "p2", TestsRun: 2, Rule: lang.SpanRuleStatic},
		"m3": {ID: "m3", ParentSHA256: "p3", TestsRun: 2, Rule: lang.SpanRuleFile},
	}
	if !reflect.DeepEqual(byID, want) {
		t.Errorf("mutant refs = %+v, want %+v", byID, want)
	}

	if !v.TestSelection.PerMutant {
		t.Error("TestSelection.PerMutant = false — the verdict does not disclose that each mutant got its own command")
	}
	if got := v.TestSelection.TestsPerMutant; got.Min != 1 || got.Median != 2 || got.Max != 2 {
		t.Errorf("TestsPerMutant = %+v, want {Min:1 Median:2 Max:2}", got)
	}
	if got, want := v.TestSelection.Rules, map[string]int{"lines": 1, "static": 1, "file": 1}; !reflect.DeepEqual(got, want) {
		t.Errorf("TestSelection.Rules = %v, want %v", got, want)
	}
}
