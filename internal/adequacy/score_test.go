// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
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
