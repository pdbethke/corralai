// SPDX-License-Identifier: Elastic-2.0

package main

import (
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
	"github.com/pdbethke/corralai/internal/sandbox"
)

// A jest run through the real wiring and the real jail, as
// TestRustGradingRunsShareAFrozenTargetDir is for cargo: the file records a
// shared cache, the baseline is the one run with the cache writable, the
// mutant runs frozen and is killed, and every graded command names the
// shared cache, inside the record hook's wrapper, which the jest row adds
// to a graded run after the cache's words (gradingRewrite).
func TestJestGradingRunsShareAFrozenCacheDirectory(t *testing.T) {
	nm := os.Getenv("CORRALAI_JEST_NODE_MODULES")
	if nm == "" {
		t.Skip("SKIP: CORRALAI_JEST_NODE_MODULES is not set (scripts/ci-provision-test-toolchains.sh installs the runner-fixture pins and sets it)")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("SKIP: node not on PATH")
	}
	iso, err := resolveLocalJail("")
	if err != nil {
		t.Skipf("SKIP: no sandbox backend on this host: %v", err)
	}
	t.Setenv("TMPDIR", t.TempDir())
	if !sandbox.Overlays(iso) || !sandbox.OverlaysDir(iso, t.TempDir()) {
		t.Skipf("SKIP: %s cannot overlay a directory here; the shared-cache path is unexercised", iso.Name())
	}

	repoDir := t.TempDir()
	files := map[string]string{
		"package.json": `{"name":"p","private":true}` + "\n",
		"calc.js":      "function add(a, b) { return a + b; }\nmodule.exports = { add };\n",
		"calc.test.js": "const { add } = require('./calc');\ntest('adds', () => { expect(add(1, 2)).toBe(3); });\n",
	}
	for rel, body := range files {
		mustWrite(t, filepath.Join(repoDir, rel), body)
	}
	seed := repoSeed{seedDir: repoDir, files: files, cleanup: func() {},
		binds: []adequacy.DepBind{{Host: nm, Rel: "node_modules"}}}
	argv := []string{"node", "./node_modules/jest/bin/jest.js"}
	w, err := buildJailWiring(context.Background(), jailWiringInput{
		iso: iso, timeout: 3 * time.Minute,
		codePath: "calc.js", testPath: "calc.test.js", repoDir: repoDir, langName: "javascript",
		fsPath:    func(q string) string { return filepath.Join(repoDir, q) },
		code:      []byte(files["calc.js"]),
		devTest:   []byte(files["calc.test.js"]),
		checkArgv: argv, inv: lang.Cmd(argv),
		stdout: io.Discard, seed: &seed,
	})
	if err != nil {
		t.Fatalf("buildJailWiring: %v", err)
	}
	defer w.cleanup()
	if w.runCache != "shared" {
		t.Fatalf("recorded run cache = %q, want %q", w.runCache, "shared")
	}
	rec := recordRuns(t, &w)
	mutants := []adequacy.Mutant{{ID: "m1", Search: "a + b", Replace: "a - b"}}
	rep, err := w.scorer.ScoreDevReport(context.Background(), w.codeKey, files["calc.js"], "", mutants, strings.Join(argv, " "), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.CompliantPass || len(rep.Killed) != 1 {
		t.Fatalf("pass=%v killed=%d, want a passing baseline and the mutant killed", rep.CompliantPass, len(rep.Killed))
	}
	if !rec.Warmed() {
		t.Error("the baseline was the warming run, but the jail does not say it was warmed")
	}
	// The compile checks (node --check) run no jest and are run as given.
	var warm, frozen int
	for _, r := range rec.runs {
		joined := strings.Join(r.cmd, " ")
		if !strings.Contains(joined, "jest") {
			continue
		}
		if !strings.Contains(joined, "--cacheDirectory="+sandbox.CacheTarget) {
			t.Errorf("a jest command does not name the shared cache: %v", r.cmd)
		}
		if r.warm {
			warm++
		} else {
			frozen++
		}
	}
	if warm != 1 || frozen < 1 {
		t.Errorf("warm runs = %d, frozen runs = %d; want exactly one warming run (the baseline) and at least one frozen jest run", warm, frozen)
	}
}

// A project that uses ts-jest gets no shared jest cache through the real
// wiring, and its record says why: the reason is read from the tree the jail
// will see, here a node_modules bind that holds ts-jest while package.json
// does not name it (a workspace root's install).
func TestJestGradingInATSJestProjectSharesNoCache(t *testing.T) {
	repoDir := t.TempDir()
	nm := t.TempDir()
	mustWrite(t, filepath.Join(nm, "ts-jest", "package.json"), `{"name":"ts-jest","version":"26.5.6"}`+"\n")
	files := map[string]string{
		"package.json": `{"name":"p","private":true,"jest":{"preset":"ts-jest"}}` + "\n",
		"calc.ts":      "export function add(a: number, b: number): number { return a + b; }\n",
		"calc.test.ts": "import { add } from './calc';\ntest('adds', () => { expect(add(1, 2)).toBe(3); });\n",
	}
	for rel, body := range files {
		mustWrite(t, filepath.Join(repoDir, rel), body)
	}
	seed := repoSeed{seedDir: repoDir, files: files, cleanup: func() {},
		binds: []adequacy.DepBind{{Host: nm, Rel: "node_modules"}}}
	argv := []string{"node", "./node_modules/jest/bin/jest.js"}
	w, err := buildJailWiring(context.Background(), jailWiringInput{
		timeout:  time.Minute,
		codePath: "calc.ts", testPath: "calc.test.ts", repoDir: repoDir, langName: "typescript",
		fsPath:    func(q string) string { return filepath.Join(repoDir, q) },
		code:      []byte(files["calc.ts"]),
		devTest:   []byte(files["calc.test.ts"]),
		checkArgv: argv, inv: lang.Cmd(argv),
		stdout: io.Discard, seed: &seed,
	})
	if err != nil {
		t.Fatalf("buildJailWiring: %v", err)
	}
	defer w.cleanup()
	const want = "unshared: the project uses ts-jest, whose cache key leaves out the files a file imports"
	if w.runCache != want {
		t.Errorf("recorded run cache = %q, want %q", w.runCache, want)
	}
	if strings.Contains(strings.Join(w.scorer.CommandRewrite(argv), " "), "--cacheDirectory") {
		t.Errorf("a ts-jest project's graded command names a cache: %v", w.scorer.CommandRewrite(argv))
	}
}
