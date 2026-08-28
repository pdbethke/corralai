// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPythonCompileCheckAddsRuffWhenAvailable pins the gap ruff closes.
//
// py_compile validates SYNTAX and nothing else, so a mutant that calls a
// function which does not exist compiles clean and reaches GRADING — where it
// fails the suite and is scored as KILLED. The tests are credited with catching
// a mutant that was never valid code: the same defect family as scoring a
// compiler-rejected mutant as caught.
//
// Go's gate (go vet) rejects exactly this. ruff's F821 is its analogue, and it
// costs ~11ms.
func TestPythonCompileCheckAddsRuffWhenAvailable(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "ruff")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	p, _ := ByName("python")
	cmds := p.CompileCheck("m.py", "t.py")

	var sawPyCompile, sawRuff bool
	for _, c := range cmds {
		joined := strings.Join(c, " ")
		if strings.Contains(joined, "py_compile") {
			sawPyCompile = true
		}
		if strings.Contains(joined, "ruff") {
			sawRuff = true
			if !strings.Contains(joined, "F821") {
				t.Errorf("ruff command does not select F821 (undefined name): %q", joined)
			}
		}
	}
	if !sawPyCompile {
		t.Error("py_compile must remain: ruff removed E999, so it is not a syntax gate")
	}
	if !sawRuff {
		t.Fatalf("ruff on PATH but not in the gate; commands = %v", cmds)
	}
}

// With no ruff on PATH the gate must still be exactly the syntax check. A
// missing optional tool must never mark every mutant invalid.
func TestPythonCompileCheckWithoutRuffIsUnchanged(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // nothing on PATH

	p, _ := ByName("python")
	cmds := p.CompileCheck("m.py", "t.py")
	if len(cmds) == 0 {
		t.Fatal("empty check sequence is never valid")
	}
	for _, c := range cmds {
		if strings.Contains(strings.Join(c, " "), "ruff") {
			t.Fatalf("ruff absent from PATH but present in the gate: %v", c)
		}
	}
}
