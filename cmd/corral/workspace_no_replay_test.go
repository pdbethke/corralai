// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/reposcan"
)

// On the workspace substrate every run uses the operator's GOCACHE, so a
// `go test` without -count=1 run twice on unchanged code reports the first
// run's result the second time ("(cached)") instead of running the tests.
// The unmutated baseline, its stability runs and the re-probe that confirms
// a timed-out kill all run unchanged code, so a replay there sizes every
// per-mutant cap from a run that took no time and confirms any timeout as a
// kill. The control runs the operator's command as typed through the pool
// and sees the replay; the wiring's rewrite (workspaceNoReplay through
// gradingRewrite) runs the tests every time.
func TestWorkspaceGoRunsNeverReplayACachedResult(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("SKIP: go not on PATH")
	}
	t.Setenv("GOCACHE", t.TempDir())
	t.Setenv("GOFLAGS", "")
	root := t.TempDir()
	files := map[string]string{
		"go.mod":    "module example.com/a\n\ngo 1.21\n",
		"a.go":      "package a\n\nfunc Add(x, y int) int { return x + y }\n",
		"a_test.go": "package a\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 2) != 3 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n",
	}
	for rel, body := range files {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	argv := []string{"go", "test", "./..."}
	w, err := buildJailWiring(context.Background(), jailWiringInput{
		substrate: substrateWorkspace,
		repoDir:   root, timeout: 2 * time.Minute,
		codePath: "a.go", testPath: "a_test.go", langName: "go",
		checkArgv: argv, inv: lang.Cmd(argv),
		code:    []byte(files["a.go"]),
		devTest: []byte(files["a_test.go"]),
		fsPath:  func(q string) string { return filepath.Join(root, q) },
		stdout:  io.Discard,
	})
	if err != nil {
		t.Fatalf("buildJailWiring: %v", err)
	}
	defer w.cleanup()
	dj, ok := w.scorer.Jail.(adequacy.DetailedJail)
	if !ok {
		t.Fatalf("the workspace pool is %T, not a DetailedJail", w.scorer.Jail)
	}
	if w.scorer.CommandRewrite == nil || w.validator.CommandRewrite == nil {
		t.Fatal("the workspace scorer and validator rewrite no command, so go test runs without -count=1")
	}
	run := func(cmd []string) string {
		t.Helper()
		pass, out, err := dj.RunTestDetailed(context.Background(), map[string]string{}, cmd)
		if err != nil || !pass {
			t.Fatalf("%v: pass=%v err=%v\n%s", cmd, pass, err, out)
		}
		return string(out)
	}
	// The control: as typed, the second run replays the first.
	run(argv)
	if out := run(argv); !strings.Contains(out, "(cached)") {
		t.Fatalf("control: the second `go test ./...` on unchanged code did not replay a cached result, so this test cannot show the hazard:\n%s", out)
	}
	// Through the scorer's and the validator's rewrite: run every time.
	for name, rw := range map[string]func([]string) []string{"scorer": w.scorer.CommandRewrite, "validator": w.validator.CommandRewrite} {
		cmd := rw(argv)
		if !strings.Contains(strings.Join(cmd, " "), "-count=1") {
			t.Errorf("%s rewrite gave %v, want -count=1", name, cmd)
		}
		for i := 0; i < 2; i++ {
			if out := run(cmd); strings.Contains(out, "(cached)") {
				t.Errorf("%s run %d of %v replayed a cached result:\n%s", name, i+1, cmd, out)
			}
		}
	}
}

// What the workspace rewrite does to each command shape: -count=1 only where
// go's own words have no -count, an explicit -count kept as given, a shell
// wrapper run as given, and a language whose runner has no no-replay rewrite
// handed no run-cache rewrite at all.
func TestWorkspaceNoReplayRewritesOnlyWhatItCanRead(t *testing.T) {
	goPlug := gradingPlugin("go")
	base := []string{"go", "test", "./..."}
	rw := workspaceNoReplay(goPlug, lang.Cmd(base), base)
	if rw == nil {
		t.Fatal("go test on the workspace has no no-replay rewrite")
	}
	for _, tc := range []struct{ in, want string }{
		{"go test ./...", "go test ./... -count=1"},
		{"go test -count=3 ./...", "go test -count=3 ./..."},
		{"sh -c go test ./...", "sh -c go test ./..."},
	} {
		if got := strings.Join(rw(strings.Fields(tc.in)), " "); got != tc.want {
			t.Errorf("%q rewritten to %q, want %q", tc.in, got, tc.want)
		}
	}
	py := []string{"python3", "-m", "pytest", "-q"}
	if rw := workspaceNoReplay(gradingPlugin("python"), lang.Cmd(py), py); rw != nil {
		t.Errorf("pytest on the workspace got a no-replay rewrite: %v", rw(py))
	}
	if rw := workspaceNoReplay(nil, lang.Cmd(base), base); rw != nil {
		t.Error("no plugin, yet a rewrite")
	}
}

// The scan header on the workspace substrate says, once per language, when
// the base command is one the no-replay rewrite cannot read (it then runs as
// given, with the operator's cache), and says nothing for a command it reads
// or a language with no such rewrite.
func TestTheWorkspaceHeaderSaysWhenACommandMayReplayACachedResult(t *testing.T) {
	jobs := []reposcan.Job{{Path: "a.go", Lang: "go"}, {Path: "b.go", Lang: "go"}}
	const line = "  test results: the go test command cannot be read as one that forbids a cached result, so on the workspace it runs as given and may report a result cached by an earlier run\n"
	for _, tc := range []struct {
		argv []string
		want int
	}{
		{[]string{"sh", "-c", "go test ./..."}, 1},
		{[]string{"go", "test", "./..."}, 0},
		{[]string{"go", "test", "-count=2", "./..."}, 0},
	} {
		var buf bytes.Buffer
		printRunDisclosures(&buf, &localExecutor{repoDir: t.TempDir(), substrate: substrateWorkspace, checkArgv: tc.argv}, jobs)
		if got := strings.Count(buf.String(), line); got != tc.want {
			t.Errorf("%v: the line appears %d times, want %d:\n%s", tc.argv, got, tc.want, buf.String())
		}
	}
	var buf bytes.Buffer
	printRunDisclosures(&buf, &localExecutor{repoDir: t.TempDir(), substrate: substrateWorkspace, checkArgv: []string{"sh", "-c", "pytest"}}, []reposcan.Job{{Path: "c.py", Lang: "python"}})
	if strings.Contains(buf.String(), "test results:") {
		t.Errorf("pytest has no no-replay rewrite, yet:\n%s", buf.String())
	}
}

// The workspace's no-replay rewrite never adds a shared cache's words: jest
// shares a cache directory in the jail by naming it on the command
// (--cacheDirectory=/corral-cache, lang.RunCacheCmd), and the workspace has
// no such directory, so a jest command there is run as typed.
func TestWorkspaceJestCommandsNameNoSharedCache(t *testing.T) {
	argv := []string{"node", "./node_modules/jest/bin/jest.js"}
	js := gradingPlugin("javascript")
	if rw := workspaceNoReplay(js, lang.Cmd(argv), argv); rw != nil {
		if got := rw(argv); strings.Contains(strings.Join(got, " "), "cacheDirectory") {
			t.Errorf("the workspace rewrite of %v names a cache: %v", argv, got)
		}
	}
	if got, _ := lang.RunCacheCmd(js, lang.Cmd(argv), argv); !strings.Contains(strings.Join(got, " "), "--cacheDirectory=") {
		t.Errorf("control: the jail's RunCacheCmd(%v) = %v, want the shared cache named", argv, got)
	}
}
