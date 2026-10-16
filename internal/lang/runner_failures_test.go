// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"slices"
	"testing"
)

// Each runner's failure parser over what the real runner printed
// (testdata/runners/*-fail.txt, scripts/capture-runner-fixtures.sh): the three
// failing tests, in report order, in the form the runner's list gives them.
func TestFailureParsersReadTheRealOutput(t *testing.T) {
	for _, c := range []struct {
		fixture string
		parse   func(string) []string
		want    []string
	}{
		{"jest-fail.txt", jestFailures, []string{"Calc::adds (a+b)", "Calc::neg::adds", "top level"}},
		{"vitest-fail.txt", vitestFailures, []string{"Calc::adds (a+b)", "Calc::neg::adds", "top level"}},
		{"mocha-fail.txt", mochaFailures, []string{"top level", "Calc::adds (a+b)", "Calc neg::adds"}},
		{"node-fail.txt", nodeFailures, []string{"Calc::adds (a+b)", "Calc::neg::adds", "top level"}},
		{"rspec-fail.txt", rspecFailures, []string{"Calc adds (a+b)", "Calc neg adds", "MyApp::Calc top level"}},
		{"minitest-fail.txt", minitestFailures, []string{"MyApp::NegTest#test_x", "CalcTest#test_adds", "CalcTest#test_boom"}},
		{"phpunit-fail.txt", phpFailures, []string{"FTest::testBoom", "FTest::testAdds", `FTest::testData with data set "two" (2)`}},
	} {
		got := c.parse(readFixture(t, c.fixture))
		if c.fixture == "minitest-fail.txt" {
			// minitest runs in a random order per seed; the set is what counts.
			slices.Sort(got)
			slices.Sort(c.want)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: %q, want %q", c.fixture, got, c.want)
		}
	}
}

// A file that failed to load, or a failing hook, ran no test: nothing is
// named, so no kill is credited to a test and no salvage is attempted.
func TestFailureParsersNameNoTestForALoadFailure(t *testing.T) {
	for _, c := range []struct {
		fixture string
		parse   func(string) []string
	}{
		{"jest-suitefail.txt", jestFailures},
		{"vitest-suitefail.txt", vitestFailures},
		{"mocha-hookfail.txt", mochaFailures},
		{"rspec-loadfail.txt", rspecFailures},
		{"node-tap-loadfail.txt", nodeFailures},
	} {
		if got := c.parse(readFixture(t, c.fixture)); got != nil {
			t.Errorf("%s named %q", c.fixture, got)
		}
	}
}

// What is not a failure entry: jest's console blocks, mocha's progress list
// above "N failing" (leaf titles only), a node suite rolled up as failed
// because a subtest failed.
func TestFailureParsersSkipWhatIsNotAFailure(t *testing.T) {
	jest := "  ● Console\n\n    console.log\n      hi\n\n  ● Calc › adds\n"
	if got := jestFailures(jest); !slices.Equal(got, []string{"Calc::adds"}) {
		t.Errorf("jest = %q", got)
	}
	mocha := "  Calc\n    1) adds\n\n  0 passing\n"
	if got := mochaFailures(mocha); got != nil {
		t.Errorf("mocha read the progress list: %q", got)
	}
	// A blank line before any title ending ":" is not an entry.
	if got := mochaFailures("  1 failing\n\n  1) Calc\n\n     Error: x\n"); got != nil {
		t.Errorf("mocha read an unterminated entry: %q", got)
	}
}

// The first failure is the parser's first entry, so the killed_by label and
// the salvage list can never disagree; phpunit's answers are unchanged.
func TestFirstFailureIsTheFirstFailedTest(t *testing.T) {
	out := readFixture(t, "rspec-fail.txt")
	if got := firstOf(rspecFailures)([]byte(out)); got != "Calc adds (a+b)" {
		t.Errorf("first = %q", got)
	}
	if got := firstOf(rspecFailures)(nil); got != "" {
		t.Errorf("first of nothing = %q", got)
	}
	php := readFixture(t, "phpunit-fail.txt")
	if got := phpFirstFailure([]byte(php)); got != "FTest::testBoom" {
		t.Errorf("phpFirstFailure = %q", got)
	}
}

// go's salvage list is top-level tests: -skip takes one pattern per level, so
// a failing subtest skips its parent (narrower, never wrong).
func TestGoFailedTestsAreTopLevel(t *testing.T) {
	out := "--- FAIL: TestA (0.00s)\n    --- FAIL: TestA/sub (0.00s)\n--- FAIL: TestB (0.00s)\nFAIL\n"
	if got := goFailedTests(out); !slices.Equal(got, []string{"TestA", "TestB"}) {
		t.Errorf("goFailedTests = %q", got)
	}
}
