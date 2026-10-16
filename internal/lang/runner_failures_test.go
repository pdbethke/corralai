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
		{"node-spec-fail.txt", nodeFailures, []string{"Calc::adds (a+b)", "Calc::neg::adds", "top level"}},
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
		{"node-spec-loadfail.txt", nodeFailures},
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
	// A suite that merely starts with "Console" is a test; --detectOpenHandles
	// prints its handles as "●  TCPSERVERWRAP", two spaces in.
	if got := jestFailures("  ● Console logger › prints\n  ●  TCPSERVERWRAP\n"); !slices.Equal(got, []string{"Console logger::prints"}) {
		t.Errorf("jest headings = %q", got)
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

// node's spec tree closes each suite with a line of its own name at the
// suite's depth; a test with the same name one level down is a test, and a
// test with subtests is rolled up like a suite.
func TestNodeSpecTreeClosesSuitesByDepth(t *testing.T) {
	out := "▶ X\n  ✖ X (1ms)\n  ▶ p\n    ✖ c (1ms)\n  ✖ p (1ms)\n✖ X (2ms)\n"
	if got := nodeFailures(out); !slices.Equal(got, []string{"X::X", "X::p::c"}) {
		t.Errorf("nodeFailures = %q", got)
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

// Each row's skip command for three failing ids holding regex syntax: the
// shape scripts/capture-runner-fixtures.sh ran against the real runner (its
// *-skip checks), appended to the operator's command.
func TestDeselectCmds(t *testing.T) {
	for _, c := range []struct {
		name string
		cmd  func(argv, ids []string) ([]string, bool)
		argv []string
		ids  []string
		want []string
	}{
		{"jest", jestDeselectCmd, []string{"npx", "jest"}, []string{"Calc::adds (a+b)", "Calc::neg::adds", "top level"},
			[]string{"npx", "jest", "--testNamePattern", `^(?!(?:Calc adds \(a\+b\)|Calc neg adds|top level)$)`}},
		{"vitest", vitestDeselectCmd, []string{"npx", "vitest", "run"}, []string{"Calc::adds (a+b)", "top level"},
			[]string{"npx", "vitest", "run", "-t", `^(?!(?:Calc adds \(a\+b\)|top level)$)`}},
		{"mocha", mochaDeselectCmd, []string{"npx", "mocha"}, []string{"top level", "Calc::adds (a+b)", "Calc neg::adds"},
			[]string{"npx", "mocha", "--grep", `^(?:top level|Calc adds \(a\+b\)|Calc neg adds)$`, "--invert"}},
		{"node", nodeDeselectCmd, []string{"node", "--test", "tests/"}, []string{"Calc::adds (a+b)", "top level"},
			[]string{"node", "--test", "--test-skip-pattern", `^(?:Calc adds \(a\+b\)|top level)$`, "tests/"}},
		{"rspec", rspecDeselectCmd, []string{"bundle", "exec", "rspec"}, []string{"Calc adds (a+b)", "MyApp::Calc top level"},
			[]string{"bundle", "exec", "rspec", "-E", `\A(?!(?:Calc adds \(a\+b\)|MyApp::Calc top level)\z)`}},
		{"minitest", minitestDeselectCmd, []string{"ruby", "-Itest", "test/f_test.rb"}, []string{"CalcTest#test_adds", "MyApp::NegTest#test_x"},
			[]string{"ruby", "-Itest", "test/f_test.rb", "--exclude", `/\A(?:CalcTest#test_adds|MyApp::NegTest#test_x)\z/`}},
		{"phpunit", phpunitDeselectCmd, []string{"vendor/bin/phpunit"}, []string{"FTest::testBoom", `App\Tests\FTest::testAdds`, `FTest::testData with data set "two" (2)`},
			[]string{"vendor/bin/phpunit", "--exclude-filter", `/(?:^|\\)(?:FTest::testBoom|FTest::testAdds|FTest::testData)(?: with data set .*)?$/`}},
		{"go", goDeselectCmd, []string{"go", "test", "./..."}, []string{"TestA", "TestB"},
			[]string{"go", "test", "./...", "-skip", `^(?:TestA|TestB)$`}},
	} {
		if got, ok := c.cmd(c.argv, c.ids); !ok || !slices.Equal(got, c.want) {
			t.Errorf("%s = %q, %v\n want %q", c.name, got, ok, c.want)
		}
		if got, ok := c.cmd(c.argv, nil); ok {
			t.Errorf("%s skipped nothing: %q", c.name, got)
		}
	}
}

// A skip refuses where a one-test run refuses: the operator's own name
// filter (ORed or overridden), a shell-wrapped command, and for minitest a
// launcher that hands no arguments to the test file (rake, testrb).
func TestDeselectCmdsRefuse(t *testing.T) {
	for _, c := range []struct {
		name string
		cmd  func(argv, ids []string) ([]string, bool)
		argv []string
	}{
		{"jest own -t", jestDeselectCmd, []string{"jest", "-t", "x"}},
		{"vitest own -t", vitestDeselectCmd, []string{"vitest", "run", "-t", "x"}},
		{"jest own kebab", jestDeselectCmd, []string{"jest", "--test-name-pattern=x"}},
		{"vitest own kebab", vitestDeselectCmd, []string{"vitest", "run", "--test-name-pattern", "x"}},
		{"vitest not vitest", vitestDeselectCmd, []string{"node", "x.js"}},
		{"mocha own --grep", mochaDeselectCmd, []string{"mocha", "--grep", "x"}},
		{"node own pattern", nodeDeselectCmd, []string{"node", "--test", "--test-name-pattern=x"}},
		{"node not --test", nodeDeselectCmd, []string{"node", "a.js"}},
		{"rspec own -e", rspecDeselectCmd, []string{"rspec", "-e", "x"}},
		{"rspec shell", rspecDeselectCmd, []string{"sh", "-c", "rspec"}},
		{"minitest rake", minitestDeselectCmd, []string{"rake", "test"}},
		{"minitest no file", minitestDeselectCmd, []string{"ruby", "-e", "1"}},
		{"minitest own -n", minitestDeselectCmd, []string{"ruby", "t.rb", "-n", "x"}},
		{"phpunit own filter", phpunitDeselectCmd, []string{"phpunit", "--filter", "x"}},
		{"go own -skip", goDeselectCmd, []string{"go", "test", "-skip", "X", "./..."}},
		{"go not go test", goDeselectCmd, []string{"make", "test"}},
	} {
		if got, ok := c.cmd(c.argv, []string{"T::x"}); ok {
			t.Errorf("%s: %q", c.name, got)
		}
	}
}
