// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// gitAdd stages paths into dir's index — TreeDigest's universe includes
// tracked files (`--cached`), and a file must be staged (not necessarily
// committed) to count as tracked.
func gitAdd(t *testing.T, dir string, paths ...string) {
	t.Helper()
	args := append([]string{"-C", dir, "add"}, paths...)
	cmd := exec.Command("git", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add %v: %v\n%s", paths, err, out)
	}
}

// TestTreeDigestIsStableAcrossDifferentRoots is the baseline: the digest is
// a function of the tree's CONTENT, not of where on disk it happens to sit —
// two different temp dirs holding the same files must agree.
func TestTreeDigestIsStableAcrossDifferentRoots(t *testing.T) {
	files := map[string]string{"a.go": "package p\n", "pkg/b.go": "package pkg\n"}
	r1 := writeTree(t, files)
	gitInit(t, r1)
	r2 := writeTree(t, files)
	gitInit(t, r2)

	d1, err1 := TreeDigest(r1)
	d2, err2 := TreeDigest(r2)
	if err1 != nil || err2 != nil {
		t.Fatalf("TreeDigest errors: %v, %v", err1, err2)
	}
	if d1 == "" || d2 == "" {
		t.Fatalf("digests must be non-empty inside a git work tree: %q, %q", d1, d2)
	}
	if d1 != d2 {
		t.Errorf("two trees with identical content at different paths digested differently: %q vs %q", d1, d2)
	}
}

// TestTreeDigestChangesWithOneTrackedByte: the whole point of a
// content-addressed cache key is that it moves when the content does.
func TestTreeDigestChangesWithOneTrackedByte(t *testing.T) {
	root := writeTree(t, map[string]string{"a.go": "package p\n"})
	gitInit(t, root)
	gitAdd(t, root, "a.go")
	before, err := TreeDigest(root)
	if err != nil || before == "" {
		t.Fatalf("before: digest=%q err=%v", before, err)
	}

	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package p // x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := TreeDigest(root)
	if err != nil {
		t.Fatalf("after: %v", err)
	}
	if after == before {
		t.Errorf("editing a tracked file did not change TreeDigest: %q", after)
	}
}

// TestTreeDigestIncludesUntrackedNotIgnored: an untracked edit must miss the
// selection cache — it is exactly the kind of change an instrumented run
// would see that a tracked-only key would silently ignore.
func TestTreeDigestIncludesUntrackedNotIgnored(t *testing.T) {
	root := writeTree(t, map[string]string{"a.go": "package p\n"})
	gitInit(t, root)
	gitAdd(t, root, "a.go")
	before, err := TreeDigest(root)
	if err != nil {
		t.Fatalf("before: %v", err)
	}

	if err := os.WriteFile(filepath.Join(root, "untracked.go"), []byte("package p\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := TreeDigest(root)
	if err != nil {
		t.Fatalf("after: %v", err)
	}
	if after == before {
		t.Error("an untracked, not-ignored file did not change TreeDigest")
	}
}

// TestTreeDigestExcludesGitignored: churn inside a gitignored directory
// (build output, a venv) must NOT invalidate the key — without this, running
// the instrumented suite once (which is exactly what leaves that churn
// behind) would poison the cache's ability to reuse its own evidence.
func TestTreeDigestExcludesGitignored(t *testing.T) {
	root := writeTree(t, map[string]string{
		".gitignore": "out/\n",
		"a.go":       "package p\n",
	})
	gitInit(t, root)
	gitAdd(t, root, "a.go", ".gitignore")
	before, err := TreeDigest(root)
	if err != nil {
		t.Fatalf("before: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(root, "out"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "out", "cache.bin"), []byte("churn"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := TreeDigest(root)
	if err != nil {
		t.Fatalf("after: %v", err)
	}
	if after != before {
		t.Errorf("a gitignored file changed TreeDigest: before=%q after=%q", before, after)
	}
}

// TestTreeDigestSymlinkDigestsTargetString: a tracked symlink's TARGET
// STRING is what changes the digest, not any content at the far end of the
// link — retargeting it (with no byte at either end touched) must still
// register as a change, and TreeDigest must never follow the link to read
// through it.
func TestTreeDigestSymlinkDigestsTargetString(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a.txt", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	gitInit(t, root)
	gitAdd(t, root, "a.txt", "b.txt", "link")

	before, err := TreeDigest(root)
	if err != nil || before == "" {
		t.Fatalf("before: digest=%q err=%v", before, err)
	}

	// Retarget the symlink to point at DIFFERENT content but keep every
	// tracked byte on disk unchanged (a.txt and b.txt are untouched): if
	// TreeDigest were following the link and hashing what it points at,
	// this retarget alone (a.txt's bytes never changed) would prove nothing
	// changed. It must.
	if err := os.Remove(filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("b.txt", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	gitAdd(t, root, "link")

	after, err := TreeDigest(root)
	if err != nil {
		t.Fatalf("after: %v", err)
	}
	if after == before {
		t.Error("retargeting a tracked symlink did not change TreeDigest")
	}
}

// TestTreeDigestOutsideGitWorkTreeIsEmpty: with no git authority to consult,
// TreeDigest returns "" and no error — the caller's bypass signal, not a
// failure.
func TestTreeDigestOutsideGitWorkTreeIsEmpty(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package p\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := TreeDigest(root)
	if err != nil {
		t.Fatalf("outside a git work tree must not error: %v", err)
	}
	if d != "" {
		t.Errorf("TreeDigest outside a git work tree = %q, want \"\"", d)
	}
}
