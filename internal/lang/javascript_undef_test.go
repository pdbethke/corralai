// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeBinOnPath(t *testing.T, name string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}

// TestJSGateAddsUndefCheckWhenProjectDeclaresItsEnv pins the gap.
//
// `node --check` validates SYNTAX only, so a mutant calling a function that
// does not exist passes the gate, reaches GRADING, fails the suite for the
// wrong reason, and is scored as KILLED — the same defect family as a
// compiler-rejected or timed-out mutant being counted as caught.
//
// The check is gated on the PROJECT having its own lint config, deliberately.
// no-undef needs to know which globals exist (`require` and `module` in
// CommonJS, `window` in a browser, and so on). Guessing that wrong rejects
// VALID mutants and silently shrinks the measurement, so corral uses the
// project's own declaration or does not run the check at all.
func TestJSGateAddsUndefCheckWhenProjectDeclaresItsEnv(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".oxlintrc.json"), []byte(`{"env":{"node":true}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	fakeBinOnPath(t, "oxlint")

	p, _ := ByName("javascript")
	cmds := p.CompileCheck("m.js", "t.js")

	var sawNodeCheck, sawUndef bool
	for _, c := range cmds {
		j := strings.Join(c, " ")
		if strings.Contains(j, "--check") {
			sawNodeCheck = true
		}
		if strings.Contains(j, "oxlint") {
			sawUndef = true
			if !strings.Contains(j, "no-undef") {
				t.Errorf("lint command does not deny no-undef: %q", j)
			}
		}
	}
	if !sawNodeCheck {
		t.Error("node --check must remain: it is the syntax gate")
	}
	if !sawUndef {
		t.Fatalf("oxlint on PATH and a project config present, but no undef check: %v", cmds)
	}
}

// No project config = no guessing. The gate stays exactly the syntax check.
func TestJSGateSkipsUndefCheckWithoutProjectConfig(t *testing.T) {
	chdir(t, t.TempDir()) // no lint config
	fakeBinOnPath(t, "oxlint")

	p, _ := ByName("javascript")
	for _, c := range p.CompileCheck("m.js", "t.js") {
		if strings.Contains(strings.Join(c, " "), "oxlint") {
			t.Fatalf("ran a no-undef check without the project declaring its env: %v", c)
		}
	}
}

// No linter = unchanged, and never an empty sequence.
func TestJSGateWithoutLinterIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".oxlintrc.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	t.Setenv("PATH", t.TempDir())

	p, _ := ByName("javascript")
	cmds := p.CompileCheck("m.js", "t.js")
	if len(cmds) == 0 {
		t.Fatal("empty check sequence is never valid")
	}
	for _, c := range cmds {
		if strings.Contains(strings.Join(c, " "), "oxlint") {
			t.Fatalf("linter absent from PATH but present in the gate: %v", c)
		}
	}
}
