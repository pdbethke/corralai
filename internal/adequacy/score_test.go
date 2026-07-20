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
	if files["code.go"] == "COMPLIANT" {
		return true, nil
	}
	return false, f.err
}
