// SPDX-License-Identifier: Elastic-2.0

package adequacy_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/lang"
)

// TestWorkspaceRunnerRunsPythonCompileCheck reproduces the real pallets/flask
// audit failure: on the WORKSPACE substrate (the one the GitHub Action
// uses), WorkspaceRunner execs argv directly — cmdArgv[0] is never handed to
// a shell (see applyRunRestore: exec.CommandContext(ctx, cmdArgv[0],
// cmdArgv[1:]...)). pyPlugin.CompileCheck used to put the
// PYTHONPYCACHEPREFIX assignment in as a bare argv element
// ("PYTHONPYCACHEPREFIX=/tmp/corral-pyc"), which only ever worked because
// the JAIL substrate shell-joins argv and runs it under `sh -c` — a shell
// interprets a leading VAR=value token as an environment assignment;
// exec.Command does not, and instead tries (and fails) to exec a file
// literally named "PYTHONPYCACHEPREFIX=/tmp/corral-pyc".
//
// This test runs pyPlugin.CompileCheck's own command through a REAL
// WorkspaceRunner — no jail, no model, no network — over a temp dir holding
// a trivial, syntactically valid Python "source" + "test" file, exactly the
// shape JailValidator.CompileTest (internal/advpool/gate.go) builds. Before
// the fix this fails with "fork/exec PYTHONPYCACHEPREFIX=...: no such file
// or directory" — the exact defect from the pallets/flask audit. After the
// fix it must pass.
func TestWorkspaceRunnerRunsPythonCompileCheck(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		if _, err2 := exec.LookPath("python"); err2 != nil {
			t.Skip("no python3/python on PATH on this host — cannot verify the workspace-substrate compile check")
		}
	}

	p, ok := lang.ByName("python")
	if !ok {
		t.Fatal("python plugin not registered")
	}

	const codePath = "pricing.py"
	const testPath = "test_pricing.py"
	files := map[string]string{
		codePath: "def add(a, b):\n    return a + b\n",
		testPath: "from pricing import add\n\ndef test_add():\n    assert add(1, 2) == 3\n",
	}

	root := t.TempDir()
	w := adequacy.NewWorkspaceRunner(root, 0)

	seq := p.CompileCheck(codePath, testPath)
	if len(seq) != 1 {
		t.Fatalf("python CompileCheck sequence = %v, want exactly 1 command (py_compile takes both files in one invocation)", seq)
	}
	pass, out, err := w.RunTestVerbose(context.Background(), files, seq[0])
	if err != nil {
		t.Fatalf("RunTestVerbose: %v (output: %s)", err, out)
	}
	if !pass {
		t.Fatalf("python compile-check did not pass on the workspace substrate — cmd=%v output=%s", seq[0], out)
	}
	if strings.Contains(out, "no such file or directory") {
		t.Fatalf("compile-check argv[0] was execed literally instead of being applied as an env var: %s", out)
	}
}

// runCompileCheckSequence replicates advpool.JailValidator.CompileTest's own
// sequencing discipline (internal/advpool/gate.go): run each command in
// order over a REAL WorkspaceRunner, stop at the first non-pass, and surface
// combined output. This is deliberately NOT a call into the advpool package
// (that would need a full JailValidator + advpool.Jail wiring); it exercises
// the same two facts CompileTest itself relies on — WorkspaceRunner.
// RunTestVerbose runs ONE command with no shell, and CompileCheck's sequence
// is meant to be chained by "stop at first failure" — asserted directly
// against the real adequacy.WorkspaceRunner so this test proves the
// workspace substrate can actually execute what CompileCheck returns, not a
// mock standing in for it.
func runCompileCheckSequence(t *testing.T, w *adequacy.WorkspaceRunner, files map[string]string, seq [][]string) (pass bool, output string) {
	t.Helper()
	var out strings.Builder
	for _, cmd := range seq {
		ok, o, err := w.RunTestVerbose(context.Background(), files, cmd)
		if err != nil {
			t.Fatalf("RunTestVerbose(%v): %v (output so far: %s)", cmd, err, out.String())
		}
		out.WriteString(o)
		if !ok {
			return false, out.String()
		}
	}
	return true, out.String()
}

// TestWorkspaceRunnerRunsRubyCompileCheck is the Ruby sibling of the Python
// test above, for the SAME class of bug: rubyPlugin.CompileCheck used to
// return a single argv element sequence containing a literal "&&" token
// (["ruby","-c",code,"&&","ruby","-c",test]) — that is only meaningful to a
// shell. The jail substrate shell-joins and runs it under `sh -c`, where
// `&&` really does mean "run the second only if the first succeeded"; the
// workspace substrate execs argv directly, where the whole slice becomes
// ONE `ruby -c` invocation whose script argument is codePath — `ruby -c`
// syntax-checks only that first script argument and treats everything after
// it (including the literal tokens "&&", "ruby", "-c", and testPath itself)
// as the checked script's own ARGV, never as more files to check. Measured
// directly below: this means testPath's syntax is NEVER actually checked —
// a silent false pass, not a crash, and the more dangerous failure mode
// (a broken test file compile-checks clean). rubyPlugin.CompileCheck now
// returns a TWO-command sequence instead (one file per invocation, run in
// order) — this test proves both halves: that the OLD shape genuinely,
// silently ignores testPath on this substrate (reproduced directly, not
// asserted from memory), and that the plugin's actual (fixed) CompileCheck
// output genuinely catches a broken test file and passes a valid one.
func TestWorkspaceRunnerRunsRubyCompileCheck(t *testing.T) {
	if _, err := exec.LookPath("ruby"); err != nil {
		t.Skip("no ruby on PATH on this host — cannot verify the workspace-substrate compile check")
	}

	p, ok := lang.ByName("ruby")
	if !ok {
		t.Fatal("ruby plugin not registered")
	}

	const codePath = "pricing.rb"
	const testPath = "pricing_test.rb"
	validFiles := map[string]string{
		codePath: "def add(a, b)\n  a + b\nend\n",
		testPath: "require 'minitest/autorun'\nrequire_relative 'pricing'\n\nclass PricingTest < Minitest::Test\n  def test_add\n    assert_equal 3, add(1, 2)\n  end\nend\n",
	}
	root := t.TempDir()
	w := adequacy.NewWorkspaceRunner(root, 0)

	// Reproduce the OLD shape directly against a BROKEN test file: this is
	// exactly what rubyPlugin.CompileCheck used to return before this fix,
	// as a single `&&`-joined argv element. Confirms the false-pass is real
	// on this substrate, not merely asserted.
	brokenFiles := map[string]string{
		codePath: validFiles[codePath],
		testPath: "def broken(\n  this is not valid ruby",
	}
	oldShapeCmd := []string{"ruby", "-c", codePath, "&&", "ruby", "-c", testPath}
	falsePass, out, err := w.RunTestVerbose(context.Background(), brokenFiles, oldShapeCmd)
	if err != nil {
		t.Fatalf("RunTestVerbose(old shape, broken test file): %v (output: %s)", err, out)
	}
	if !falsePass {
		t.Fatalf("expected the OLD &&-joined shape to FALSELY pass a broken test file on this host (`ruby -c` only checks its first script argument under direct exec) — got a genuine failure instead (cmd=%v output=%s); this reproduction may be stale", oldShapeCmd, out)
	}

	// The plugin's real (fixed) sequence must genuinely catch the broken
	// test file...
	brokenSeq := p.CompileCheck(codePath, testPath)
	if len(brokenSeq) != 2 {
		t.Fatalf("ruby CompileCheck sequence = %v, want exactly 2 commands (ruby -c checks one file per invocation)", brokenSeq)
	}
	fixedPass, _ := runCompileCheckSequence(t, w, brokenFiles, brokenSeq)
	if fixedPass {
		t.Fatalf("ruby compile-check sequence FALSELY passed a syntactically invalid test file — seq=%v", brokenSeq)
	}

	// ...and genuinely pass on valid files.
	validSeq := p.CompileCheck(codePath, testPath)
	seqPass, seqOut := runCompileCheckSequence(t, w, validFiles, validSeq)
	if !seqPass {
		t.Fatalf("ruby compile-check sequence did not pass on the workspace substrate — seq=%v output=%s", validSeq, seqOut)
	}
}

// TestWorkspaceRunnerRunsJavaScriptCompileCheck is the JavaScript sibling —
// see TestWorkspaceRunnerRunsRubyCompileCheck's doc comment for the shared
// rationale. jsPlugin.CompileCheck used to return
// ["node","--check",code,"&&","node","--check",test] as one argv element;
// `node --check` only ever looks at its FIRST file argument, so under a
// direct exec this would silently syntax-check `code` and ignore
// "&&"/test.js entirely rather than failing outright with a clear "no such
// file" error the way ruby/py_compile do — a quieter, more dangerous
// failure mode (a broken test file could pass compile-check unnoticed).
// This test proves the NEW two-command sequence actually checks both files
// on the real workspace substrate.
func TestWorkspaceRunnerRunsJavaScriptCompileCheck(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("no node on PATH on this host — cannot verify the workspace-substrate compile check")
	}

	p, ok := lang.ByName("javascript")
	if !ok {
		t.Fatal("javascript plugin not registered")
	}

	const codePath = "pricing.js"
	const testPath = "pricing.test.js"
	validFiles := map[string]string{
		codePath: "function add(a, b) {\n  return a + b;\n}\nmodule.exports = { add };\n",
		testPath: "const { test } = require('node:test');\nconst assert = require('node:assert');\nconst { add } = require('./pricing.js');\n\ntest('add', () => {\n  assert.strictEqual(add(1, 2), 3);\n});\n",
	}
	root := t.TempDir()
	w := adequacy.NewWorkspaceRunner(root, 0)

	// The old `&&`-joined shape, run against files where the TEST file is
	// syntactically INVALID: `node --check code.js && node --check
	// broken.js` as a single argv element under a direct exec becomes `node
	// --check code.js '&&' 'node' '--check' 'broken.js'` — node --check
	// looks at argv[1] only ("code.js") and ignores the rest, so this
	// WRONGLY passes even though the test file is broken. This is the
	// silent-failure variant of the bug: not a crash, a false pass.
	brokenFiles := map[string]string{
		codePath: validFiles[codePath],
		testPath: "this is not valid javascript {{{",
	}
	oldShapeCmd := []string{"node", "--check", codePath, "&&", "node", "--check", testPath}
	falsePass, out, err := w.RunTestVerbose(context.Background(), brokenFiles, oldShapeCmd)
	if err != nil {
		t.Fatalf("RunTestVerbose(old shape, broken test file): %v (output: %s)", err, out)
	}
	if !falsePass {
		t.Fatalf("expected the OLD &&-joined shape to FALSELY pass a broken test file on this host (node --check only reads argv[1] under direct exec) — got a genuine failure instead (cmd=%v output=%s); this reproduction may be stale", oldShapeCmd, out)
	}

	// The plugin's real (fixed) two-command sequence must genuinely catch
	// the broken test file.
	brokenSeq := p.CompileCheck(codePath, testPath)
	if len(brokenSeq) != 2 {
		t.Fatalf("javascript CompileCheck sequence = %v, want exactly 2 commands (node --check checks one file per invocation)", brokenSeq)
	}
	fixedPass, _ := runCompileCheckSequence(t, w, brokenFiles, brokenSeq)
	if fixedPass {
		t.Fatalf("javascript compile-check sequence FALSELY passed a syntactically invalid test file — seq=%v", brokenSeq)
	}

	// And it must genuinely pass on valid files.
	validSeq := p.CompileCheck(codePath, testPath)
	validPass, validOut := runCompileCheckSequence(t, w, validFiles, validSeq)
	if !validPass {
		t.Fatalf("javascript compile-check sequence did not pass on valid files — seq=%v output=%s", validSeq, validOut)
	}
}
