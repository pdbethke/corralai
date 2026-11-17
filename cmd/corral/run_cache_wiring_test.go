// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/sandbox"
)

// gradedRun is one command the grading jail was handed, and whether it was
// the warming run (the one run that has the cache writable).
type gradedRun struct {
	cmd  []string
	warm bool
}

// recordingGradingJail wraps the jail an audit built and records every command
// it is handed, through every run method a door can use. It forwards to the
// real jail, so the runs are real.
type recordingGradingJail struct {
	adequacy.RunCacheJail
	mu   sync.Mutex
	runs []gradedRun
}

func (r *recordingGradingJail) note(cmd []string, warm bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs = append(r.runs, gradedRun{cmd: slices.Clone(cmd), warm: warm})
}

func (r *recordingGradingJail) RunTest(ctx context.Context, f map[string]string, cmd []string) (bool, error) {
	r.note(cmd, false)
	return r.RunCacheJail.RunTest(ctx, f, cmd)
}

func (r *recordingGradingJail) RunTestDetailed(ctx context.Context, f map[string]string, cmd []string) (bool, []byte, error) {
	r.note(cmd, false)
	return r.RunCacheJail.(adequacy.DetailedJail).RunTestDetailed(ctx, f, cmd)
}

func (r *recordingGradingJail) RunTestVerbose(ctx context.Context, f map[string]string, cmd []string) (bool, string, error) {
	r.note(cmd, false)
	return r.RunCacheJail.(adequacy.VerboseJail).RunTestVerbose(ctx, f, cmd)
}

func (r *recordingGradingJail) WarmRun(ctx context.Context, f map[string]string, cmd []string) (bool, []byte, error) {
	r.note(cmd, true)
	return r.RunCacheJail.WarmRun(ctx, f, cmd)
}

// recordRuns swaps the scorer's and validator's jail for a recorder over it.
func recordRuns(t *testing.T, w *jailWiring) *recordingGradingJail {
	t.Helper()
	inner, ok := w.scorer.Jail.(adequacy.RunCacheJail)
	if !ok {
		t.Fatalf("the grading jail is %T, not a RunCacheJail", w.scorer.Jail)
	}
	rec := &recordingGradingJail{RunCacheJail: inner}
	w.scorer.Jail = rec
	w.validator.Jail = rec
	return rec
}

// wrapRecordingIsolator is a backend that cannot overlay a directory (so a run
// cache is unshared) and records the Options and command of every run it is
// handed, then runs `true`, which passes.
type wrapRecordingIsolator struct {
	mu    sync.Mutex
	cache []*sandbox.CacheMount
	cmds  []string
}

func (w *wrapRecordingIsolator) Name() string     { return "bwrap" }
func (w *wrapRecordingIsolator) Preflight() error { return nil }
func (w *wrapRecordingIsolator) Wrap(command string, o sandbox.Options, _ []string) ([]string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cache = append(w.cache, o.Cache)
	w.cmds = append(w.cmds, command)
	return []string{"true"}, nil
}

// goCacheModule is a one-package Go module as a shared seed, with the files a
// graded run needs; repoDir holds only the subject and its test.
func goCacheModule(t *testing.T) (repoDir string, seed repoSeed) {
	t.Helper()
	repoDir = t.TempDir()
	files := map[string]string{
		"go.mod":      "module example.com/m\n\ngo 1.21\n",
		"p/p.go":      "package p\n\nfunc One() int { return 1 }\n",
		"p/p_test.go": "package p\n\nimport \"testing\"\n\nfunc TestOne(t *testing.T) {\n\tif One() != 1 {\n\t\tt.Fatal(\"no\")\n\t}\n}\n",
	}
	for rel, body := range files {
		mustWrite(t, filepath.Join(repoDir, rel), body)
	}
	return repoDir, repoSeed{seedDir: repoDir, files: files, cleanup: func() {}}
}

func wireGo(t *testing.T, iso sandbox.Isolator, repoDir string, seed *repoSeed, argv []string, langName string) jailWiring {
	t.Helper()
	w, err := buildJailWiring(context.Background(), jailWiringInput{
		iso: iso, timeout: 2 * time.Minute,
		codePath: "p/p.go", testPath: "p/p_test.go", repoDir: repoDir, langName: langName,
		fsPath:    func(q string) string { return filepath.Join(repoDir, q) },
		code:      []byte(seed.files["p/p.go"]),
		devTest:   []byte(seed.files["p/p_test.go"]),
		checkArgv: argv, inv: lang.Cmd(argv),
		stdout: io.Discard, seed: seed,
	})
	if err != nil {
		t.Fatalf("buildJailWiring: %v", err)
	}
	return w
}

var oneMutant = []adequacy.Mutant{{ID: "m1", Search: "return 1", Replace: "return 2"}}

// overlayingGoJailOrSkip is the real jail where a shared cache is possible:
// a bwrap that overlays, and a go toolchain.
func overlayingGoJailOrSkip(t *testing.T) sandbox.Isolator {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go on PATH")
	}
	iso, err := resolveLocalJail("")
	if err != nil {
		t.Skipf("no sandbox backend on this host: %v", err)
	}
	t.Setenv("TMPDIR", t.TempDir()) // the cache directory is made under it
	if !sandbox.Overlays(iso) || !sandbox.OverlaysDir(iso, t.TempDir()) {
		t.Skipf("%s cannot overlay a directory here; the shared-cache path is unexercised", iso.Name())
	}
	return iso
}

// A Go run through the real wiring and the real jail: the baseline is the one
// run with the cache writable, a mutant runs frozen, every test command
// carries -count=1, and the cache directory is gone once the file's run ends.
func TestGoGradingRunsShareAFrozenRunCache(t *testing.T) {
	iso := overlayingGoJailOrSkip(t)
	repoDir, seed := goCacheModule(t)
	w := wireGo(t, iso, repoDir, &seed, []string{"go", "test", "./..."}, "go")
	rec := recordRuns(t, &w)

	if w.runCache != "shared" {
		t.Fatalf("recorded run cache = %q, want %q", w.runCache, "shared")
	}
	rep, err := w.scorer.ScoreReport(context.Background(), w.codeKey, string(seed.files["p/p.go"]), "", oneMutant, "go test ./...")
	if err != nil {
		t.Fatal(err)
	}
	if !rep.CompliantPass || len(rep.Killed) != 1 {
		t.Fatalf("grading changed: pass=%v killed=%d, want a passing baseline and the mutant killed", rep.CompliantPass, len(rep.Killed))
	}
	if rep.RunCache.Target == "" {
		t.Errorf("the report says the cache was not shared: %+v", rep.RunCache)
	}
	var warm, frozen int
	for _, r := range rec.runs {
		isTest := len(r.cmd) > 1 && r.cmd[1] == "test"
		if isTest && !slices.Contains(r.cmd, "-count=1") {
			t.Errorf("a go test command reached the shared-cache jail without -count=1: %v", r.cmd)
		}
		if r.warm {
			warm++
		} else if isTest {
			frozen++
		}
	}
	if warm != 1 || frozen < 1 {
		t.Errorf("warm runs = %d, frozen test runs = %d; want exactly one warming run (the baseline) and at least one frozen run", warm, frozen)
	}

	tmp := os.TempDir() // the test's own TMPDIR (overlayingGoJailOrSkip)
	if left, _ := filepath.Glob(filepath.Join(tmp, "corral-cache-*")); len(left) != 1 {
		t.Fatalf("before the run ends there should be one cache directory under %s, got %v", tmp, left)
	}
	w.cleanup()
	if left, _ := filepath.Glob(filepath.Join(tmp, "corral-cache-*")); len(left) != 0 {
		t.Errorf("the file's run ended and left cache directories behind: %v", left)
	}
}

// A language whose runner row has no run cache mounts nothing and rewrites
// nothing: today's run, byte for byte.
func TestPythonGradingRunsMountNoRunCache(t *testing.T) {
	fake := &wrapRecordingIsolator{}
	repoDir, seed := goCacheModule(t)
	argv := []string{"python3", "-m", "pytest"}
	w := wireGo(t, fake, repoDir, &seed, argv, "python")
	rec := recordRuns(t, &w)
	defer w.cleanup()

	if w.runCache != "" {
		t.Errorf("recorded run cache = %q, want empty for a runner with no run cache", w.runCache)
	}
	if w.scorer.CommandRewrite != nil || w.validator.CommandRewrite != nil {
		t.Error("a runner with no run cache must carry no command rewrite")
	}
	if _, err := w.scorer.ScoreReport(context.Background(), w.codeKey, seed.files["p/p.go"], "", oneMutant, "python3 -m pytest"); err != nil {
		t.Fatal(err)
	}
	for _, r := range rec.runs {
		if strings.Join(r.cmd, " ") != "python3 -m pytest" && !strings.Contains(strings.Join(r.cmd, " "), "py_compile") {
			if slices.Contains(r.cmd, "-count=1") {
				t.Errorf("a python command was rewritten: %v", r.cmd)
			}
		}
	}
	for i, c := range fake.cache {
		if c != nil {
			t.Errorf("run %d mounted a cache %+v", i, c)
		}
	}
}

// A backend that cannot overlay: the file's record says why, the operator's
// command reaches the jail unchanged, no cache is mounted, and the grading is
// what a jail with no run cache grades.
func TestAnUnsharedRunCacheIsRecordedAndChangesNothingElse(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	fake := &wrapRecordingIsolator{}
	repoDir, seed := goCacheModule(t)
	w := wireGo(t, fake, repoDir, &seed, []string{"go", "test", "./..."}, "go")
	rec := recordRuns(t, &w)
	defer w.cleanup()

	if !strings.HasPrefix(w.runCache, "unshared: this bwrap cannot overlay") {
		t.Fatalf("recorded run cache = %q, want unshared with the jail's reason", w.runCache)
	}
	if w.scorer.CommandRewrite != nil || w.validator.CommandRewrite != nil {
		t.Fatal("an unshared run must keep the operator's command: no rewrite")
	}
	got, err := w.scorer.ScoreReport(context.Background(), w.codeKey, seed.files["p/p.go"], "", oneMutant, "go test ./...")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rec.runs {
		if slices.Contains(r.cmd, "-count=1") {
			t.Errorf("an unshared run's command was rewritten: %v", r.cmd)
		}
	}
	for i, c := range fake.cache {
		if c != nil {
			t.Errorf("run %d mounted a cache %+v", i, c)
		}
	}

	// The same grading through a jail built the way every jail was before.
	plain := advpool.JailScorer{Jail: newRunJail(&wrapRecordingIsolator{}, 2*time.Minute, nil), RepoDir: repoDir, BaseFiles: seed.files, Lang: "go"}
	want, err := plain.ScoreReport(context.Background(), w.codeKey, seed.files["p/p.go"], "", oneMutant, "go test ./...")
	if err != nil {
		t.Fatal(err)
	}
	if got.CompliantPass != want.CompliantPass || len(got.Killed) != len(want.Killed) || got.Total != want.Total || got.CanaryKilled != want.CanaryKilled {
		t.Errorf("an unshared run graded differently from a jail with no run cache: %+v vs %+v", got, want)
	}

	// prepareAuditJail says so on the file's own lines.
	var out bytes.Buffer
	plug, _ := lang.ByName("go")
	prep, err := prepareAuditJail(context.Background(), localAuditInput{
		repoDir: repoDir, codePath: "p/p.go", testPath: "p/p_test.go", goal: "g", lang: "go",
		checkArgv: []string{"go", "test", "./..."}, seed: &seed, iso: fake,
	}, plug, time.Minute, &out)
	if err != nil {
		t.Fatal(err)
	}
	defer prep.cleanup()
	if !strings.Contains(out.String(), "  build cache: not shared for p/p.go: this bwrap cannot overlay") {
		t.Errorf("the file's output lacks the not-shared line:\n%s", out.String())
	}
}

// A warming command the row cannot read must not share the cache: the warm
// run would then be free to write a cached test result. The file runs exactly
// as it always did, and its record says why, naming what became of the command.
func TestAWarmCommandWeCannotRewriteSharesNoCache(t *testing.T) {
	for _, tc := range []struct {
		argv  []string
		state string
	}{
		{[]string{"go", "test", "-no-such-flag", "./..."}, "unreadable"},
		{[]string{"go", "vet", "./..."}, "no-test"},
	} {
		t.Run(strings.Join(tc.argv, " "), func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			fake := &wrapRecordingIsolator{}
			repoDir, seed := goCacheModule(t)
			w := wireGo(t, fake, repoDir, &seed, tc.argv, "go")
			defer w.cleanup()
			want := "unshared: the test command cannot be given the flag that forbids a cached result (" + tc.state + ")"
			if w.runCache != want {
				t.Errorf("recorded run cache = %q, want %q", w.runCache, want)
			}
			if w.scorer.CommandRewrite != nil || w.validator.CommandRewrite != nil {
				t.Error("no rewrite when the cache is not shared")
			}
			if left, _ := filepath.Glob(filepath.Join(os.TempDir(), "corral-cache-*")); len(left) != 0 {
				t.Errorf("a cache directory was made for a run that shares none: %v", left)
			}
		})
	}
}

// A run cancelled mid-grading leaves no cache directory behind.
func TestRunCacheIsRemovedWhenARunIsCancelled(t *testing.T) {
	iso := overlayingGoJailOrSkip(t)
	repoDir, seed := goCacheModule(t)
	w := wireGo(t, iso, repoDir, &seed, []string{"go", "test", "./..."}, "go")
	if left, _ := filepath.Glob(filepath.Join(os.TempDir(), "corral-cache-*")); len(left) != 1 {
		t.Fatalf("the wiring should have made one cache directory, got %v", left)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = w.scorer.ScoreReport(ctx, w.codeKey, seed.files["p/p.go"], "", oneMutant, "go test ./...")
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done
	w.cleanup()
	if left, _ := filepath.Glob(filepath.Join(os.TempDir(), "corral-cache-*")); len(left) != 0 {
		t.Errorf("a cancelled run left cache directories behind: %v", left)
	}
}
