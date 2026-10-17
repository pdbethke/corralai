// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Under --substrate workspace every reproduction starts from the recorded
// commit. One worktree used to serve the whole review, and a script's own
// edits were never restored, so a later finding (or the verifier's
// refutation) could pass or fail on what an earlier script left behind
// rather than on the commit. Found and reproduced by an outside review
// (Codex, 2026-10-17); the default jail substrate already seeds each run
// fresh.
func TestWorkspaceReproductionsEachStartFromTheCommit(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\nfunc Add(a, b int) int { return a - b }\n")
	git := gitCmd(t, root)
	git("init", "-q")
	git("add", ".")
	git("commit", "-q", "-m", "Fixture commits the subtracting source.", "--no-gpg-sign")

	rep, cleanup, err := newReproducer(substrateWorkspace, nil, root, gitHeadCommit(root), "pkg", 10*time.Second, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, code, err := rep.Run(ctx, "printf 'package pkg\\nfunc Add(a, b int) int { return a + b }\\n' > pkg/a.go && touch pkg/stray.txt"); err != nil || code != 0 {
		t.Fatalf("first script: exit %d, %v", code, err)
	}
	if _, code, err := rep.Run(ctx, "grep -q 'return a + b' pkg/a.go"); err != nil || code != 1 {
		t.Errorf("second script saw the first script's edit: exit %d (want 1), %v", code, err)
	}
	if _, code, err := rep.Run(ctx, "test -e pkg/stray.txt"); err != nil || code != 1 {
		t.Errorf("second script saw the first script's new file: exit %d (want 1), %v", code, err)
	}
	cleanup()
	// Nothing a script wrote reaches the operator's checkout, and no worktree
	// is left registered behind the review.
	if b, _ := os.ReadFile(filepath.Join(root, "pkg", "a.go")); string(b) != "package pkg\nfunc Add(a, b int) int { return a - b }\n" {
		t.Errorf("the checkout changed: %q", b)
	}
	out, err := exec.Command("git", "-C", root, "worktree", "list", "--porcelain").Output()
	if err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(out, []byte("worktree ")); n != 1 {
		t.Errorf("%d worktrees registered after cleanup, want only the checkout:\n%s", n, out)
	}
}
