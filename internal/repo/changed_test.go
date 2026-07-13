// SPDX-License-Identifier: Elastic-2.0

package repo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestDiffAddedLines verifies the history-scan source: a secret committed in an
// earlier phase and then DELETED (clean working tree) is still present in the
// full-history patch (`git log -p base..HEAD`), so the egress gate can catch it.
func TestDiffAddedLines(t *testing.T) {
	bare := makeBareRepoWithCommit(t) // helper from repo_test.go
	dest := filepath.Join(t.TempDir(), "w")
	e := New("", "")
	ctx := context.Background()
	if err := e.Clone(ctx, bare, "main", dest); err != nil {
		t.Fatal(err)
	}
	_ = e.Checkout(ctx, dest, "feature")

	const secret = "AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	// Phase 1: commit the secret.
	if err := os.WriteFile(filepath.Join(dest, "config.env"), []byte(secret+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Commit(ctx, dest, "phase1: add config"); err != nil {
		t.Fatal(err)
	}
	// Phase 2: delete it — the final working tree is clean.
	if err := os.Remove(filepath.Join(dest, "config.env")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Commit(ctx, dest, "phase2: remove config"); err != nil {
		t.Fatal(err)
	}

	// Working tree no longer contains the secret.
	if _, err := os.ReadFile(filepath.Join(dest, "config.env")); !os.IsNotExist(err) {
		t.Fatalf("expected config.env to be deleted from the working tree, err=%v", err)
	}

	// But the full-history patch DOES contain the added secret line.
	patch, err := e.DiffAddedLines(ctx, dest, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(patch, secret) {
		t.Fatalf("DiffAddedLines must contain the history-only secret; got:\n%s", patch)
	}
	if !strings.Contains(patch, "+++ b/config.env") {
		t.Fatalf("DiffAddedLines patch should carry a +++ b/ file header; got:\n%s", patch)
	}
}

// TestDiffAddedLines_CatchesEvilMergeCommit is the regression guard for F5:
// `git log -p` omits merge-commit diffs BY DEFAULT, so a secret introduced
// only in a merge's conflict resolution (an "evil merge" — the resolved text
// differs from both parents) would never appear in DiffAddedLines' output
// unless --diff-merges=first-parent is passed. This builds a real conflicting
// merge (both branches edit the same line) whose manual resolution injects a
// secret line absent from either parent, then asserts the secret is caught.
func TestDiffAddedLines_CatchesEvilMergeCommit(t *testing.T) {
	bare := makeBareRepoWithCommit(t)
	dest := filepath.Join(t.TempDir(), "w")
	e := New("", "")
	ctx := context.Background()
	if err := e.Clone(ctx, bare, "main", dest); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dest
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runAllowFail := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dest
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		_, _ = c.CombinedOutput() // conflict is expected; ignored
	}

	// Capture the pre-divergence base.
	out, err := exec.Command("git", "-C", dest, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	base := strings.TrimSpace(string(out))

	// Branch "feature" off base, edit README.md's only line.
	run("checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(dest, "README.md"), []byte("feature line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("commit", "-am", "feature: edit line")

	// Back on main, edit the SAME line differently, so merging feature conflicts.
	run("checkout", "main")
	if err := os.WriteFile(filepath.Join(dest, "README.md"), []byte("main line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("commit", "-am", "main: edit line")

	// Merge feature into main — conflicts on README.md (expected failure).
	runAllowFail("merge", "--no-ff", "-m", "merge feature (conflict)", "feature")

	const secret = "AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	// Resolve the conflict with an "evil" resolution: text present in NEITHER
	// parent, including a secret — this is what a malicious/careless
	// conflict resolution during a merge can smuggle in.
	if err := os.WriteFile(filepath.Join(dest, "README.md"), []byte("resolved\n"+secret+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "--no-edit")

	patch, err := e.DiffAddedLines(ctx, dest, base)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(patch, secret) {
		t.Fatalf("DiffAddedLines must catch a secret introduced via a merge commit's conflict resolution; got:\n%s", patch)
	}
}

func TestChangedFiles(t *testing.T) {
	bare := makeBareRepoWithCommit(t) // helper from repo_test.go
	dest := filepath.Join(t.TempDir(), "w")
	e := New("", "")
	ctx := context.Background()
	if err := e.Clone(ctx, bare, "main", dest); err != nil {
		t.Fatal(err)
	}
	_ = e.Checkout(ctx, dest, "feature")
	if err := os.WriteFile(filepath.Join(dest, "new.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Commit(ctx, dest, "add new"); err != nil {
		t.Fatal(err)
	}
	changed, err := e.ChangedFiles(ctx, dest)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range changed {
		if f == "new.go" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ChangedFiles missing new.go: %v", changed)
	}
}

func TestChangedFilesRange(t *testing.T) {
	bare := makeBareRepoWithCommit(t) // helper from repo_test.go
	dest := filepath.Join(t.TempDir(), "w")
	e := New("", "")
	ctx := context.Background()
	if err := e.Clone(ctx, bare, "main", dest); err != nil {
		t.Fatal(err)
	}
	_ = e.Checkout(ctx, dest, "feature")
	// Two phase commits, mirroring how the mission engine commits per-phase.
	if err := os.WriteFile(filepath.Join(dest, "phase1.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Commit(ctx, dest, "phase1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "phase2.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Commit(ctx, dest, "phase2"); err != nil {
		t.Fatal(err)
	}
	// ChangedFiles (last commit only) must miss phase1.go.
	last, err := e.ChangedFiles(ctx, dest)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range last {
		if f == "phase1.go" {
			t.Fatalf("ChangedFiles unexpectedly includes phase1.go (should be last-commit only): %v", last)
		}
	}
	// ChangedFilesRange against base must see BOTH phase commits.
	all, err := e.ChangedFilesRange(ctx, dest, "main")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"phase1.go": false, "phase2.go": false}
	for _, f := range all {
		if _, ok := want[f]; ok {
			want[f] = true
		}
	}
	for f, seen := range want {
		if !seen {
			t.Errorf("ChangedFilesRange missing %s: %v", f, all)
		}
	}
}
