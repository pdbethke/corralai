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
	"time"

	"github.com/pdbethke/corralai/internal/clip"
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
    hook = os.path.dirname(sys.modules["` + pyReportModule + `"].__file__)
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
	// The one thing corral adds to the words is -rfE, before pytest's own
	// `--`; the plugin is loaded through the environment.
	want := []string{"tests", "-p", "no:cacheprovider", "--deselect", odd, "-rfE", "--", "tests/test_ok.py"}
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
// wrapper's `"$@"` as the operator's own command. Shown on a real run: the
// interpreter is a link in a directory of its own, InterpretersIn names that
// path through the wrapper, and the run executed under it.
func TestPytestHookWrapperStillNamesTheOperatorsInterpreter(t *testing.T) {
	py := pytestFor(t)
	real, err := exec.LookPath(py)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "bin", "python-of-the-operator")
	if err := os.MkdirAll(filepath.Join(dir, "tests"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "bin"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("SKIP: cannot link an interpreter into the fixture: %v", err)
	}
	test := "import sys\n\n\ndef test_exe():\n    open('exe.txt', 'w').write(sys.executable)\n"
	if err := os.WriteFile(filepath.Join(dir, "tests", "test_exe.py"), []byte(test), 0o600); err != nil {
		t.Fatal(err)
	}
	graded := rewritten(t, []string{link, "-m", "pytest", "tests", "-p", "no:cacheprovider"})
	if got := InterpretersIn(graded); !slices.Contains(got, link) {
		t.Fatalf("InterpretersIn = %v, want the operator's interpreter %s among them", got, link)
	}
	out, code := runIn(t, dir, graded)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if _, ok := pyReportBlock(out); !ok {
		t.Fatalf("the hook did not load under the linked interpreter:\n%s", out)
	}
	ran, err := os.ReadFile(filepath.Join(dir, "exe.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(ran) != link {
		t.Errorf("the run executed under %s, want the interpreter InterpretersIn named, %s", ran, link)
	}
}

// The plugin is loaded through PYTEST_PLUGINS: before any conftest, with
// plugin autoloading off, and beside a plugin the operator already names
// there, which still loads.
func TestPytestHookLoadsThroughTheEnvironment(t *testing.T) {
	py := pytestFor(t)
	dir := reportFixture(t, "")
	for name, body := range map[string]string{
		"conftest.py":   "import json, os, sys\n\njson.dump({'hook': '" + pyReportModule + "' in sys.modules, 'own': 'own_plugin' in sys.modules, 'plugins': os.environ.get('PYTEST_PLUGINS')}, open('seen.json', 'w'))\n",
		"own_plugin.py": "# the operator's own plugin\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out, code := runIn(t, dir, rewritten(t, []string{py, "-m", "pytest", "tests/test_x.py::test_bad", "-p", "no:cacheprovider"}),
		"PYTEST_DISABLE_PLUGIN_AUTOLOAD=1", "PYTEST_PLUGINS=own_plugin")
	if code != 1 {
		t.Fatalf("exit %d, want 1:\n%s", code, out)
	}
	if strings.Contains(out, "did not load") {
		t.Fatalf("the run was repeated without the hook:\n%s", out)
	}
	if got := pyFirstFailure([]byte(out)); got != "tests/test_x.py::test_bad" {
		t.Errorf("FirstFailure = %q, want tests/test_x.py::test_bad", got)
	}
	if _, ok := pyReportBlock(out); !ok {
		t.Errorf("no complete block in:\n%s", out)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "seen.json"))
	if err != nil {
		t.Fatal(err)
	}
	var seen struct {
		Hook, Own bool
		Plugins   string
	}
	if err := json.Unmarshal(raw, &seen); err != nil {
		t.Fatal(err)
	}
	if !seen.Hook || !seen.Own || seen.Plugins != "own_plugin,"+pyReportModule {
		t.Errorf("at conftest import: hook loaded=%v, operator's plugin loaded=%v, PYTEST_PLUGINS=%q; want both loaded and own_plugin,%s", seen.Hook, seen.Own, seen.Plugins, pyReportModule)
	}
}

// PYTEST_PLUGINS is inherited, so a pytest that a test starts imports the
// plugin too. Neither a subprocess run nor a pytester in-process run records
// into the outer run's block: only the outer run's own failure is named.
func TestPytestHookRecordsOnlyTheOuterSession(t *testing.T) {
	py := pytestFor(t)
	dir := t.TempDir()
	for _, d := range []string{"tests", "inner"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{
		"conftest.py":         "pytest_plugins = ['pytester']\n",
		"inner/test_inner.py": "def test_inner_fails():\n    assert False\n",
		"tests/test_outer.py": `import subprocess, sys


def test_subprocess_run_fails_on_purpose():
    r = subprocess.run([sys.executable, "-m", "pytest", "inner", "-p", "no:cacheprovider", "--rootdir", "inner", "-c", "/dev/null"])
    assert r.returncode == 1


def test_in_process_run_fails_on_purpose(pytester):
    pytester.makepyfile("def test_nested_fails():\n    assert False\n")
    result = pytester.runpytest_inprocess()
    result.assert_outcomes(failed=1)


def test_outer_fails():
    assert 1 == 2
`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out, code := runIn(t, dir, rewritten(t, []string{py, "-m", "pytest", "tests", "-p", "no:cacheprovider"}))
	if code != 1 {
		t.Fatalf("exit %d, want 1:\n%s", code, out)
	}
	recs, ok := pyReportBlock(out)
	if want := []pyReportRec{{pyReportFailed, "tests/test_outer.py::test_outer_fails"}}; !ok || !slices.Equal(recs, want) {
		t.Errorf("block complete=%v records=%v, want exactly %v, from:\n%s", ok, recs, want, out)
	}
}

// The stated limit: the block is the last thing printed, and a door keeps
// only the END of a run's output up to a cap (64 KiB where a killing test is
// named). A block bigger than that has its begin cut off, so it is not a
// block: the readers fall back to the printed lines, and name nothing where
// those are hidden. Never a wrong test, and no partial list.
func TestABlockLargerThanTheKeptTailIsNotRead(t *testing.T) {
	var b strings.Builder
	b.WriteString("1 failed\n\n" + pyReportBegin + "\n")
	for i := 0; b.Len() < 80<<10; i++ {
		b.WriteString(pyReportRecord + pyReportFailed + " tests/test_many.py::test_case_number_" + strings.Repeat("x", 40) + "\n")
	}
	b.WriteString(pyReportEnd + "\n")
	full := b.String()
	if _, ok := pyReportBlock(full); !ok {
		t.Fatal("the whole output holds no complete block")
	}
	tail := full[len(full)-(64<<10):]
	if got := pyFirstFailure([]byte(tail)); got != "" {
		t.Errorf("FirstFailure = %q from a block whose begin was cut, want nothing named", got)
	}
	if got := pyFailedTests(tail); len(got) != 0 {
		t.Errorf("failed tests = %d ids from a block whose begin was cut, want none", len(got))
	}
}

// The scan line says what corral adds: the flags when it adds them, the
// wrapper whenever it wraps (an operator's own -rfE does not stop that), and
// nothing for a command it cannot read.
func TestRunDisclosuresSayWhenTheHookWraps(t *testing.T) {
	p, _ := ByName("python")
	const hook = "pytest: corral runs every graded run through `sh -c` with a pytest plugin of its own loaded (named in PYTEST_PLUGINS"
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
		{"tox", "-e", "py", "--", "pytest"},
		{"python3", "-I", "-m", "pytest"},
	} {
		if has(argv, hook) {
			t.Errorf("%v: the wrapper is disclosed though corral does not wrap it", argv)
		}
	}
}

// onceFixture is a pytest project whose one test FAILS ON ITS FIRST RUN ONLY
// (it leaves a marker file) and appends a line to runs.txt each time it
// runs: a run of it repeated behind the operator's back turns exit 1 into
// exit 0, and the count shows how many times the tests really ran. extra is
// more of the test's body, run before it fails.
func onceFixture(t *testing.T, extra string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "tests"), 0o750); err != nil {
		t.Fatal(err)
	}
	test := "import os, shutil, sys\n\n\ndef test_first_run_only():\n    with open('runs.txt', 'a') as out:\n        out.write('run\\n')\n" +
		extra + "    first = not os.path.exists('ran-before')\n    open('ran-before', 'w').close()\n    assert not first\n"
	if err := os.WriteFile(filepath.Join(dir, "tests", "test_once.py"), []byte(test), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func testRuns(t *testing.T, dir string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "runs.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(raw), "run\n")
}

// THE REPEAT CAN ONLY FOLLOW A START-UP FAILURE. A run in which the plugin
// never loaded is not thereby a run that failed to start: where the command
// drops PYTEST_PLUGINS, a project module of the plugin's name loads in its
// place, or a test deletes the plugin's directory, the tests RAN, and their
// exit code is the genuine one. Each case here has a test that fails on its
// first run only: the wrapped command exits 1 as the operator's own does, and
// the tests ran once. (Repeating whenever "loaded" was absent turned these
// into exit 0.)
func TestPytestHookNeverRepeatsARunWhoseTestsRan(t *testing.T) {
	py := pytestFor(t)
	cases := []struct {
		name  string
		argv  []string
		extra string
		file  map[string]string
	}{
		{"the command drops PYTEST_PLUGINS", []string{"env", "-u", "PYTEST_PLUGINS", py, "-m", "pytest"}, "", nil},
		{"a project module of the plugin's name loads instead", []string{py, "-m", "pytest"}, "",
			map[string]string{pyReportModule + ".py": "# the project's own module of that name\n"}},
		{"a test deletes the plugin's directory", []string{py, "-m", "pytest"},
			"    hook = sys.modules.get('" + pyReportModule + "')\n    if hook is not None:\n        shutil.rmtree(os.path.dirname(hook.__file__))\n", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plainDir := onceFixture(t, c.extra)
			dir := onceFixture(t, c.extra)
			for name, body := range c.file {
				for _, d := range []string{plainDir, dir} {
					if err := os.WriteFile(filepath.Join(d, name), []byte(body), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			operator := append(slices.Clone(c.argv), "tests", "-p", "no:cacheprovider")
			if plain, code := runIn(t, plainDir, operator); code != 1 {
				t.Fatalf("control: the operator's own command exited %d, want 1:\n%s", code, plain)
			}
			out, code := runIn(t, dir, rewritten(t, operator))
			if code != 1 {
				t.Errorf("the graded command exited %d, the operator's own exits 1:\n%s", code, out)
			}
			if n := testRuns(t, dir); n != 1 {
				t.Errorf("the tests ran %d times, want once:\n%s", n, out)
			}
			if strings.Contains(out, "run again without the hook") {
				t.Errorf("the run was repeated:\n%s", out)
			}
		})
	}
}

// THE FALLBACK: pytest could not import the plugin, said so, and exited
// before running any test; the command is then run once more without the
// hook and that run's exit code is the script's. The launcher gate
// (pyReportWraps) would decline python -I and -E, so the wrapper is applied
// directly here: this exercises the fallback itself, which is the guarantee
// for every shape the gate does not list. Each case with a passing suite
// (exit 0, no failed baseline) and a failing one (exit 1, the failing test
// named from the repeat's printed lines), and the tests ran exactly once.
func TestPytestHookFallsBackWhenPytestCannotImportIt(t *testing.T) {
	py := pytestFor(t)
	cases := []struct {
		name string
		argv []string
		file map[string]string
	}{
		{"python -I ignores PYTHONPATH", []string{py, "-I", "-m", "pytest"}, nil},
		{"python -E ignores PYTHONPATH", []string{py, "-E", "-m", "pytest"}, nil},
		{"env -u PYTHONPATH drops it", []string{"env", "-u", "PYTHONPATH", py, "-m", "pytest"}, nil},
		{"a project module of the plugin's name raises at import", []string{py, "-m", "pytest"},
			map[string]string{pyReportModule + ".py": "raise RuntimeError('the project module of that name does not import')\n"}},
	}
	for _, c := range cases {
		for _, suite := range []struct {
			name, test string
			exit       int
			first      string
		}{
			{"passing", "tests/test_x.py::test_good", 0, ""},
			{"failing", "tests/test_x.py::test_bad", 1, "tests/test_x.py::test_bad"},
		} {
			t.Run(c.name+"/"+suite.name, func(t *testing.T) {
				dir := reportFixture(t, "")
				files := map[string]string{"conftest.py": "def pytest_sessionstart(session):\n    with open('sessions.txt', 'a') as out:\n        out.write('run\\n')\n"}
				for name, body := range c.file {
					files[name] = body
				}
				for name, body := range files {
					if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				operator := append(slices.Clone(c.argv), suite.test, "-p", "no:cacheprovider")
				out, code := runIn(t, dir, pyReportHook.Wrap(operator))
				if code != suite.exit {
					t.Fatalf("the wrapped command exited %d, want the operator's own %d:\n%s", code, suite.exit, out)
				}
				if !strings.Contains(out, "run again without the hook") {
					t.Errorf("no line says the run was repeated:\n%s", out)
				}
				if got := pyFirstFailure([]byte(out)); got != suite.first {
					t.Errorf("FirstFailure = %q, want %q, from:\n%s", got, suite.first, out)
				}
				raw, err := os.ReadFile(filepath.Join(dir, "sessions.txt"))
				if err != nil || strings.Count(string(raw), "run\n") != 1 {
					t.Errorf("pytest sessions started: %q (%v), want exactly one: the first run must fail before any session", raw, err)
				}
			})
		}
	}
}

// The commands the launcher gate declines keep the report flags alone and
// run exactly as the operator's own: no wrapper, no failed start, no repeat.
func TestPytestCommandsTheGateDeclinesRunUnwrapped(t *testing.T) {
	py := pytestFor(t)
	p, _ := ByName("python")
	for _, flag := range []string{"-I", "-E"} {
		for _, suite := range []struct {
			test string
			exit int
		}{{"tests/test_x.py::test_good", 0}, {"tests/test_x.py::test_bad", 1}} {
			t.Run(flag+"/"+suite.test, func(t *testing.T) {
				dir := reportFixture(t, "")
				operator := []string{py, flag, "-m", "pytest", suite.test, "-p", "no:cacheprovider"}
				graded, _ := ReportCmd(p, Cmd(operator), operator)
				if cmdIsShellWrapped(graded) {
					t.Fatalf("ReportCmd wrapped %v", operator)
				}
				out, code := runIn(t, dir, graded)
				if code != suite.exit || strings.Contains(out, "Error importing plugin") {
					t.Errorf("exit %d, want %d, with no plugin import error:\n%s", code, suite.exit, out)
				}
			})
		}
	}
}

// A wrapper that cannot set itself up runs the command as it was: no
// temporary directory, or no awk for the filter that watches pytest's error
// stream.
func TestPytestHookRunsTheCommandPlainWhenItCannotBeSetUp(t *testing.T) {
	py := pytestFor(t)
	abs, err := exec.LookPath(py)
	if err != nil {
		t.Fatal(err)
	}
	noAwk := t.TempDir()
	for _, tool := range []string{"sh", "mktemp", "mkfifo", "cat", "rm"} {
		at, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("SKIP: %s is not on PATH: %v", tool, err)
		}
		if err := os.Symlink(at, filepath.Join(noAwk, tool)); err != nil {
			t.Skipf("SKIP: cannot link %s: %v", tool, err)
		}
	}
	for _, c := range []struct {
		name string
		env  string
	}{
		{"no temporary directory", "TMPDIR=/nonexistent/corral"},
		{"no awk", "PATH=" + noAwk},
	} {
		for _, suite := range []struct {
			test, first string
			exit        int
		}{{"tests/test_x.py::test_good", "", 0}, {"tests/test_x.py::test_bad", "tests/test_x.py::test_bad", 1}} {
			t.Run(c.name+"/"+suite.test, func(t *testing.T) {
				dir := reportFixture(t, "")
				out, code := runIn(t, dir, rewritten(t, []string{abs, "-m", "pytest", suite.test, "-p", "no:cacheprovider"}), c.env)
				if code != suite.exit {
					t.Fatalf("exit %d, want %d:\n%s", code, suite.exit, out)
				}
				if _, block := pyReportBlock(out); block || !strings.Contains(out, "could not be set up") {
					t.Errorf("want a plain run and a line saying the hook could not be set up:\n%s", out)
				}
				if got := pyFirstFailure([]byte(out)); got != suite.first {
					t.Errorf("FirstFailure = %q, want %q", got, suite.first)
				}
			})
		}
	}
}

// The fallback is triggered by what pytest prints when it cannot import a
// plugin named in PYTEST_PLUGINS. This pins the exact wording corral matches,
// on the stream it watches (stderr), so a pytest that changes either fails
// here instead of silently never falling back.
func TestPytestStillSaysWhatCorralMatchesWhenAPluginCannotBeImported(t *testing.T) {
	py := pytestFor(t)
	dir := reportFixture(t, "")
	run := func(env ...string) (stdout, stderr string) {
		cmd := exec.Command(py, "-m", "pytest", "tests/test_x.py::test_good", "-p", "no:cacheprovider") // #nosec G204 -- a test's own fixture command
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), env...)
		var o, e strings.Builder
		cmd.Stdout, cmd.Stderr = &o, &e
		if err := cmd.Run(); err == nil {
			t.Fatalf("pytest passed with a plugin it cannot import:\n%s", o.String())
		}
		return o.String(), e.String()
	}
	stdout, stderr := run("PYTEST_PLUGINS=" + pyReportModule)
	if want := `Error importing plugin "` + pyReportModule + `"`; !strings.Contains(stderr, want) || strings.Contains(stdout, want) {
		t.Errorf("a missing plugin: want %q on stderr and not on stdout\nstderr:\n%s\nstdout:\n%s", want, stderr, stdout)
	}
	if err := os.WriteFile(filepath.Join(dir, pyReportModule+".py"), []byte("raise RuntimeError('no')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr = run("PYTEST_PLUGINS=" + pyReportModule)
	found := false
	for _, line := range strings.Split(stderr, "\n") {
		if strings.Contains(line, pyReportModule+`.py", line `) && strings.Contains(line, ", in <module>") {
			found = true
		}
	}
	if !found {
		t.Errorf("a plugin that raises at import: want a traceback line naming %s.py ... in <module> on stderr:\n%s", pyReportModule, stderr)
	}
}

// A begin with no end after it means the record is incomplete, and then no
// block in the output is trusted, an earlier complete one included: a test
// that prints a forged block and kills the process (os._exit) leaves exactly
// that, the forged block followed by the real begin. Both readers fall back
// to the printed lines.
func TestPytestReadersVoidEveryBlockWhenTheLastBeginHasNoEnd(t *testing.T) {
	out := "corral-report-begin\ncorral-report failed t.py::forged\ncorral-report-end\n" +
		"FAILED t.py::printed - boom\n" +
		"corral-report-begin\ncorral-report failed t.py::real\n"
	if got := pyFirstFailure([]byte(out)); got != "t.py::printed" {
		t.Errorf("FirstFailure = %q, want the printed line's t.py::printed", got)
	}
	if got := pyFailedTests(out); !slices.Equal(got, []string{"t.py::printed"}) {
		t.Errorf("failed tests = %v, want [t.py::printed]", got)
	}
}

// The same by a real run: the forging test's process dies before the session
// finishes, so the real record has a begin and no end.
func TestAForgedBlockFromATestThatKillsTheProcessIsNotTrusted(t *testing.T) {
	py := pytestFor(t)
	dir := reportFixture(t, "--no-summary")
	forge := `import os, sys


def test_forge(capfd):
    with capfd.disabled():
        print("\ncorral-report-begin\ncorral-report failed tests/test_x.py::test_good\ncorral-report-end")
        sys.stdout.flush()
    os._exit(1)
`
	if err := os.WriteFile(filepath.Join(dir, "tests", "test_forge.py"), []byte(forge), 0o600); err != nil {
		t.Fatal(err)
	}
	out, code := runIn(t, dir, rewritten(t, []string{py, "-m", "pytest", "tests/test_forge.py", "-p", "no:cacheprovider"}))
	if code != 1 {
		t.Fatalf("exit %d, want the test's own 1:\n%s", code, out)
	}
	if got := pyFirstFailure([]byte(out)); got != "" {
		t.Errorf("FirstFailure = %q from a forged block, want nothing named:\n%s", got, out)
	}
	if got := pyFailedTests(out); len(got) != 0 {
		t.Errorf("failed tests = %v from a forged block, want none", got)
	}
}

// A capture that keeps both ends of an over-long output puts an elision line
// between them. A block wholly in the kept tail is read; a block split by
// the elision (begin in the head, end in the tail) has lost records and is
// not a block, an earlier complete block before the elision included.
func TestPytestReadersTreatABlockSplitByAnElisionAsIncomplete(t *testing.T) {
	el := clip.Elision(4096)
	cases := []struct {
		name, out, first string
	}{
		{"block wholly after the elision", "noise\n" + el + "\nmore\ncorral-report-begin\ncorral-report failed t.py::real\ncorral-report-end\n", "t.py::real"},
		{"block split by the elision", "corral-report-begin\ncorral-report failed t.py::a\n" + el + "\ncorral-report failed t.py::z\ncorral-report-end\n", ""},
		{"forged block before, real block split", "corral-report-begin\ncorral-report failed t.py::forged\ncorral-report-end\ncorral-report-begin\n" + el + "\ncorral-report failed t.py::z\ncorral-report-end\n", ""},
		{"split block, printed line in the tail", "corral-report-begin\n" + el + "\nFAILED t.py::printed - boom\ncorral-report-end\n", "t.py::printed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pyFirstFailure([]byte(c.out)); got != c.first {
				t.Errorf("FirstFailure = %q, want %q", got, c.first)
			}
		})
	}
}

// No runner's failure reader reads an elision line as a test, a unit or one
// of its own markers, alone or between two lines of real output.
func TestNoFailureReaderReadsAnElisionLine(t *testing.T) {
	for _, out := range []string{clip.Elision(123456) + "\n", "ok\n" + clip.Elision(1) + "\nok\n"} {
		for _, r := range runners {
			if r.FirstFailure != nil {
				if got := r.FirstFailure([]byte(out)); got != "" {
					t.Errorf("%s: FirstFailure read %q from an elision line", r.ID, got)
				}
			}
			if r.FailedTests != nil {
				if got := r.FailedTests(out); len(got) != 0 {
					t.Errorf("%s: FailedTests read %v from an elision line", r.ID, got)
				}
			}
			if r.FailedUnits != nil {
				if got := r.FailedUnits(out); len(got) != 0 {
					t.Errorf("%s: FailedUnits read %v from an elision line", r.ID, got)
				}
			}
			if r.Flaky != nil {
				if got := r.Flaky([]byte(out)); len(got) != 0 {
					t.Errorf("%s: Flaky read %v from an elision line", r.ID, got)
				}
			}
		}
	}
}

// A process a test leaves running holds pytest's error stream open. The
// wrapper's filter stops at the script's own sentinel, not at end of file, so
// the run ends when pytest does and not when that process dies.
func TestPytestHookDoesNotWaitForAProcessATestLeftRunning(t *testing.T) {
	py := pytestFor(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "tests"), 0o750); err != nil {
		t.Fatal(err)
	}
	test := "import subprocess, sys\n\n\ndef test_leaves_a_process(capfd):\n    with capfd.disabled():\n        subprocess.Popen(['sleep', '60'])\n    assert 1 == 2\n"
	if err := os.WriteFile(filepath.Join(dir, "tests", "test_bg.py"), []byte(test), 0o600); err != nil {
		t.Fatal(err)
	}
	graded := rewritten(t, []string{py, "-m", "pytest", "tests", "-p", "no:cacheprovider"})
	cmd := exec.Command(graded[0], graded[1:]...) // #nosec G204 -- a test's own fixture command
	cmd.Dir = dir
	// The streams are files, so nothing here waits on a pipe the left-over
	// process holds: only the wrapper itself could.
	outFile, err := os.Create(filepath.Join(dir, "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer outFile.Close()
	cmd.Stdout, cmd.Stderr = outFile, outFile
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the wrapper was still running 30s after a one-test run: it is waiting for the process the test left behind")
	}
	out, err := os.ReadFile(filepath.Join(dir, "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if cmd.ProcessState.ExitCode() != 1 || pyFirstFailure(out) != "tests/test_bg.py::test_leaves_a_process" {
		t.Errorf("exit %d, FirstFailure %q; want 1 and the failing test:\n%s", cmd.ProcessState.ExitCode(), pyFirstFailure(out), out)
	}
}

// A conftest that starts pytest as a subprocess AT IMPORT runs before the
// outer session is configured, so the inner process would be the first to
// open the record. It imported the plugin second, is not the owner, and
// records nothing: the block names the outer run's failure alone. (Without
// the owner check the block named test_inner.py::test_inner_fails.)
func TestPytestHookIsNotTakenOverByAPytestAConftestStarts(t *testing.T) {
	py := pytestFor(t)
	dir := t.TempDir()
	for _, d := range []string{"tests", "inner"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{
		// The guard variable is what stops the chain: the inner pytest loads
		// this conftest too (it sits above inner/, and -c /dev/null moves the
		// conftest cut-off away from the rootdir), so without it every inner
		// run starts another, without end.
		"conftest.py":         "import os, subprocess, sys\n\nif not os.environ.get('CORRAL_TEST_INNER_RUN'):\n    subprocess.run([sys.executable, '-m', 'pytest', 'inner', '-p', 'no:cacheprovider', '--rootdir', 'inner', '-c', '/dev/null'], capture_output=True, env={**os.environ, 'CORRAL_TEST_INNER_RUN': '1'})\n",
		"inner/test_inner.py": "def test_inner_fails():\n    assert False\n",
		"tests/test_outer.py": "def test_outer_fails():\n    assert 1 == 2\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out, code := runIn(t, dir, rewritten(t, []string{py, "-m", "pytest", "tests", "-p", "no:cacheprovider"}))
	if code != 1 {
		t.Fatalf("exit %d, want 1:\n%s", code, out)
	}
	recs, ok := pyReportBlock(out)
	if want := []pyReportRec{{pyReportFailed, "tests/test_outer.py::test_outer_fails"}}; !ok || !slices.Equal(recs, want) {
		t.Errorf("block complete=%v records=%v, want exactly %v, from:\n%s", ok, recs, want, out)
	}
}
