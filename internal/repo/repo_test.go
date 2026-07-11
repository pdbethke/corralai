// SPDX-License-Identifier: Elastic-2.0

// internal/repo/repo_test.go
package repo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// makeBareRepoWithCommit creates a bare repo with one commit on branch "main".
func makeBareRepoWithCommit(t *testing.T) (bareURL string) {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "seed")
	run := func(dir string, args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := exec.Command("git", "init", "--bare", "-b", "main", bare).Run(); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(work, 0o755)
	run(work, "init", "-b", "main")
	os.WriteFile(filepath.Join(work, "README.md"), []byte("hello\n"), 0o644)
	run(work, "add", "-A")
	run(work, "commit", "-m", "seed")
	run(work, "remote", "add", "origin", bare)
	run(work, "push", "origin", "main")
	return "file://" + bare
}

func TestCloneCheckoutCommitPush(t *testing.T) {
	bare := makeBareRepoWithCommit(t)
	dest := filepath.Join(t.TempDir(), "work")
	e := New("", "") // no token (file:// remote)
	ctx := context.Background()
	if err := e.Clone(ctx, bare, "main", dest); err != nil {
		t.Fatalf("clone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "README.md")); err != nil {
		t.Fatalf("expected cloned README: %v", err)
	}
	if err := e.Checkout(ctx, dest, "corralai/m1"); err != nil {
		t.Fatalf("checkout: %v", err)
	}
	// empty diff → no commit
	if committed, err := e.Commit(ctx, dest, "noop"); err != nil || committed {
		t.Fatalf("empty commit should be a no-op, got committed=%v err=%v", committed, err)
	}
	os.WriteFile(filepath.Join(dest, "calc.go"), []byte("package calc\n"), 0o644)
	if committed, err := e.Commit(ctx, dest, "build: add calc"); err != nil || !committed {
		t.Fatalf("commit should happen, got committed=%v err=%v", committed, err)
	}
	if err := e.Push(ctx, dest, "corralai/m1"); err != nil {
		t.Fatalf("push: %v", err)
	}
	// the branch now exists on the bare remote
	out, _ := exec.Command("git", "--git-dir", strings.TrimPrefix(bare, "file://"), "branch", "--list", "corralai/m1").CombinedOutput()
	if !strings.Contains(string(out), "corralai/m1") {
		t.Fatalf("pushed branch missing on remote: %q", out)
	}
}

func TestRepoIdent(t *testing.T) {
	e := New("", "")
	o, r, err := e.RepoIdent("https://github.com/pdbethke/corralai.git")
	if err != nil || o != "pdbethke" || r != "corralai" {
		t.Fatalf("ident https: %s/%s err=%v", o, r, err)
	}
	if o, r, _ := e.RepoIdent("git@github.com:pdbethke/corralai.git"); o != "pdbethke" || r != "corralai" {
		t.Fatalf("ident ssh: %s/%s", o, r)
	}
}

// TestTokenNeverPersistedInConfig covers the file:// reset path: even when a
// token is set, a file:// remote means tokenURL() never injects it, and Clone's
// explicit remote reset still leaves no token in .git/config. The actual token
// INJECTION + REDACTION logic (which the file:// path leaves unexercised) is
// proven directly by TestTokenURLInjectionAndRedaction below.
func TestTokenNeverPersistedInConfig(t *testing.T) {
	bare := makeBareRepoWithCommit(t)
	dest := filepath.Join(t.TempDir(), "work")
	e := New("supersecrettoken", "") // token set, but remote is file:// so it isn't used; still must not land in config
	if err := e.Clone(context.Background(), bare, "main", dest); err != nil {
		t.Fatalf("clone: %v", err)
	}
	cfg, _ := os.ReadFile(filepath.Join(dest, ".git", "config"))
	if strings.Contains(string(cfg), "supersecrettoken") {
		t.Fatalf(".git/config leaks the token (jail-readable!):\n%s", cfg)
	}
}

// TestTokenURLInjectionAndRedaction exercises the credential boundary directly,
// without needing a live HTTPS git server: tokenURL must inject the token into
// an https remote and leave non-https/empty-token URLs untouched, and redact
// must scrub the token from any string.
func TestTokenURLInjectionAndRedaction(t *testing.T) {
	e := New("tok123", "")
	if got, want := e.tokenURL("https://github.com/o/r.git"), "https://x-access-token:tok123@github.com/o/r.git"; got != want {
		t.Fatalf("tokenURL https: got %q want %q", got, want)
	}
	// non-https remotes (ssh, file://) are returned unchanged.
	for _, u := range []string{"git@github.com:o/r.git", "file:///tmp/bare.git", "ssh://git@github.com/o/r.git"} {
		if got := e.tokenURL(u); got != u {
			t.Fatalf("tokenURL(%q) should be unchanged, got %q", u, got)
		}
	}
	// an empty token never injects, even for https.
	if e0 := New("", ""); e0.tokenURL("https://github.com/o/r.git") != "https://github.com/o/r.git" {
		t.Fatal("empty token must not inject into the URL")
	}
	// redact scrubs the token wherever it appears.
	if got, want := e.redact("err with tok123 inside"), "err with *** inside"; got != want {
		t.Fatalf("redact: got %q want %q", got, want)
	}
	if e0 := New("", ""); e0.redact("nothing to scrub") != "nothing to scrub" {
		t.Fatal("empty-token redact must be a passthrough")
	}
}

// TestCheckoutPRFetchesHeadAndLandsOnSHA stands up a bare repo, exposes a
// commit at refs/pull/1/head (the GitHub/Gitea PR-head convention), and
// asserts CheckoutPR fetches that ref and leaves the working copy detached
// at exactly the expected sha (the fail-closed check inside CheckoutPR).
func TestCheckoutPRFetchesHeadAndLandsOnSHA(t *testing.T) {
	bare := makeBareRepoWithCommit(t)
	bareDir := strings.TrimPrefix(bare, "file://")

	out, err := exec.Command("git", "--git-dir", bareDir, "rev-parse", "main").CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse main: %v\n%s", err, out)
	}
	sha := strings.TrimSpace(string(out))

	if out, err := exec.Command("git", "--git-dir", bareDir, "update-ref", "refs/pull/1/head", sha).CombinedOutput(); err != nil {
		t.Fatalf("update-ref refs/pull/1/head: %v\n%s", err, out)
	}

	dest := filepath.Join(t.TempDir(), "work")
	e := New("", "") // no token (file:// remote)
	ctx := context.Background()
	if err := e.CheckoutPR(ctx, bare, 1, sha, dest); err != nil {
		t.Fatalf("CheckoutPR: %v", err)
	}

	got, err := exec.Command("git", "-C", dest, "rev-parse", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v\n%s", err, got)
	}
	if strings.TrimSpace(string(got)) != sha {
		t.Fatalf("dest HEAD = %s, want %s", strings.TrimSpace(string(got)), sha)
	}
}

// TestCheckoutPRFailsClosedOnShaMismatch asserts CheckoutPR errors out
// (rather than silently succeeding) when the caller's expected sha doesn't
// match what the PR-head ref actually resolves to.
func TestCheckoutPRFailsClosedOnShaMismatch(t *testing.T) {
	bare := makeBareRepoWithCommit(t)
	bareDir := strings.TrimPrefix(bare, "file://")

	out, err := exec.Command("git", "--git-dir", bareDir, "rev-parse", "main").CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse main: %v\n%s", err, out)
	}
	sha := strings.TrimSpace(string(out))
	if out, err := exec.Command("git", "--git-dir", bareDir, "update-ref", "refs/pull/1/head", sha).CombinedOutput(); err != nil {
		t.Fatalf("update-ref refs/pull/1/head: %v\n%s", err, out)
	}

	dest := filepath.Join(t.TempDir(), "work")
	e := New("", "")
	ctx := context.Background()
	if err := e.CheckoutPR(ctx, bare, 1, "0000000000000000000000000000000000000000", dest); err == nil {
		t.Fatal("expected CheckoutPR to fail closed on sha mismatch, got nil error")
	}
}

// TestCheckoutPRDoesNotPersistTokenInConfig is the regression guard for the
// credential-exfil finding: CheckoutPR must fetch the PR head by an ad-hoc
// (possibly token-carrying) URL WITHOUT ever persisting an "origin" remote
// pointed at that URL into destDir/.git/config — the gate runner bind-mounts
// destDir into the bwrap jail where untrusted PR code runs, and a persisted
// remote config is jail-readable. Mirrors TestTokenNeverPersistedInConfig for
// Clone.
func TestCheckoutPRDoesNotPersistTokenInConfig(t *testing.T) {
	bare := makeBareRepoWithCommit(t)
	bareDir := strings.TrimPrefix(bare, "file://")

	out, err := exec.Command("git", "--git-dir", bareDir, "rev-parse", "main").CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse main: %v\n%s", err, out)
	}
	sha := strings.TrimSpace(string(out))
	if out, err := exec.Command("git", "--git-dir", bareDir, "update-ref", "refs/pull/1/head", sha).CombinedOutput(); err != nil {
		t.Fatalf("update-ref refs/pull/1/head: %v\n%s", err, out)
	}

	dest := filepath.Join(t.TempDir(), "work")
	// token set (as it would be for a real forge), remote is file:// so
	// tokenURL never injects it — but a buggy CheckoutPR would still write
	// a plain "remote add origin <url>" into .git/config, which is exactly
	// what this test guards against regardless of injection.
	e := New("supersecrettoken", "")
	ctx := context.Background()
	if err := e.CheckoutPR(ctx, bare, 1, sha, dest); err != nil {
		t.Fatalf("CheckoutPR: %v", err)
	}

	cfg, err := os.ReadFile(filepath.Join(dest, ".git", "config"))
	if err != nil {
		t.Fatalf("read .git/config: %v", err)
	}
	if strings.Contains(string(cfg), "[remote \"origin\"]") {
		t.Fatalf("CheckoutPR must not persist an origin remote (jail-readable!):\n%s", cfg)
	}
	if strings.Contains(string(cfg), "@") {
		t.Fatalf(".git/config contains a credential-shaped URL (jail-readable!):\n%s", cfg)
	}
}

// TestEngineListOpenPRs verifies that Engine.ListOpenPRs (which takes a
// repoURL) routes through the forge registry to a githubProvider pointed at
// the test server, and parses the PR list into PRRef values.
func TestEngineListOpenPRs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/o/r/pulls" && r.Method == http.MethodGet {
			json.NewEncoder(w).Encode([]map[string]any{
				{
					"number": 7,
					"head":   map[string]any{"sha": "deadbeef", "ref": "corralai/m1"},
					"base":   map[string]any{"ref": "main"},
				},
			})
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	e := New("tok", srv.URL)
	prs, err := e.ListOpenPRs(context.Background(), "https://github.com/o/r", "main")
	if err != nil {
		t.Fatalf("Engine.ListOpenPRs via registry: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("expected 1 PR, got %d", len(prs))
	}
	pr := prs[0]
	if pr.Number != 7 || pr.HeadSHA != "deadbeef" || pr.HeadRef != "corralai/m1" || pr.Base != "main" {
		t.Errorf("PRRef fields wrong: %+v", pr)
	}
}

// TestEngineSetCommitStatus verifies that Engine.SetCommitStatus routes
// through the forge registry to a githubProvider pointed at the test server.
func TestEngineSetCommitStatus(t *testing.T) {
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/o/r/statuses/deadbeef" && r.Method == http.MethodPost {
			hit = true
			w.WriteHeader(201)
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	e := New("tok", srv.URL)
	err := e.SetCommitStatus(context.Background(), "https://github.com/o/r", "deadbeef", "corral/gate", "success", "http://x", "passed")
	if err != nil {
		t.Fatalf("Engine.SetCommitStatus via registry: %v", err)
	}
	if !hit {
		t.Fatal("expected the test server to receive the commit-status POST")
	}
}
