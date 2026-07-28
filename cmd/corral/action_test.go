// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

// TestActionNamesTheRecordItProduces: a run's report header is
// "Repo adequacy — <owner>/<repo> @ <commit>". The action passed no --commit
// and `--repo .`, so EmitConfig.Repo was filepath.Base(".") = "." and the
// header read `local/. @ (no commit given)` — a signed, published record that
// names nothing. This asserts the shipped invocation carries the identity
// GitHub already knows, and — separately — that certify --repo really accepts
// those flag names, so the assertion cannot pass on a flag that doesn't exist.
func TestActionNamesTheRecordItProduces(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "action.yml"))
	if err != nil {
		t.Fatalf("reading action.yml: %v", err)
	}
	body := string(b)
	for _, want := range []string{
		"--commit", "github.sha",
		"--owner", "github.repository_owner",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("action.yml does not pass %q: the record it publishes names nothing", want)
		}
	}

	// The flags must exist and parse on the real command, not just look
	// plausible in YAML.
	var out, errb bytes.Buffer
	if code := runCertifyRepo([]string{
		"--repo", t.TempDir(), "--dry-run",
		"--commit", "deadbeef", "--owner", "pdbethke",
	}, &out, &errb); code != 0 {
		t.Fatalf("certify --repo rejected the flags the action passes: exit %d, stderr=%s", code, errb.String())
	}
}

// TestDocsNeverAdvertiseAnUncutActionTag: the docs showed
// `uses: pdbethke/corralai@v1`, and no v1 tag exists (the repo's tags are
// v0.1.0 and v0.2.0) — a copy-pasteable snippet that cannot resolve. The
// project's rule is that documentation describes what exists, so every
// `pdbethke/corralai@<ref>` in the docs must name a ref that resolves: a
// branch that exists, or a tag that has actually been cut.
func TestDocsNeverAdvertiseAnUncutActionTag(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repoRoot := filepath.Join("..", "..")
	tagsOut, err := runGit(t, repoRoot, "tag", "-l")
	if err != nil {
		t.Skipf("git tag: %v: %s", err, tagsOut)
	}
	if strings.TrimSpace(tagsOut) == "" {
		t.Skip("no tags in this clone (shallow/tagless); cannot tell a cut tag from an uncut one")
	}
	tags := map[string]bool{}
	for _, tg := range strings.Fields(tagsOut) {
		tags[tg] = true
	}
	// Branches are resolvable refs too; `main` is where the action lands.
	tags["main"] = true

	ref := regexp.MustCompile(`pdbethke/corralai@([A-Za-z0-9._-]+)`)
	for _, doc := range []string{"README.md", "ROADMAP.md", filepath.Join("docs", "corral", "github-action.md")} {
		b, rerr := os.ReadFile(filepath.Join(repoRoot, doc))
		if rerr != nil {
			t.Fatalf("reading %s: %v", doc, rerr)
		}
		for _, m := range ref.FindAllStringSubmatch(string(b), -1) {
			if !tags[m[1]] {
				t.Errorf("%s advertises %s, but %q is neither an existing tag nor `main` — the snippet does not resolve", doc, m[0], m[1])
			}
		}
	}
}
