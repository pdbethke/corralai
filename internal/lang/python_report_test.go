// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// reportFixture is a pytest project with one passing test, one failing in
// its call phase, one failing whose parametrized id holds a space, and one
// whose fixture raises (an error, not a failure). addopts is the project's
// own pytest configuration.
func reportFixture(t *testing.T, addopts string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "tests"), 0o750); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"pyproject.toml": "[tool.pytest.ini_options]\naddopts = \"" + addopts + "\"\n",
		"tests/test_x.py": `import pytest


def test_good():
    assert True


def test_bad():
    assert 1 == 2


@pytest.mark.parametrize("v", ["a b"])
def test_spaced(v):
    assert v == 1


@pytest.fixture
def boom():
    raise RuntimeError("setup")


def test_setup(boom):
    pass
`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// runIn runs argv in dir with the extra environment and returns its combined
// output and exit code.
func runIn(t *testing.T, dir string, argv []string, env ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...) // #nosec G204 -- a test's own fixture command
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return string(out), 0
	case errors.As(err, &exit):
		return string(out), exit.ExitCode()
	}
	t.Fatalf("%v did not run: %v", argv, err)
	return "", 0
}

// rewritten is the graded form of a pytest command, which must be the hook's
// wrapper.
func rewritten(t *testing.T, operator []string) []string {
	t.Helper()
	p, _ := ByName("python")
	out, applied := ReportCmd(p, Cmd(operator), operator)
	if !applied || !cmdIsShellWrapped(out) {
		t.Fatalf("ReportCmd(%v) = %v applied=%v, want the report wrapper", operator, out, applied)
	}
	return out
}

// The killing test is read from pytest's own reports, so it is named
// whatever the project's configuration prints: under pytest's defaults, under
// a project -r, under -q -q, and under the two settings no flag undoes
// (--no-summary, -p no:terminal). Each id is whole, a space in it included, and a
// test whose fixture raised is an error, not a failure. The control half runs
// the operator's own command and shows that those two settings really do hide
// every printed line (-q -q does not, on the pytest this ran on).
func TestPytestHookNamesTheFailingTestsWhateverTheProjectPrints(t *testing.T) {
	py := pytestFor(t)
	hidesPrintedLines := map[string]bool{"--no-summary": true, "-p no:terminal": true}
	for _, addopts := range []string{"", "-rXs", "--no-summary", "-p no:terminal", "-q -q"} {
		t.Run("addopts="+addopts, func(t *testing.T) {
			dir := reportFixture(t, addopts)
			operator := []string{py, "-m", "pytest", "tests", "-p", "no:cacheprovider"}

			if hidesPrintedLines[addopts] {
				plain, code := runIn(t, dir, operator)
				if code != 1 {
					t.Fatalf("control: %v exited %d, want 1:\n%s", operator, code, plain)
				}
				if got := pyFirstFailure([]byte(plain)); got != "" {
					t.Fatalf("control: the printed lines name %q under addopts %q; this setting no longer hides them:\n%s", got, addopts, plain)
				}
			}

			out, code := runIn(t, dir, rewritten(t, operator))
			if code != 1 {
				t.Fatalf("exit %d, want pytest's 1:\n%s", code, out)
			}
			recs, ok := pyReportBlock(out)
			if !ok {
				t.Fatalf("no complete marker block in:\n%s", out)
			}
			want := []pyReportRec{
				{pyReportFailed, "tests/test_x.py::test_bad"},
				{pyReportFailed, "tests/test_x.py::test_spaced[a b]"},
				{pyReportError, "tests/test_x.py::test_setup"},
			}
			if !slices.Equal(recs, want) {
				t.Errorf("records = %v, want %v", recs, want)
			}
			if got := pyFirstFailure([]byte(out)); got != "tests/test_x.py::test_bad" {
				t.Errorf("FirstFailure = %q, want tests/test_x.py::test_bad", got)
			}
			if got, want := pyFailedTests(out), []string{"tests/test_x.py::test_bad", "tests/test_x.py::test_spaced[a b]"}; !slices.Equal(got, want) {
				t.Errorf("failed tests = %v, want %v", got, want)
			}
		})
	}
}

// A setup error that comes first is reported as an error, as the printed
// ERROR line is, and never as a failing test.
func TestPytestHookReportsASetupErrorAsAnError(t *testing.T) {
	py := pytestFor(t)
	dir := reportFixture(t, "--no-summary")
	out, code := runIn(t, dir, rewritten(t, []string{py, "-m", "pytest", "tests/test_x.py::test_setup", "-p", "no:cacheprovider"}))
	if code != 1 {
		t.Fatalf("exit %d, want 1:\n%s", code, out)
	}
	if got := pyFirstFailure([]byte(out)); got != "error:tests/test_x.py::test_setup" {
		t.Errorf("FirstFailure = %q, want error:tests/test_x.py::test_setup", got)
	}
	if got := pyFailedTests(out); len(got) != 0 {
		t.Errorf("failed tests = %v, want none: an error is not a failure", got)
	}
}

// The wrapper's exit code is pytest's: 0 for a passing run (whose block is
// present and empty, the plugin's "nothing failed"), 1 for a failing one, 2
// for a collection error (recorded as an error that names no test), 4 for a
// usage error (pytest never configured the plugin: no block) and 5 for a run
// that collected no test.
func TestPytestHookKeepsPytestsExitCode(t *testing.T) {
	py := pytestFor(t)
	dir := reportFixture(t, "")
	if err := os.WriteFile(filepath.Join(dir, "tests", "test_broken.py"), []byte("def test_x(:\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := []string{py, "-m", "pytest", "-p", "no:cacheprovider"}
	cases := []struct {
		name      string
		args      []string
		exit      int
		block     bool
		wantFirst string
	}{
		{"pass", []string{"tests/test_x.py::test_good"}, 0, true, ""},
		{"fail", []string{"tests/test_x.py::test_bad"}, 1, true, "tests/test_x.py::test_bad"},
		{"collection error", []string{"tests/test_broken.py"}, 2, true, ""},
		{"usage error", []string{"--no-such-option", "tests/test_x.py"}, 4, false, ""},
		{"no tests", []string{"tests/test_x.py", "-k", "matches_nothing"}, 5, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			operator := append(slices.Clone(base), c.args...)
			plain, plainCode := runIn(t, dir, operator)
			if plainCode != c.exit {
				t.Fatalf("control: the operator's own command exited %d, want %d:\n%s", plainCode, c.exit, plain)
			}
			out, code := runIn(t, dir, rewritten(t, operator))
			if code != c.exit {
				t.Errorf("exit %d, want pytest's %d:\n%s", code, c.exit, out)
			}
			recs, ok := pyReportBlock(out)
			if ok != c.block {
				t.Fatalf("complete block = %v, want %v, in:\n%s", ok, c.block, out)
			}
			if c.name == "pass" || c.name == "no tests" {
				if len(recs) != 0 {
					t.Errorf("records = %v, want none", recs)
				}
			}
			if c.name == "collection error" {
				if want := []pyReportRec{{pyReportError, "tests/test_broken.py"}}; !slices.Equal(recs, want) {
					t.Errorf("records = %v, want %v", recs, want)
				}
			}
			if got := pyFirstFailure([]byte(out)); got != c.wantFirst {
				t.Errorf("FirstFailure = %q, want %q", got, c.wantFirst)
			}
		})
	}
}

// The plugin's directory goes LAST on PYTHONPATH: a pool tree (TreeEnv puts
// it first) and the operator's own entries stay ahead of it, in their order.
func TestPytestHookPutsItsDirectoryLastOnPythonPath(t *testing.T) {
	py := pytestFor(t)
	dir := t.TempDir()
	tree, own := filepath.Join(dir, "tree"), filepath.Join(dir, "own")
	for _, d := range []string{tree, own, filepath.Join(dir, "tests")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	test := `import json, os, sys


def test_path():
    hook = os.path.dirname(sys.modules["corral_report"].__file__)
    with open("seen.json", "w") as out:
        json.dump({"hook": hook, "path": sys.path}, out)
`
	if err := os.WriteFile(filepath.Join(dir, "tests", "test_path.py"), []byte(test), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		env  []string
		want []string // entries that must precede the hook's directory, in order
	}{
		{"tree and operator entries", []string{"PYTHONPATH=" + tree + string(os.PathListSeparator) + own}, []string{tree, own}},
		{"no PYTHONPATH", []string{"PYTHONPATH="}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, code := runIn(t, dir, rewritten(t, []string{py, "-m", "pytest", "tests", "-p", "no:cacheprovider"}), c.env...)
			if code != 0 {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			raw, err := os.ReadFile(filepath.Join(dir, "seen.json"))
			if err != nil {
				t.Fatal(err)
			}
			var seen struct {
				Hook string
				Path []string
			}
			if err := json.Unmarshal(raw, &seen); err != nil {
				t.Fatal(err)
			}
			hookAt := slices.Index(seen.Path, seen.Hook)
			if hookAt < 0 {
				t.Fatalf("the hook's directory %s is not on sys.path %v", seen.Hook, seen.Path)
			}
			last := -1
			for _, w := range c.want {
				at := slices.Index(seen.Path, w)
				if at < 0 || at <= last || at > hookAt {
					t.Errorf("%s is at %d and the hook's directory at %d in %v; want %v in order, ahead of the hook", w, at, hookAt, seen.Path, c.want)
				}
				last = at
			}
			if _, err := os.Stat(seen.Hook); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the hook's temporary directory %s was not removed (stat: %v)", seen.Hook, err)
			}
		})
	}
}

// The operator's words reach pytest exactly as written: the wrapper passes
// them as positional parameters, so the shell never reads a space, a quote
// or a `$` in one. Asserted on what pytest itself received.
func TestPytestHookPassesTheOperatorsWordsUnchanged(t *testing.T) {
	py := pytestFor(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "tests"), 0o750); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"conftest.py": `import json


def pytest_configure(config):
    with open("args.json", "w") as out:
        json.dump(list(config.invocation_params.args), out)
`,
		"tests/test_ok.py": "def test_ok():\n    assert True\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	odd := `tests/test_ok.py::it's a $HOME "test" \ * ; echo`
	args := []string{"tests", "-p", "no:cacheprovider", "--deselect", odd, "--", "tests/test_ok.py"}
	out, code := runIn(t, dir, rewritten(t, append([]string{py, "-m", "pytest"}, args...)))
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "args.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	// What corral adds goes before pytest's own `--`, and nowhere else.
	want := []string{"tests", "-p", "no:cacheprovider", "--deselect", odd, "-rfE", "-p", pyReportModule, "--", "tests/test_ok.py"}
	if !slices.Equal(got, want) {
		t.Errorf("pytest received %q\nwant            %q", got, want)
	}
}

// The readers take the block only when it is complete, and the LAST one:
// without an end line (the session never finished, or the cap cut the
// output) they read the printed lines, the second source, exactly as they
// did before the hook existed.
func TestPytestReadersFallBackWithoutACompleteBlock(t *testing.T) {
	printed := "FAILED tests/a.py::test_printed - boom\n"
	cases := []struct {
		name, out, first string
		failed           []string
	}{
		{"no block", printed, "tests/a.py::test_printed", []string{"tests/a.py::test_printed"}},
		// The printed lines cannot say where an id with a space ends: the
		// second source cuts it there, which is why it is the second.
		{"no block, spaced id", "FAILED tests/a.py::test_p[a b] - boom\n", "tests/a.py::test_p[a", []string{"tests/a.py::test_p[a"}},
		{"begin without end", printed + "corral-report-begin\ncorral-report failed tests/a.py::test_hook\n",
			"tests/a.py::test_printed", []string{"tests/a.py::test_printed"}},
		{"end without begin (the cap cut the block's head)", printed + "corral-report failed tests/a.py::test_hook\ncorral-report-end\n",
			"tests/a.py::test_printed", []string{"tests/a.py::test_printed"}},
		{"complete block wins over the printed lines", printed + "corral-report-begin\ncorral-report failed tests/a.py::test_hook[a b]\ncorral-report-end\n",
			"tests/a.py::test_hook[a b]", []string{"tests/a.py::test_hook[a b]"}},
		{"complete empty block: nothing failed", printed + "corral-report-begin\ncorral-report-end\n", "", nil},
		{"an id fails twice (call, then teardown)", "corral-report-begin\ncorral-report failed t.py::a\ncorral-report error t.py::a\ncorral-report failed t.py::a\ncorral-report-end\n",
			"t.py::a", []string{"t.py::a"}},
		{"error first", "corral-report-begin\ncorral-report error t.py::a\ncorral-report failed t.py::b\ncorral-report-end\n",
			"error:t.py::a", []string{"t.py::b"}},
		{"a collector names no test", "corral-report-begin\ncorral-report error tests/broken.py\ncorral-report failed t.py::b\ncorral-report-end\n",
			"t.py::b", []string{"t.py::b"}},
		{"the last block", "corral-report-begin\ncorral-report failed t.py::inner\ncorral-report-end\ncorral-report-begin\ncorral-report failed t.py::outer\ncorral-report-end\n",
			"t.py::outer", []string{"t.py::outer"}},
		{"unknown kind and CRLF", "corral-report-begin\r\ncorral-report passed t.py::a\r\ncorral-report failed t.py::b\r\ncorral-report-end\r\n",
			"t.py::b", []string{"t.py::b"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pyFirstFailure([]byte(c.out)); got != c.first {
				t.Errorf("FirstFailure = %q, want %q", got, c.first)
			}
			if got := pyFailedTests(c.out); !slices.Equal(got, c.failed) {
				t.Errorf("failed tests = %v, want %v", got, c.failed)
			}
		})
	}
}

// The jail binds a command's toolchain by the programs it runs, and reads a
// wrapper's `"$@"` as the operator's own command: the interpreter pytest
// runs under is still found through the wrapper.
func TestPytestHookWrapperStillNamesTheOperatorsInterpreter(t *testing.T) {
	got := InterpretersIn(rewritten(t, []string{"/opt/py/bin/python3", "-m", "pytest", "tests"}))
	if !slices.Contains(got, "/opt/py/bin/python3") {
		t.Errorf("InterpretersIn = %v, want the operator's interpreter among them", got)
	}
}

// The scan line says what corral adds: the flags when it adds them, the
// wrapper whenever it wraps (an operator's own -rfE does not stop that), and
// nothing for a command it cannot read.
func TestRunDisclosuresSayWhenTheHookWraps(t *testing.T) {
	p, _ := ByName("python")
	const hook = "pytest: corral runs every graded run through `sh -c` with a pytest plugin of its own loaded (-p corral_report"
	has := func(argv []string, prefix string) bool {
		return slices.ContainsFunc(RunDisclosures(p, t.TempDir(), Cmd(argv)), func(l string) bool { return strings.HasPrefix(l, prefix) })
	}
	for _, argv := range [][]string{
		{"python3", "-m", "pytest", "tests"},
		{"python3", "-m", "pytest", "tests", "-rfE"},
	} {
		if !has(argv, hook) {
			t.Errorf("%v: no disclosure of the wrapper corral runs it through", argv)
		}
	}
	for _, argv := range [][]string{
		{"sh", "-c", "pytest tests"},
		{"python3", "-m", "unittest"},
		{"tox", "-e", "py"},
	} {
		if has(argv, hook) {
			t.Errorf("%v: the wrapper is disclosed though corral does not wrap it", argv)
		}
	}
}
