// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/sandbox"
)

func wsTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		full := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The mutant must be visible to the command while it runs...
func TestWorkspaceRunnerAppliesTheMutantDuringTheRun(t *testing.T) {
	root := wsTree(t, map[string]string{"a.txt": "ORIGINAL\n"})
	w := NewWorkspaceRunner(root, 0)

	// `grep -q MUTANT a.txt` exits 0 only if the mutant is on disk right then.
	pass, err := w.RunTest(context.Background(),
		map[string]string{"a.txt": "MUTANT\n"},
		[]string{"grep", "-q", "MUTANT", "a.txt"})
	if err != nil {
		t.Fatalf("RunTest: %v", err)
	}
	if !pass {
		t.Error("the command did not see the mutant on disk")
	}
}

// ...and must be gone afterwards. A leftover mutant would poison every later
// job in this workspace, and on an ephemeral runner nobody would notice.
func TestWorkspaceRunnerRestoresAfterTheRun(t *testing.T) {
	root := wsTree(t, map[string]string{"a.txt": "ORIGINAL\n"})
	w := NewWorkspaceRunner(root, 0)

	if _, err := w.RunTest(context.Background(),
		map[string]string{"a.txt": "MUTANT\n"},
		[]string{"true"}); err != nil {
		t.Fatalf("RunTest: %v", err)
	}
	if got := read(t, root, "a.txt"); got != "ORIGINAL\n" {
		t.Errorf("file left as %q; the mutant was not restored", got)
	}
}

// Restoration must survive a FAILING command, which is the common case —
// most mutants are supposed to make the suite fail.
func TestWorkspaceRunnerRestoresWhenTheCommandFails(t *testing.T) {
	root := wsTree(t, map[string]string{"a.txt": "ORIGINAL\n"})
	w := NewWorkspaceRunner(root, 0)

	pass, err := w.RunTest(context.Background(),
		map[string]string{"a.txt": "MUTANT\n"},
		[]string{"false"})
	if err != nil {
		t.Fatalf("a failing command is a RESULT, not an error: %v", err)
	}
	if pass {
		t.Error("pass = true for a command that exited non-zero")
	}
	if got := read(t, root, "a.txt"); got != "ORIGINAL\n" {
		t.Errorf("file left as %q after a failing command", got)
	}
}

// A file the mutant map names but that does not exist must be created and
// then REMOVED, not left behind as a stray.
func TestWorkspaceRunnerRemovesFilesItCreated(t *testing.T) {
	root := wsTree(t, map[string]string{"a.txt": "ORIGINAL\n"})
	w := NewWorkspaceRunner(root, 0)

	if _, err := w.RunTest(context.Background(),
		map[string]string{"new.txt": "TEMP\n"},
		[]string{"true"}); err != nil {
		t.Fatalf("RunTest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "new.txt")); !os.IsNotExist(err) {
		t.Error("a file the runner created was left on disk")
	}
}

// A path escaping the checkout must be refused. The files map is derived from
// repo-relative candidate paths, but this is the one place a bad key would
// write to the runner's filesystem outside the repo.
func TestWorkspaceRunnerRefusesPathsOutsideTheRoot(t *testing.T) {
	root := wsTree(t, map[string]string{"a.txt": "ORIGINAL\n"})
	w := NewWorkspaceRunner(root, 0)

	for _, bad := range []string{"../escape.txt", "/etc/passwd", "sub/../../escape.txt"} {
		if _, err := w.RunTest(context.Background(),
			map[string]string{bad: "X\n"}, []string{"true"}); err == nil {
			t.Errorf("path %q was accepted; it escapes the checkout", bad)
		}
	}
}

// Verify is the pre-flight: a configured root that exists and is a
// directory must be accepted. (Verify only checks that much — it cannot
// detect a leftover mutant, since it has no record of the tree's prior
// state; that guarantee comes from applyFiles' restore instead.)
func TestWorkspaceRunnerVerifyPassesOnACleanTree(t *testing.T) {
	w := NewWorkspaceRunner(wsTree(t, map[string]string{"a.txt": "ORIGINAL\n"}), 0)
	if err := w.Verify(); err != nil {
		t.Errorf("Verify on a clean tree: %v", err)
	}
}

// Enumerate must apply the SAME apply/restore discipline as RunTest: a file
// the mutant map names must be visible to the command while it runs, and
// gone afterward — an Enumerate that failed to restore would poison every
// later job just as badly as a RunTest that failed to.
func TestWorkspaceRunnerEnumerateAppliesAndRestores(t *testing.T) {
	root := wsTree(t, map[string]string{"a.txt": "ORIGINAL\n"})
	w := NewWorkspaceRunner(root, 0)

	stdout, err := w.Enumerate(context.Background(),
		map[string]string{"a.txt": "MUTANT\n"},
		[]string{"cat", "a.txt"})
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	if stdout != "MUTANT\n" {
		t.Errorf("stdout = %q, want the mutant content captured while it was on disk", stdout)
	}
	if got := read(t, root, "a.txt"); got != "ORIGINAL\n" {
		t.Errorf("file left as %q; Enumerate did not restore", got)
	}
}

// Enumerate's restore must survive a failing command too, for the same
// reason RunTest's must: most mutants are supposed to make the command fail,
// and a restore that only runs on success is not a restore.
func TestWorkspaceRunnerEnumerateRestoresWhenTheCommandFails(t *testing.T) {
	root := wsTree(t, map[string]string{"a.txt": "ORIGINAL\n"})
	w := NewWorkspaceRunner(root, 0)

	if _, err := w.Enumerate(context.Background(),
		map[string]string{"a.txt": "MUTANT\n"},
		[]string{"false"}); err != nil {
		t.Fatalf("a failing command is a RESULT, not an error: %v", err)
	}
	if got := read(t, root, "a.txt"); got != "ORIGINAL\n" {
		t.Errorf("file left as %q after a failing Enumerate command", got)
	}
}

// A mutant that names a path under a directory that does not yet exist must
// have that directory removed too, not just the file. Restore only ever
// wrote back or removed the file entry; a directory it had to create to hold
// that file is exactly as much a stray as the file would be, and just as
// invisible on an ephemeral runner.
func TestWorkspaceRunnerRemovesDirectoriesItCreated(t *testing.T) {
	root := wsTree(t, map[string]string{"a.txt": "ORIGINAL\n"})
	w := NewWorkspaceRunner(root, 0)

	if _, err := w.RunTest(context.Background(),
		map[string]string{"newdir/sub/new.txt": "TEMP\n"},
		[]string{"true"}); err != nil {
		t.Fatalf("RunTest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "newdir", "sub", "new.txt")); !os.IsNotExist(err) {
		t.Error("the file the runner created was left on disk")
	}
	if _, err := os.Stat(filepath.Join(root, "newdir", "sub")); !os.IsNotExist(err) {
		t.Error("the nested directory the runner created was left on disk")
	}
	if _, err := os.Stat(filepath.Join(root, "newdir")); !os.IsNotExist(err) {
		t.Error("the top-level directory the runner created was left on disk")
	}
}

// A path that is lexically inside the root but is itself a symlink pointing
// outside it must be refused, not followed. The tree under audit is a PR
// checkout: whoever opened the PR could have committed a symlink, and this
// is the one seam where following it would read or write outside the repo.
func TestWorkspaceRunnerRefusesASymlinkThatEscapesTheRoot(t *testing.T) {
	if _, err := os.Readlink("/proc/self"); err != nil {
		t.Skip("symlinks unavailable on this host")
	}

	outsideDir := t.TempDir()
	outsidePath := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsidePath, []byte("SECRET\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	root := wsTree(t, map[string]string{"a.txt": "ORIGINAL\n"})
	linkPath := filepath.Join(root, "link.txt")
	if err := os.Symlink(outsidePath, linkPath); err != nil {
		t.Skip("symlink creation unavailable on this host")
	}

	w := NewWorkspaceRunner(root, 0)
	if _, err := w.RunTest(context.Background(),
		map[string]string{"link.txt": "MUTANT\n"},
		[]string{"true"}); err == nil {
		t.Error("a mutant key pointing through a symlink out of the root was accepted")
	}

	got, rerr := os.ReadFile(outsidePath)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(got) != "SECRET\n" {
		t.Errorf("outside file content = %q; the symlink write escaped the checkout", got)
	}
}

// TestWorkspaceRunnerBoundsTheRunWithItsOwnTimeout: the jail path bounds
// EVERY run — baseline, canary and mutants alike — because NewJail's timeout
// is passed into each RunGuarded call. adequacy.Score only wraps MUTANT runs
// in a deadline of its own; the baseline and the canary run on the caller's
// bare ctx, which on `certify --repo` is context.Background() with no
// per-job deadline. So a deadlocking baseline suite — plausible precisely
// because this substrate, unlike the jail, has network — hung the CI job
// forever, and ErrTestTimeout (the outcome Score already models for the jail
// path) could never be produced for a baseline here.
func TestWorkspaceRunnerBoundsTheRunWithItsOwnTimeout(t *testing.T) {
	root := wsTree(t, map[string]string{"a.txt": "ORIGINAL\n"})
	w := NewWorkspaceRunner(root, 100*time.Millisecond)

	start := time.Now()
	ok, err := w.RunTest(context.Background(),
		map[string]string{"a.txt": "MUTANT\n"},
		[]string{"sleep", "30"})
	elapsed := time.Since(start)

	if !errors.Is(err, ErrTestTimeout) {
		t.Fatalf("RunTest on a hanging command = (%v, %v), want ErrTestTimeout", ok, err)
	}
	if elapsed > 10*time.Second {
		t.Errorf("the run took %s: the timeout did not bound it", elapsed)
	}
	// The tree is still restored: the timeout goes through the same deferred
	// restore every other exit path does.
	if got := read(t, root, "a.txt"); got != "ORIGINAL\n" {
		t.Errorf("file left as %q after a timed-out run", got)
	}
}

// Enumerate is bounded the same way, off the same field: it shares RunTest's
// apply/run/restore discipline and must share its wall-clock bound too.
func TestWorkspaceRunnerEnumerateIsBoundedToo(t *testing.T) {
	root := wsTree(t, map[string]string{"a.txt": "ORIGINAL\n"})
	w := NewWorkspaceRunner(root, 100*time.Millisecond)

	start := time.Now()
	if _, err := w.Enumerate(context.Background(), nil, []string{"sleep", "30"}); !errors.Is(err, ErrTestTimeout) {
		t.Fatalf("Enumerate on a hanging command = %v, want ErrTestTimeout", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("the run took %s: the timeout did not bound it", elapsed)
	}
}

// TestWorkspaceRunnerEnumerateWithoutMaxOutputStaysUnbounded pins F1's
// backward-compatibility half: a WorkspaceRunner built WITHOUT
// WithWorkspaceMaxOutput (every caller before this option existed,
// including certify_local.go's ordinary per-mutant runs) must keep
// buffering Enumerate's output completely unbounded, exactly as it always
// has — this option is opt-in, and RunTest/RunTestVerbose never accept it
// at all.
func TestWorkspaceRunnerEnumerateWithoutMaxOutputStaysUnbounded(t *testing.T) {
	root := wsTree(t, map[string]string{"a.txt": "x\n"})
	w := NewWorkspaceRunner(root, 0)

	// 100 KiB of 'x' — bigger than sandbox.Run's own 16 KiB default, to
	// prove nothing is silently capping this path either.
	const want = 100 << 10
	out, err := w.Enumerate(context.Background(), nil,
		[]string{"sh", "-c", "yes x | head -c " + itoa(want)})
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	if len(out) != want {
		t.Fatalf("output = %d bytes, want exactly %d (unbounded, no truncation marker expected)", len(out), want)
	}
}

// TestWorkspaceRunnerEnumerateWithMaxOutputTruncates is the F1 fix itself:
// this is the coverage pre-flight's own substrate, and before this option
// existed nothing bounded what Enumerate buffered here at all — measured
// against a real grpc-go run, a 253 MB coverage profile read entirely into
// memory (827 MB peak RSS). WithWorkspaceMaxOutput must actually cap it,
// using the same sandbox.CappedWriter + sandbox.TruncationMarker contract
// reposcan.Preflight already knows how to detect (checked as a suffix, see
// preflight.go), so a truncated run here reports exactly the way a
// truncated jail run always has.
func TestWorkspaceRunnerEnumerateWithMaxOutputTruncates(t *testing.T) {
	root := wsTree(t, map[string]string{"a.txt": "x\n"})
	const cap = 1024
	w := NewWorkspaceRunner(root, 0, WithWorkspaceMaxOutput(cap))

	out, err := w.Enumerate(context.Background(), nil,
		[]string{"sh", "-c", "yes x | head -c " + itoa(cap*10)})
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	if !strings.HasSuffix(out, sandbox.TruncationMarker) {
		t.Fatalf("output does not end with sandbox.TruncationMarker: got %d bytes, tail %q", len(out), tail(out, 40))
	}
	// The captured payload itself (before the marker) must still be capped
	// at cap bytes — a marker appended to an otherwise-unbounded buffer
	// would defeat the whole point.
	payload := strings.TrimSuffix(strings.TrimRight(out, "\n"), "\n"+sandbox.TruncationMarker)
	if len(payload) > cap {
		t.Fatalf("payload is %d bytes, want <= %d (the cap)", len(payload), cap)
	}
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// The caller's own ctx still wins when it is TIGHTER than the runner's
// timeout — the runner's bound is a backstop, never a widening of a deadline
// the caller already set (adequacy.Score's per-mutant deadline is exactly
// such a caller).
func TestWorkspaceRunnerStillHonoursATighterCallerDeadline(t *testing.T) {
	root := wsTree(t, map[string]string{"a.txt": "ORIGINAL\n"})
	w := NewWorkspaceRunner(root, time.Hour)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := w.RunTest(ctx, nil, []string{"sleep", "30"}); !errors.Is(err, ErrTestTimeout) {
		t.Fatalf("RunTest = %v, want ErrTestTimeout from the caller's own deadline", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("the run took %s: the caller's tighter deadline was not honoured", elapsed)
	}
}

// TestWorkspaceRunnerRunTestVerboseReturnsTheOutput: advpool's CompileTest
// type-asserts its Jail to the optional verbose interface and, when that
// fails, silently degrades to a bare pass/fail — so the test-writer gets
// "does not compile" with no compiler output to fix itself from, and burns
// retries (and model spend) repeating the same mistake. On the substrate
// whose whole pitch is cost, that is the wrong place to degrade. Same
// apply/run/restore discipline as RunTest, via the same applyFiles.
func TestWorkspaceRunnerRunTestVerboseReturnsTheOutput(t *testing.T) {
	root := wsTree(t, map[string]string{"a.txt": "ORIGINAL\n"})
	w := NewWorkspaceRunner(root, 0)

	ok, out, err := w.RunTestVerbose(context.Background(),
		map[string]string{"a.txt": "MUTANT\n"},
		[]string{"sh", "-c", "cat a.txt; echo 'compiler said this' >&2; exit 1"})
	if err != nil {
		t.Fatalf("a non-zero exit is a RESULT, not an error: %v", err)
	}
	if ok {
		t.Error("a command that exited 1 must report false")
	}
	if !strings.Contains(out, "MUTANT") {
		t.Errorf("output %q must include the command's stdout, seen against the applied files", out)
	}
	if !strings.Contains(out, "compiler said this") {
		t.Errorf("output %q must include stderr — that is where a compiler writes", out)
	}
	if got := read(t, root, "a.txt"); got != "ORIGINAL\n" {
		t.Errorf("file left as %q after RunTestVerbose", got)
	}
}

// TestWorkspaceRunnerEnumerateReturnsWhenAChildOutlivesTheCommand is the
// regression for the review's Important 3: applyRunRestore used
// exec.CommandContext + cmd.Run() with no process group, no cmd.Cancel and no
// cmd.WaitDelay, so a command that exits cleanly while leaving a background
// grandchild alive left that grandchild holding the inherited write end of
// the output pipe — and cmd.Wait blocked on the copying goroutine FOREVER,
// past the runner's own wall-clock bound. sandbox.Run has carried all three
// guards for exactly this reason; this substrate never goes through it.
//
// `sh -c "sleep 120 & echo hello; exit 0"` is the minimal shape of an
// ordinary suite that leaves a worker running (the reproduction in the
// review), and 120s is far past both the runner's 2s bound and this test's
// own 20s ceiling, so a hang here cannot pass by accident.
func TestWorkspaceRunnerEnumerateReturnsWhenAChildOutlivesTheCommand(t *testing.T) {
	root := wsTree(t, map[string]string{"a.txt": "ORIGINAL\n"})
	w := NewWorkspaceRunner(root, 2*time.Second, WithWorkspaceMaxOutput(1<<20))

	done := make(chan struct{})
	var out string
	var err error
	go func() {
		defer close(done)
		out, err = w.Enumerate(context.Background(), nil,
			[]string{"sh", "-c", "sleep 120 & echo hello; exit 0"})
	}()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Enumerate did not return within 20s: a leaked grandchild held the output pipe open")
	}
	if err != nil {
		t.Fatalf("Enumerate = %v, want nil: the command itself exited 0", err)
	}
	if !strings.Contains(out, "hello") {
		t.Errorf("Enumerate output = %q, want it to contain the command's own stdout", out)
	}
}

// The RunTest half of the same defect: RunTestVerbose shares applyRunRestore
// (and, being the variant that captures output, shares the inherited pipe
// that a grandchild holds open), so it shares both the bug and the fix. A
// non-zero exit stays a RESULT, not an error, even when a grandchild
// outlives the command.
func TestWorkspaceRunnerRunTestVerboseReturnsWhenAChildOutlivesTheCommand(t *testing.T) {
	root := wsTree(t, map[string]string{"a.txt": "ORIGINAL\n"})
	w := NewWorkspaceRunner(root, 2*time.Second)

	done := make(chan struct{})
	var ok bool
	var err error
	go func() {
		defer close(done)
		ok, _, err = w.RunTestVerbose(context.Background(), nil,
			[]string{"sh", "-c", "sleep 120 & exit 1"})
	}()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("RunTestVerbose did not return within 20s: a leaked grandchild held the output pipe open")
	}
	if ok || err != nil {
		t.Fatalf("RunTestVerbose = (%v, %v), want (false, nil): a non-zero exit is a result", ok, err)
	}
}
