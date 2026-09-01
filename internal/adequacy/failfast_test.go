// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
)

// fakeSuite is a runner, not a rubber stamp. Its argv is `run t1 t2 ...`: each
// positional names one test, and a test fails when the code under test
// contains its own kill token ("kill1" for t1). It executes them IN THE ORDER
// GIVEN, and — when pytest's `-x` is present — stops at the first failure,
// exactly as a real runner would. Everything the scorer reads (pass/fail, the
// pytest-shaped short-summary line) is therefore produced by simulated
// EXECUTION, so a change to test order or to fail-fast really does flow
// through the same seams it would in a real audit.
//
// It is fully hermetic: no process, no network, no model.
type fakeSuite struct {
	mu sync.Mutex
	// runs is every command handed to the jail, in order.
	runs [][]string
	// executed counts individual test executions across all runs — the number
	// fail-fast is supposed to reduce.
	executed int
	// rejectFlag makes the runner behave like one that does NOT understand
	// `-x`: it exits non-zero whatever the source says.
	rejectFlag bool
}

func (f *fakeSuite) RunTest(ctx context.Context, files map[string]string, cmd []string) (bool, error) {
	ok, _, err := f.RunTestDetailed(ctx, files, cmd)
	return ok, err
}

func (f *fakeSuite) RunTestDetailed(_ context.Context, files map[string]string, cmd []string) (bool, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, append([]string{}, cmd...))

	failFast := false
	var tests []string
	for _, a := range cmd[1:] {
		if a == "-x" {
			failFast = true
			continue
		}
		tests = append(tests, a)
	}
	if f.rejectFlag && failFast {
		return false, []byte("ERROR: unrecognized arguments: -x\n"), nil
	}
	code := files["a.py"]
	if code == CanaryCode {
		f.executed++
		return false, []byte("FAILED a.py::collect - SyntaxError\n"), nil
	}
	var first string
	for _, t := range tests {
		f.executed++
		if strings.Contains(code, "kill"+strings.TrimPrefix(t, "t")) {
			if first == "" {
				first = t
			}
			if failFast {
				break
			}
		}
	}
	if first == "" {
		return true, nil, nil
	}
	return false, []byte(fmt.Sprintf("FAILED a.py::%s - boom\n", first)), nil
}

// pyFailFast is the python plugin's own FailFastFor — the exact seam the
// engine is wired with in production. Nothing about the flag is hardcoded in
// adequacy.
func pyFailFast(t *testing.T) FailFastFor {
	t.Helper()
	p, ok := lang.ByName("python")
	if !ok {
		t.Fatal("python plugin not registered")
	}
	return func(cmd []string) ([]string, bool) { return lang.FailFastArgsFor(p, cmd) }
}

var ffCmd = []string{"pytest", "t1", "t2", "t3", "t4"}

func ffMutants() []Mutant {
	return []Mutant{
		{ID: "m1", Replace: "kill1\n"},       // t1 catches it: one test, not four
		{ID: "m2", Replace: "kill3\n"},       // t3 catches it, after two that don't
		{ID: "m3", Replace: "kill1 kill4\n"}, // two catch it; t1 is first either way
		{ID: "m4", Replace: "kill2\n"},
		{ID: "m5", Replace: "harmless\n"}, // nothing catches it: the full set runs
	}
}

// THE BASELINE MUST RUN EVERYTHING. A green baseline is corral's claim that
// the suite passes; a baseline that stopped at the first failure would certify
// a suite that was never fully executed. The canary is a baseline run too.
//
// The ONE compliant run that legitimately carries the flag is the probe that
// proves the runner accepts it — and it is not the run whose result becomes
// CompliantPass.
func TestFailFastNeverReachesTheBaselineOrTheCanary(t *testing.T) {
	f := &fakeSuite{}
	rep, err := Score(context.Background(), f, map[string]string{}, "a.py", "ORIGINAL\n",
		ffMutants(), ffCmd,
		WithFailureParser(pythonFailureParser(t)),
		WithMutantFailFast(pyFailFast(t)))
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if !rep.FailFast {
		t.Fatalf("FailFast = false, note %q — the fake runner accepts -x", rep.FailFastNote)
	}
	if len(f.runs) == 0 {
		t.Fatal("no runs recorded")
	}
	// The FIRST run is the compliant baseline, and it must be the operator's
	// own command, untouched.
	if got := strings.Join(f.runs[0], " "); got != strings.Join(ffCmd, " ") {
		t.Errorf("baseline ran %q, want the operator's own command %q — the baseline must execute everything", got, strings.Join(ffCmd, " "))
	}
	// No CANARY run may carry it either. The canary is the run on CanaryCode;
	// it is the second command issued (baseline, canary, probe, mutants).
	if len(f.runs) < 2 {
		t.Fatal("expected a canary run")
	}
	for _, a := range f.runs[1] {
		if a == "-x" {
			t.Errorf("the canary run carried -x: %v — it is a baseline run on invalid source and must execute everything", f.runs[1])
		}
	}
}

// Fail-fast must not change WHAT is measured. The same recorded mutant set is
// graded twice — once with fail-fast, once with it disabled — and the two
// verdicts must be identical field by field. This is the acceptance for the
// whole change: speed that moves a number is a bug, not speed.
func TestVerdictIsIdenticalWithAndWithoutFailFast(t *testing.T) {
	grade := func(opts ...ScoreOption) (Report, *fakeSuite) {
		t.Helper()
		f := &fakeSuite{}
		base := []ScoreOption{WithFailureParser(pythonFailureParser(t))}
		rep, err := Score(context.Background(), f, map[string]string{}, "a.py", "ORIGINAL\n",
			ffMutants(), ffCmd, append(base, opts...)...)
		if err != nil {
			t.Fatalf("Score: %v", err)
		}
		return rep, f
	}

	slow, slowJail := grade()                                  // the path corral shipped
	fast, fastJail := grade(WithMutantFailFast(pyFailFast(t))) // the new one

	if !fast.FailFast {
		t.Fatalf("fail-fast was not enabled: %q", fast.FailFastNote)
	}
	if slow.FailFast {
		t.Fatal("the control run enabled fail-fast — it is not a control")
	}
	assertSameVerdict(t, slow, fast)

	// ...and it really is cheaper: m1 is killed by t1, so t2 never runs.
	if fastJail.executed >= slowJail.executed {
		t.Errorf("fail-fast executed %d tests, control executed %d — no saving", fastJail.executed, slowJail.executed)
	}
}

// A runner that does not accept the flag must not be given it. Without the
// probe, its non-zero exit would read as a kill for EVERY mutant and take the
// kill rate to 1.00 silently — the exact defect class the compile gate exists
// for. Here the verdict must still be the honest one, and the report must say
// why fail-fast was dropped.
func TestFailFastIsDroppedWhenTheRunnerRejectsTheFlag(t *testing.T) {
	honest := &fakeSuite{}
	want, err := Score(context.Background(), honest, map[string]string{}, "a.py", "ORIGINAL\n",
		ffMutants(), ffCmd, WithFailureParser(pythonFailureParser(t)))
	if err != nil {
		t.Fatalf("control Score: %v", err)
	}

	f := &fakeSuite{rejectFlag: true}
	got, err := Score(context.Background(), f, map[string]string{}, "a.py", "ORIGINAL\n",
		ffMutants(), ffCmd,
		WithFailureParser(pythonFailureParser(t)),
		WithMutantFailFast(pyFailFast(t)))
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if got.FailFast {
		t.Fatal("fail-fast was enabled against a runner that rejects the flag")
	}
	if got.FailFastNote == "" {
		t.Error("fail-fast was dropped with no disclosure")
	}
	assertSameVerdict(t, want, got)
	for _, r := range f.runs {
		for _, a := range r {
			if a == "-x" && len(r) > 0 {
				// only the probe may carry it
				if r[len(r)-1] != "-x" || !containsAll(r, ffCmd) {
					t.Errorf("a graded run carried the rejected flag: %v", r)
				}
			}
		}
	}
}

// A plugin with no stop-at-first-failure flag is simply unchanged.
func TestUnknownRunnerGetsNoFlagAndIsUnchanged(t *testing.T) {
	f := &fakeSuite{}
	rep, err := Score(context.Background(), f, map[string]string{}, "a.py", "ORIGINAL\n",
		ffMutants(), []string{"nosuchrunner", "t1", "t2"},
		WithFailureParser(pythonFailureParser(t)),
		WithMutantFailFast(pyFailFast(t)))
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if rep.FailFast {
		t.Fatal("fail-fast enabled for a runner corral does not recognise")
	}
	if rep.FailFastNote == "" {
		t.Error("no disclosure for a runner with no fail-fast flag")
	}
	for _, r := range f.runs {
		for _, a := range r {
			if a == "-x" {
				t.Errorf("an unrecognised runner was handed -x: %v", r)
			}
		}
	}
}

func containsAll(have, want []string) bool {
	for _, w := range want {
		found := false
		for _, h := range have {
			if h == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// assertSameVerdict compares every field a verdict is made of. Duration is
// excluded on purpose — it is the ONE thing this change is meant to move.
func assertSameVerdict(t *testing.T, want, got Report) {
	t.Helper()
	if want.CompliantPass != got.CompliantPass || want.CanaryKilled != got.CanaryKilled {
		t.Errorf("soundness differs: CompliantPass %v/%v CanaryKilled %v/%v",
			want.CompliantPass, got.CompliantPass, want.CanaryKilled, got.CanaryKilled)
	}
	if want.KillRate() != got.KillRate() {
		t.Errorf("kill rate %v != %v", want.KillRate(), got.KillRate())
	}
	if want.Total != got.Total {
		t.Errorf("graded total %d != %d", want.Total, got.Total)
	}
	if strings.Join(want.Killed, ",") != strings.Join(got.Killed, ",") {
		t.Errorf("Killed %v != %v", want.Killed, got.Killed)
	}
	if strings.Join(want.Survived, ",") != strings.Join(got.Survived, ",") {
		t.Errorf("Survived %v != %v", want.Survived, got.Survived)
	}
	if strings.Join(want.Invalid, ",") != strings.Join(got.Invalid, ",") {
		t.Errorf("Invalid %v != %v", want.Invalid, got.Invalid)
	}
	if len(want.PerMutant) != len(got.PerMutant) {
		t.Fatalf("PerMutant sizes %d != %d", len(want.PerMutant), len(got.PerMutant))
	}
	for id, w := range want.PerMutant {
		g, ok := got.PerMutant[id]
		if !ok {
			t.Errorf("mutant %s missing from the new report", id)
			continue
		}
		if w.KilledBy != g.KilledBy {
			t.Errorf("mutant %s killed_by %q != %q", id, w.KilledBy, g.KilledBy)
		}
		if w.TestsRun != g.TestsRun || w.Rule != g.Rule {
			t.Errorf("mutant %s grading %+v != %+v", id, w, g)
		}
	}
}
