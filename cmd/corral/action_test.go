// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// actionFetchLine returns the `git fetch` command action.yml actually ships,
// read from the file itself rather than restated here — a test that asserted
// a copy of the command would keep passing while the shipped action broke.
func actionFetchLine(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "action.yml"))
	if err != nil {
		t.Fatalf("reading action.yml: %v", err)
	}
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.Contains(ln, "git fetch") {
			return strings.TrimSpace(ln)
		}
	}
	t.Fatal("action.yml has no `git fetch` line")
	return ""
}

// runGit runs a git command in dir, skipping the test when git itself is
// unusable in this environment.
func runGit(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=corral", "GIT_AUTHOR_EMAIL=corral@example.com",
		"GIT_COMMITTER_NAME=corral", "GIT_COMMITTER_EMAIL=corral@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := runGit(t, dir, args...)
	if err != nil {
		t.Skipf("git %v in %s unusable here: %v: %s", args, dir, err, out)
	}
	return out
}

// actionPRFixture builds the shape a real pull_request run has: an origin
// repo whose `main` has ADVANCED since the fork point, and a checkout made
// the way actions/checkout makes it — a single-ref refspec covering only the
// PR branch, so `refs/remotes/origin/main` does not exist locally.
func actionPRFixture(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	origin := filepath.Join(root, "origin")
	if err := os.MkdirAll(origin, 0o750); err != nil {
		t.Fatal(err)
	}
	mustGit(t, origin, "init", "--initial-branch=main", ".")
	if err := os.WriteFile(filepath.Join(origin, "base.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, origin, "add", ".")
	mustGit(t, origin, "commit", "-m", "fork point")
	mustGit(t, origin, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(origin, "changed.txt"), []byte("pr\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, origin, "add", ".")
	mustGit(t, origin, "commit", "-m", "the PR's change")
	// main advances after the fork point — the normal case, and the one that
	// makes a truncated base ancestry fatal.
	mustGit(t, origin, "checkout", "main")
	if err := os.WriteFile(filepath.Join(origin, "base.txt"), []byte("base moved on\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, origin, "add", ".")
	mustGit(t, origin, "commit", "-m", "base moves on")

	// actions/checkout's shape: a fresh repo with a SINGLE-ref refspec.
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(work, 0o750); err != nil {
		t.Fatal(err)
	}
	mustGit(t, work, "init", "--initial-branch=feature", ".")
	mustGit(t, work, "remote", "add", "origin", origin)
	mustGit(t, work, "config", "remote.origin.fetch", "+refs/heads/feature:refs/remotes/origin/feature")
	mustGit(t, work, "fetch", "--no-tags", "--prune", "origin", "+refs/heads/feature:refs/remotes/origin/feature")
	mustGit(t, work, "checkout", "feature")
	return work
}

// TestActionBaseFetchMakesTheDiffBaseUsable runs the EXACT `git fetch`
// command action.yml ships, against a checkout shaped the way
// actions/checkout shapes one, and then asks git the exact question
// --diff-base asks (`<base>...HEAD`, three dots, against the merge base).
//
// Before the fix the shipped line was `git fetch --no-tags --depth=1 origin
// "$GITHUB_BASE_REF"`, which fails two independent ways, both reproduced
// here in one fixture:
//
//	(a) a bare `origin main` refspec updates only FETCH_HEAD; it writes
//	    refs/remotes/origin/main only if the remote's configured refspec
//	    covers it, and actions/checkout configures a single-ref refspec, so
//	    `origin/main` does not exist → "unknown revision".
//	(b) --depth=1 writes .git/shallow and truncates the base's ancestry,
//	    destroying the merge base the documented `fetch-depth: 0` exists to
//	    provide → "no merge base".
func TestActionBaseFetchMakesTheDiffBaseUsable(t *testing.T) {
	work := actionPRFixture(t)

	line := actionFetchLine(t)
	// The line is shell, with "$GITHUB_BASE_REF" in it; run it as shell with
	// that variable set, exactly as the action's `run:` block does.
	cmd := exec.Command("bash", "-c", "set -euo pipefail; "+line)
	cmd.Dir = work
	cmd.Env = append(os.Environ(), "GITHUB_BASE_REF=main",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("action.yml's fetch failed: %v\n%s", err, out)
	}

	if out, err := runGit(t, work, "rev-parse", "--verify", "refs/remotes/origin/main"); err != nil {
		t.Fatalf("the action's fetch did not create refs/remotes/origin/main, so --diff-base origin/main cannot resolve: %v\n%s", err, out)
	}
	out, err := runGit(t, work, "diff", "--name-only", "origin/main...HEAD")
	if err != nil {
		t.Fatalf("`git diff origin/main...HEAD` (what --diff-base runs) failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "changed.txt") {
		t.Errorf("the three-dot diff should name the PR's own file; got:\n%s", out)
	}
	if strings.Contains(out, "base.txt") {
		t.Errorf("the three-dot diff must exclude files that only moved on the base branch; got:\n%s", out)
	}
}
