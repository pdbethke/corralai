// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"os"
	"path/filepath"
	"testing"
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
	w := NewWorkspaceRunner(root)

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
	w := NewWorkspaceRunner(root)

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
	w := NewWorkspaceRunner(root)

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
	w := NewWorkspaceRunner(root)

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
	w := NewWorkspaceRunner(root)

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
	w := NewWorkspaceRunner(wsTree(t, map[string]string{"a.txt": "ORIGINAL\n"}))
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
	w := NewWorkspaceRunner(root)

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
	w := NewWorkspaceRunner(root)

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
	w := NewWorkspaceRunner(root)

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

	w := NewWorkspaceRunner(root)
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
