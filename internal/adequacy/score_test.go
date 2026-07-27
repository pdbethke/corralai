// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// fakeJail is a test double for Jail: it "passes" or "fails" a test run based
// on the code content it receives at codePath, keyed via passOn. No sandbox,
// no process exec — pure map lookup, so Score's logic is exercised directly.
type fakeJail struct {
	passOn map[string]bool
	calls  int
}

func (f *fakeJail) RunTest(ctx context.Context, files map[string]string, testCmd []string) (bool, error) {
	f.calls++
	return f.passOn[files["code.go"]], nil
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestScore(t *testing.T) {
	// fake jail: the test "passes" (returns true) on the compliant code and on
	// mutant m2 (a survivor the test misses); it "fails" (false) on m1 and m3.
	fj := &fakeJail{passOn: map[string]bool{"COMPLIANT": true, "m1": false, "m2": true, "m3": false}}
	base := map[string]string{"code_test.go": "<test>", "go.mod": "module target\ngo 1.26\n"}
	// Mutant.Code is the marker the fake jail keys on (matching passOn); ID is
	// the identifier Score reports in Killed/Survived. Same value here — the
	// fake doesn't care about ID, only about the code content it's handed.
	muts := []Mutant{{ID: "m1", Code: "m1"}, {ID: "m2", Code: "m2"}, {ID: "m3", Code: "m3"}}
	rep, err := Score(context.Background(), fj, base, "code.go", "COMPLIANT", muts, []string{"go", "test", "./"})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.CompliantPass || rep.Total != 3 {
		t.Fatalf("unexpected: %+v", rep)
	}
	if got := rep.KillRate(); got < 0.66 || got > 0.67 {
		t.Errorf("KillRate = %v, want ~0.667 (2/3)", got)
	}
	if !eq(rep.Killed, []string{"m1", "m3"}) || !eq(rep.Survived, []string{"m2"}) {
		t.Errorf("killed=%v survived=%v", rep.Killed, rep.Survived)
	}
}

func TestScoreInvalidWhenCompliantFails(t *testing.T) {
	// A test that fails on compliant code is broken/overreaching: report invalid, no mutants run.
	fj := &fakeJail{passOn: map[string]bool{"COMPLIANT": false}}
	rep, err := Score(context.Background(), fj, map[string]string{}, "code.go", "COMPLIANT",
		[]Mutant{{ID: "m1", Code: "M1"}}, []string{"go", "test"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.CompliantPass {
		t.Fatal("want CompliantPass=false")
	}
	if len(rep.Killed)+len(rep.Survived) != 0 || fj.calls != 1 {
		t.Fatalf("mutants must NOT run when compliant fails: %+v calls=%d", rep, fj.calls)
	}
}

// timeoutJail is a Jail double whose RunTest reports whatever passOn says for
// most code, but returns ErrTestTimeout (wrapped, as the real bwrapJail does)
// for any code content listed in timeoutOn — simulating a mutant that makes
// the candidate suite hang, with no real sandbox/process involved.
type timeoutJail struct {
	passOn    map[string]bool
	timeoutOn map[string]bool
	calls     int
}

func (f *timeoutJail) RunTest(ctx context.Context, files map[string]string, testCmd []string) (bool, error) {
	f.calls++
	code := files["code.go"]
	if f.timeoutOn[code] {
		return false, fmt.Errorf("%w: simulated hang", ErrTestTimeout)
	}
	return f.passOn[code], nil
}

// TestScoreMutantTimeoutCountsAsKilled is THE load-bearing assertion this
// fix exists for: a mutant that makes the suite hang (reported via
// ErrTestTimeout) must be scored as a fast KILL, not abort the whole run.
// Before this fix, Score returned the raw error and aborted scoring —
// yielding a vacuous 0% kill rate on exactly the mutants (loop-bound breaks)
// that are most likely to hang.
func TestScoreMutantTimeoutCountsAsKilled(t *testing.T) {
	fj := &timeoutJail{
		passOn:    map[string]bool{"COMPLIANT": true, "m2": false},
		timeoutOn: map[string]bool{"m1": true},
	}
	muts := []Mutant{{ID: "m1", Code: "m1"}, {ID: "m2", Code: "m2"}}
	rep, err := Score(context.Background(), fj, map[string]string{}, "code.go", "COMPLIANT", muts, []string{"go", "test"})
	if err != nil {
		t.Fatalf("a mutant timeout must not abort Score: %v", err)
	}
	if !rep.CompliantPass || rep.Total != 2 {
		t.Fatalf("unexpected: %+v", rep)
	}
	if !eq(rep.Killed, []string{"m1", "m2"}) {
		t.Fatalf("both the timed-out mutant (m1) and the normally-killed mutant (m2) should be Killed, got killed=%v survived=%v", rep.Killed, rep.Survived)
	}
	if len(rep.Survived) != 0 {
		t.Fatalf("no survivors expected, got %v", rep.Survived)
	}
}

// TestScoreBaselineTimeoutFailsClosed proves the OTHER half of the contract:
// a baseline (compliant-code) run that itself times out is NOT scored as a
// kill or silently ignored — it must fail closed (CompliantPass=false, no
// mutants run, no error), the same fail-safe shape as a baseline that simply
// fails its own tests. A suite that can't even pass on good code within the
// jail's generous budget is broken/too-slow; it must never earn a kill rate.
func TestScoreBaselineTimeoutFailsClosed(t *testing.T) {
	fj := &timeoutJail{timeoutOn: map[string]bool{"COMPLIANT": true}}
	muts := []Mutant{{ID: "m1", Code: "m1"}}
	rep, err := Score(context.Background(), fj, map[string]string{}, "code.go", "COMPLIANT", muts, []string{"go", "test"})
	if err != nil {
		t.Fatalf("a baseline timeout must fail closed, not error: %v", err)
	}
	if rep.CompliantPass {
		t.Fatal("want CompliantPass=false on a baseline timeout")
	}
	if rep.Total != 0 || len(rep.Killed) != 0 || len(rep.Survived) != 0 {
		t.Fatalf("no mutants should be scored against a timed-out baseline: %+v", rep)
	}
	if fj.calls != 1 {
		t.Fatalf("mutants must NOT run after a baseline timeout: calls=%d", fj.calls)
	}
}

// ignoringJail models a check command pointed at some OTHER package: it
// passes regardless of what the audited file contains, because it never
// compiles or imports it.
type ignoringJail struct{ runs int }

func (j *ignoringJail) RunTest(ctx context.Context, files map[string]string, cmd []string) (bool, error) {
	j.runs++
	return true, nil
}

// realJail models a command that DOES exercise the file: invalid source
// fails, and a mutant that changes behaviour fails too.
type realJail struct{ runs int }

func (j *realJail) RunTest(ctx context.Context, files map[string]string, cmd []string) (bool, error) {
	j.runs++
	return files["a.go"] == "package a\n", nil
}

func TestScoreCanarySurvivesWhenSuiteIgnoresTheFile(t *testing.T) {
	j := &ignoringJail{}
	rep, err := Score(context.Background(), j, map[string]string{}, "a.go", "package a\n",
		[]Mutant{{ID: "m1", Code: "package a // mutated\n"}}, []string{"true"})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if !rep.CompliantPass {
		t.Fatal("baseline should pass — this suite passes everything")
	}
	if rep.CanaryKilled {
		t.Fatal("canary must NOT be killed: the suite never compiles the file")
	}
	if rep.KillRate() != 0 {
		t.Errorf("KillRate = %v; a surviving canary must not yield a score", rep.KillRate())
	}
}

func TestScoreCanaryKilledLeavesTheMeasurementUntouched(t *testing.T) {
	j := &realJail{}
	rep, err := Score(context.Background(), j, map[string]string{}, "a.go", "package a\n",
		[]Mutant{{ID: "m1", Code: "package a // mutated\n"}}, []string{"true"})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if !rep.CanaryKilled {
		t.Fatal("canary should be killed: this suite does compile the file")
	}
	// The canary is a GATE, not a measurement: it must not appear in the tally.
	if rep.Total != 1 {
		t.Errorf("Total = %d, want 1 — the canary must not be counted", rep.Total)
	}
	if len(rep.Killed)+len(rep.Survived) != 1 {
		t.Errorf("killed+survived = %d, want 1 — the canary must not be listed",
			len(rep.Killed)+len(rep.Survived))
	}
	// Every reported id must be a MUTANT's id. Checked positively rather than
	// against a canary-id constant: the canary is not a Mutant and never
	// carries an id, so "no entry whose id is the canary's" could never fire.
	for _, id := range append(append([]string{}, rep.Killed...), rep.Survived...) {
		if id != "m1" {
			t.Errorf("report lists %q; the only graded subject was mutant m1 — the canary must never appear", id)
		}
	}
}

// The baseline-stability path calls Score with no mutants, twice per file.
// Running a canary there would triple its cost for no information.
func TestScoreRunsNoCanaryWithoutMutants(t *testing.T) {
	j := &ignoringJail{}
	rep, err := Score(context.Background(), j, map[string]string{}, "a.go", "package a\n", nil, []string{"true"})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if j.runs != 1 {
		t.Errorf("jail ran %d times for a zero-mutant Score, want 1 (baseline only)", j.runs)
	}
	if rep.CanaryKilled {
		t.Error("CanaryKilled must stay false when no canary ran")
	}
}

// TestScoreMutantOtherErrorPropagates confirms a NON-timeout mutant error
// (a real infra failure) still aborts Score as before — only ErrTestTimeout
// gets the "count as killed" treatment.
func TestScoreMutantOtherErrorPropagates(t *testing.T) {
	wantErr := errors.New("boom: sandbox could not start")
	fj := &errJail{err: wantErr}
	muts := []Mutant{{ID: "m1", Code: "m1"}}
	_, err := Score(context.Background(), fj, map[string]string{}, "code.go", "COMPLIANT", muts, []string{"go", "test"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("want the infra error propagated, got %v", err)
	}
}

// errJail always returns the given error (never ErrTestTimeout) — passes on
// the compliant baseline (so the mutant loop is reached) then errors.
type errJail struct {
	err   error
	calls int
}

func (f *errJail) RunTest(ctx context.Context, files map[string]string, testCmd []string) (bool, error) {
	f.calls++
	switch files["code.go"] {
	case "COMPLIANT":
		return true, nil
	case CanaryCode:
		// FAIL (not error) on invalid source, so the canary is KILLED and the
		// mutant loop is actually reached. Without this the canary run — which
		// happens first — would absorb the error and this fake would never
		// exercise the mutant branch it exists for.
		return false, nil
	}
	return false, f.err
}

// TestScoreCanaryTimeoutCountsAsKilled is the canary half of the
// mutant-timeout contract (TestScoreMutantTimeoutCountsAsKilled): a suite
// that HANGS on deliberately invalid source did react to it — it is not a
// suite that ignores the file — so the canary counts as killed and scoring
// proceeds normally. The alternative (treating a hang as a surviving canary)
// would report could-not-grade for a suite that demonstrably reads the file.
func TestScoreCanaryTimeoutCountsAsKilled(t *testing.T) {
	fj := &timeoutJail{
		passOn:    map[string]bool{"COMPLIANT": true, "m1": false},
		timeoutOn: map[string]bool{CanaryCode: true},
	}
	muts := []Mutant{{ID: "m1", Code: "m1"}}
	rep, err := Score(context.Background(), fj, map[string]string{}, "code.go", "COMPLIANT", muts, []string{"go", "test"})
	if err != nil {
		t.Fatalf("a canary timeout must not abort Score: %v", err)
	}
	if !rep.CanaryKilled {
		t.Fatal("a suite that hangs on invalid source DID react to it — CanaryKilled must be true")
	}
	if rep.Total != 1 || !eq(rep.Killed, []string{"m1"}) {
		t.Fatalf("scoring must proceed after a killed canary, got %+v", rep)
	}
	if rep.KillRate() != 1 {
		t.Errorf("KillRate = %v, want 1", rep.KillRate())
	}
}

// TestScoreCanaryOtherErrorPropagates: only ErrTestTimeout gets the
// count-as-killed treatment on the canary run. A real infra failure (the
// sandbox could not start) must abort Score, never be silently read as
// "canary killed" — which would let an unrunnable jail produce a graded
// report.
func TestScoreCanaryOtherErrorPropagates(t *testing.T) {
	wantErr := errors.New("boom: sandbox could not start")
	fj := &canaryErrJail{err: wantErr}
	muts := []Mutant{{ID: "m1", Code: "m1"}}
	_, err := Score(context.Background(), fj, map[string]string{}, "code.go", "COMPLIANT", muts, []string{"go", "test"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("want the infra error propagated from the canary run, got %v", err)
	}
	if fj.mutantRuns != 0 {
		t.Errorf("mutants ran (%d) after a failed canary run; nothing may be graded", fj.mutantRuns)
	}
}

// canaryErrJail passes the baseline and errors ONLY on the canary run.
type canaryErrJail struct {
	err        error
	mutantRuns int
}

func (f *canaryErrJail) RunTest(ctx context.Context, files map[string]string, testCmd []string) (bool, error) {
	switch files["code.go"] {
	case "COMPLIANT":
		return true, nil
	case CanaryCode:
		return false, f.err
	}
	f.mutantRuns++
	return false, nil
}
