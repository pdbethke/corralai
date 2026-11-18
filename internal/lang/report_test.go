// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// unwrapped is the command inside the pytest report wrapper, without the
// `-p corral_report` the wrapper adds, and whether cmd is that wrapper: the
// script, its $0, then the command as positional parameters.
func unwrapped(cmd []string) ([]string, bool) {
	if len(cmd) < 5 || !slices.Equal(cmd[:4], []string{"sh", "-c", pyReportScript, pyReportArgv0}) {
		return nil, false
	}
	inner := cmd[4:]
	for i := 0; i+1 < len(inner); i++ {
		if inner[i] == "-p" && inner[i+1] == pyReportModule {
			return slices.Concat(inner[:i], inner[i+2:]), true
		}
	}
	return nil, false
}

// ReportCmd gives a command its runner's report flags and hook only where
// the command names that runner and corral can see where the runner's own
// options end. Everything else is left exactly as it was: its failures are
// then read from what the runner printed, which is what corral did before,
// never a guess.
func TestReportCmdAppliesTheRowsReportArgs(t *testing.T) {
	cases := []struct {
		name string
		lang string
		inv  Invocation
		// want is the command inside the wrapper: the operator's with the
		// flags where they belong. nil = unchanged, and not wrapped.
		want []string
	}{
		{"bare pytest", "python", Cmd([]string{"pytest"}), []string{"pytest", "-rfE"}},
		{"python3 -m pytest -q", "python", Cmd([]string{"python3", "-m", "pytest", "-q"}),
			[]string{"python3", "-m", "pytest", "-q", "-rfE"}},
		{"python -m pytest tests -x", "python", Cmd([]string{"python", "-m", "pytest", "tests", "-x"}),
			[]string{"python", "-m", "pytest", "tests", "-x", "-rfE"}},
		{"venv entry script", "python", Cmd([]string{".venv/bin/pytest", "tests/"}),
			[]string{".venv/bin/pytest", "tests/", "-rfE"}},
		{"uv run pytest", "python", Cmd([]string{"uv", "run", "pytest", "tests"}),
			[]string{"uv", "run", "pytest", "tests", "-rfE"}},
		// uv's own separator comes before the runner's word: it is uv's, and
		// the runner's options run to the end.
		{"uv run -- pytest", "python", Cmd([]string{"uv", "run", "--", "pytest", "tests"}),
			[]string{"uv", "run", "--", "pytest", "tests", "-rfE"}},
		// After pytest's own `--` every word is a path: the flag goes before it.
		{"-- then paths", "python", Cmd([]string{"python3", "-m", "pytest", "-q", "--", "tests/a.py"}),
			[]string{"python3", "-m", "pytest", "-q", "-rfE", "--", "tests/a.py"}},
		// Flags the operator already wrote gain no second copy; the command
		// is still wrapped.
		{"already last", "python", Cmd([]string{"pytest", "tests", "-rfE"}), []string{"pytest", "tests", "-rfE"}},
		{"already, only paths after", "python", Cmd([]string{"pytest", "-rfE", "tests"}), []string{"pytest", "-rfE", "tests"}},
		{"already before --", "python", Cmd([]string{"pytest", "-q", "-rfE", "--", "tests"}), []string{"pytest", "-q", "-rfE", "--", "tests"}},
		// An option after the operator's -rfE could be a -r cluster (-vrs):
		// corral's goes last again, which costs nothing.
		{"already, option after", "python", Cmd([]string{"pytest", "-rfE", "-vrs", "tests"}),
			[]string{"pytest", "-rfE", "-vrs", "tests", "-rfE"}},
		// The operator's own -r is replaced: pytest keeps the LAST -r.
		{"operator -rXs", "python", Cmd([]string{"pytest", "-rXs", "tests"}),
			[]string{"pytest", "-rXs", "tests", "-rfE"}},
		{"shell-wrapped", "python", Cmd([]string{"sh", "-c", "pytest tests"}), nil},
		{"env prefix then sh", "python", Cmd([]string{"env", "A=1", "sh", "-c", "pytest"}), nil},
		{"tox", "python", Cmd([]string{"tox", "-e", "py"}), nil},
		{"make test", "python", Cmd([]string{"make", "test"}), nil},
		// unittest's row has no report flags: `python -m unittest` has no
		// option that hides or shows its failure lines.
		{"unittest", "python", Cmd([]string{"python3", "-m", "unittest", "discover"}), nil},
		// A Python command no row names gets nothing from pytest, the
		// language's Default row: the compile check is one.
		{"py_compile", "python", Cmd([]string{"python3", "-m", "py_compile", "a.py"}), nil},
		{"go test", "go", Cmd([]string{"go", "test", "./..."}), nil},
		{"jest", "javascript", Cmd([]string{"npx", "jest"}), nil},
		// --runner names another row than the command's words do.
		{"--runner unittest on pytest", "python", Invocation{Argv: []string{"pytest", "tests"}, Runner: "unittest"}, nil},
		{"--runner pytest on pytest", "python", Invocation{Argv: []string{"pytest", "tests"}, Runner: "pytest"},
			[]string{"pytest", "tests", "-rfE"}},
		// A package script corral has not narrowed to one command it can
		// append to is left alone, as fail-fast leaves it.
		{"package script", "python", Invocation{Argv: []string{"npm", "test"}, Script: []string{"pytest"}, Pass: "--", Narrow: true}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, ok := ByName(c.lang)
			if !ok {
				t.Fatalf("%s plugin not registered", c.lang)
			}
			in := slices.Clone(c.inv.Argv)
			got, applied := ReportCmd(p, c.inv, c.inv.Argv)
			if !slices.Equal(c.inv.Argv, in) {
				t.Fatalf("ReportCmd modified its input: %v", c.inv.Argv)
			}
			if c.want == nil {
				if !slices.Equal(got, in) || applied {
					t.Fatalf("ReportCmd(%v) = %v applied=%v, want it unchanged", in, got, applied)
				}
			} else {
				inner, wrapped := unwrapped(got)
				if !wrapped || !applied || !slices.Equal(inner, c.want) {
					t.Fatalf("ReportCmd(%v) = %v applied=%v, want the report wrapper around %v", in, got, applied, c.want)
				}
				// The plugin is loaded where the flags go: before the
				// runner's own `--`, after which every word is a path.
				positional := got[4:]
				at := slices.Index(positional, pyReportModule)
				if dash := slices.Index(c.want, "--"); dash >= 0 && slices.Index(c.want, "pytest") < dash && !slices.Equal(positional[at+1:], c.want[dash:]) {
					t.Errorf("the plugin is loaded at %d in %v, want it directly before pytest's own --", at, positional)
				}
			}
			// Idempotent: a command it has already rewritten is a shell
			// wrapper, which it cannot read and leaves alone.
			again, appliedAgain := ReportCmd(p, c.inv.WithArgv(got), got)
			if !slices.Equal(again, got) || appliedAgain {
				t.Fatalf("second ReportCmd(%v) = %v applied=%v, want it unchanged", got, again, appliedAgain)
			}
		})
	}
}

// ReportArgv is the hook grading hands its jail: each command is judged on
// its own words, so the compile check of a pytest run is left alone while
// the run's own commands get the flag.
func TestReportArgvJudgesEachCommandOnItsOwnWords(t *testing.T) {
	p, _ := ByName("python")
	hook := ReportArgv(p, Cmd([]string{"python3", "-m", "pytest", "tests"}))
	if got := hook([]string{"python3", "-m", "py_compile", "a.py"}); !slices.Equal(got, []string{"python3", "-m", "py_compile", "a.py"}) {
		t.Errorf("compile check got %v, want it unchanged", got)
	}
	got := hook([]string{"python3", "-m", "pytest", "-q", "tests/a.py::t"})
	if inner, wrapped := unwrapped(got); !wrapped || !slices.Equal(inner, []string{"python3", "-m", "pytest", "-q", "tests/a.py::t", "-rfE"}) {
		t.Errorf("one-test command got %v, want the wrapper around it with -rfE appended", got)
	}
	// Applied to its own output it changes nothing: the wrap happens once.
	if again := hook(got); !slices.Equal(again, got) {
		t.Errorf("second application changed the command: %v", again)
	}
}

// Every row that declares report flags or a report hook names its runner by
// a word, so ReportCmd can find where the runner's options start, and has a
// failure reader they exist for. A hook's wrapper is one ReportCmd cannot
// read again, and says what it does.
func TestEveryRowWithReportArgsHasWordsAndAReader(t *testing.T) {
	for _, r := range runners {
		if h := r.ReportHook; h != nil {
			if h.Wrap == nil || strings.TrimSpace(h.Says) == "" {
				t.Errorf("%s declares a ReportHook with no Wrap or no disclosure", r.ID)
			} else if w := h.Wrap([]string{r.Words[0]}, 1); !cmdIsShellWrapped(w) {
				t.Errorf("%s: the hook's wrapper %v is not a shell wrapper, so ReportCmd would wrap it again", r.ID, w)
			}
		}
		if len(r.ReportArgs) == 0 && r.ReportHook == nil {
			continue
		}
		if len(r.Words) == 0 {
			t.Errorf("%s declares ReportArgs but no Words: ReportCmd cannot find where its options start", r.ID)
		}
		if r.FirstFailure == nil && r.FailedTests == nil {
			t.Errorf("%s declares ReportArgs but no failure reader they would serve", r.ID)
		}
	}
}

// The disclosure is said only when corral actually adds the flags.
func TestRunDisclosuresSayWhenReportArgsAreAdded(t *testing.T) {
	p, _ := ByName("python")
	const want = "pytest: corral adds -rfE to every graded run so it can read which test failed"
	has := func(inv Invocation) bool {
		for _, l := range RunDisclosures(p, t.TempDir(), inv) {
			if strings.HasPrefix(l, want) {
				return true
			}
		}
		return false
	}
	if !has(Cmd([]string{"python3", "-m", "pytest", "tests"})) {
		t.Errorf("no disclosure for a pytest command corral adds -rfE to")
	}
	for _, argv := range [][]string{
		{"python3", "-m", "pytest", "tests", "-rfE"},
		{"sh", "-c", "pytest tests"},
		{"python3", "-m", "unittest"},
	} {
		if has(Cmd(argv)) {
			t.Errorf("%v: disclosure printed though corral adds nothing", argv)
		}
	}
}

// pytestFor is a python3 that imports pytest, or skips the test by name.
func pytestFor(t *testing.T) string {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skipf("SKIP: python3 not on PATH: %v", err)
	}
	if out, err := exec.Command(py, "-c", "import pytest").CombinedOutput(); err != nil {
		t.Skipf("SKIP: pytest is not importable by %s: %s", py, strings.TrimSpace(string(out)))
	}
	return py
}

// A project whose pytest config sets its own -r (starlette's addopts carry
// -rXs) replaces pytest's default -rfE, so its failing run prints no
// `FAILED <id>` line and neither reader names anything. With the command
// ReportCmd builds, both name the failing test. The unrewritten half is the
// control: it shows the bug was real and that this test can fail.
func TestReportArgsRestoreFailureLinesAProjectHides(t *testing.T) {
	py := pytestFor(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "tests"), 0o750); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"pyproject.toml":    "[tool.pytest.ini_options]\naddopts = \"-rXs\"\n",
		"tests/test_x.py":   "import pytest\n\ndef test_good():\n    assert True\n\ndef test_bad():\n    assert 1 == 2\n\n@pytest.mark.skip(reason='on purpose')\ndef test_skip():\n    pass\n",
		"tests/__init__.py": "",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p, _ := ByName("python")
	operator := []string{py, "-m", "pytest", "tests", "-q", "-p", "no:cacheprovider"}
	inv := Cmd(operator)
	run := func(argv []string) string {
		cmd := exec.Command(argv[0], argv[1:]...) // #nosec G204 -- a test's own fixture command
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("%v passed; the fixture has a failing test:\n%s", argv, out)
		}
		return string(out)
	}
	parser := FailureParserFor(p, inv)
	failed, ok := DeselectorFor(p, inv)
	if parser == nil || !ok {
		t.Fatal("pytest has no failure readers")
	}

	hidden := run(operator)
	if got := parser.FirstFailure([]byte(hidden)); got != "" {
		t.Fatalf("control: FirstFailure read %q from a run whose config hides the FAILED lines:\n%s", got, hidden)
	}
	if got := failed(hidden); len(got) != 0 {
		t.Fatalf("control: the failed-tests reader read %v from a run whose config hides the FAILED lines:\n%s", got, hidden)
	}

	// The flags alone, without the hook: the printed lines are back. This is
	// the second source a reader falls back to, so it is still checked.
	flagged, flagsAdded, _ := reportCmd(p, inv, operator)
	inner, wrapped := unwrapped(flagged)
	if !flagsAdded || !wrapped {
		t.Fatalf("reportCmd left %v without flags or wrapper: %v", operator, flagged)
	}
	shown := run(inner)
	const want = "tests/test_x.py::test_bad"
	if _, block := pyReportBlock(shown); block {
		t.Fatalf("the unwrapped command printed a marker block:\n%s", shown)
	}
	if got := parser.FirstFailure([]byte(shown)); got != want {
		t.Errorf("FirstFailure = %q, want %q, from:\n%s", got, want, shown)
	}
	if got := failed(shown); !slices.Equal(got, []string{want}) {
		t.Errorf("failed tests = %v, want [%s], from:\n%s", got, want, shown)
	}
}

// --no-summary and -p no:terminal in a project's config hide the FAILED
// lines even with -rfE applied: that was the stated limit of the flags, and
// the first half here still pins it (the flags alone name nothing). The hook
// removes the limit: the full graded command names the failing test.
func TestTheHookNamesWhatNoSummaryOrNoTerminalHide(t *testing.T) {
	py := pytestFor(t)
	p, _ := ByName("python")
	for _, opt := range []string{"--no-summary", "-p no:terminal"} {
		t.Run(opt, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, "tests"), 0o750); err != nil {
				t.Fatal(err)
			}
			for name, body := range map[string]string{
				"pyproject.toml":  "[tool.pytest.ini_options]\naddopts = \"" + opt + "\"\n",
				"tests/test_x.py": "def test_good():\n    assert True\n\ndef test_bad():\n    assert 1 == 2\n",
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			operator := []string{py, "-m", "pytest", "tests", "-q", "-p", "no:cacheprovider"}
			inv := Cmd(operator)
			graded, applied := ReportCmd(p, inv, operator)
			flagsOnly, wrapped := unwrapped(graded)
			if !applied || !wrapped {
				t.Fatalf("ReportCmd left %v unwrapped: %v", operator, graded)
			}
			failed, _ := DeselectorFor(p, inv)
			parser := FailureParserFor(p, inv)

			out, code := runIn(t, dir, flagsOnly)
			if code != 1 {
				t.Fatalf("%v exited %d, want 1:\n%s", flagsOnly, code, out)
			}
			if got := parser.FirstFailure([]byte(out)); got != "" {
				t.Errorf("control: the flags alone name %q under addopts %q; -rfE now undoes it, so say so in the pytest row's note", got, opt)
			}

			out, code = runIn(t, dir, graded)
			if code != 1 {
				t.Fatalf("the graded command exited %d, want 1:\n%s", code, out)
			}
			const want = "tests/test_x.py::test_bad"
			if got := parser.FirstFailure([]byte(out)); got != want {
				t.Errorf("FirstFailure = %q under addopts %q, want %q, from:\n%s", got, opt, want, out)
			}
			if got := failed(out); !slices.Equal(got, []string{want}) {
				t.Errorf("failed tests = %v under addopts %q, want [%s]", got, opt, want)
			}
		})
	}
}
