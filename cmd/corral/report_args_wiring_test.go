// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/sandbox"
)

// hiddenLinesRepo is a pytest project whose config replaces pytest's default
// report characters (as starlette's addopts do), so a failing run prints no
// `FAILED <node id>` line unless corral adds -rfE.
func hiddenLinesRepo(t *testing.T) (repoDir string, seed repoSeed) {
	t.Helper()
	return hidingRepo(t, "-rXs")
}

// hidingRepo is a pytest project whose own config carries hides, an option
// that keeps pytest from printing the `FAILED <node id>` lines: -rXs, which
// -rfE on the command line undoes, or --no-summary or -p no:terminal, which
// no flag undoes and only the failure hook answers.
func hidingRepo(t *testing.T, hides string) (repoDir string, seed repoSeed) {
	t.Helper()
	repoDir = t.TempDir()
	files := map[string]string{
		"pyproject.toml":     "[tool.pytest.ini_options]\naddopts = \"" + hides + " -p no:cacheprovider\"\n",
		"calc.py":            "def add(a, b):\n    return a + b\n",
		"tests/__init__.py":  "",
		"tests/test_calc.py": "from calc import add\n\n\ndef test_add():\n    assert add(2, 3) == 5\n\n\ndef test_zero():\n    assert add(0, 0) == 0\n",
	}
	for rel, body := range files {
		mustWrite(t, filepath.Join(repoDir, rel), body)
	}
	return repoDir, repoSeed{seedDir: repoDir, files: files, cleanup: func() {}}
}

// isReportWrapper reports whether cmd is a shell wrapper whose positional
// parameters end with want: the failure hook's wrapper around that command.
func isReportWrapper(cmd, want []string) bool {
	return len(cmd) > len(want)+3 && cmd[0] == "sh" && cmd[1] == "-c" && slices.Equal(cmd[len(cmd)-len(want):], want)
}

var subtractMutant = []adequacy.Mutant{{ID: "m1", Search: "return a + b", Replace: "return a - b"}}

const pytestArgv = "python3 -m pytest tests -q"

// wirePython builds the grading wiring for the fixture on one substrate:
// "workspace", "jail" (whole-repo jail) or "single" (single-file jail).
func wirePython(t *testing.T, iso sandbox.Isolator, substrate, repoDir string, seed *repoSeed) jailWiring {
	t.Helper()
	argv := strings.Fields(pytestArgv)
	in := jailWiringInput{
		iso: iso, timeout: 2 * time.Minute,
		codePath: "calc.py", testPath: "tests/test_calc.py", repoDir: repoDir, langName: "python",
		fsPath:    func(q string) string { return filepath.Join(repoDir, q) },
		code:      []byte(seed.files["calc.py"]),
		devTest:   []byte(seed.files["tests/test_calc.py"]),
		checkArgv: argv, inv: lang.Cmd(argv),
		stdout: io.Discard, seed: seed,
	}
	switch substrate {
	case "workspace":
		in.substrate = substrateWorkspace
	case "single":
		in.repoDir, in.seed, in.codePath, in.testPath = "", nil, filepath.Join(repoDir, "calc.py"), filepath.Join(repoDir, "tests/test_calc.py")
		in.fsPath = func(q string) string { return q }
	}
	w, err := buildJailWiring(context.Background(), in)
	if err != nil {
		t.Fatalf("buildJailWiring(%s): %v", substrate, err)
	}
	t.Cleanup(w.cleanup)
	return w
}

// passingRecorder is a Jail every door can use that records each command it
// is handed and passes it.
type passingRecorder struct {
	mu   sync.Mutex
	cmds [][]string
}

func (r *passingRecorder) note(cmd []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cmds = append(r.cmds, slices.Clone(cmd))
}

func (r *passingRecorder) RunTest(_ context.Context, _ map[string]string, cmd []string) (bool, error) {
	r.note(cmd)
	return true, nil
}

func (r *passingRecorder) RunTestVerbose(_ context.Context, _ map[string]string, cmd []string) (bool, string, error) {
	r.note(cmd)
	return true, "", nil
}

func (r *passingRecorder) RunTestDetailed(_ context.Context, _ map[string]string, cmd []string) (bool, []byte, error) {
	r.note(cmd)
	return true, nil, nil
}

// Every door a file's grading runs a command through, on every substrate,
// hands its runner the pytest command with -rfE: Score's own runs (the
// scorer's CommandRewrite through adequacy.WithCommandRewrite), CompliantRun
// (salvage reads its output), and the validator's commands. A command that is
// not pytest's (the compile check) is left alone.
func TestPytestReportArgsReachEveryDoorOnEverySubstrate(t *testing.T) {
	for _, substrate := range []string{"workspace", "jail", "single"} {
		t.Run(substrate, func(t *testing.T) {
			repoDir, seed := hiddenLinesRepo(t)
			w := wirePython(t, &wrapRecordingIsolator{}, substrate, repoDir, &seed)
			for name, f := range map[string]func([]string) []string{"scorer": w.scorer.CommandRewrite, "validator": w.validator.CommandRewrite} {
				if f == nil {
					t.Fatalf("%s has no command rewrite: the report flags never reach the runner", name)
				}
				if got := f([]string{"python3", "-m", "pytest", "-q", "tests/test_calc.py::test_add"}); !slices.Contains(got, "-rfE") {
					t.Errorf("%s rewrite of a one-test command = %v, want -rfE", name, got)
				}
				if got := f([]string{"python3", "-m", "py_compile", "calc.py"}); slices.Contains(got, "-rfE") {
					t.Errorf("%s rewrite of the compile check = %v, want it unchanged", name, got)
				}
			}

			rec := &passingRecorder{}
			w.scorer.Jail, w.validator.Jail = rec, rec
			if _, err := w.scorer.ScoreReport(context.Background(), w.codeKey, seed.files["calc.py"], seed.files["tests/test_calc.py"], subtractMutant, pytestArgv); err != nil {
				t.Fatal(err)
			}
			if _, _, err := w.scorer.CompliantRun(context.Background(), w.codeKey, seed.files["calc.py"], seed.files["tests/test_calc.py"], pytestArgv); err != nil {
				t.Fatal(err)
			}
			if len(rec.cmds) < 3 {
				t.Fatalf("recorded %d commands, want the baseline, the canary and the compliant run at least: %v", len(rec.cmds), rec.cmds)
			}
			for _, c := range rec.cmds {
				if slices.Contains(c, "pytest") && !slices.Contains(c, "-rfE") {
					t.Errorf("a pytest command reached the runner without -rfE: %v", c)
				}
			}
		})
	}
}

// pytestOnHost skips the test by name unless python3 imports pytest.
func pytestOnHost(t *testing.T) {
	t.Helper()
	if out, err := exec.Command("python3", "-c", "import pytest").CombinedOutput(); err != nil {
		t.Skipf("SKIP: python3 cannot import pytest: %s", strings.TrimSpace(string(out)))
	}
}

// A kill on a project that hides pytest's FAILED lines is named and confirmed
// by the killing test run alone, on the workspace substrate and in the real
// jail: under -rXs, which -rfE undoes, and under --no-summary and
// -p no:terminal, which only the failure hook answers. The confirmation is
// the proof that the killer command is still narrowed with the wrapper on: it
// is derived from the operator's command and wrapped afterwards, and a killer
// derived from a wrapped command would be refused and the kill confirmed by
// repeating the run. With the rewrite taken away, the same kill names nothing
// and is confirmed by repeating the whole run: the control, showing the
// fixture really hides the lines.
func TestAKillOnAProjectHidingFailedLinesIsNamedAndConfirmedAlone(t *testing.T) {
	pytestOnHost(t)
	for _, hides := range []string{"-rXs", "--no-summary", "-p no:terminal"} {
		for _, substrate := range []string{"workspace", "jail"} {
			t.Run(hides+"/"+substrate, func(t *testing.T) { killIsNamedAndConfirmedAlone(t, hides, substrate) })
		}
	}
}

func killIsNamedAndConfirmedAlone(t *testing.T, hides, substrate string) {
	var iso sandbox.Isolator
	if substrate == "jail" {
		b, err := sandbox.Resolve(sandbox.Config{Backend: "bwrap"})
		if err != nil {
			t.Skipf("SKIP: no bwrap backend: %v", err)
		}
		iso = b
	}
	for _, control := range []bool{false, true} {
		repoDir, seed := hidingRepo(t, hides)
		w := wirePython(t, iso, substrate, repoDir, &seed)
		if control {
			w.scorer.CommandRewrite = nil
		}
		rep, err := w.scorer.ScoreReport(context.Background(), w.codeKey, seed.files["calc.py"], seed.files["tests/test_calc.py"], subtractMutant, pytestArgv)
		if err != nil {
			t.Fatal(err)
		}
		if !rep.CompliantPass {
			if substrate == "jail" {
				t.Skipf("SKIP: the jail did not pass the fixture's suite on unmutated code (jail or toolchain unavailable): %s", rep.BaselineOutput)
			}
			t.Fatalf("baseline failed: %s", rep.BaselineOutput)
		}
		if !slices.Contains(rep.Killed, "m1") {
			t.Fatalf("control=%v: m1 not killed: %+v", control, rep)
		}
		g := rep.PerMutant["m1"]
		if control {
			if g.KilledBy != "" || g.KillConfirmation != adequacy.KillConfirmedRepeated {
				t.Errorf("control: KilledBy=%q KillConfirmation=%q, want \"\" and %q: the fixture must hide the FAILED lines", g.KilledBy, g.KillConfirmation, adequacy.KillConfirmedRepeated)
			}
			continue
		}
		if g.KilledBy != "tests/test_calc.py::test_add" || g.KillConfirmation != adequacy.KillConfirmedAlone {
			t.Errorf("KilledBy=%q KillConfirmation=%q, want tests/test_calc.py::test_add and %q", g.KilledBy, g.KillConfirmation, adequacy.KillConfirmedAlone)
		}
	}
}

// brokenAuthored is an authored test file with one test that passes on the
// unmutated code and kills the subtract mutant, and one that fails on the
// unmutated code and whose parametrized id holds a space.
const brokenAuthored = `import pytest

from calc import add


def test_sum():
    assert add(2, 3) == 5


@pytest.mark.parametrize("v", ["a b"])
def test_wrong(v):
    assert add(1, 1) == 3
`

// Salvage reads the failing authored tests from the clean-code run and
// reruns without them. On a project whose config hides pytest's summary, in
// the real jail and on the workspace substrate: the run's output carries the
// hook's block (so the jail's /tmp, mktemp, sh and cat all served it), the
// failing id is read whole, the deselecting command is built from the
// OPERATOR's command (a wrapped one cannot be narrowed: the control), and
// the salvaged pass then passes on clean code and kills the mutant.
func TestSalvageStillDeselectsWithTheFailureHookOn(t *testing.T) {
	pytestOnHost(t)
	plug, _ := lang.ByName("python")
	argv := strings.Fields(pytestArgv)
	inv := lang.Cmd(argv)
	for _, substrate := range []string{"workspace", "jail"} {
		t.Run(substrate, func(t *testing.T) {
			var iso sandbox.Isolator
			if substrate == "jail" {
				b, err := sandbox.Resolve(sandbox.Config{Backend: "bwrap"})
				if err != nil {
					t.Skipf("SKIP: no bwrap backend: %v", err)
				}
				iso = b
			}
			repoDir, seed := hidingRepo(t, "--no-summary")
			w := wirePython(t, iso, substrate, repoDir, &seed)
			ctx := context.Background()
			code := seed.files["calc.py"]

			if substrate == "jail" {
				if pass, out, err := w.scorer.CompliantRun(ctx, w.codeKey, code, seed.files["tests/test_calc.py"], pytestArgv); err != nil || !pass {
					t.Skipf("SKIP: the jail did not pass the fixture's own suite on unmutated code (jail or toolchain unavailable): %v\n%s", err, out)
				}
			}
			pass, out, err := w.scorer.CompliantRun(ctx, w.codeKey, code, brokenAuthored, pytestArgv)
			if err != nil {
				t.Fatal(err)
			}
			if pass {
				t.Fatalf("the broken authored test passed on clean code:\n%s", out)
			}
			if !strings.Contains(out, "corral-report-begin\n") || !strings.Contains(out, "corral-report-end") {
				t.Fatalf("the graded run's output carries no hook block:\n%s", out)
			}
			failedTests, ok := lang.DeselectorFor(plug, inv)
			if !ok {
				t.Fatal("pytest has no failed-tests reader")
			}
			failing := failedTests(out)
			if len(failing) != 1 || !strings.HasSuffix(failing[0], "::test_wrong[a b]") {
				t.Fatalf("failing = %q, want the one id ending ::test_wrong[a b], from:\n%s", failing, out)
			}

			// The control: a command the hook has wrapped cannot be narrowed.
			wrapped := w.scorer.CommandRewrite(argv)
			if _, ok := lang.DeselectCmd(plug, inv.WithArgv(wrapped), failing); ok {
				t.Fatal("control: DeselectCmd narrowed an already-wrapped command; the wrapper must be applied last")
			}
			deselect, ok := lang.DeselectCmd(plug, inv, failing)
			if !ok {
				t.Fatalf("DeselectCmd refused the operator's command %v", argv)
			}
			rep, err := w.scorer.ScoreAuthoredDeselected(ctx, w.codeKey, code, brokenAuthored, subtractMutant, pytestArgv, adequacy.ShellJoin(deselect), failing)
			if err != nil {
				t.Fatal(err)
			}
			if !rep.CompliantPass || !rep.CanaryKilled || !slices.Contains(rep.Killed, "m1") {
				t.Errorf("salvaged pass: CompliantPass=%v CanaryKilled=%v Killed=%v, want a sound pass that kills m1\n%s", rep.CompliantPass, rep.CanaryKilled, rep.Killed, rep.BaselineOutput)
			}
		})
	}
}

// The stated limit, pinned by execution: the hook's record is the LAST thing
// a run prints, and the clean-code run salvage reads (CompliantRun, through
// the jail's RunTestVerbose) keeps the FIRST 16 KiB of a run's output. An
// authored test whose run prints more than that loses the record, and with a
// config that hides the printed lines the reader names nothing, so nothing is
// salvaged (never a wrong test). The workspace substrate holds the whole
// output and is the control. A fix (that door keeping the end of the output,
// as the door that names a killing test does) must change this test and the
// changelog's limit.
func TestTheSalvageDoorInTheJailLosesTheHookRecordPastItsOutputCap(t *testing.T) {
	pytestOnHost(t)
	plug, _ := lang.ByName("python")
	failedTests, _ := lang.DeselectorFor(plug, lang.Cmd(strings.Fields(pytestArgv)))
	// 400 lines of 80 bytes, printed past pytest's capture: 32 KiB.
	noisy := brokenAuthored + "\n\ndef test_noise(capsys):\n    with capsys.disabled():\n        print((\"n\" * 79 + \"\\n\") * 400)\n"
	for _, substrate := range []string{"workspace", "jail"} {
		t.Run(substrate, func(t *testing.T) {
			var iso sandbox.Isolator
			if substrate == "jail" {
				b, err := sandbox.Resolve(sandbox.Config{Backend: "bwrap"})
				if err != nil {
					t.Skipf("SKIP: no bwrap backend: %v", err)
				}
				iso = b
			}
			repoDir, seed := hidingRepo(t, "--no-summary")
			w := wirePython(t, iso, substrate, repoDir, &seed)
			ctx := context.Background()
			if substrate == "jail" {
				if pass, out, err := w.scorer.CompliantRun(ctx, w.codeKey, seed.files["calc.py"], seed.files["tests/test_calc.py"], pytestArgv); err != nil || !pass {
					t.Skipf("SKIP: the jail did not pass the fixture's own suite on unmutated code (jail or toolchain unavailable): %v\n%s", err, out)
				}
			}
			pass, out, err := w.scorer.CompliantRun(ctx, w.codeKey, seed.files["calc.py"], noisy, pytestArgv)
			if err != nil || pass {
				t.Fatalf("pass=%v err=%v, want a failing run", pass, err)
			}
			failing := failedTests(out)
			if substrate == "workspace" {
				if len(failing) != 1 || !strings.HasSuffix(failing[0], "::test_wrong[a b]") {
					t.Errorf("control: failing = %q from %d bytes of output, want the one id ending ::test_wrong[a b]", failing, len(out))
				}
				return
			}
			if strings.Contains(out, "corral-report-end") || len(failing) != 0 {
				t.Errorf("the jail's salvage door now returns the hook's record (%d bytes, failing=%q): the stated limit no longer holds, update the changelog and this test", len(out), failing)
			}
		})
	}
}

// The deselecting command reaches the runner narrowed AND wrapped. Asserted
// on what pytest itself received in a real run on the workspace substrate
// (its conftest writes down its arguments and whether the hook was loaded):
// the --deselect and the whole id, spaces included, with the hook loaded. The
// recorder half shows the same shape reaching the runner on all three
// wirings: the wrapper, with the command's words inside it.
func TestADeselectedCommandReachesTheRunnerNarrowedInsideTheWrapper(t *testing.T) {
	plug, _ := lang.ByName("python")
	argv := strings.Fields(pytestArgv)
	const id = "tests/test_calc_corral.py::test_wrong[a b]"
	deselect, ok := lang.DeselectCmd(plug, lang.Cmd(argv), []string{id})
	if !ok {
		t.Fatal("DeselectCmd refused the operator's command")
	}
	t.Run("real run", func(t *testing.T) {
		pytestOnHost(t)
		repoDir, seed := hidingRepo(t, "--no-summary")
		seen := filepath.Join(t.TempDir(), "seen.jsonl")
		mustWrite(t, filepath.Join(repoDir, "conftest.py"), "import json, os, sys\n\n\ndef pytest_configure(config):\n"+
			"    with open("+strconv.Quote(seen)+", 'a') as out:\n"+
			"        out.write(json.dumps({'args': list(config.invocation_params.args), 'hook': any(m.startswith('_corral') for m in sys.modules)}) + '\\n')\n")
		w := wirePython(t, nil, "workspace", repoDir, &seed)
		rep, err := w.scorer.ScoreAuthoredDeselected(context.Background(), w.codeKey, seed.files["calc.py"], brokenAuthored, subtractMutant, pytestArgv, adequacy.ShellJoin(deselect), []string{id})
		if err != nil {
			t.Fatal(err)
		}
		if !rep.CompliantPass || !slices.Contains(rep.Killed, "m1") {
			t.Fatalf("salvaged pass: CompliantPass=%v Killed=%v, want a pass that kills m1\n%s", rep.CompliantPass, rep.Killed, rep.BaselineOutput)
		}
		raw, err := os.ReadFile(seen)
		if err != nil {
			t.Fatal(err)
		}
		runs := 0
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			var got struct {
				Args []string
				Hook bool
			}
			if err := json.Unmarshal([]byte(line), &got); err != nil {
				t.Fatal(err)
			}
			runs++
			at := slices.Index(got.Args, "--deselect")
			if at < 0 || at+1 >= len(got.Args) || got.Args[at+1] != id {
				t.Errorf("pytest received %q, want --deselect %q among its arguments", got.Args, id)
			}
			if !got.Hook {
				t.Errorf("pytest ran %q without the failure hook loaded", got.Args)
			}
		}
		if runs < 3 {
			t.Errorf("pytest ran %d times, want the baseline, the canary and the mutant at least", runs)
		}
	})
	for _, substrate := range []string{"workspace", "jail", "single"} {
		t.Run(substrate, func(t *testing.T) {
			repoDir, seed := hidingRepo(t, "--no-summary")
			w := wirePython(t, &wrapRecordingIsolator{}, substrate, repoDir, &seed)
			rec := &passingRecorder{}
			w.scorer.Jail = rec
			if _, err := w.scorer.ScoreAuthoredDeselected(context.Background(), w.codeKey, seed.files["calc.py"], brokenAuthored, subtractMutant, pytestArgv, adequacy.ShellJoin(deselect), []string{id}); err != nil {
				t.Fatal(err)
			}
			pytestRuns := 0
			for _, c := range rec.cmds {
				if !slices.Contains(c, "pytest") {
					continue
				}
				pytestRuns++
				// sh -c <script> <$0> then the command's own words, which
				// start with the operator's and carry the deselect whole.
				at := slices.Index(c, "--deselect")
				if len(c) < 4+len(argv) || c[0] != "sh" || c[1] != "-c" || !slices.Equal(c[4:4+3], argv[:3]) || at < 4 || at+1 >= len(c) || c[at+1] != id {
					t.Errorf("a salvage command reached the runner without the wrapper around %v ... --deselect %q: %v", argv[:3], id, c[4:])
				}
			}
			if pytestRuns == 0 {
				t.Fatalf("no pytest command reached the runner: %v", rec.cmds)
			}
		})
	}
}

// The run-cache decision is the warming command's alone, and the report
// rewrite does not move it: a Go command is still read as RunCacheRewritten
// and a pytest command still gets no cache.
func TestReportArgsLeaveTheRunCacheDecisionAlone(t *testing.T) {
	goP, _ := lang.ByName("go")
	goCmd := []string{"go", "test", "./..."}
	if _, state := lang.RunCacheCmd(goP, lang.Cmd(goCmd), goCmd); state != lang.RunCacheRewritten {
		t.Errorf("go test ./... is %v, want rewritten", state)
	}
	if _, applied := lang.ReportCmd(goP, lang.Cmd(goCmd), goCmd); applied {
		t.Error("the report rewrite changed a go test command")
	}
	repoDir, seed := hiddenLinesRepo(t)
	w := wirePython(t, &wrapRecordingIsolator{}, "jail", repoDir, &seed)
	if w.runCache != "" || w.scorer.WarmCheck != nil {
		t.Errorf("a pytest run got run cache %q (WarmCheck set: %v); want none", w.runCache, w.scorer.WarmCheck != nil)
	}
}

// gradingRewrite composes the run cache's rewrite with the report rewrite:
// each is kept whole when the other is absent, the report rewrite is applied
// last, and a run the operator's command cannot carry the flags on gets none
// of them.
func TestGradingRewriteComposesTheTwoRewrites(t *testing.T) {
	py, _ := lang.ByName("python")
	mark := func(cmd []string) []string { return append(slices.Clone(cmd), "--cache") }
	pyCmd := []string{"python3", "-m", "pytest"}

	if f := gradingRewrite(py, lang.Cmd([]string{"sh", "-c", "pytest"}), []string{"sh", "-c", "pytest"}, nil); f != nil {
		t.Error("a shell-wrapped base command got a rewrite")
	}
	if got := gradingRewrite(py, lang.Cmd(pyCmd), pyCmd, nil)(pyCmd); !isReportWrapper(got, []string{"python3", "-m", "pytest", "-rfE"}) {
		t.Errorf("report alone = %v", got)
	}
	goP, _ := lang.ByName("go")
	goCmd := []string{"go", "test", "./..."}
	if got := gradingRewrite(goP, lang.Cmd(goCmd), goCmd, mark)(goCmd); !slices.Equal(got, []string{"go", "test", "./...", "--cache"}) {
		t.Errorf("run cache alone = %v", got)
	}
	// The report rewrite goes LAST: its wrapper is around the command the
	// run cache's rewrite already finished.
	if got := gradingRewrite(py, lang.Cmd(pyCmd), pyCmd, mark)(pyCmd); !isReportWrapper(got, []string{"python3", "-m", "pytest", "--cache", "-rfE"}) {
		t.Errorf("both = %v", got)
	}
	if f := gradingRewrite(nil, lang.Cmd(pyCmd), pyCmd, nil); f != nil {
		t.Error("no plugin, no run cache: want no rewrite")
	}
}

// Single-file --local with no `--` command grades with the plugin's own
// command, and gradingRewrite gives that command -rfE: the scan line must say
// so, read off the same base command, not off the empty one the operator gave.
func TestSingleFileRunWithNoCommandDisclosesTheReportArgs(t *testing.T) {
	dir := t.TempDir()
	code, test := filepath.Join(dir, "calc.py"), filepath.Join(dir, "test_calc.py")
	mustWrite(t, code, "def add(a, b):\n    return a + b\n")
	mustWrite(t, test, "from calc import add\n\ndef test_add():\n    assert add(2, 3) == 5\n")
	plug, _ := lang.ByName("python")
	var out strings.Builder
	prep, err := prepareAuditJail(context.Background(), localAuditInput{
		codePath: code, testPath: test, goal: "g", lang: "python", iso: &wrapRecordingIsolator{},
	}, plug, time.Minute, &out)
	if err != nil {
		t.Fatal(err)
	}
	defer prep.cleanup()
	if !strings.Contains(out.String(), "pytest: corral adds -rfE to every graded run") {
		t.Errorf("no report-flags disclosure for a single-file run graded with the plugin's own pytest command:\n%s", out.String())
	}
	if got := prep.wiring.scorer.CommandRewrite(plug.TestCmd()); !slices.Contains(got, "-rfE") {
		t.Fatalf("the plugin's own command got no -rfE (%v), so the disclosure would be false", got)
	}
}
