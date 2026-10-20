// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitFixture(t *testing.T, origin string) (dir, head string) {
	t.Helper()
	dir = t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	run("init", "-q")
	if origin != "" {
		run("remote", "add", "origin", origin)
	}
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "f")
	run("commit", "-q", "-m", "c")
	head = run("rev-parse", "HEAD")
	return dir, head[:len(head)-1]
}

// The process cwd here is the corral checkout (itself a git repo with an
// origin); the answer must be the --repo-dir's, not that one's.
func TestLocalSubjectFromRepoDirNotCwd(t *testing.T) {
	dir, head := gitFixture(t, "https://example.test/acme/widget.git")
	repo, commit, id := localSubject(dir)
	if repo != "https://example.test/acme/widget" || commit != head || id != "git" {
		t.Fatalf("got (%q, %q, %q), want the fixture's origin, %s, git", repo, commit, id, head)
	}
}

func TestLocalSubjectNoOrigin(t *testing.T) {
	dir, head := gitFixture(t, "")
	repo, commit, id := localSubject(dir)
	if repo != "" || id != "none" {
		t.Fatalf("got repo=%q identity=%q; an unresolved repo is \"\" with identity none, never a basename or \"local\"", repo, id)
	}
	if commit != head {
		t.Errorf("the commit that did resolve must still be reported: got %q want %q", commit, head)
	}
}

func TestLocalSubjectNotAGitDir(t *testing.T) {
	repo, commit, id := localSubject(t.TempDir())
	if repo != "" || commit != "" || id != "none" {
		t.Fatalf("got (%q, %q, %q)", repo, commit, id)
	}
}

func TestLocalSubjectNoRepoDir(t *testing.T) {
	repo, commit, id := localSubject("  ")
	if repo != "" || commit != "" || id != "none" {
		t.Fatalf("single-file mode: got (%q, %q, %q), want \"\", \"\", none", repo, commit, id)
	}
}

func TestMachineLedgerDirCreatedFromOverride(t *testing.T) {
	want := filepath.Join(t.TempDir(), "nested", "ledger")
	t.Setenv("CORRALAI_MACHINE_LEDGER", want)
	got, err := machineLedgerDir()
	if err != nil || got != want {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
	fi, err := os.Stat(want)
	if err != nil || !fi.IsDir() {
		t.Fatalf("not created: %v", err)
	}
	if m := fi.Mode().Perm(); m != 0o750 {
		t.Errorf("mode %o, want 750", m)
	}
}

func TestResolveSubjectIsAPair(t *testing.T) {
	dir, head := gitFixture(t, "https://example.test/acme/widget.git")
	if r, c, id, err := resolveSubject("X", "abc", dir); err != nil || r != "X" || c != "abc" || id != subjectIdentityOperator {
		t.Errorf("both given must be used as typed, identity operator: (%q, %q, %q, %v)", r, c, id, err)
	}
	if r, c, id, err := resolveSubject("", "", dir); err != nil || r != "https://example.test/acme/widget" || c != head || id != subjectIdentityGit {
		t.Errorf("neither given must derive both, identity git: (%q, %q, %q, %v)", r, c, id, err)
	}
	for _, tc := range [][2]string{{"X", ""}, {"", "abc"}} {
		if _, _, _, err := resolveSubject(tc[0], tc[1], dir); err == nil || !strings.Contains(err.Error(), "both or neither") {
			t.Errorf("repo=%q commit=%q: want a both-or-neither refusal, got %v", tc[0], tc[1], err)
		}
	}
}

func TestRunCertifyLocal_OneOfRepoCommitIsRefused(t *testing.T) {
	for _, extra := range [][]string{{"--repo", "X"}, {"--commit", "abc"}} {
		var stdout, stderr bytes.Buffer
		code := runCertifyLocal(append([]string{"--code", "foo.go", "--goal", "correctness"}, extra...), &stdout, &stderr)
		if code != 2 || !strings.Contains(stderr.String(), "both or neither") {
			t.Errorf("%v: want exit 2 naming both-or-neither, got %d %q", extra, code, stderr.String())
		}
	}
}

// treeFixture makes a committed one-file repository.
func treeFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, argv := range [][]string{
		{"init"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "seed"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, argv...)...).CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v: %s", err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{
		{"add", "a.txt"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "add a"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, argv...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", argv, err, out)
		}
	}
	return dir
}

func wantDirty(t *testing.T, got *bool, want bool) {
	t.Helper()
	if got == nil {
		t.Fatalf("treeDirty = nil, want %v", want)
	}
	if *got != want {
		t.Fatalf("treeDirty = %v, want %v", *got, want)
	}
}

func TestTreeDirtyClean(t *testing.T) {
	wantDirty(t, treeDirty(treeFixture(t)), false)
}

func TestTreeDirtyModifiedTracked(t *testing.T) {
	dir := treeFixture(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantDirty(t, treeDirty(dir), true)
}

func TestTreeDirtyUntrackedFile(t *testing.T) {
	dir := treeFixture(t)
	// Inside a new directory: --untracked-files=all is what stops git
	// reporting only "newdir/" (still dirty, but the property is the file).
	if err := os.MkdirAll(filepath.Join(dir, "newdir"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "newdir", "b.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantDirty(t, treeDirty(dir), true)
}

func TestTreeDirtyIgnoresCorralDir(t *testing.T) {
	dir := treeFixture(t)
	if err := os.MkdirAll(filepath.Join(dir, ".corral", "ledger"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".corral", "ledger", "x"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantDirty(t, treeDirty(dir), false)
}

func TestTreeDirtyNotARepo(t *testing.T) {
	if got := treeDirty(t.TempDir()); got != nil {
		t.Fatalf("treeDirty(non-repo) = %v, want nil (unknown, never clean)", *got)
	}
	if got := treeDirty(""); got != nil {
		t.Fatalf("treeDirty(\"\") = %v, want nil", *got)
	}
}

// A repoDir that is a subdirectory of the work tree: its own .corral/ is
// corral's, the sibling file's change is not.
func TestTreeDirtySubdirCorralDir(t *testing.T) {
	dir := treeFixture(t)
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(filepath.Join(sub, ".corral"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, ".corral", "x"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantDirty(t, treeDirty(sub), false)
}

// --repo with no origin falls back to the directory basename for the repo
// string; that must be labelled "none", while a real origin is "git".
func TestRepoIdentityBasenameFallbackIsNone(t *testing.T) {
	dir := treeFixture(t)
	if got := repoIdentity(dir); got != subjectIdentityNone {
		t.Fatalf("repoIdentity(no origin) = %q, want %q", got, subjectIdentityNone)
	}
	if out, err := exec.Command("git", "-C", dir, "remote", "add", "origin", "https://github.com/acme/widget.git").CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v: %s", err, out)
	}
	if got := repoIdentity(dir); got != subjectIdentityGit {
		t.Fatalf("repoIdentity(origin) = %q, want %q", got, subjectIdentityGit)
	}
}

// One checkout is ONE repository name, whichever verb ran: --local
// (localSubject), --repo and review (resolveRepoName) and the identity word
// (repoIdentity) all read originRepoName. --local used to keep the ".git"
// suffix --repo stripped, filing one checkout as two repositories.
func TestOneCheckoutIsOneRepoNameAcrossVerbs(t *testing.T) {
	for _, origin := range []string{
		"https://github.com/o/r.git", "https://github.com/o/r", "git@github.com:o/r.git",
	} {
		dir, _ := gitFixture(t, origin)
		local, _, id := localSubject(dir)
		repo := resolveRepoName(dir, "")
		if local != repo || local == "" {
			t.Errorf("origin %s: --local records %q, --repo records %q; want one name", origin, local, repo)
		}
		if strings.HasSuffix(local, ".git") {
			t.Errorf("origin %s: recorded %q keeps the transport suffix", origin, local)
		}
		if id != subjectIdentityGit || repoIdentity(dir) != subjectIdentityGit {
			t.Errorf("origin %s: identities %q / %q, want git / git", origin, id, repoIdentity(dir))
		}
	}
}

// TreeDirty is "differed from HEAD", so it is measured only when the
// recorded commit IS HEAD — by full SHA, an abbreviation counting only when
// git resolves it to HEAD. Any other commit, an unresolvable one, or none is
// nil: unknown, never a false that reads as "this commit, unmodified".
func TestTreeDirtyAtOnlyMeasuresHead(t *testing.T) {
	dir := treeFixture(t)
	first := strings.TrimSpace(gitOut(t, dir, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun := gitCmd(t, dir)
	gitRun("add", "b.txt")
	gitRun("commit", "-q", "-m", "second", "--no-gpg-sign")
	head := strings.TrimSpace(gitOut(t, dir, "rev-parse", "HEAD"))

	wantDirty(t, treeDirtyAt(dir, head), false)
	wantDirty(t, treeDirtyAt(dir, head[:10]), false)
	for _, c := range []string{first, first[:10], "deadbeefdeadbeef", "", "  ", "--all", "HEAD~1"} {
		if got := treeDirtyAt(dir, c); got != nil {
			t.Errorf("treeDirtyAt(%q) = %v, want nil: the recorded commit is not HEAD", c, *got)
		}
	}
	// A dirty tree is still reported dirty against HEAD.
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantDirty(t, treeDirtyAt(dir, head), true)
	if got := treeDirtyAt(t.TempDir(), head); got != nil {
		t.Errorf("treeDirtyAt(not a repo) = %v, want nil", *got)
	}
}
