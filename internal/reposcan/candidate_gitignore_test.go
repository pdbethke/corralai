// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// gitInit turns dir into a git work tree. No commit is needed: `git ls-files
// --others --ignored` classifies untracked files against .gitignore on its own.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	cmd := exec.Command("git", "-C", dir, "init", "-q")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
}

func reasonsByPath(excl []Exclusion) map[string]string {
	m := map[string]string{}
	for _, e := range excl {
		m[e.Path] = e.Reason
	}
	return m
}

// A gitignored copy of a source file pairs perfectly with its own test and
// would otherwise be counted — and could be SELECTED — as a candidate. The
// repo's own .gitignore is the authority on what is not this repo's source.
func TestEnumerateExcludesGitignoredFiles(t *testing.T) {
	root := writeTree(t, map[string]string{
		".gitignore":    "out/\n",
		"a.go":          "package pkg\n",
		"a_test.go":     "package pkg\n",
		"out/a.go":      "package pkg\n",
		"out/a_test.go": "package pkg\n",
		"out/other.py":  "x = 1\n",
	})
	gitInit(t, root)

	cands, excl, err := Enumerate(root)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	if len(cands) != 1 || cands[0].Path != "a.go" {
		t.Errorf("candidates = %+v, want only a.go", cands)
	}
	reasons := reasonsByPath(excl)
	// ACCOUNTED, never dropped: the walked total must still say the files
	// were there, with a reason a reader can check against .gitignore.
	for _, p := range []string{"out/a.go", "out/a_test.go", "out/other.py"} {
		if reasons[p] != ReasonGitignored {
			t.Errorf("%s reason = %q, want %q", p, reasons[p], ReasonGitignored)
		}
	}
	if reasons["a_test.go"] != ReasonIsTest {
		t.Errorf("a_test.go reason = %q, want %q", reasons["a_test.go"], ReasonIsTest)
	}
}

// A sibling git worktree is the case that produced the bug: a full copy of
// the repo under an ignored directory that is ITSELF a git repository. git
// reports such a directory as a single ignored entry rather than listing its
// files, so the walk has to treat the whole subtree as ignored.
func TestEnumerateExcludesGitignoredNestedRepo(t *testing.T) {
	root := writeTree(t, map[string]string{
		".gitignore":                  ".worktrees/\n",
		"pkg/a.go":                    "package pkg\n",
		"pkg/a_test.go":               "package pkg\n",
		".worktrees/wt/pkg/a.go":      "package pkg\n",
		".worktrees/wt/pkg/a_test.go": "package pkg\n",
		// A linked worktree carries a .git FILE pointing at the main repo,
		// not a .git directory. Its content is irrelevant to the walk.
		".worktrees/wt/.git": "gitdir: /nonexistent/.git/worktrees/wt\n",
	})
	gitInit(t, root)

	cands, excl, err := Enumerate(root)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	if len(cands) != 1 || cands[0].Path != "pkg/a.go" {
		t.Errorf("candidates = %+v, want only pkg/a.go", cands)
	}
	reasons := reasonsByPath(excl)
	for _, p := range []string{".worktrees/wt/pkg/a.go", ".worktrees/wt/pkg/a_test.go"} {
		if reasons[p] != ReasonGitignored {
			t.Errorf("%s reason = %q, want %q", p, reasons[p], ReasonGitignored)
		}
	}
	// The nested worktree's .git pointer is VCS, invisible like the root's.
	if _, listed := reasons[".worktrees/wt/.git"]; listed {
		t.Error(".worktrees/wt/.git was enumerated; VCS entries must stay invisible")
	}
}

// A nested .gitignore is honoured: git evaluates the whole ignore stack, and
// the scan must agree with git rather than reimplement it.
func TestEnumerateHonoursNestedGitignore(t *testing.T) {
	root := writeTree(t, map[string]string{
		"pkg/.gitignore":    "gen_*.go\n",
		"pkg/a.go":          "package pkg\n",
		"pkg/a_test.go":     "package pkg\n",
		"pkg/gen_b.go":      "package pkg\n",
		"pkg/gen_b_test.go": "package pkg\n",
	})
	gitInit(t, root)

	cands, excl, err := Enumerate(root)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	if len(cands) != 1 || cands[0].Path != "pkg/a.go" {
		t.Errorf("candidates = %+v, want only pkg/a.go", cands)
	}
	reasons := reasonsByPath(excl)
	if reasons["pkg/gen_b.go"] != ReasonGitignored {
		t.Errorf("pkg/gen_b.go reason = %q, want %q", reasons["pkg/gen_b.go"], ReasonGitignored)
	}
}

// Outside a git work tree there is no .gitignore authority, and the scan
// walks exactly as it always has. This pins the fallback so the git path can
// never become a silent requirement.
func TestEnumerateWithoutGitWalksIgnoreFileAsPlainFile(t *testing.T) {
	root := writeTree(t, map[string]string{
		".gitignore":    "out/\n",
		"a.go":          "package pkg\n",
		"a_test.go":     "package pkg\n",
		"out/a.go":      "package pkg\n",
		"out/a_test.go": "package pkg\n",
	})
	if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
		t.Fatal("test tree must not be a git repo")
	}

	cands, _, err := Enumerate(root)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	got := []string{}
	for _, c := range cands {
		got = append(got, c.Path)
	}
	if len(got) != 2 || got[0] != "a.go" || got[1] != "out/a.go" {
		t.Errorf("candidates = %v, want [a.go out/a.go] (no git: .gitignore is just a file)", got)
	}
}
