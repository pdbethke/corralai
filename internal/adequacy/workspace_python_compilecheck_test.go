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
// or directory" (or the exec.Error string form on Go >=1.19, "no such file
// or directory" wrapped in *exec.Error) — the exact defect from the
// pallets/flask audit. After the fix it must pass.
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

	cmd := p.CompileCheck(codePath, testPath)
	pass, out, err := w.RunTestVerbose(context.Background(), files, cmd)
	if err != nil {
		t.Fatalf("RunTestVerbose: %v (output: %s)", err, out)
	}
	if !pass {
		t.Fatalf("python compile-check did not pass on the workspace substrate — cmd=%v output=%s", cmd, out)
	}
	if strings.Contains(out, "no such file or directory") {
		t.Fatalf("compile-check argv[0] was execed literally instead of being applied as an env var: %s", out)
	}
}
