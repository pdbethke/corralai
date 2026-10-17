// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/agentbackend"
)

// An API seat is shown the files of the COMMIT the review is recorded
// against, never the operator's working copy. It used to be shown the
// working copy (review.LoadScope(root, …)) while the entry said HEAD and the
// reproductions ran on HEAD's worktree, so an uncommitted edit could make a
// finding about code nobody committed, then be demoted when the script ran
// against the code that was. Found by an outside review (Codex, 2026-10-17).
func TestReviewShowsTheAPISeatTheCommitNotTheWorkingCopy(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\nfunc Add(a, b int) int { return a - b }\n")
	git := gitCmd(t, root)
	git("init", "-q")
	git("add", ".")
	git("commit", "-q", "-m", "Fixture commits the subtracting source.", "--no-gpg-sign")
	// Dirty the checkout two ways: a modified tracked file and an untracked one.
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\nfunc Add(a, b int) int { return a + b }\n")
	mustWrite(t, filepath.Join(root, "pkg", "untracked.go"), "package pkg\nconst Untracked = 1\n")

	var saw string
	orig := newReviewerBackend
	t.Cleanup(func() { newReviewerBackend = orig })
	newReviewerBackend = func(string, string) (agentbackend.Backend, error) {
		return cannedReviewer{saw: &saw, reply: `{"opinion":"It subtracts.","findings":[{"claim":"Add subtracts","tier":"REPRODUCED","file":"pkg/a.go","line":2,"severity":"low","script":"grep -q 'return a - b' pkg/a.go"}],"sound":[]}`}, nil
	}
	var out, errout bytes.Buffer
	code := runReview([]string{"--repo", root, "--scope", "pkg", "--reviewer-model", "reviewer-x", "--substrate", "workspace", "--no-ledger"}, &out, &errout)
	if code != 0 {
		t.Fatalf("review exited %d\n%s\n%s", code, out.String(), errout.String())
	}
	if !strings.Contains(saw, "return a - b") || strings.Contains(saw, "return a + b") {
		t.Errorf("the API seat was not shown the committed source:\n%s", saw)
	}
	if strings.Contains(saw, "Untracked") || strings.Contains(saw, "untracked.go") {
		t.Errorf("the API seat was shown a file that is not in the commit:\n%s", saw)
	}
	if strings.Contains(out.String(), "the script exited 1") {
		t.Errorf("the finding about the committed source did not reproduce against it:\n%s", out.String())
	}
}
