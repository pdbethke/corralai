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
		{ID: "m1", Replace: "c1", ParentSHA256: "p1", Span: lang.LineRange{Start: 41, End: 41}},
		{ID: "m2", Replace: "c2", ParentSHA256: "p2", Span: lang.LineRange{Start: 2, End: 2}},
		{ID: "m3", Replace: "c3", ParentSHA256: "p3"},
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
	// The span rides on the ref too: it is the term an exam's coverage is
	// computed from, and both ledgers stored NULL for it for a month.
	want := map[string]MutantRef{
		"m1": {ID: "m1", ParentSHA256: "p1", TestsRun: 1, Rule: lang.SpanRuleLines, Span: lang.LineRange{Start: 41, End: 41}},
		"m2": {ID: "m2", ParentSHA256: "p2", TestsRun: 2, Rule: lang.SpanRuleStatic, Span: lang.LineRange{Start: 2, End: 2}},
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
		{ID: "m1", Replace: "c1", Span: lang.LineRange{Start: 41, End: 41}},
		{ID: "m2", Replace: "c2", Span: lang.LineRange{Start: 2, End: 2}},
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

// spanRecordingJail is a project whose suite passes on the compliant code,
// fails on anything else, and records every command it was asked to run. It
// is the only way to see WHICH command each mutant actually faced: the
// per-mutant closure is handed to adequacy.Score as an option, and a wiring
// mistake between JailScorer and Score would leave every mutant graded by
// the shared command while the report still said "coverage-lines".
type spanRecordingJail struct {
	codePath string
	code     string
	cmds     [][]string // test commands only; the compile gate is not a grading
}

func (j *spanRecordingJail) RunTest(_ context.Context, files map[string]string, cmd []string) (bool, error) {
	// The compile gate runs the language plugin's own check, never pytest.
	// It must PASS, or every mutant is invalid and nothing is graded.
	if !strings.Contains(strings.Join(cmd, " "), "pytest") {
		return true, nil
	}
	j.cmds = append(j.cmds, append([]string{}, cmd...))
	return files[j.codePath] == j.code, nil // the compliant baseline passes; a mutant is killed
}

// JailScorer.ScoreReportFor must hand the per-mutant closure all the way
// through to adequacy.Score, so each mutant is executed against the tests
// that reach ITS span while the baseline stays the file's shared command.
func TestScoreReportForRunsEachMutantsOwnCommand(t *testing.T) {
	const codePath, code = "pkg/a.py", "x = 1\n"
	jail := &spanRecordingJail{codePath: codePath, code: code}
	s := repoScorer(lineSelection())
	s.Jail = jail
	s.BaseFiles = map[string]string{codePath: code, "tests/test_a.py": "def test_x(): pass\n"}

	mutants := []adequacy.Mutant{
		{ID: "m1", Replace: "x = 2\n", Span: lang.LineRange{Start: 41, End: 41}}, // reached by t2 alone
		{ID: "m2", Replace: "x = 3\n", Span: lang.LineRange{Start: 2, End: 2}},   // import-time: the file selection
	}
	shared := DevCommand(RunSpec{Lang: "python", TestCmd: "python3 -m pytest -q", Selection: lineSelection()})
	rep, err := s.ScoreReportFor(context.Background(), codePath, code, "", mutants,
		shared, DevCommandFor(RunSpec{Lang: "python", Selection: lineSelection()}))
	if err != nil {
		t.Fatal(err)
	}

	// The baseline is a question about the FILE, so it runs the shared
	// command; each mutant then runs its own.
	if len(jail.cmds) < 3 {
		t.Fatalf("jail ran %d test command(s): %v", len(jail.cmds), jail.cmds)
	}
	wantBase := []string{"python3", "-m", "pytest", "-q", "tests/test_a.py::t1", "tests/test_a.py::t2"}
	if !reflect.DeepEqual(jail.cmds[0], wantBase) {
		t.Errorf("baseline ran %v, want the file's shared selection %v", jail.cmds[0], wantBase)
	}
	byCmd := map[string]bool{}
	for _, c := range jail.cmds {
		byCmd[strings.Join(c, " ")] = true
	}
	narrowed := "python3 -m pytest -q tests/test_a.py::t2"
	if !byCmd[narrowed] {
		t.Errorf("no mutant ran the narrowed command %q: %v", narrowed, jail.cmds)
	}

	// And the grading is REPORTED, not merely performed. Duration is left out
	// of the comparison on purpose: every graded mutant is also TIMED now,
	// and a wall clock cannot be a golden value.
	if got := rep.PerMutant["m1"]; got.TestsRun != 1 || got.Rule != lang.SpanRuleLines {
		t.Errorf("m1 grading = %+v, want TestsRun=1 Rule=%s", got, lang.SpanRuleLines)
	}
	if got := rep.PerMutant["m2"]; got.TestsRun != 2 || got.Rule != lang.SpanRuleStatic {
		t.Errorf("m2 grading = %+v, want TestsRun=2 Rule=%s", got, lang.SpanRuleStatic)
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
		{ID: "m1", Replace: "c1", ParentSHA256: "p1", Span: lang.LineRange{Start: 41, End: 41}},
		{ID: "m2", Replace: "c2", ParentSHA256: "p2", Span: lang.LineRange{Start: 2, End: 2}},
	}
	scorer := &recordingPerMutantScorer{fakeScorer: fakeScorer{devKillRate: 0.5, devSurvivors: mutants}}
	validator := &fakeValidator{mutants: mutants}
	q := newTestQueue(t)
	clk := &fakeClock{t: time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)}
	d, err := NewDriver(q, scorer, validator, decorrelatedAssign(), 0.1)
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}
	d.CertifyIntervalWidth = 0 // small fixture; the exam-size rule is tested on its own
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

// The AUTHORED pass grades survivors of the dev pass. No selected dev test
// killed them, so the only test that can is the authored one — and every
// question the pass asks is a question about THAT test: does it pass on the
// unmutated code, does it react to broken source, does it kill the survivor.
// So under a selection the whole pass runs the authored test ALONE: the
// baseline, the canary and each survivor. The shared command (the file's
// selection + the authored path) must never appear. It used to: the baseline,
// the canary and the positive control each ran the file's full selection per
// seat, so on psf/requests a hub file with 146–336 covering tests paid three
// half-minute suite runs per survivor and timed out with most survivors
// never attempted — while the report said "proven by the authored test
// alone". Alone also makes the canary STRONGER: with the dev tests in the
// command, a dev test reacting to broken source satisfied it for free,
// whether or not the authored test ever read the file.
func TestScoreAuthoredReportRunsTheWholePassWithTheAuthoredTestAlone(t *testing.T) {
	const codePath, code = "pkg/a.py", "x = 1\n"
	jail := &spanRecordingJail{codePath: codePath, code: code}
	s := repoScorer(lineSelection())
	s.Jail = jail
	s.BaseFiles = map[string]string{codePath: code, "tests/test_a.py": "def test_x(): pass\n"}
	authored := authoredTestPath(codePath, s.DevTestPath, s.BaseFiles)

	mutants := []adequacy.Mutant{
		{ID: "m1", Replace: "x = 2\n", Span: lang.LineRange{Start: 41, End: 41}},
		{ID: "m2", Replace: "x = 3\n", Span: lang.LineRange{Start: 2, End: 2}},
	}
	rep, err := s.ScoreAuthoredReport(context.Background(), codePath, code, "def test_authored(): pass\n", mutants, "python3 -m pytest -q")
	if err != nil {
		t.Fatal(err)
	}
	alone := []string{"python3", "-m", "pytest", "-q", authored}
	if len(jail.cmds) < 4 {
		t.Fatalf("want baseline + canary + 2 survivors (+ the positive control), ran %d: %v", len(jail.cmds), jail.cmds)
	}
	for i, c := range jail.cmds {
		if !reflect.DeepEqual(c, alone) {
			t.Errorf("run %d used %v; every run of the authored pass must be the authored test alone %v", i, c, alone)
		}
	}
	for _, id := range []string{"m1", "m2"} {
		if got := rep.PerMutant[id]; got.TestsRun != 1 || got.Rule != RuleAuthoredAlone {
			t.Errorf("%s grading = %+v, want TestsRun=1 Rule=%s", id, got, RuleAuthoredAlone)
		}
	}
}

// Without a selector (whole suite, --local, Ruby) the authored pass is
// byte-for-byte what it was: one shared command, no per-mutant grading.
func TestScoreAuthoredReportWithoutASelectorIsUnchanged(t *testing.T) {
	const codePath, code = "pkg/a.py", "x = 1\n"
	jail := &spanRecordingJail{codePath: codePath, code: code}
	s := repoScorer(lang.Selection{})
	s.Jail = jail
	s.BaseFiles = map[string]string{codePath: code, "tests/test_a.py": "def test_x(): pass\n"}
	rep, err := s.ScoreAuthoredReport(context.Background(), codePath, code, "def test_authored(): pass\n",
		[]adequacy.Mutant{{ID: "m1", Replace: "x = 2\n"}}, "python3 -m pytest -q")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range jail.cmds {
		if !reflect.DeepEqual(c, []string{"python3", "-m", "pytest", "-q"}) {
			t.Errorf("a zero Selection must leave every authored-pass command as before: %v", c)
		}
	}
	// PerMutant is populated on every graded run (each mutant is timed), but
	// a run with no selector records no per-mutant COMMAND evidence — which
	// is what "unchanged" means: nothing here can make a whole-suite verdict
	// disclose a narrowing that did not happen.
	if got := rep.PerMutant["m1"]; got.TestsRun != 0 || got.Rule != "" {
		t.Errorf("no per-mutant grading without a selector: %+v", got)
	}
}

func TestVerdictSaysWhetherTheAuthoredPassProvedAlone(t *testing.T) {
	if v := verdictFromSpec(RunSpec{Lang: "python", Selection: lineSelection()}); !v.TestSelection.AuthoredAlone {
		t.Error("a python run with a selection proves survivors with the authored test alone; the verdict must say so")
	}
	if v := verdictFromSpec(RunSpec{Lang: "python"}); v.TestSelection.AuthoredAlone {
		t.Error("a whole-suite run proves the old way")
	}
	if v := verdictFromSpec(RunSpec{Lang: "ruby", Selection: lang.Selection{Method: "coverage-context"}}); v.TestSelection.AuthoredAlone {
		t.Error("ruby has no selector")
	}
}
