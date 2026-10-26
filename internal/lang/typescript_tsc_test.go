// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

// THE PROJECT'S TARGET AND LIB GOVERN THE COMPILE CHECK (ruling R27). The
// check used to pin target es2022 with es2022's default lib, so a project
// built for es2023 — REA, whose code calls Array.prototype.toSorted — failed
// on code the audit never touched (TS2550), at baseline, and the gate was
// switched off for the whole run: every authored test went un-type-checked.
// The project's own tsc resolves its tsconfig (extends included) with
// --showConfig, and only target and lib are carried over; file selection
// stays corral's, so the check is still scoped.
func TestTypeScriptCompileCheckUsesTheProjectsTargetAndLib(t *testing.T) {
	nodeModules := os.Getenv("CORRALAI_TYPESCRIPT_NODE_MODULES")
	globalTsc, _ := exec.LookPath("tsc")
	if nodeModules == "" && globalTsc == "" {
		t.Skip("no TypeScript compiler: set CORRALAI_TYPESCRIPT_NODE_MODULES or install tsc globally")
	}
	run := func(t *testing.T, files map[string]string) (string, error) {
		t.Helper()
		repo := t.TempDir()
		if nodeModules != "" {
			if err := os.Symlink(nodeModules, filepath.Join(repo, "node_modules")); err != nil {
				t.Fatal(err)
			}
		}
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		cmd := tsPlugin{}.CompileCheck("foo.ts", "foo.test.ts")[0]
		c := exec.Command(cmd[0], cmd[1:]...) // #nosec G204 -- the command under test
		c.Dir = repo
		path := filepath.Dir(mustLook(t, "node")) + ":/usr/bin:/bin"
		if globalTsc != "" {
			path = filepath.Dir(globalTsc) + ":" + path
		}
		c.Env = []string{"PATH=" + path, "HOME=" + t.TempDir()}
		out, err := c.CombinedOutput()
		return string(out), err
	}
	const code = "export function f(xs: number[]): number[] { return xs.toSorted((a, b) => a - b); }\n"
	const test = "import { f } from './foo.ts';\nexport const g: number[] = f([2, 1]);\n"

	es2023 := `{"compilerOptions":{"target":"es2023","lib":["es2023"]}}`
	if out, err := run(t, map[string]string{"foo.ts": code, "foo.test.ts": test, "tsconfig.json": es2023}); err != nil {
		t.Errorf("an es2023 project's toSorted was rejected; the check must use the project's target and lib: %v\n%s", err, out)
	}
	// Resolved through extends, the way real projects spread their options.
	if out, err := run(t, map[string]string{"foo.ts": code, "foo.test.ts": test, "base.json": es2023, "tsconfig.json": `{"extends":"./base.json"}`}); err != nil {
		t.Errorf("target and lib inherited through extends were not used: %v\n%s", err, out)
	}
	// Only target and lib are carried over: the project's file selection
	// never is, so an error elsewhere in the project cannot fail the check.
	if out, err := run(t, map[string]string{"foo.ts": code, "foo.test.ts": test, "tsconfig.json": es2023, "broken.ts": "export const x: number = 'no';\n"}); err != nil {
		t.Errorf("an error in a file outside the audit failed the scoped check: %v\n%s", err, out)
	}
	// With no tsconfig the scaffold's es2022 still applies, so the
	// es2023 method is a type error: the pin was what failed REA.
	if out, err := run(t, map[string]string{"foo.ts": code, "foo.test.ts": test}); err == nil || !strings.Contains(out, "toSorted") {
		t.Errorf("with no tsconfig the es2022 check must reject toSorted: %v\n%s", err, out)
	}
}

// What the check hands tsc, read back through a fake project compiler that
// answers --showConfig with a fixed resolution and prints the project file
// it is then run with — so these hold on any TypeScript version.
func TestTypeScriptCompileCheckCarriesOnlyTargetAndLib(t *testing.T) {
	cfg := func(t *testing.T, showConfig string, showRC int, tsconfig bool) (string, error) {
		t.Helper()
		repo := t.TempDir()
		writeExe(t, filepath.Join(repo, "node_modules", ".bin", "tsc"),
			"#!/bin/sh\nif [ \"$1\" = --showConfig ]; then printf '%s' '"+showConfig+"'; exit "+strconv.Itoa(showRC)+"; fi\ncat \"$2\"\n")
		if tsconfig {
			if err := os.WriteFile(filepath.Join(repo, "tsconfig.json"), []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		cmd := tsPlugin{}.CompileCheck("foo.ts", "foo.test.ts")[0]
		c := exec.Command(cmd[0], cmd[1:]...) // #nosec G204 -- the command under test
		c.Dir = repo
		c.Env = []string{"PATH=" + filepath.Dir(mustLook(t, "node")) + ":/usr/bin:/bin", "HOME=" + t.TempDir()}
		out, err := c.CombinedOutput()
		return string(out), err
	}
	out, err := cfg(t, `{"compilerOptions":{"target":"es2023","lib":["lib.es2023.d.ts","dom"],"rootDir":"src","composite":true,"paths":{"x":["y"]}}}`, 0, true)
	if err != nil {
		t.Fatalf("check failed: %v\n%s", err, out)
	}
	for _, want := range []string{`"target":"es2023"`, `"lib":["es2023","dom"]`, `"strict":true`, `"files":[`} {
		if !strings.Contains(out, want) {
			t.Errorf("want %s in the project file tsc ran with:\n%s", want, out)
		}
	}
	for _, never := range []string{"rootDir", "composite", "paths", "es2022"} {
		if strings.Contains(out, never) {
			t.Errorf("%s must not reach the scoped check:\n%s", never, out)
		}
	}
	// No target in the project: the project compiler's own default, not es2022.
	if out, err := cfg(t, `{"compilerOptions":{}}`, 0, true); err != nil || strings.Contains(out, "es2022") || strings.Contains(out, `"target"`) {
		t.Errorf("a project with no target must get its compiler's default, not corral's es2022: %v\n%s", err, out)
	}
	// No tsconfig at all (the single-file scaffold): es2022 as before.
	if out, err := cfg(t, `unused`, 0, false); err != nil || !strings.Contains(out, `"target":"es2022"`) {
		t.Errorf("with no tsconfig the scaffold's es2022 must stand: %v\n%s", err, out)
	}
	// A --showConfig that fails fails the check, saying so.
	if out, err := cfg(t, `error TS5083: Cannot read file`, 1, true); err == nil || !strings.Contains(out, "could not resolve tsconfig.json") {
		t.Errorf("a failing --showConfig must fail the check by name: %v\n%s", err, out)
	}
}
