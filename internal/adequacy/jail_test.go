// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/sandbox"
)

func TestJailAdapterExitMapping(t *testing.T) {
	backend, err := sandbox.Resolve(sandbox.Config{})
	if err != nil || backend == nil {
		t.Skip("no sandbox backend available (bwrap) — adapter exit-mapping needs the real jail")
	}
	j := NewJail(backend, 30*time.Second)
	// a command that exits 0 => passed; one that exits nonzero => not passed.
	pass, err := j.RunTest(context.Background(), map[string]string{"marker.txt": "x"}, []string{"true"})
	if err != nil || !pass {
		t.Fatalf("exit-0 command: passed=%v err=%v, want passed=true", pass, err)
	}
	fail, err := j.RunTest(context.Background(), map[string]string{"marker.txt": "x"}, []string{"false"})
	if err != nil || fail {
		t.Fatalf("exit-1 command: passed=%v err=%v, want passed=false", fail, err)
	}
}

func TestJailAdapterNilBackendErrors(t *testing.T) {
	j := NewJail(nil, time.Second)
	if _, err := j.RunTest(context.Background(), map[string]string{}, []string{"true"}); err == nil {
		t.Fatal("nil backend must error, never run unsandboxed")
	}
}

func TestJailAdapterWritesFilesIntoWorkspace(t *testing.T) {
	backend, err := sandbox.Resolve(sandbox.Config{})
	if err != nil || backend == nil {
		t.Skip("no sandbox backend available (bwrap) — needs the real jail to exercise workspace writes")
	}
	j := NewJail(backend, 30*time.Second)
	// A nested path key must have its parent dir created, and the file content
	// must actually be readable by the command run in the jail.
	pass, err := j.RunTest(context.Background(), map[string]string{
		"sub/dir/marker.txt": "hello",
	}, []string{"cat", "sub/dir/marker.txt"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !pass {
		t.Fatalf("expected pass=true reading a written nested file, got false")
	}
}

// TestJailAdapterWorkspaceFilesAreWorldReadable is the cheap unit-level
// complement to TestJailAdapterContainerBackendCanReadOwnWorkspace below: it
// locks in the actual perm bits RunTest writes (0644 files, 0755 dirs)
// rather than the mechanism used to read them. It runs through the bwrap
// backend (same-uid, so this alone would never have caught the container
// bug) purely to get a real `stat` inside the jail's own workspace.
func TestJailAdapterWorkspaceFilesAreWorldReadable(t *testing.T) {
	backend, err := sandbox.Resolve(sandbox.Config{})
	if err != nil || backend == nil {
		t.Skip("no sandbox backend available (bwrap) — needs the real jail to stat the workspace")
	}
	j := NewJail(backend, 30*time.Second)
	// NOTE: RunTest joins testCmd's elements with a single space and hands the
	// result to sandbox.RunGuarded, which is itself wrapped in exactly one
	// "sh -c" by the isolator's Wrap — so a compound shell script must be a
	// SINGLE testCmd element, not pre-wrapped in its own "sh", "-c" pair
	// (that would double-wrap and mangle the parsing).
	pass, err := j.RunTest(context.Background(), map[string]string{
		"sub/marker.txt": "hello",
	}, []string{`test "$(stat -c %a sub/marker.txt 2>/dev/null || stat -f %Lp sub/marker.txt)" = "644" && test "$(stat -c %a sub 2>/dev/null || stat -f %Lp sub)" = "755"`})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !pass {
		t.Fatalf("expected the written file/dir to be 0644/0755 (world-readable), got a mismatch")
	}
}

// TestJailAdapterContainerBackendCanReadOwnWorkspace is the integration test
// that would have CAUGHT the real bug: container.go always runs with
// --cap-drop=ALL, which strips CAP_DAC_OVERRIDE, and standard language
// images default to a container-root uid that differs from the host uid
// owning the bind-mounted workspace. Before the perm fix in RunTest, a
// trivial `cat` of a file this very code wrote failed with a permission
// error inside the container, and every --jail container audit vacuously
// "passed" grading because the compliant baseline itself never ran. This
// test proves the workspace is actually readable under the real
// --cap-drop=ALL container invocation, not just that some mechanism was
// chosen.
//
// It skips cleanly (never fails) when docker/podman or CORRALAI_EXEC_IMAGE
// aren't available — e.g. via:
//
//	docker build -t corral-test-py -f - . <<<'FROM python:3.12-slim
//	RUN pip install --no-cache-dir pytest'
//	CORRALAI_EXEC_IMAGE=corral-test-py go test ./internal/adequacy/... -run Container
func TestJailAdapterContainerBackendCanReadOwnWorkspace(t *testing.T) {
	if os.Getenv("CORRALAI_EXEC_IMAGE") == "" {
		t.Skip("CORRALAI_EXEC_IMAGE not set — container integration test needs a real image (see docker build recipe in this test's doc comment)")
	}
	backend, err := sandbox.Resolve(sandbox.Config{Backend: "container"})
	if err != nil || backend == nil {
		t.Skipf("container backend unavailable: %v", err)
	}
	j := NewJail(backend, 60*time.Second)
	pass, err := j.RunTest(context.Background(), map[string]string{
		"sub/marker.txt": "hello",
	}, []string{"cat", "sub/marker.txt"})
	if err != nil {
		t.Fatalf("unexpected error running the container backend: %v", err)
	}
	if !pass {
		t.Fatalf("container could not read its own bind-mounted workspace under --cap-drop=ALL — this is exactly the cap-drop/uid-mismatch bug this test guards against")
	}
}

// TestJailAdapterContainerBackendCanCompilePythonInWorkspace guards the bug
// where a SYNTACTICALLY-VALID authored test was falsely rejected as "does not
// compile" on the container backend: `python -m py_compile` writes a .pyc into
// __pycache__ NEXT TO each source, but the container's (different-uid) root
// cannot write the jail-read-only workspace, so the write EACCES'd and the
// whole test-writer role was silently defeated. The fix redirects bytecode to
// the sandbox's writable /tmp via the python plugin's PYTHONPYCACHEPREFIX
// token; this test runs the REAL python CompileCheck through the REAL container
// jail on valid files and asserts it passes. It fails (EACCES) without the fix.
func TestJailAdapterContainerBackendCanCompilePythonInWorkspace(t *testing.T) {
	if os.Getenv("CORRALAI_EXEC_IMAGE") == "" {
		t.Skip("CORRALAI_EXEC_IMAGE not set — container integration test needs a real python image (see the sibling container test's doc comment)")
	}
	backend, err := sandbox.Resolve(sandbox.Config{Backend: "container"})
	if err != nil || backend == nil {
		t.Skipf("container backend unavailable: %v", err)
	}
	p, ok := lang.ByName("python")
	if !ok {
		t.Fatal("python plugin not registered")
	}
	code := "def take(n, it):\n    return list(it)[:n]\n"
	test := "import recipes\n\ndef test_take():\n    assert recipes.take(2, range(9)) == [0, 1]\n"
	j := NewJail(backend, 60*time.Second)
	// The workspace dir the real jail creates is world-readable-but-not-writable
	// to the container's uid — exactly the condition that made py_compile EACCES.
	pass, err := j.RunTest(context.Background(),
		map[string]string{"recipes.py": code, "test_recipes.py": test},
		p.CompileCheck("recipes.py", "test_recipes.py"))
	if err != nil {
		t.Fatalf("unexpected error running the python compile check in the container: %v", err)
	}
	if !pass {
		t.Fatalf("py_compile falsely rejected a valid test in the container jail — the __pycache__ write EACCES bug this test guards against")
	}
}

func TestJailAdapterTimeoutNeverReadsAsPassed(t *testing.T) {
	backend, err := sandbox.Resolve(sandbox.Config{})
	if err != nil || backend == nil {
		t.Skip("no sandbox backend available (bwrap) — needs the real jail to exercise a timeout")
	}
	j := NewJail(backend, 200*time.Millisecond)
	pass, err := j.RunTest(context.Background(), map[string]string{"marker.txt": "x"}, []string{"sleep", "5"})
	if pass {
		t.Fatalf("a timed-out run must never read as passed")
	}
	if err == nil {
		t.Fatalf("a timed-out run should surface an error")
	}
}
