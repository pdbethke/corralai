// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/pdbethke/corralai/internal/queue"
)

// TestFixtureAnswerKeysAreExecuted: an answer key that says a test "can never
// fail" is a claim, so it is executed. Each fixture runs with `go test`
// twice: against its real code every test passes, and against its broken
// code EXACTLY the tests marked vacuous still pass while every other one
// fails. A wrong label fails here instead of quietly grading the critic
// against a mistake.
func TestFixtureAnswerKeysAreExecuted(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	for _, f := range fixtures {
		passedReal := runFixture(t, f, f.code)
		if len(passedReal) != len(f.all) {
			t.Errorf("%s: against the real code %d of %d tests pass: %v", f.name, len(passedReal), len(f.all), passedReal)
		}
		passedBroken := runFixture(t, f, f.broken)
		want := append([]string(nil), f.vacuous...)
		sort.Strings(want)
		if !equal(passedBroken, want) {
			t.Errorf("%s: against the broken code these pass %v; the answer key says only %v can never fail", f.name, passedBroken, want)
		}
	}
}

// runFixture writes the fixture as a module and returns the tests that passed.
func runFixture(t *testing.T, f fixture, code string) []string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module fixture\n\ngo 1.22\n")
	write(f.codePath, code)
	write(f.testPath, f.tests)
	cmd := exec.Command("go", "test", "-json", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=", "GOWORK=off")
	out, _ := cmd.Output()
	var passed []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		var ev struct{ Action, Test string }
		if json.Unmarshal(sc.Bytes(), &ev) == nil && ev.Action == "pass" && ev.Test != "" {
			passed = append(passed, ev.Test)
		}
	}
	sort.Strings(passed)
	return passed
}

func equal(a, b []string) bool {
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

// grade scores by test NAME against the key: a right flag, a miss, and a
// false alarm (a sound test flagged, or a name not in the file at all).
func TestGrade(t *testing.T) {
	f := fixtures[0] // wallet: vacuous TestDepositRuns, TestWithdrawTautology
	flagged, right, missed, fals := grade(f, []queue.Finding{
		{Target: "TestDepositRuns"},
		{TestSelector: "wallet/wallet_test.go::TestDepositAdds"},
		{Target: "TestDoesNotExist"},
		{Target: "TestDepositRuns asserts nothing"}, // a duplicate is not counted twice
	})
	if right != 1 || missed != 1 || fals != 2 {
		t.Fatalf("right %d missed %d false %d, want 1 1 2 (flagged %v)", right, missed, fals, flagged)
	}
}
