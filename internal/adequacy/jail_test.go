// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

// TestJailAdapterBwrapWorkspaceStaysLockedDown pins the security-hardening
// (2026-07-20 review): the workspace is loosened to world-readable ONLY for the
// container backend, which needs it to read its own files under --cap-drop=ALL.
// The bwrap backend runs the jailed process as the SAME host uid, so it reads
// the Go-default locked-down 0700/0600 fine — and MUST stay locked down, or on
// a shared host it would gratuitously expose the operator's code-under-audit +
// tests to any other local user. This stat's the real bits inside a bwrap jail.
func TestJailAdapterBwrapWorkspaceStaysLockedDown(t *testing.T) {
	backend, err := sandbox.Resolve(sandbox.Config{})
	if err != nil || backend == nil {
		t.Skip("no sandbox backend available (bwrap) — needs the real jail to stat the workspace")
	}
	if backend.Name() != "bwrap" {
		t.Skipf("this test pins bwrap perms; resolved backend is %q", backend.Name())
	}
	j := NewJail(backend, 30*time.Second)
	// NOTE: RunTest joins testCmd's elements with a single space and hands the
	// result to sandbox.RunGuarded, which is itself wrapped in exactly one
	// "sh -c" by the isolator's Wrap — so a compound shell script must be a
	// SINGLE testCmd element, not pre-wrapped in its own "sh", "-c" pair.
	pass, err := j.RunTest(context.Background(), map[string]string{
		"sub/marker.txt": "hello",
	}, []string{`test "$(stat -c %a sub/marker.txt 2>/dev/null || stat -f %Lp sub/marker.txt)" = "600" && test "$(stat -c %a sub 2>/dev/null || stat -f %Lp sub)" = "700"`})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !pass {
		t.Fatalf("bwrap workspace must be locked down (file 0600, dir 0700) — world-readable perms are gratuitous exposure on the same-uid backend")
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

// captureIsolator is a fake sandbox.Isolator that records the Options it was
// given (via Wrap) and returns a harmless argv so sandbox.RunGuarded proceeds
// to actually exec — letting RunTest/Enumerate run for real while we inspect
// what Options they built (in particular, the resolved ReadOnlyBinds).
type captureIsolator struct {
	name   string
	onWrap func(sandbox.Options)
}

func (c *captureIsolator) Wrap(command string, opts sandbox.Options, env []string) ([]string, error) {
	if c.onWrap != nil {
		c.onWrap(opts)
	}
	return []string{"true"}, nil
}

func (c *captureIsolator) Preflight() error { return nil }
func (c *captureIsolator) Name() string {
	if c.name != "" {
		return c.name
	}
	return "capture"
}

// TestJailResolvesDepBindsToWorkspaceTarget pins the KEY design point of the
// bind-mount-deps feature: DepBind.Rel is repo-relative and is resolved to an
// ABSOLUTE sandbox.Bind.Target under the per-run temp workspace inside
// RunTest itself — never a static path — because only RunTest knows the
// per-run dir.
func TestJailResolvesDepBindsToWorkspaceTarget(t *testing.T) {
	// Host must be a REAL, non-symlink directory — resolveBinds lstat-refuses a
	// symlinked/missing bind source (the TOCTOU hardening).
	host := filepath.Join(t.TempDir(), "node_modules")
	if err := os.Mkdir(host, 0o755); err != nil {
		t.Fatal(err)
	}
	var got sandbox.Options
	fake := &captureIsolator{name: "bwrap", onWrap: func(o sandbox.Options) { got = o }}
	j := NewJail(fake, time.Second, WithReadOnlyBinds([]DepBind{{Host: host, Rel: "node_modules"}}))
	_, _ = j.RunTest(context.Background(), map[string]string{"a.js": "1"}, []string{"true"})
	if len(got.ReadOnlyBinds) != 1 {
		t.Fatalf("want 1 bind, got %d", len(got.ReadOnlyBinds))
	}
	b := got.ReadOnlyBinds[0]
	if b.Host != host {
		t.Fatalf("host = %q, want %q", b.Host, host)
	}
	// Target is the PER-RUN temp workspace joined with Rel — not a static path.
	if b.Target != got.Workspace+"/node_modules" {
		t.Fatalf("target = %q, want %q (per-run workspace + Rel)", b.Target, got.Workspace+"/node_modules")
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

// TestJailRefusesSymlinkedDepBind is the TOCTOU hardening: a dependency bind
// whose Host is a symlink (e.g. swapped in after the walk to point at a host
// secret) must be REFUSED at mount time, not bound. RunTest/Enumerate return an
// error rather than mounting the symlink's target read-only into the jail.
func TestJailRefusesSymlinkedDepBind(t *testing.T) {
	tmp := t.TempDir()
	real := filepath.Join(tmp, "realdir")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(tmp, "node_modules") // a symlink standing in for a dep dir
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	backend := &captureIsolator{name: "bwrap"}
	j := NewJail(backend, time.Second, WithReadOnlyBinds([]DepBind{{Host: link, Rel: "node_modules"}}))
	_, err := j.RunTest(context.Background(), map[string]string{"a.txt": "x"}, []string{"true"})
	if err == nil {
		t.Fatal("RunTest must refuse a symlinked dependency bind, got nil error")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("error should mention the symlink refusal, got: %v", err)
	}
}
