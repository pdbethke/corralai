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

// Verify is the pre-flight: a workspace that already carries a leftover
// mutant must be caught before the first job, not after.
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
