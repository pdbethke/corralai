// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

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

// A Selection whose node ids overflowed the argv cap is collapsed to test
// FILES, and the line evidence — keyed by the node ids that no longer run —
// can no longer narrow anything. The run must fall back to the file's shared
// command rather than grade every mutant against a lookup that always misses.
func TestDevCommandForIsNilForACollapsedSelection(t *testing.T) {
	sel := lineSelection()
	sel.Tests = []string{"tests/test_a.py"}
	sel.Cmd = []string{"python3", "-m", "pytest", "-q", "tests/test_a.py"}
	if f := DevCommandFor(RunSpec{Lang: "python", Selection: sel}); f != nil {
		t.Errorf("collapsed selection must grade per FILE: got %+v", f(adequacy.Mutant{ID: "m", Span: lang.LineRange{Start: 41, End: 41}}))
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
	// The dev report must KILL a real mutant, not the fake's synthetic "k0"
	// ids: killedFrom drops an id that names no mutant, so a scripted-by-rate
	// report leaves DevKilledMutants empty and the grading assertion below
	// would pass vacuously over half the evidence it exists to check.
	// devReported pre-set routes the dev pass through reportFn.
	scorer := &recordingPerMutantScorer{fakeScorer: fakeScorer{
		devKillRate:  0.5,
		devSurvivors: mutants[1:],
		devReported:  true,
		reportFn: func(_ context.Context, _, _, _ string, _ []adequacy.Mutant, _ string) (adequacy.Report, error) {
			return adequacy.Report{CompliantPass: true, CanaryKilled: true, Total: 3,
				Killed: []string{"m1"}, Survived: []string{"m2", "m3"}}, nil
		},
	}}
	validator := &fakeValidator{mutants: mutants}
	d := newTestDriverWithSpec(t, 91, scorer, validator, 0.1, perMutantRunSpec())

	v := drivePoolToConvergence(t, d, 91)

	if scorer.gotCmdFor == nil {
		t.Fatal("the driver graded the dev pass through ScoreReport, not ScoreReportFor — the per-mutant closure never reached a scorer that can take it")
	}
	if len(v.DevKilledMutants) != 1 || v.DevKilledMutants[0].ID != "m1" {
		t.Fatalf("DevKilledMutants = %+v, want the one killed mutant m1 — the killed-ref grading path must be exercised, not skipped", v.DevKilledMutants)
	}
	if k := v.DevKilledMutants[0]; k.TestsRun != 1 || k.Rule != lang.SpanRuleLines {
		t.Errorf("killed ref m1 = %+v, want TestsRun 1 / rule %q — a KILLED mutant must carry what killed it", k, lang.SpanRuleLines)
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
	if got := v.TestSelection.TestsPerMutant; got == nil || *got != (TestsPerMutantSpread{Min: 1, Median: 2, Max: 2}) {
		t.Errorf("TestsPerMutant = %+v, want {Min:1 Median:2 Max:2}", got)
	}
	if got, want := v.TestSelection.Rules, map[string]int{"lines": 1, "static": 1, "file": 1}; !reflect.DeepEqual(got, want) {
		t.Errorf("TestSelection.Rules = %v, want %v", got, want)
	}
}

// allInvalidPerMutantScorer is a PerMutantScorer whose dev report grades
// NOTHING: every mutant was rejected by the compile gate, which is exactly
// the shape adequacy.Score returns — Invalid full, PerMutant empty — and the
// case that must NOT be mistaken for "this run was not graded per mutant".
type allInvalidPerMutantScorer struct {
	fakeScorer
	mutants []adequacy.Mutant
}

func (a *allInvalidPerMutantScorer) ScoreReportFor(_ context.Context, _, _, _ string, mutants []adequacy.Mutant, _ string, _ adequacy.CommandFor) (adequacy.Report, error) {
	rep := adequacy.Report{CompliantPass: true, CanaryKilled: true}
	for _, m := range mutants {
		rep.Invalid = append(rep.Invalid, m.ID)
	}
	return rep, nil
}

func (a *allInvalidPerMutantScorer) ScoreFor(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string, _ adequacy.CommandFor) (float64, []adequacy.Mutant, error) {
	return a.fakeScorer.Score(ctx, codePath, code, test, mutants, testCmd)
}

// A per-mutant run whose whole exam was rejected by the compile gate still
// graded per mutant: the closure WAS handed to the scorer, and the record has
// to say which measurement produced its (empty) numbers. Inferring the
// disclosure from the report's PerMutant map instead would sign this run as
// an ordinary whole-selection one — a claim about behaviour that never ran.
func TestPerMutantDisclosedEvenWhenEveryMutantIsInvalid(t *testing.T) {
	mutants := []adequacy.Mutant{
		{ID: "m1", Code: "c1", Span: lang.LineRange{Start: 41, End: 41}},
		{ID: "m2", Code: "c2", Span: lang.LineRange{Start: 2, End: 2}},
	}
	scorer := &allInvalidPerMutantScorer{mutants: mutants}
	validator := &fakeValidator{mutants: mutants}
	d := newTestDriverWithSpec(t, 92, scorer, validator, 0.1, perMutantRunSpec())

	// Not drivePoolToConvergence: an exam with nothing left to grade leaves no
	// survivors, so the test-writer seat is moot and never becomes claimable.
	ready := claimAllReady(t, d.Q)
	mustComplete(t, d.Q, ready[RoleTestCritic].ID, "critic findings filed")
	mustComplete(t, d.Q, ready[RoleMutantGenerator].ID, "raw mutants")
	v, err := d.Tick(context.Background(), 92)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if v == nil {
		t.Fatal("expected a verdict: an all-invalid exam has nothing left to wait for")
	}

	if v.MutantsInvalid != len(mutants) || v.MutantsTotal != 0 {
		t.Fatalf("invalid=%d total=%d, want the whole exam rejected", v.MutantsInvalid, v.MutantsTotal)
	}
	if !v.TestSelection.PerMutant {
		t.Error("PerMutant = false — an all-invalid exam was still graded per mutant, and the record must say so")
	}
	if v.TestSelection.Method != MethodCoverageLines {
		t.Errorf("Method = %q, want %q", v.TestSelection.Method, MethodCoverageLines)
	}
	if got := v.TestSelection.TestsPerMutant; got != nil {
		t.Errorf("TestsPerMutant = %+v, want ABSENT — nothing was graded, so there is no spread to report", got)
	}
	if len(v.TestSelection.Rules) != 0 {
		t.Errorf("Rules = %v, want empty — no mutant was graded by any rule", v.TestSelection.Rules)
	}
}

// The spread is a MEASUREMENT, and a Verdict is marshalled whole into the
// signed record, the ledger and the warehouse. A run that measured no spread
// must therefore carry no spread at all: a struct field with `omitempty`
// never omits, so every whole-suite verdict was signing
// "tests_per_mutant":{"min":0,"median":0,"max":0} — three numbers nobody
// measured, indistinguishable from a real spread of zero.
func TestVerdictOmitsAnUnmeasuredSpread(t *testing.T) {
	for _, tc := range []struct {
		name string
		v    Verdict
		want []string // substrings that MUST appear
	}{
		{name: "whole suite", v: Verdict{}},
		{
			name: "per-mutant, nothing graded",
			v:    Verdict{TestSelection: TestSelection{Method: MethodCoverageLines, PerMutant: true}},
			want: []string{`"per_mutant":true`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.v)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), "tests_per_mutant") {
				t.Errorf("an unmeasured spread must be ABSENT, not zero: %s", b)
			}
			for _, w := range tc.want {
				if !strings.Contains(string(b), w) {
					t.Errorf("missing %s in %s", w, b)
				}
			}
		})
	}
	// And a measured one is still there, with its three numbers.
	v := Verdict{TestSelection: TestSelection{PerMutant: true, TestsPerMutant: &TestsPerMutantSpread{Min: 1, Median: 2, Max: 41}}}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"tests_per_mutant":{"min":1,"median":2,"max":41}`) {
		t.Errorf("a measured spread must be carried whole: %s", b)
	}
}

// A run that graded per mutant and only THEN stalled must sign the same
// disclosure the converged path signs. timeoutVerdict was a bare Verdict
// literal: no TestSelection at all, and ungraded survivor refs beside graded
// killed ones — two grains of the same evidence in one signed record.
func TestTimeoutVerdictCarriesPerMutantGrading(t *testing.T) {
	const mission int64 = 93
	mutants := []adequacy.Mutant{
		{ID: "m1", Code: "c1", ParentSHA256: "p1", Span: lang.LineRange{Start: 41, End: 41}},
		{ID: "m2", Code: "c2", ParentSHA256: "p2", Span: lang.LineRange{Start: 2, End: 2}},
	}
	scorer := &recordingPerMutantScorer{fakeScorer: fakeScorer{devKillRate: 0.5, devSurvivors: mutants}}
	validator := &fakeValidator{mutants: mutants}
	q := newTestQueue(t)
	clk := &fakeClock{t: time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)}
	d, err := NewDriver(q, scorer, validator, decorrelatedAssign(), 0.1)
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}
	d.Now = clk.Now
	d.RunDeadline = 10 * time.Minute
	if err := d.StartRun(mission, perMutantRunSpec(), nil); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if _, err := q.PromoteReady(mission); err != nil {
		t.Fatalf("PromoteReady: %v", err)
	}
	// The dev pass runs (and grades per mutant); then the run stalls.
	mg := claimByKey(t, d.Q, RoleMutantGenerator)
	mustComplete(t, d.Q, mg.ID, "raw mutants")
	if v, err := d.Tick(context.Background(), mission); err != nil || v != nil {
		t.Fatalf("dev-adequacy Tick: v=%+v err=%v, want nil/nil", v, err)
	}
	clk.advance(d.RunDeadline + time.Second)

	v, err := d.Tick(context.Background(), mission)
	if err != nil {
		t.Fatalf("deadline Tick: %v", err)
	}
	if v == nil || !v.TimedOut {
		t.Fatalf("want a TimedOut verdict, got %+v", v)
	}
	if v.TestSelection.Method != MethodCoverageLines || !v.TestSelection.PerMutant {
		t.Errorf("TestSelection = %+v, want method %q and PerMutant true", v.TestSelection, MethodCoverageLines)
	}
	if len(v.DevSurvivedMutants) != len(mutants) {
		t.Fatalf("DevSurvivedMutants = %+v, want %d graded refs", v.DevSurvivedMutants, len(mutants))
	}
	for _, r := range v.DevSurvivedMutants {
		if r.TestsRun <= 0 || r.Rule == "" {
			t.Errorf("survivor ref %+v lost its grading on the timeout path", r)
		}
	}
	if got := v.TestSelection.TestsPerMutant; got == nil || *got != (TestsPerMutantSpread{Min: 1, Median: 2, Max: 2}) {
		t.Errorf("TestsPerMutant = %+v, want {Min:1 Median:2 Max:2}", got)
	}
	if got, want := v.TestSelection.Rules, map[string]int{"lines": 1, "static": 1}; !reflect.DeepEqual(got, want) {
		t.Errorf("Rules = %v, want %v", got, want)
	}
}
