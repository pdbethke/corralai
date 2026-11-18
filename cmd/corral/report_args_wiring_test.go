// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"io"
	"os/exec"
	"path/filepath"
	"slices"
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
	repoDir = t.TempDir()
	files := map[string]string{
		"pyproject.toml":     "[tool.pytest.ini_options]\naddopts = \"-rXs -p no:cacheprovider\"\n",
		"calc.py":            "def add(a, b):\n    return a + b\n",
		"tests/__init__.py":  "",
		"tests/test_calc.py": "from calc import add\n\n\ndef test_add():\n    assert add(2, 3) == 5\n\n\ndef test_zero():\n    assert add(0, 0) == 0\n",
	}
	for rel, body := range files {
		mustWrite(t, filepath.Join(repoDir, rel), body)
	}
	return repoDir, repoSeed{seedDir: repoDir, files: files, cleanup: func() {}}
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
// jail. With the rewrite taken away, the same kill names nothing and is
// confirmed by repeating the whole run: the control, showing the fixture
// really hides the lines.
func TestAKillOnAProjectHidingFailedLinesIsNamedAndConfirmedAlone(t *testing.T) {
	pytestOnHost(t)
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
			for _, control := range []bool{false, true} {
				repoDir, seed := hiddenLinesRepo(t)
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

// gradingRewrite composes the report rewrite with the run cache's: each is
// kept whole when the other is absent, and a run the operator's command
// cannot carry the flags on gets none of them.
func TestGradingRewriteComposesTheTwoRewrites(t *testing.T) {
	py, _ := lang.ByName("python")
	mark := func(cmd []string) []string { return append(slices.Clone(cmd), "--cache") }
	pyCmd := []string{"python3", "-m", "pytest"}

	if f := gradingRewrite(py, lang.Cmd([]string{"sh", "-c", "pytest"}), []string{"sh", "-c", "pytest"}, nil); f != nil {
		t.Error("a shell-wrapped base command got a rewrite")
	}
	if got := gradingRewrite(py, lang.Cmd(pyCmd), pyCmd, nil)(pyCmd); !slices.Equal(got, []string{"python3", "-m", "pytest", "-rfE"}) {
		t.Errorf("report alone = %v", got)
	}
	goP, _ := lang.ByName("go")
	goCmd := []string{"go", "test", "./..."}
	if got := gradingRewrite(goP, lang.Cmd(goCmd), goCmd, mark)(goCmd); !slices.Equal(got, []string{"go", "test", "./...", "--cache"}) {
		t.Errorf("run cache alone = %v", got)
	}
	if got := gradingRewrite(py, lang.Cmd(pyCmd), pyCmd, mark)(pyCmd); !slices.Equal(got, []string{"python3", "-m", "pytest", "-rfE", "--cache"}) {
		t.Errorf("both = %v", got)
	}
	if f := gradingRewrite(nil, lang.Cmd(pyCmd), pyCmd, nil); f != nil {
		t.Error("no plugin, no run cache: want no rewrite")
	}
}
