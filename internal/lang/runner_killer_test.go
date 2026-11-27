// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func killerRow(t *testing.T, lang, id string) Runner {
	t.Helper()
	p, ok := ByName(lang)
	if !ok {
		t.Fatalf("no language %s", lang)
	}
	r, err := RunnerByID(p, id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestKillerCmdNarrowsTheRunByName(t *testing.T) {
	cases := []struct {
		lang, runner string
		argv         []string
		id           string
		want         []string
	}{
		{"javascript", "vitest", []string{"node", "./node_modules/vitest/vitest.mjs", "run", "--project", "domain"}, "A::b",
			[]string{"node", "./node_modules/vitest/vitest.mjs", "run", "--project", "domain", "-t", `^A(?: | > )b$`}},
		{"javascript", "jest", []string{"npx", "jest"}, "Calc::adds", []string{"npx", "jest", "--testNamePattern", "^Calc adds$"}},
		{"python", "pytest", []string{"python3", "-m", "pytest", "-q", "tests"}, "tests/test_a.py::test_x",
			[]string{"python3", "-m", "pytest", "-q", "tests/test_a.py::test_x"}},
		{"python", "pytest", []string{"python3", "-m", "pytest", "-p", "no:cacheprovider", "tests"}, "tests/test_a.py::test_x",
			[]string{"python3", "-m", "pytest", "-p", "no:cacheprovider", "tests/test_a.py::test_x"}},
		{"go", "go-test", []string{"go", "test", "./..."}, "TestX/sub", []string{"go", "test", "-run", "^TestX$/^sub$", "./..."}},
		{"go", "go-test", []string{"go", "test", "./..."}, "TestX/case_(a)", []string{"go", "test", "-run", `^TestX$/^case_\(a\)$`, "./..."}},
		{"go", "go-test", []string{"go", "test", "./..."}, "TestX/x|y", []string{"go", "test", "-run", `^TestX$/^x\|y$`, "./..."}},
		{"javascript", "vitest", []string{"node", "./node_modules/vitest/vitest.mjs", "watch"}, "A::b",
			[]string{"node", "./node_modules/vitest/vitest.mjs", "run", "-t", `^A(?: | > )b$`}},
		{"javascript", "vitest", []string{"node", "./node_modules/vitest/vitest.mjs"}, "A::b",
			[]string{"node", "./node_modules/vitest/vitest.mjs", "run", "-t", `^A(?: | > )b$`}},
		// mocha: the full title, anchored, added to the failing run's own argv.
		{"javascript", "mocha", []string{"npx", "mocha", "test"}, "Calc::adds (a+b)",
			[]string{"npx", "mocha", "test", "--grep", `^Calc adds \(a\+b\)$`}},
		{"typescript", "mocha", []string{"npx", "mocha", "--bail"}, "top level",
			[]string{"npx", "mocha", "--bail", "--grep", "^top level$"}},
		// rspec: the full description `Failed examples:` names, anchored.
		{"ruby", "rspec", []string{"bundle", "exec", "rspec", "spec"}, "Calc adds (a+b)",
			[]string{"bundle", "exec", "rspec", "spec", "-E", `\ACalc adds \(a\+b\)\z`}},
		{"ruby", "rspec", []string{"rspec"}, "MyApp::Calc top level", []string{"rspec", "-E", `\AMyApp::Calc top level\z`}},
		// cargo test: libtest's --exact, after the binaries' `--`.
		{"rust", "cargo-test", []string{"cargo", "test", "--workspace"}, "tests::adds",
			[]string{"cargo", "test", "--workspace", "--", "--exact", "tests::adds"}},
		{"rust", "cargo-test", []string{"cargo", "test", "-p", "a", "--", "--nocapture"}, "tests::adds",
			[]string{"cargo", "test", "-p", "a", "--", "--nocapture", "--exact", "tests::adds"}},
		// nextest: that test of that binary, before nextest's own `--`.
		{"rust", "cargo-nextest", []string{"cargo", "nextest", "run", "--workspace"}, "units-a::it integrates",
			[]string{"cargo", "nextest", "run", "--workspace", "-E", "binary_id(=units-a::it) & test(=integrates)"}},
		{"rust", "cargo-nextest", []string{"cargo", "nextest", "run", "--", "--nocapture"}, "rf tests::bad",
			[]string{"cargo", "nextest", "run", "-E", "binary_id(=rf) & test(=tests::bad)", "--", "--nocapture"}},
		// PHPUnit: the method, every data set of it, any namespace before it.
		{"php", "phpunit", []string{"vendor/bin/phpunit"}, "FTest::testData",
			[]string{"vendor/bin/phpunit", "--filter", `/(?:^|\\)FTest::testData(?: with data set .*)?$/`}},
		{"php", "phpunit", []string{"vendor/bin/phpunit", "tests"}, `App\Tests\FTest::testAdds`,
			[]string{"vendor/bin/phpunit", "tests", "--filter", `/(?:^|\\)App\\Tests\\FTest::testAdds(?: with data set .*)?$/`}},
	}
	for _, c := range cases {
		got, ok := killerRow(t, c.lang, c.runner).KillerCmd(c.argv, c.id)
		if !ok || !slices.Equal(got, c.want) {
			t.Errorf("%s: got %q ok=%v, want %q", c.runner, got, ok, c.want)
		}
	}
}

func TestKillerCmdRefusesAFilteredArgv(t *testing.T) {
	cases := []struct {
		lang, runner string
		argv         []string
		id           string
	}{
		{"go", "go-test", []string{"go", "test", "-run", "TestY", "./..."}, "TestX"},
		{"javascript", "vitest", []string{"node", "./node_modules/vitest/vitest.mjs", "run", "-t", "y"}, "A::b"},
		{"javascript", "jest", []string{"npx", "jest", "--testNamePattern", "y"}, "A::b"},
		{"python", "pytest", []string{"python3", "-m", "pytest", "-k", "y"}, "t.py::x"},
		{"go", "go-test", []string{"go", "test", "./..."}, ""},
		{"go", "go-test", []string{"make", "test"}, "TestX"},
		{"go", "go-test", []string{"sh", "-c", "go test ./..."}, "TestX"},
		{"javascript", "vitest", []string{"node", "./node_modules/vitest/vitest.mjs", "run"}, ""},
		{"javascript", "jest", []string{"npx", "jest"}, ""},
		{"javascript", "mocha", []string{"npx", "mocha", "--grep", "y"}, "A::b"},
		{"javascript", "mocha", []string{"npx", "mocha", "-g", "y"}, "A::b"},
		{"javascript", "mocha", []string{"npx", "mocha", "--fgrep", "y"}, "A::b"},
		{"javascript", "mocha", []string{"npx", "mocha", "--invert"}, "A::b"},
		{"javascript", "mocha", []string{"npx", "mocha"}, ""},
		{"ruby", "rspec", []string{"rspec", "-e", "adds"}, "Calc adds"},
		{"ruby", "rspec", []string{"rspec", "--example", "adds"}, "Calc adds"},
		{"ruby", "rspec", []string{"rspec"}, ""},
		{"php", "phpunit", []string{"vendor/bin/phpunit", "--filter", "x"}, "FTest::testAdds"},
		{"php", "phpunit", []string{"vendor/bin/phpunit"}, "FTest"},
		{"php", "phpunit", []string{"vendor/bin/phpunit"}, ""},
		// cargo test with a name filter of its own, a --skip, or a nextest id.
		{"rust", "cargo-test", []string{"cargo", "test", "adds"}, "tests::adds"},
		{"rust", "cargo-test", []string{"cargo", "test", "--", "--skip", "slow"}, "tests::adds"},
		{"rust", "cargo-test", []string{"cargo", "test"}, "units-a tests::adds"},
		{"rust", "cargo-test", []string{"sh", "-c", "cargo test"}, "tests::adds"},
		// nextest with a filterset of its own (unioned), or an id with no binary.
		{"rust", "cargo-nextest", []string{"cargo", "nextest", "run", "-E", "test(x)"}, "rf tests::bad"},
		{"rust", "cargo-nextest", []string{"cargo", "nextest", "run"}, "tests::bad"},
		{"rust", "cargo-nextest", []string{"cargo", "nextest", "run", "--partition", "count:1/2"}, "rf tests::bad"},
	}
	for _, c := range cases {
		if got, ok := killerRow(t, c.lang, c.runner).KillerCmd(c.argv, c.id); ok {
			t.Errorf("%s %q id %q: narrowed to %q, want a refusal", c.runner, c.argv, c.id, got)
		}
	}
}

func TestKillerCmdRefusesPytestErrorIDs(t *testing.T) {
	r := killerRow(t, "python", "pytest")
	for _, id := range []string{"error:tests/test_a.py", "test_x", ""} {
		if got, ok := r.KillerCmd([]string{"python3", "-m", "pytest", "-q"}, id); ok {
			t.Errorf("id %q: narrowed to %q, want a refusal", id, got)
		}
	}
}

func TestStripPyPositionalsKeepsOptionsAndTheirValues(t *testing.T) {
	cases := []struct{ in, want []string }{
		{[]string{"python3", "-m", "pytest", "-q", "tests"}, []string{"python3", "-m", "pytest", "-q"}},
		{[]string{"pytest", "--maxfail", "3", "a/", "b/test_x.py::t"}, []string{"pytest", "--maxfail", "3"}},
		{[]string{"pytest", "--maxfail=3", "a/"}, []string{"pytest", "--maxfail=3"}},
		{nil, nil},
	}
	for _, c := range cases {
		if got := stripPyPositionals(c.in); !slices.Equal(got, c.want) {
			t.Errorf("%q: got %q, want %q", c.in, got, c.want)
		}
	}
}

func TestKillerCmdForIsNilWithoutARow(t *testing.T) {
	p, _ := ByName("javascript")
	if KillerCmdFor(p, Invocation{Argv: []string{"node", "--test"}}) != nil {
		t.Error("node --test has no KillerCmd, KillerCmdFor must be nil")
	}
}

func TestKillerCmdForShapesThroughAPackageScriptAsSingleTestCmdDoes(t *testing.T) {
	p, _ := ByName("javascript")
	inv := Invocation{Argv: []string{"npm", "test"}, Script: []string{"jest"}, Pass: "--", Narrow: true}
	killer := KillerCmdFor(p, inv)
	if killer == nil {
		t.Fatal("KillerCmdFor is nil for a jest script")
	}
	got, ok := killer(inv.Argv, "Calc::adds")
	want := []string{"npm", "test", "--", "--testNamePattern", "^Calc adds$"}
	if !ok || !slices.Equal(got, want) {
		t.Fatalf("got %q ok=%v, want %q", got, ok, want)
	}
	single, ok := SingleTestCmd(p, inv, "calc.test.js", "Calc::adds")
	if !ok || !slices.Equal(single[:3], want[:3]) {
		t.Fatalf("SingleTestCmd shapes as %q; killer %q does not match its prefix", single, got)
	}
}

// A path-valued option's value is not a collection target. Dropping it made
// `pytest --junit-xml out.xml tests` + K into `pytest --junit-xml K`: pytest
// took K as the report path, ran the whole suite, and a confirmation that
// ran no single test was recorded "alone".
func TestPyKillerCmdKeepsAPathValuedOptionsValue(t *testing.T) {
	r := killerRow(t, "python", "pytest")
	for _, opt := range []string{"--junit-xml", "--html", "--alluredir", "--json-report-file", "--report-log"} {
		got, ok := r.KillerCmd([]string{"pytest", opt, "out.xml", "tests"}, "tests/test_a.py::test_x")
		want := []string{"pytest", opt, "out.xml", "tests/test_a.py::test_x"}
		if !ok || !slices.Equal(got, want) {
			t.Errorf("%s: got %q ok=%v, want %q", opt, got, ok, want)
		}
	}
}

// An unknown long option may take a value, and the word after it is then
// not a target to drop. Narrowing would guess; it refuses, and the kill is
// confirmed by repeating the grading run instead.
func TestPyKillerCmdRefusesAWordAfterAnUnknownLongOption(t *testing.T) {
	r := killerRow(t, "python", "pytest")
	if got, ok := r.KillerCmd([]string{"pytest", "--some-plugin-opt", "val", "tests"}, "tests/test_a.py::test_x"); ok {
		t.Errorf("narrowed to %q, want a refusal", got)
	}
	// A short flag is a boolean and still narrows.
	got, ok := r.KillerCmd([]string{"pytest", "-q", "tests"}, "tests/test_a.py::test_x")
	if want := []string{"pytest", "-q", "tests/test_a.py::test_x"}; !ok || !slices.Equal(got, want) {
		t.Errorf("-q tests: got %q ok=%v, want %q", got, ok, want)
	}
	// A long option carrying its own value consumes nothing.
	got, ok = r.KillerCmd([]string{"pytest", "--some-plugin-opt=val", "tests"}, "tests/test_a.py::test_x")
	if want := []string{"pytest", "--some-plugin-opt=val", "tests/test_a.py::test_x"}; !ok || !slices.Equal(got, want) {
		t.Errorf("--opt=val tests: got %q ok=%v, want %q", got, ok, want)
	}
}

// The capture script's killer checks (scripts/capture-runner-fixtures.sh,
// "Each killer command") ran these filters against the real runners; each
// must be what KillerCmd builds, so the row cannot drift from what was run.
func TestKillerFiltersAreTheOnesTheCaptureScriptRan(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "scripts", "capture-runner-fixtures.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ lang, runner, id string }{
		{"javascript", "mocha", "Calc::adds (a+b)"},
		{"javascript", "mocha", "Calc neg::adds"},
		{"javascript", "mocha", "top level"},
		{"ruby", "rspec", "Calc adds (a+b)"},
		{"ruby", "rspec", "MyApp::Calc top level"},
		{"php", "phpunit", "FTest::testAdds"},
		{"php", "phpunit", "FTest::testData"},
		{"rust", "cargo-nextest", "units-a::it integrates"},
	} {
		argv := []string{c.runner}
		if c.runner == "cargo-nextest" {
			argv = []string{"cargo", "nextest", "run", "--workspace"}
		}
		cmd, ok := killerRow(t, c.lang, c.runner).KillerCmd(argv, c.id)
		if !ok {
			t.Fatalf("%s %q: no killer command", c.runner, c.id)
		}
		if filter := "'" + cmd[len(cmd)-1] + "'"; !strings.Contains(string(b), filter) {
			t.Errorf("%s %q: the capture script ran no %s", c.runner, c.id, filter)
		}
	}
}

// A failing data set's killer command runs that data set alone: the ids
// phpFailures reads from the captured runs, made into KillerCmd's filter,
// are the filters the capture script ran and saw run exactly one test
// ("phpunit-killer-one-dataset*"). With the printed argument suffix kept in
// the id (`with data set "two" (2)`), the filter selected no test at all
// (PHPUnit 11.5.57: "No tests executed!").
func TestPHPUnitKillerRunsTheFailingDataSetAlone(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "scripts", "capture-runner-fixtures.sh"))
	if err != nil {
		t.Fatal(err)
	}
	r := killerRow(t, "php", "phpunit")
	for _, c := range []struct{ fixture, id string }{
		{"phpunit-fail.txt", `FTest::testData with data set "two"`},
		{"phpunit-datasets-fail.txt", `DTest::testNamed with data set "x (y)"`},
		{"phpunit-datasets-fail.txt", "DTest::testUnnamed with data set #2"},
	} {
		if !slices.Contains(phpFailures(readFixture(t, c.fixture)), c.id) {
			t.Errorf("%s: phpFailures names no %q (got %q)", c.fixture, c.id, phpFailures(readFixture(t, c.fixture)))
			continue
		}
		cmd, ok := r.KillerCmd([]string{"phpunit"}, c.id)
		if !ok {
			t.Fatalf("%q: no killer command", c.id)
		}
		filter := "'" + cmd[len(cmd)-1] + "'"
		var ran bool
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "want phpunit-killer-one-dataset") && strings.Contains(line, `"Tests: 1, Assertions: 1, Failures: 1"`) && strings.Contains(line, filter) {
				ran = true
			}
		}
		if !ran {
			t.Errorf("%q: the capture script saw no %s run exactly one test", c.id, filter)
		}
	}
}

// The failures mocha's reader takes from the captured failing run are the
// ids the capture script's killer checks ran, each selecting one test: what
// the scorer blames is what the killer runs.
func TestMochaKillerRunsTheCapturedFailuresAlone(t *testing.T) {
	ids := mochaFailures(readFixture(t, "mocha-fail.txt"))
	want := []string{"top level", "Calc::adds (a+b)", "Calc neg::adds"}
	if !slices.Equal(ids, want) {
		t.Fatalf("mochaFailures = %q, want %q", ids, want)
	}
	b, err := os.ReadFile(filepath.Join("..", "..", "scripts", "capture-runner-fixtures.sh"))
	if err != nil {
		t.Fatal(err)
	}
	r := killerRow(t, "javascript", "mocha")
	for _, id := range ids {
		cmd, ok := r.KillerCmd([]string{"npx", "mocha", "fail/f.mocha.js"}, id)
		if !ok {
			t.Fatalf("%q: no killer command", id)
		}
		filter := "'" + cmd[len(cmd)-1] + "'"
		var ran bool
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "want mocha-killer") && strings.Contains(line, `"0 passing 1 failing"`) && strings.Contains(line, filter) {
				ran = true
			}
		}
		if !ran {
			t.Errorf("%q: the capture script saw no %s run exactly one failing test", id, filter)
		}
	}
}

// Through a package script the killer's words follow the script's `--`,
// as the jest script's do: the scorer asks through KillerCmdFor.
func TestKillerCmdForNarrowsAMochaPackageScript(t *testing.T) {
	p, _ := ByName("javascript")
	inv := Invocation{Argv: []string{"npm", "test"}, Script: []string{"mocha"}, Pass: "--", Narrow: true}
	killer := KillerCmdFor(p, inv)
	if killer == nil {
		t.Fatal("KillerCmdFor is nil for a mocha script")
	}
	got, ok := killer(inv.Argv, "Calc neg::adds")
	want := []string{"npm", "test", "--", "--grep", "^Calc neg adds$"}
	if !ok || !slices.Equal(got, want) {
		t.Fatalf("got %q ok=%v, want %q", got, ok, want)
	}
}
