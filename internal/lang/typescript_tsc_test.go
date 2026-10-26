// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// writeExe writes an executable shell script at path.
func writeExe(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil { // #nosec G306 -- an executable the test runs
		t.Fatal(err)
	}
}

// THE PROJECT'S OWN COMPILER COUNTS (B7, ruling R26). REA's TypeScript is
// node_modules/.bin/tsc and this machine has no global tsc, so the pre-check
// refused every TS file before the first mutant. The project's tsc (its
// node_modules/.bin/tsc, or the typescript package's bin/tsc) is accepted
// before a global one; with neither, the refusal names both places looked.
func TestTypeScriptPreflightAcceptsTheProjectsOwnTsc(t *testing.T) {
	ts := tsPlugin{}
	bin := t.TempDir()
	node := filepath.Join(bin, "node")
	writeExe(t, node, "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", bin) // node only: no global tsc

	local := t.TempDir()
	writeExe(t, filepath.Join(local, "node_modules", ".bin", "tsc"), "#!/bin/sh\nexit 0\n")
	if err := ts.Preflight(local, []string{node, "--test"}); err != nil {
		t.Errorf("a repo with node_modules/.bin/tsc and no global tsc was refused: %v", err)
	}
	pkg := t.TempDir()
	writeExe(t, filepath.Join(pkg, "node_modules", "typescript", "bin", "tsc"), "#!/usr/bin/env node\n")
	if err := ts.Preflight(pkg, []string{node, "--test"}); err != nil {
		t.Errorf("a repo with node_modules/typescript/bin/tsc and no global tsc was refused: %v", err)
	}
	none := t.TempDir()
	err := ts.Preflight(none, []string{node, "--test"})
	if err == nil {
		t.Fatal("no project tsc and no global tsc must be refused")
	}
	for _, want := range []string{filepath.Join(none, "node_modules", ".bin", "tsc"), filepath.Join(none, "node_modules", "typescript", "bin", "tsc"), "PATH"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name %q: %v", want, err)
		}
	}
	// A global tsc alone still passes, as before.
	writeExe(t, filepath.Join(bin, "tsc"), "#!/bin/sh\nexit 0\n")
	if err := ts.Preflight(none, []string{node, "--test"}); err != nil {
		t.Errorf("a global tsc must still pass: %v", err)
	}
	if err := ts.Preflight("", []string{node, "--test"}); err != nil {
		t.Errorf("no repository (single-file) with a global tsc must still pass: %v", err)
	}
}

// THE COMPILE CHECK RUNS THE PROJECT'S tsc, BY EXECUTION (ruling R26), in the
// working directory a workspace-substrate run uses and with no tsc on PATH,
// which is the host's PATH on that substrate. A fake project tsc says what
// it was run with; with CORRALAI_TYPESCRIPT_NODE_MODULES set to a real
// node_modules holding typescript, the real compiler accepts a correct file
// and rejects a type error.
func TestTypeScriptCompileCheckRunsTheProjectsTscOnTheHost(t *testing.T) {
	runIn := func(t *testing.T, repo string) (string, error) {
		t.Helper()
		cmd := tsPlugin{}.CompileCheck("foo.ts", "foo.test.ts")[0]
		c := exec.Command(cmd[0], cmd[1:]...) // #nosec G204 -- the command under test
		c.Dir = repo
		c.Env = []string{"PATH=" + filepath.Dir(mustLook(t, "node")) + ":/usr/bin:/bin", "HOME=" + t.TempDir()}
		out, err := c.CombinedOutput()
		return string(out), err
	}
	writeTS := func(t *testing.T, repo, code string) {
		t.Helper()
		for name, body := range map[string]string{"foo.ts": code, "foo.test.ts": "import { f } from './foo.ts';\nexport const g: number = f(1);\n"} {
			if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := exec.LookPath("tsc"); err == nil && os.Getenv("CORRALAI_TYPESCRIPT_NODE_MODULES") == "" {
		t.Log("a global tsc is on PATH; the fake case below still proves the project's is chosen first")
	}
	repo := t.TempDir()
	writeExe(t, filepath.Join(repo, "node_modules", ".bin", "tsc"), "#!/bin/sh\necho \"corral-project-tsc $*\"\nexit 0\n")
	writeTS(t, repo, "export function f(n: number): number { return n; }\n")
	out, err := runIn(t, repo)
	if err != nil || !strings.Contains(out, "corral-project-tsc -p ") {
		t.Errorf("the compile check did not run the project's tsc: %v\n%s", err, out)
	}

	nm := os.Getenv("CORRALAI_TYPESCRIPT_NODE_MODULES")
	if nm == "" {
		t.Skip("CORRALAI_TYPESCRIPT_NODE_MODULES is not set — the real-compiler half needs a node_modules with typescript in it")
	}
	real := t.TempDir()
	if err := os.Symlink(nm, filepath.Join(real, "node_modules")); err != nil {
		t.Fatal(err)
	}
	writeTS(t, real, "export function f(n: number): number { return n; }\n")
	if out, err := runIn(t, real); err != nil {
		t.Errorf("the project's real tsc rejected a correct file: %v\n%s", err, out)
	}
	writeTS(t, real, "export function f(n: number): number { return 'x'; }\n")
	if out, err := runIn(t, real); err == nil || !strings.Contains(out, "TS2322") {
		t.Errorf("the project's real tsc must reject a type error (TS2322): %v\n%s", err, out)
	}
}

func mustLook(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s not installed", name)
	}
	return p
}
