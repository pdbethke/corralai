// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	// NOTE: testCmd is an ARGV — each element is quoted literally by shellJoin
	// before the isolator's single "sh -c" wrap — so a compound shell script
	// must be passed as an explicit []string{"sh", "-c", script}, not as one
	// bare element (which would be run as a literal program name).
	pass, err := j.RunTest(context.Background(), map[string]string{
		"sub/marker.txt": "hello",
	}, []string{"sh", "-c", `test "$(stat -c %a sub/marker.txt 2>/dev/null || stat -f %Lp sub/marker.txt)" = "600" && test "$(stat -c %a sub 2>/dev/null || stat -f %Lp sub)" = "700"`})
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
	vj, ok := j.(VerboseJail)
	if !ok {
		t.Fatal("the real jail must satisfy VerboseJail — without the run's own output a failure here costs a CI round trip per hypothesis")
	}
	// RunTestVerbose, not RunTest: this backend runs where nobody can log in,
	// and a failure that reports only "it did not pass" costs a CI round trip
	// per hypothesis. It cost three. The runtime's own stderr is the evidence.
	pass, out, err := vj.RunTestVerbose(context.Background(), map[string]string{
		"sub/marker.txt": "hello",
	}, []string{"cat", "sub/marker.txt"})
	if err != nil {
		t.Fatalf("unexpected error running the container backend: %v\noutput:\n%s", err, out)
	}
	if !pass {
		t.Fatalf("container could not read its own bind-mounted workspace under --cap-drop=ALL — this is exactly the cap-drop/uid-mismatch bug this test guards against.\nCONTAINER OUTPUT:\n%s", out)
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
	vj, ok := j.(VerboseJail)
	if !ok {
		t.Fatal("the real jail must satisfy VerboseJail — see the sibling container test")
	}
	// The workspace dir the real jail creates is world-readable-but-not-writable
	// to the container's uid — exactly the condition that made py_compile EACCES.
	// python's CompileCheck sequence is always exactly one command (py_compile
	// takes multiple files in a single invocation) — see lang.Plugin.CompileCheck.
	pass, out, err := vj.RunTestVerbose(context.Background(),
		map[string]string{"recipes.py": code, "test_recipes.py": test},
		p.CompileCheck("recipes.py", "test_recipes.py")[0])
	if err != nil {
		t.Fatalf("unexpected error running the python compile check in the container: %v\noutput:\n%s", err, out)
	}
	if !pass {
		t.Fatalf("py_compile falsely rejected a valid test in the container jail — the __pycache__ write EACCES bug this test guards against.\nCONTAINER OUTPUT:\n%s", out)
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

// TestJailWithMaxOutputSetsSandboxOption pins that WithMaxOutput actually
// reaches sandbox.Options.MaxOutput on every run this jail performs — the
// seam the coverage pre-flight needs to raise the 16 KiB default that would
// otherwise head-truncate a real `coverage json` report (467 KB, measured
// against pallets/flask) before ParseCoverage ever sees it. Uses the fake
// isolator (no real bwrap needed) so this is checkable on a bare host.
func TestJailWithMaxOutputSetsSandboxOption(t *testing.T) {
	var got sandbox.Options
	fake := &captureIsolator{name: "bwrap", onWrap: func(o sandbox.Options) { got = o }}
	j := NewJail(fake, time.Second, WithMaxOutput(8<<20))
	_, _ = j.RunTest(context.Background(), map[string]string{"a.go": "1"}, []string{"true"})
	if got.MaxOutput != 8<<20 {
		t.Fatalf("MaxOutput = %d, want %d (WithMaxOutput must reach sandbox.Options)", got.MaxOutput, 8<<20)
	}
}

// TestJailWithoutMaxOutputLeavesSandboxDefault pins the complement: a jail
// built WITHOUT WithMaxOutput passes MaxOutput == 0 into sandbox.Options,
// which sandbox.Run itself then defaults to its own 16 KiB (see
// sandbox.go's `if opts.MaxOutput <= 0 { opts.MaxOutput = 16 << 10 }`,
// applied BEFORE the backend even sees Options) — so every other caller
// (RunTest/RunTestVerbose scoring mutants) keeps that stock default rather
// than inheriting a raised cap it never asked for.
func TestJailWithoutMaxOutputLeavesSandboxDefault(t *testing.T) {
	var got sandbox.Options
	fake := &captureIsolator{name: "bwrap", onWrap: func(o sandbox.Options) { got = o }}
	j := NewJail(fake, time.Second)
	_, _ = j.RunTest(context.Background(), map[string]string{"a.go": "1"}, []string{"true"})
	const sandboxDefault = 16 << 10
	if got.MaxOutput != sandboxDefault {
		t.Fatalf("MaxOutput = %d, want %d (sandbox.Run's own default) when WithMaxOutput is not used", got.MaxOutput, sandboxDefault)
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

// TestShellJoinQuotesMetacharacters pins Bug C's fix: a test command with shell
// metacharacters survives the jail's `sh -c` as literal argv, not re-parsed into
// pipes/subshells (the failure that fail-closed a real self-audit).
func TestShellJoinQuotesMetacharacters(t *testing.T) {
	cmd := []string{"go", "test", "-run", "^TestScore$|^TestScore(Invalid|Mutant)", "./pkg/"}
	want := `'go' 'test' '-run' '^TestScore$|^TestScore(Invalid|Mutant)' './pkg/'`
	if got := shellJoin(cmd); got != want {
		t.Fatalf("shellJoin mangled metacharacters:\n got: %s\nwant: %s", got, want)
	}
	if q := shellQuote("a'b"); q != `'a'\''b'` {
		t.Fatalf("shellQuote embedded quote: got %s", q)
	}
}

// TestEnvWithDepBinPaths pins the fix that lets the test-writer's own compile
// check run at all. The host's PATH is meaningless inside the jail: a developer
// resolves `tsc` or `vitest` through the repo's node_modules/.bin, and that
// absolute host path does not exist in a fresh temp workspace. Without the
// jail-side .bin on PATH, `tsc --noEmit` fails "not found", no authored test
// ever compiles, and every survivor is reported unproven — while the run still
// grades and looks fine.
func TestEnvWithDepBinPaths(t *testing.T) {
	got := envWithDepBinPaths(
		[]string{"PATH=/usr/bin", "HOME=/home/agent"},
		[]sandbox.Bind{{Target: "/ws/node_modules"}},
	)
	var path string
	for _, kv := range got {
		if strings.HasPrefix(kv, "PATH=") {
			path = strings.TrimPrefix(kv, "PATH=")
		}
	}
	if !strings.HasPrefix(path, "/ws/node_modules/.bin:") {
		t.Fatalf("dep .bin must come FIRST on PATH, got %q", path)
	}
	if !strings.Contains(path, "/usr/bin") {
		t.Fatalf("the existing PATH must be preserved, got %q", path)
	}
}

// TestEnvWithDepBinPathsIgnoresNonDepBinds: only a real dependency directory
// contributes a PATH entry. A vendor/ or .venv bind has no .bin convention, and
// inventing PATH entries would make host tooling reachable that the binds did
// not already make reachable.
func TestEnvWithDepBinPathsIgnoresNonDepBinds(t *testing.T) {
	in := []string{"PATH=/usr/bin"}
	got := envWithDepBinPaths(in, []sandbox.Bind{{Target: "/ws/vendor"}})
	if len(got) != 1 || got[0] != "PATH=/usr/bin" {
		t.Fatalf("PATH must be untouched with no node_modules bind, got %v", got)
	}
}

// TestShellSplitRoundTripsShellJoin is the property that matters: an argv
// flattened into RunSpec.TestCmd must come back out unchanged.
//
// A plain strings.Join + strings.Fields is NOT reversible, and the failure is
// silent. A Ruby one-liner passed as a single -e argument came back cut at its
// first comma and was reported as the project's own syntax error.
func TestShellSplitRoundTripsShellJoin(t *testing.T) {
	for _, argv := range [][]string{
		{"go", "test", "./..."},
		{"rspec", "spec/chunky_png/color_spec.rb"},
		{"node", "./node_modules/mocha/bin/mocha.js", "--reporter", "dot", "test*.js"},
		{"go", "test", "-run", "^Foo$|^Bar"},
		{"ruby", "-e", `Dir["test/*.rb"].each { |f| require File.expand_path(f) }`},
		{"pytest", "-k", "not slow and not network"},
		{"npx", "jest", "--testPathPattern=src/a b/c"},
	} {
		got := ShellSplit(ShellJoin(argv))
		if len(got) != len(argv) {
			t.Fatalf("argv %q: round-trip changed arity: %q", argv, got)
		}
		for i := range argv {
			if got[i] != argv[i] {
				t.Fatalf("argv %q: element %d became %q", argv, i, got[i])
			}
		}
	}
}

// TestShellSplitMatchesFieldsOnSimpleCommands: every command already stored
// without quoting must parse exactly as strings.Fields parsed it, or this
// change would silently alter existing runs.
func TestShellSplitMatchesFieldsOnSimpleCommands(t *testing.T) {
	for _, s := range []string{"go test ./...", "  pytest   -q  ", "npm test", ""} {
		got, want := ShellSplit(s), strings.Fields(s)
		if len(got) != len(want) {
			t.Fatalf("%q: got %q want %q", s, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%q: element %d got %q want %q", s, i, got[i], want[i])
			}
		}
	}
}

// TestShellSplitUnterminatedQuoteDoesNotLoseInput: a malformed command must not
// silently drop its tail. Running it and reporting the runner's own complaint
// beats a parse error from us.
func TestShellSplitUnterminatedQuoteDoesNotLoseInput(t *testing.T) {
	got := ShellSplit(`ruby -e 'puts 1`)
	if len(got) != 3 || got[2] != "puts 1" {
		t.Fatalf("unterminated quote lost input: %q", got)
	}
}

// overlayJailOrSkip is the real bwrap backend, skipped (and so printed by
// the CI census) when bwrap is unusable or cannot overlay: a bwrap older
// than 0.10 has no --tmp-overlay, and Seed refuses it with ErrNoOverlay.
func overlayJailOrSkip(t *testing.T) sandbox.Isolator {
	t.Helper()
	backend, err := sandbox.Resolve(sandbox.Config{})
	if err != nil || backend == nil {
		t.Skipf("no sandbox backend available (bwrap): %v", err)
	}
	if !sandbox.Overlays(backend) {
		t.Skipf("%s cannot overlay; overlay path unexercised", backendVersion(backend))
	}
	return backend
}

// overlayDirOrSkip skips unless backend overlays a dependency directory on
// dir's filesystem (sandbox.OverlaysDir).
func overlayDirOrSkip(t *testing.T, backend sandbox.Isolator, dir string) {
	t.Helper()
	if !sandbox.OverlaysDir(backend, dir) {
		t.Skipf("%s cannot overlay a dependency directory on the filesystem of %s; dependency overlay path unexercised", backendVersion(backend), dir)
	}
}

// backendVersion names the backend for a skip message: bwrap's own
// --version line when it is bwrap.
func backendVersion(backend sandbox.Isolator) string {
	if backend.Name() == "bwrap" {
		if out, err := exec.Command("bwrap", "--version").Output(); err == nil {
			return strings.TrimSpace(string(out))
		}
	}
	return backend.Name()
}

func seedOrFatal(t *testing.T, e Enumerator, files map[string]string) SeededJail {
	t.Helper()
	s, err := e.(bwrapJail).Seed(files, nil)
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// treeDigest is a listing of every path under root with each file's sha256,
// so a before/after comparison catches an added, removed or changed file.
func treeDigest(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() {
			fmt.Fprintf(&b, "%s/\n", rel)
			return nil
		}
		data, err := os.ReadFile(p) // #nosec G304 -- the test's own temp tree
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "%s %x\n", rel, sha256.Sum256(data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// Two runs of one seed see the same tree.
func TestSeededJailRunsSeeIdenticalTree(t *testing.T) {
	e := NewEnumerator(overlayJailOrSkip(t), 30*time.Second)
	files := map[string]string{"a.txt": "x"}
	s := seedOrFatal(t, e, files)
	for i := range 2 {
		out, err := s.Enumerate(context.Background(), files, []string{"cat", "a.txt"})
		if err != nil || out != "x" {
			t.Errorf("run %d: out=%q err=%v, want \"x\"", i+1, out, err)
		}
	}
}

// What one run writes — into the seed or into a dependency directory — no
// later run sees, and neither the seed nor the host's dependency directory
// changes. This is the guarantee a fresh copy per run gave, now given by
// the kernel: each run's writes land in its own tmpfs upper layer.
func TestSeededJailWritesDoNotLeak(t *testing.T) {
	backend := overlayJailOrSkip(t)
	nm := filepath.Join(t.TempDir(), "node_modules")
	if err := os.MkdirAll(filepath.Join(nm, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nm, "pkg", "index.js"), []byte("dep"), 0o600); err != nil {
		t.Fatal(err)
	}
	overlayDirOrSkip(t, backend, nm)
	e := NewEnumerator(backend, 30*time.Second, WithReadOnlyBinds([]DepBind{{Host: nm, Rel: "node_modules"}}))
	files := map[string]string{"a.txt": "x"}
	s := seedOrFatal(t, e, files)
	seedDir := s.(*seededJail).dir
	seedBefore, nmBefore := treeDigest(t, seedDir), treeDigest(t, nm)

	write := "echo y > b.txt && echo z > a.txt && echo w > node_modules/new.js && echo v > node_modules/pkg/index.js && mkdir node_modules/.vite && cat b.txt node_modules/new.js"
	res, err := s.EnumerateDetailed(context.Background(), files, []string{"sh", "-c", write})
	if err != nil || res.ExitCode != 0 || strings.TrimSpace(res.Output) != "y\nw" {
		t.Fatalf("run 1 could not write: %+v err=%v", res, err)
	}
	check := "test ! -e b.txt && test ! -e node_modules/new.js && test ! -e node_modules/.vite && cat a.txt node_modules/pkg/index.js"
	res, err = s.EnumerateDetailed(context.Background(), files, []string{"sh", "-c", check})
	if err != nil || res.ExitCode != 0 || res.Output != "xdep" {
		t.Errorf("run 2 saw run 1's writes: %+v err=%v", res, err)
	}
	if got := treeDigest(t, seedDir); got != seedBefore {
		t.Errorf("the host seed changed:\nbefore %s\nafter  %s", seedBefore, got)
	}
	if got := treeDigest(t, nm); got != nmBefore {
		t.Errorf("the host node_modules changed:\nbefore %s\nafter  %s", nmBefore, got)
	}
}

// A seeded jail runs only the tree it was seeded with. Any other map — even
// one with equal contents — is refused before anything runs, so a caller
// can never get a result for a tree that is not the one in the seed.
func TestSeededJailRefusesAnotherMap(t *testing.T) {
	e := NewEnumerator(overlayJailOrSkip(t), 30*time.Second)
	files := map[string]string{"a.txt": "x"}
	s := seedOrFatal(t, e, files)
	// Nothing a jailed command writes reaches the host (that is the
	// point), so "it never ran" is shown by time: the command sleeps 5 s,
	// and a refusal returns at once.
	other := map[string]string{"a.txt": "x"}
	start := time.Now()
	if _, err := s.Enumerate(context.Background(), other, []string{"sleep", "5"}); err == nil {
		t.Error("Enumerate ran a map that was not the seeded one")
	}
	if _, err := s.EnumerateDetailed(context.Background(), other, []string{"sleep", "5"}); err == nil {
		t.Error("EnumerateDetailed ran a map that was not the seeded one")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("the refusals took %s: the command ran", d)
	}
}

// The run's working directory is the seed's own host path, so every path a
// run prints is the path a fresh copy at that location would print.
func TestSeededJailPathUnchanged(t *testing.T) {
	e := NewEnumerator(overlayJailOrSkip(t), 30*time.Second)
	files := map[string]string{"a.txt": "x"}
	s := seedOrFatal(t, e, files)
	out, err := s.Enumerate(context.Background(), files, []string{"pwd"})
	if err != nil {
		t.Fatal(err)
	}
	if want := s.(*seededJail).dir; strings.TrimSpace(out) != want {
		t.Errorf("pwd = %q, want the seed's host path %q", strings.TrimSpace(out), want)
	}
}

// An overlaid dependency directory is not a bind: a symlink in it that
// leaves the directory is resolved inside the jail, where the host path it
// names is not mounted, so it exposes nothing (the per-entry binds refused
// to mount such a link for the same reason).
func TestSeededJailDependencySymlinkReachesNothing(t *testing.T) {
	backend := overlayJailOrSkip(t)
	secret := t.TempDir()
	if err := os.WriteFile(filepath.Join(secret, "key"), []byte("SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	nm := filepath.Join(t.TempDir(), "node_modules")
	if err := os.MkdirAll(nm, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(nm, "evil")); err != nil {
		t.Fatal(err)
	}
	overlayDirOrSkip(t, backend, nm)
	e := NewEnumerator(backend, 30*time.Second, WithReadOnlyBinds([]DepBind{{Host: nm, Rel: "node_modules"}}))
	files := map[string]string{"a.txt": "x"}
	s := seedOrFatal(t, e, files)
	out, _ := s.Enumerate(context.Background(), files, []string{"sh", "-c", "readlink node_modules/evil; cat node_modules/evil/key 2>&1; true"})
	if strings.Contains(out, "SECRET") {
		t.Errorf("a dependency symlink exposed a host file: %q", out)
	}
	// The link itself is there (the overlay shows Host as it is); it is
	// its target that the jail does not have.
	if !strings.Contains(out, secret) {
		t.Errorf("the dependency symlink is not in the jail, so this proves nothing: %q", out)
	}
}

// Seed refuses a backend that cannot overlay, naming ErrNoOverlay, and
// writes nothing: the caller runs each unit on a fresh copy instead and
// says so.
func TestSeedRefusesABackendThatCannotOverlay(t *testing.T) {
	j := NewEnumerator(&captureIsolator{name: "container"}, time.Second).(bwrapJail)
	s, err := j.Seed(map[string]string{"a.txt": "x"}, &lang.EvidenceCache{Env: "GOCACHE"})
	var no *sandbox.NoOverlayError
	if !errors.Is(err, ErrNoOverlay) || !errors.As(err, &no) || no.Backend != "container" || s != nil {
		t.Errorf("Seed = (%v, %v), want (nil, ErrNoOverlay naming the container backend)", s, err)
	}
}

// After Close, a seeded jail runs nothing, and its seed is gone.
func TestSeededJailRunsNothingAfterClose(t *testing.T) {
	e := NewEnumerator(overlayJailOrSkip(t), 30*time.Second)
	files := map[string]string{"a.txt": "x"}
	s, err := e.(bwrapJail).Seed(files, &lang.EvidenceCache{Env: "GOCACHE"})
	if err != nil {
		t.Fatal(err)
	}
	dir, cache := s.(*seededJail).dir, s.(*seededJail).cacheDir
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the seed %s outlived Close: %v", dir, err)
	}
	if cache == "" {
		t.Error("the seed shares no cache, so its removal is unexercised")
	} else if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Errorf("the shared cache %s outlived Close: %v", cache, err)
	}
	if _, err := s.Enumerate(context.Background(), files, []string{"true"}); err == nil {
		t.Error("a closed seeded jail ran a command")
	}
}

// The jail offers Seed in exactly the shape reposcan's seedingRunner reads
// it in (reposcan does not import this package, so the match is
// structural, and a drift in either signature would silently turn the unit
// pass back into a fresh copy per unit). NewEnumerator's value is what
// certify --repo hands the unit pass on the jail substrate.
func TestEnumeratorOffersReposcansSeed(t *testing.T) {
	var e Enumerator = NewEnumerator(nil, time.Second)
	if _, ok := e.(interface {
		Seed(files map[string]string, cache *lang.EvidenceCache) (sandbox.SeededRunner, error)
	}); !ok {
		t.Error("the jail enumerator has no Seed(map[string]string, *lang.EvidenceCache) (sandbox.SeededRunner, error)")
	}
}

// An overlaid dependency directory's cache directories start empty on every
// run, as they did under the per-entry binds: a host node_modules/.vite
// (vitest's results cache) is not what the jail sees, a write into it does
// not reach the host, and dot-FILES and .pnpm stay visible.
func TestOverlaidDependencyCacheStartsEmpty(t *testing.T) {
	backend := overlayJailOrSkip(t)
	nm := filepath.Join(t.TempDir(), "node_modules")
	for _, d := range []string{".vite", ".pnpm"} {
		if err := os.MkdirAll(filepath.Join(nm, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{".vite/x": "cached", ".pnpm/store": "s", ".package-lock.json": "lock"} {
		if err := os.WriteFile(filepath.Join(nm, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	overlayDirOrSkip(t, backend, nm)
	e := NewEnumerator(backend, 30*time.Second, WithReadOnlyBinds([]DepBind{{Host: nm, Rel: "node_modules"}}))
	files := map[string]string{"a.txt": "x"}
	script := "test -d node_modules/.vite && test ! -e node_modules/.vite/x && echo y > node_modules/.vite/y && cat node_modules/.package-lock.json node_modules/.pnpm/store"
	for name, run := range map[string]func() (sandbox.EnumerateResult, error){
		"fresh copy": func() (sandbox.EnumerateResult, error) {
			return e.(bwrapJail).EnumerateDetailed(context.Background(), files, []string{"sh", "-c", script})
		},
		"seed": func() (sandbox.EnumerateResult, error) {
			return seedOrFatal(t, e, files).EnumerateDetailed(context.Background(), files, []string{"sh", "-c", script})
		},
	} {
		res, err := run()
		if err != nil || res.ExitCode != 0 || strings.TrimSpace(res.Output) != "locks" {
			t.Errorf("%s: %+v err=%v, want an empty writable .vite and visible .package-lock.json and .pnpm", name, res, err)
		}
		if _, err := os.Stat(filepath.Join(nm, ".vite", "y")); err == nil {
			t.Errorf("%s: a write into node_modules/.vite reached the host", name)
		}
	}
}

// seedCacheOrSkip is a seed sharing a GOCACHE, skipped when this host's
// temp filesystem cannot be an overlay's lower layer (the seed then shares
// no cache, and says why).
func seedCacheOrSkip(t *testing.T, e Enumerator, files map[string]string) SeededJail {
	t.Helper()
	s, err := e.(bwrapJail).Seed(files, &lang.EvidenceCache{Env: "GOCACHE"})
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if c := s.Cache(); c.Target == "" {
		t.Skipf("no shared cache on this host (%s); frozen-cache path unexercised", c.Unshared)
	}
	return s
}

// The one warming run writes into the shared cache; a frozen run that
// writes another file and deletes the first changes nothing on the host.
// Both runs find the cache through GOCACHE, which the jail sets.
func TestFrozenCacheIsNotWritten(t *testing.T) {
	e := NewEnumerator(overlayJailOrSkip(t), 30*time.Second)
	files := map[string]string{"a.txt": "x"}
	s := seedCacheOrSkip(t, e, files)
	host := s.(*seededJail).cacheDir

	res, err := s.Warm(context.Background(), files, []string{"sh", "-c", `echo first > "$GOCACHE/first" && echo "$GOCACHE"`})
	if err != nil || res.ExitCode != 0 || strings.TrimSpace(res.Output) != sandbox.CacheTarget {
		t.Fatalf("warm: %+v err=%v, want GOCACHE=%s and a write", res, err, sandbox.CacheTarget)
	}
	before := treeDigest(t, host)
	if !strings.Contains(before, "first ") {
		t.Fatalf("the warming write did not reach the host cache: %s", before)
	}
	res, err = s.EnumerateDetailed(context.Background(), files, []string{"sh", "-c", `cat "$GOCACHE/first" && echo second > "$GOCACHE/second" && rm "$GOCACHE/first" && test ! -e "$GOCACHE/first"`})
	if err != nil || res.ExitCode != 0 || strings.TrimSpace(res.Output) != "first" {
		t.Fatalf("frozen: %+v err=%v, want it to read the warm write and write over it", res, err)
	}
	if got := treeDigest(t, host); got != before {
		t.Errorf("a frozen run changed the host cache:\nbefore %s\nafter  %s", before, got)
	}
	// A frozen run never sees another's writes either.
	if out, err := s.Enumerate(context.Background(), files, []string{"sh", "-c", `ls "$GOCACHE"`}); err != nil || strings.TrimSpace(out) != "first" {
		t.Errorf("a later frozen run sees %q (err=%v), want only the warm write", out, err)
	}
	// Warm happens once, before any frozen run.
	if _, err := s.Warm(context.Background(), files, []string{"true"}); err == nil {
		t.Error("a second Warm, after frozen runs, was run")
	}
}

// Warm is refused on a seed that shares no cache, and on a map that is not
// the seeded one, before anything runs.
func TestWarmRefusals(t *testing.T) {
	e := NewEnumerator(overlayJailOrSkip(t), 30*time.Second)
	files := map[string]string{"a.txt": "x"}
	if _, err := seedOrFatal(t, e, files).Warm(context.Background(), files, []string{"true"}); err == nil {
		t.Error("Warm ran on a seed with no shared cache")
	}
	s := seedCacheOrSkip(t, e, files)
	if _, err := s.Warm(context.Background(), map[string]string{"a.txt": "x"}, []string{"true"}); err == nil {
		t.Error("Warm ran a map that was not the seeded one")
	}
}

// A real go, in the jail, over a warmed and then frozen GOCACHE: the
// frozen cache is really shared (without -count=1, go replays the warm
// run's cached result, the negative control), and the unit evidence pass's
// command (lang.EvidenceUnitCmd adds -count=1) runs the test again rather
// than replaying it. The test prints the time, so a replay is its old line.
func TestFrozenGoCacheNeverReplaysAResult(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go on PATH")
	}
	e := NewEnumerator(overlayJailOrSkip(t), 3*time.Minute)
	files := map[string]string{
		"go.mod":      "module example.com/m\n\ngo 1.21\n",
		"p/p.go":      "package p\n\nfunc One() int { return 1 }\n",
		"p/p_test.go": "package p\n\nimport (\n\t\"fmt\"\n\t\"testing\"\n\t\"time\"\n)\n\nfunc TestOne(t *testing.T) {\n\tfmt.Println(\"RAN\", time.Now().UnixNano())\n\tif One() != 1 {\n\t\tt.Fatal(\"no\")\n\t}\n}\n",
	}
	s := seedCacheOrSkip(t, e, files)
	g, _ := lang.ByName("go")
	unit := []string{"go", "test", "-v", "example.com/m/p"}
	evidence := lang.EvidenceUnitCmd(g, lang.Cmd([]string{"go", "test", "-v", "./..."}), unit)
	if !slices.Contains(evidence, "-count=1") {
		t.Fatalf("the evidence command %q has no -count=1", evidence)
	}
	ran := func(out string) string {
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "RAN ") {
				return line
			}
		}
		return ""
	}

	warm, err := s.Warm(context.Background(), files, unit)
	if err != nil || warm.ExitCode != 0 || ran(warm.Output) == "" {
		t.Fatalf("warm: %+v err=%v", warm, err)
	}
	replay, err := s.EnumerateDetailed(context.Background(), files, unit)
	if err != nil || replay.ExitCode != 0 || !strings.Contains(replay.Output, "(cached)") || ran(replay.Output) != ran(warm.Output) {
		t.Fatalf("negative control: without -count=1 a frozen run should replay the warm result, got %+v err=%v; the cache is not shared, so this proves nothing", replay, err)
	}
	fresh, err := s.EnumerateDetailed(context.Background(), files, evidence)
	if err != nil || fresh.ExitCode != 0 || strings.Contains(fresh.Output, "(cached)") || ran(fresh.Output) == "" || ran(fresh.Output) == ran(warm.Output) {
		t.Errorf("the evidence command replayed a cached result: %+v err=%v (warm printed %q)", fresh, err, ran(warm.Output))
	}
}
