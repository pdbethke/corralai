# Repo-Work Mode (git → PR) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A mission operates on a real git repo and produces a PR — the brain owns all privileged git (clone/commit/push/PR + the token), the jail stays secret-free, and the brain serves the repo over a read MCP surface.

**Architecture:** New `internal/repo` git/PR engine (shells to `git`, GitHub REST over HTTP). The mission engine commits gate-passed phases on a feature branch and pushes + opens a PR when the mission completes. The brain's `create_mission` provisions the clone; three read tools (`read_repo`/`repo_tree`/`repo_grep`) serve the working copy. The token lives only in the brain and is never persisted where the jail can read it.

**Tech Stack:** Go 1.26; `git` CLI (added to the brain image); GitHub REST API over `net/http`; existing mission engine (`internal/mission`), queue, sandbox.

## Global Constraints

- **Secrets live only in the brain; the jail stays secret-free.** The token is read ONLY in `cmd/corral`, lives ONLY in `repo.Engine`, never enters `sandbox.MinimalEnv` or a bee's env, and is never logged (redact URL userinfo).
- **Never persist the token in the working copy.** The jail bind-mounts the working copy → a bee can `cat .git/config`. `Clone` resets `origin` to the token-LESS URL; `Push` supplies the token via a one-shot explicit URL on the command line, never in `.git/config`. A test asserts the post-clone `.git/config` has no token.
- **Brain owns all git** (clone/commit/push/PR); bees never invoke them.
- **Per gate-passed phase → one commit** (idempotent: commit each phase exactly once); mission `done` → push + PR.
- **Graceful:** no token + public/local repo → clone/commit work, push/PR skipped cleanly; push/PR failure → the local branch survives, logged, mission still completes. A plain (no-repo) mission is unchanged.
- **PR via GitHub REST API** (`POST /repos/{owner}/{repo}/pulls`) — no `gh` CLI.
- Path-confined reads: `read_repo`/`repo_tree`/`repo_grep` reject `..`/absolute escapes.
- The agent binary build is untouched (no CGO concern here); `go build ./...` stays clean each task.

---

## File Structure

- `internal/repo/repo.go` (create) — `Engine` + git ops (`Clone`/`Checkout`/`Commit`/`Push`/`RepoIdent`) + token injection/redaction.
- `internal/repo/pr.go` (create) — `OpenPR` (GitHub REST).
- `internal/repo/read.go` (create) — `ReadFile`/`Tree`/`Grep` (path-confined).
- `internal/repo/*_test.go` (create) — git ops against a `file://` bare repo; PR against a stub server; reads + escape rejection; token-redaction + no-token-in-config.
- `internal/mission/store.go` (modify) — `Repo`/`Base`/`Branch`/`PRURL` columns + struct fields + `SetRepo`.
- `internal/mission/engine.go` (modify) — `RepoOps` interface; `Engine.Repo`/`Workspace`/`committed`; per-phase commit + done push/PR in `Tick`.
- `internal/mission/engine_test.go` (modify/create) — fake `RepoOps` spy.
- `internal/brain/missions.go` (modify) — `create_mission` gains `repo`/`base`, provisions.
- `internal/brain/repofiles.go` (create) — `read_repo`/`repo_tree`/`repo_grep` tools.
- `internal/brain/identity.go` (modify) — `Options.Repo`/`Workspace`.
- `internal/brain/server.go` (modify) — register the read tools.
- `cmd/corral/main.go` (modify) — construct `repo.Engine` from env; wire engine + brain.
- `internal/sandbox/sandbox_test.go` (modify) — credential-boundary test.
- `deploy/demo/Dockerfile.brain`, `deploy/demo/docker-compose.yml` (modify) — `git`; brain mounts workspace + token.

---

## Task 1: `internal/repo` engine — git ops + token discipline

**Files:** Create `internal/repo/repo.go`, `internal/repo/repo_test.go`

**Interfaces:**
- Produces: `type Engine struct{...}`; `func New(token, apiBase string) *Engine`;
  `func (e *Engine) Clone(ctx context.Context, repoURL, base, destDir string) error`;
  `func (e *Engine) Checkout(ctx context.Context, dir, branch string) error`;
  `func (e *Engine) Commit(ctx context.Context, dir, message string) (committed bool, err error)`;
  `func (e *Engine) Push(ctx context.Context, dir, branch string) error`;
  `func (e *Engine) RepoIdent(repoURL string) (owner, repo string, err error)` — a **method** on `Engine` (so `*repo.Engine` satisfies the `RepoOps` interface in Task 4). The per-mission clone dir is `mission.MissionDir(workspace, id)` = `<workspace>/m<id>` (Task 3), NOT a repo-name dir — so concurrent missions never collide and #16's snapshot resolves the same path.

- [ ] **Step 1: Write the failing test**

```go
// internal/repo/repo_test.go
package repo

import (
	"context"
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
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/repo/`
Expected: FAIL — package/`New` undefined.

- [ ] **Step 3: Implement `internal/repo/repo.go`**

```go
// Package repo is the brain-side git/PR engine. The brain (trusted plane) owns all
// privileged git — clone, commit, push, PR — and the only copy of the token. The
// token is injected into the HTTPS remote only for the network call and is NEVER
// persisted in the working copy's .git/config (the jail bind-mounts the working copy
// and a bee could read it), and never logged.
package repo

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type Engine struct {
	token   string
	apiBase string
}

func New(token, apiBase string) *Engine {
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}
	return &Engine{token: token, apiBase: apiBase}
}

// RepoIdent parses owner/repo from https or ssh GitHub URLs. A method (not a package
// func) so *Engine satisfies the mission engine's RepoOps interface.
func (e *Engine) RepoIdent(repoURL string) (owner, repo string, err error) {
	s := strings.TrimSuffix(repoURL, ".git")
	s = strings.TrimPrefix(s, "https://github.com/")
	s = strings.TrimPrefix(s, "git@github.com:")
	parts := strings.Split(strings.Trim(s, "/"), "/")
	if len(parts) < 2 || parts[len(parts)-2] == "" || parts[len(parts)-1] == "" {
		return "", "", fmt.Errorf("cannot parse owner/repo from %q", repoURL)
	}
	return parts[len(parts)-2], parts[len(parts)-1], nil
}

// tokenURL injects the token into an https URL for a one-shot network call. Returns
// the URL unchanged for non-https (file://, ssh) or an empty token.
func (e *Engine) tokenURL(u string) string {
	if e.token == "" || !strings.HasPrefix(u, "https://") {
		return u
	}
	return "https://x-access-token:" + e.token + "@" + strings.TrimPrefix(u, "https://")
}

// redact removes the token from a string for safe logging/errors.
func (e *Engine) redact(s string) string {
	if e.token == "" {
		return s
	}
	return strings.ReplaceAll(s, e.token, "***")
}

func (e *Engine) git(ctx context.Context, dir string, args ...string) (string, error) {
	c := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		c.Dir = dir
	}
	// deterministic identity for commits; no global config dependency.
	c.Env = append([]string{
		"GIT_AUTHOR_NAME=corralai", "GIT_AUTHOR_EMAIL=corralai@local",
		"GIT_COMMITTER_NAME=corralai", "GIT_COMMITTER_EMAIL=corralai@local",
		"GIT_TERMINAL_PROMPT=0",
	}, envPassthrough()...)
	var buf bytes.Buffer
	c.Stdout, c.Stderr = &buf, &buf
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, e.redact(buf.String()))
	}
	return buf.String(), nil
}

// envPassthrough is PATH/HOME/etc. so git finds itself + ssl certs; NO secrets.
func envPassthrough() []string {
	var out []string
	for _, k := range []string{"PATH", "HOME", "SSL_CERT_FILE", "SSL_CERT_DIR", "GIT_SSL_CAINFO"} {
		if v := osGetenv(k); v != "" {
			out = append(out, k+"="+v)
		}
	}
	return out
}

func (e *Engine) Clone(ctx context.Context, repoURL, base, destDir string) error {
	if base == "" {
		base = "main"
	}
	if _, err := e.git(ctx, "", "clone", "--branch", base, "--single-branch", e.tokenURL(repoURL), destDir); err != nil {
		return err
	}
	// CRITICAL: never leave the token in the working copy's config (jail-readable).
	_, err := e.git(ctx, destDir, "remote", "set-url", "origin", repoURL)
	return err
}

func (e *Engine) Checkout(ctx context.Context, dir, branch string) error {
	_, err := e.git(ctx, dir, "checkout", "-b", branch)
	return err
}

func (e *Engine) Commit(ctx context.Context, dir, message string) (bool, error) {
	if _, err := e.git(ctx, dir, "add", "-A"); err != nil {
		return false, err
	}
	out, err := e.git(ctx, dir, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(out) == "" {
		return false, nil // empty diff → no commit
	}
	if _, err := e.git(ctx, dir, "commit", "-m", message); err != nil {
		return false, err
	}
	return true, nil
}

func (e *Engine) Push(ctx context.Context, dir, branch string) error {
	// one-shot token URL on the command line; never stored in config. Read origin's
	// clean URL, push with the token-injected form.
	origin, err := e.git(ctx, dir, "remote", "get-url", "origin")
	if err != nil {
		return err
	}
	_, err = e.git(ctx, dir, "push", e.tokenURL(strings.TrimSpace(origin)), "HEAD:refs/heads/"+branch)
	return err
}
```

Add a small `osGetenv` shim at the top (or just use `os.Getenv` directly and import `os`):
```go
import "os"
func osGetenv(k string) string { return os.Getenv(k) }
```
(or replace `osGetenv` calls with `os.Getenv` and import `os`.)

- [ ] **Step 4: Run tests**

Run: `go test ./internal/repo/`
Expected: PASS — clone/checkout/commit(+empty no-op)/push work; ident parse; `.git/config` has no token.

- [ ] **Step 5: Commit**

```bash
git add internal/repo/repo.go internal/repo/repo_test.go
git commit -m "feat(repo): git engine (clone/checkout/commit/push) with token never persisted in the working copy"
```

---

## Task 2: `internal/repo` — OpenPR (REST) + read surface

**Files:** Create `internal/repo/pr.go`, `internal/repo/read.go`, `internal/repo/pr_test.go`, `internal/repo/read_test.go`

**Interfaces:**
- Consumes: `Engine`, `e.token`, `e.apiBase` (Task 1).
- Produces:
  - `func (e *Engine) OpenPR(ctx context.Context, owner, repo, head, base, title, body string) (prURL string, err error)`
  - `func (e *Engine) ReadFile(dir, path string) (string, error)`
  - `func (e *Engine) Tree(dir, subdir string) ([]string, error)`
  - `func (e *Engine) Grep(dir, query string, k int) ([]string, error)`
  - `func safeJoin(dir, rel string) (string, error)` — rejects `..`/absolute escapes.

- [ ] **Step 1: Write the failing tests**

```go
// internal/repo/pr_test.go
package repo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenPR(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		gotBody, _ = in["head"].(string)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"html_url": "https://github.com/o/r/pull/7"})
	}))
	defer srv.Close()
	e := New("tok123", srv.URL)
	url, err := e.OpenPR(context.Background(), "o", "r", "corralai/m1", "main", "T", "B")
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://github.com/o/r/pull/7" {
		t.Fatalf("pr url: %s", url)
	}
	if !strings.Contains(gotAuth, "tok123") || gotBody != "corralai/m1" {
		t.Fatalf("request wrong: auth=%q head=%q", gotAuth, gotBody)
	}
}
```

```go
// internal/repo/read_test.go
package repo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadSurfaceAndEscape(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "pkg"), 0o755)
	os.WriteFile(filepath.Join(dir, "pkg", "a.go"), []byte("package pkg\nfunc Auth() {}\n"), 0o644)
	e := New("", "")
	if c, err := e.ReadFile(dir, "pkg/a.go"); err != nil || !contains(c, "func Auth") {
		t.Fatalf("read: %q err=%v", c, err)
	}
	if _, err := e.ReadFile(dir, "../secret"); err == nil {
		t.Fatal("escape via .. must be rejected")
	}
	tree, err := e.Tree(dir, "")
	if err != nil || !hasItem(tree, "pkg/a.go") {
		t.Fatalf("tree: %v %v", tree, err)
	}
	hits, err := e.Grep(dir, "Auth", 10)
	if err != nil || len(hits) == 0 || !contains(hits[0], "a.go") {
		t.Fatalf("grep: %v %v", hits, err)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (s == sub || (len(sub) > 0 && stringIndex(s, sub) >= 0)) }
func stringIndex(s, sub string) int { for i := 0; i+len(sub) <= len(s); i++ { if s[i:i+len(sub)] == sub { return i } }; return -1 }
func hasItem(xs []string, x string) bool { for _, v := range xs { if v == x { return true } }; return false }
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/repo/ -run 'TestOpenPR|TestReadSurface'`
Expected: FAIL — `OpenPR`/`ReadFile` undefined.

- [ ] **Step 3: Implement `pr.go`**

```go
// internal/repo/pr.go
package repo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// OpenPR creates a pull request via the GitHub REST API. Returns the PR's html_url.
func (e *Engine) OpenPR(ctx context.Context, owner, repo, head, base, title, body string) (string, error) {
	payload, _ := json.Marshal(map[string]any{"title": title, "head": head, "base": base, "body": body})
	url := e.apiBase + "/repos/" + owner + "/" + repo + "/pulls"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.github+json")
	if e.token != "" {
		req.Header.Set("Authorization", "Bearer "+e.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("open PR: %s: %s", resp.Status, e.redact(string(b)))
	}
	var out struct {
		HTMLURL string `json:"html_url"`
	}
	json.Unmarshal(b, &out)
	return out.HTMLURL, nil
}
```

- [ ] **Step 4: Implement `read.go`**

```go
// internal/repo/read.go
package repo

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const maxReadBytes = 256 * 1024

// safeJoin resolves rel under dir, rejecting absolute paths and any .. escape.
func safeJoin(dir, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("absolute path not allowed: %q", rel)
	}
	full := filepath.Join(dir, rel)
	rp, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	base, _ := filepath.Abs(dir)
	if rp != base && !strings.HasPrefix(rp, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes the repo: %q", rel)
	}
	return rp, nil
}

func (e *Engine) ReadFile(dir, path string) (string, error) {
	p, err := safeJoin(dir, path)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	if len(b) > maxReadBytes {
		b = b[:maxReadBytes]
	}
	return string(b), nil
}

func skip(name string) bool { return name == ".git" || name == "node_modules" || name == "vendor" }

func (e *Engine) Tree(dir, subdir string) ([]string, error) {
	root, err := safeJoin(dir, subdir)
	if err != nil {
		return nil, err
	}
	var out []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skip(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		out = append(out, rel)
		return nil
	})
	return out, nil
}

func (e *Engine) Grep(dir, query string, k int) ([]string, error) {
	if k <= 0 {
		k = 20
	}
	var out []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || len(out) >= k {
			return nil
		}
		if d.IsDir() {
			if skip(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil || len(b) > maxReadBytes {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		for i, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, query) {
				out = append(out, fmt.Sprintf("%s:%d: %s", rel, i+1, strings.TrimSpace(line)))
				if len(out) >= k {
					break
				}
			}
		}
		return nil
	})
	return out, nil
}
```

- [ ] **Step 5: Run tests + commit**

Run: `go test ./internal/repo/`
Expected: PASS.

```bash
git add internal/repo/pr.go internal/repo/read.go internal/repo/pr_test.go internal/repo/read_test.go
git commit -m "feat(repo): OpenPR via GitHub REST + path-confined read surface"
```

---

## Task 3: mission model — repo columns + SetRepo

**Files:** Modify `internal/mission/store.go`; Test `internal/mission/store_test.go`

**Interfaces:**
- Produces: `Mission` gains `Repo`, `Base`, `Branch`, `PRURL string`;
  `func (s *Store) SetRepo(id int64, repo, base, branch string) error`;
  `func (s *Store) SetPRURL(id int64, url string) error`;
  `func MissionDir(workspace string, id int64) string` (= `<workspace>/m<id>`, the
  per-mission clone-dir convention used by the engine, the brain provisioning, the
  read tools, and #16's snapshot).

- [ ] **Step 1: Write the failing test**

```go
// internal/mission/store_test.go — add (reuse the existing temp-store helper)
func TestMissionRepoFields(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "m.duckdb"))
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { s.Close() })
	q := openQueue(t, dir) // use whatever the existing tests use to build a queue.Store
	id, err := CreateMission(s, q, "build calc", DefaultPlan("build calc"), false)
	if err != nil { t.Fatal(err) }
	if err := s.SetRepo(id, "https://github.com/o/r", "main", "corralai/m1"); err != nil {
		t.Fatal(err)
	}
	m, err := s.Mission(id)
	if err != nil || m.Repo != "https://github.com/o/r" || m.Base != "main" || m.Branch != "corralai/m1" {
		t.Fatalf("repo fields not persisted: %+v err=%v", m, err)
	}
	if err := s.SetPRURL(id, "https://github.com/o/r/pull/7"); err != nil { t.Fatal(err) }
	m, _ = s.Mission(id)
	if m.PRURL != "https://github.com/o/r/pull/7" {
		t.Fatalf("PRURL not persisted: %+v", m)
	}
	if got := MissionDir("/ws", 42); got != "/ws/m42" {
		t.Fatalf("MissionDir = %q, want /ws/m42", got)
	}
}
```

> NOTE: read `internal/mission/store_test.go` for how existing tests construct the `queue.Store` (the `q` arg to `CreateMission`) and the mission store; reuse those helpers rather than inventing `openQueue`.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/mission/ -run TestMissionRepoFields`
Expected: FAIL — `SetRepo`/`Mission.Repo` undefined.

- [ ] **Step 3: Implement**

In `internal/mission/store.go`:
(a) After the missions-table creation in `Open`, add idempotent migrations (mirror the existing `ALTER TABLE` idempotent pattern used elsewhere — wrap with the duplicate-column tolerance this store uses):
```go
	s.db.Exec(`ALTER TABLE missions ADD COLUMN repo VARCHAR NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE missions ADD COLUMN base VARCHAR NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE missions ADD COLUMN branch VARCHAR NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE missions ADD COLUMN pr_url VARCHAR NOT NULL DEFAULT ''`)
```
(b) Add to the `Mission` struct:
```go
	Repo   string `json:"repo,omitempty"`
	Base   string `json:"base,omitempty"`
	Branch string `json:"branch,omitempty"`
	PRURL  string `json:"pr_url,omitempty"`
```
(c) Add the columns to the SELECT + scan in `Mission(id)` and `RunningMissions()` (both read missions). Append `repo, base, branch, pr_url` to each SELECT column list and scan into the new fields.
(d) Setters:
```go
func (s *Store) SetRepo(id int64, repo, base, branch string) error {
	_, err := s.db.Exec(`UPDATE missions SET repo=?, base=?, branch=?, updated_ts=? WHERE id=?`, repo, base, branch, now(), id)
	return err
}
func (s *Store) SetPRURL(id int64, url string) error {
	_, err := s.db.Exec(`UPDATE missions SET pr_url=?, updated_ts=? WHERE id=?`, url, now(), id)
	return err
}
```
(use the store's existing `now()` helper; if `updated_ts` isn't a column, drop it from the UPDATEs.)
(e) The per-mission clone-dir convention (add `"path/filepath"` + `"strconv"` if not already imported):
```go
// MissionDir is the per-mission working-copy path: <workspace>/m<id>. The engine,
// brain provisioning, the read tools, and #16's snapshot all resolve here, so a
// mission's working copy is keyed by id (concurrent missions never collide).
func MissionDir(workspace string, id int64) string {
	return filepath.Join(workspace, "m"+strconv.FormatInt(id, 10))
}
```

- [ ] **Step 4: Run tests + commit**

Run: `go test ./internal/mission/`
Expected: PASS (new + existing; migration idempotent on re-open).

```bash
git add internal/mission/store.go internal/mission/store_test.go
git commit -m "feat(mission): repo/base/branch/pr_url fields + SetRepo/SetPRURL"
```

---

## Task 4: engine integration — per-phase commit + done push/PR

**Files:** Modify `internal/mission/engine.go`; Test `internal/mission/engine_test.go`

**Interfaces:**
- Consumes: `repo.Engine` methods via a local interface; `Store.Phases`, `Store.Mission`, `Store.SetPRURL`, `MissionDir` (Task 3).
- Produces:
  - `type RepoOps interface { Commit(ctx, dir, msg string) (bool, error); Push(ctx, dir, branch string) error; OpenPR(ctx, owner, repo, head, base, title, body string) (string, error); RepoIdent(repoURL string) (owner, repo string, err error) }`
  - `Engine.Repo RepoOps`, `Engine.Workspace string`, and an internal `committed map[int64]map[string]bool`.

- [ ] **Step 1: Write the failing test**

```go
// internal/mission/engine_test.go — add
import "context"

type fakeRepo struct {
	commits []string
	pushed  bool
	prURL   string
}
func (f *fakeRepo) Commit(_ context.Context, _ , msg string) (bool, error) { f.commits = append(f.commits, msg); return true, nil }
func (f *fakeRepo) Push(_ context.Context, _, _ string) error             { f.pushed = true; return nil }
func (f *fakeRepo) OpenPR(_ context.Context, o, r, head, base, title, body string) (string, error) { return "https://github.com/" + o + "/" + r + "/pull/1", nil }
func (f *fakeRepo) RepoIdent(_ string) (string, string, error)            { return "o", "r", nil }

func TestEnginePhaseCommitAndPRForRepoMission(t *testing.T) {
	// Build a mission store + queue with a tiny repo mission whose single phase
	// completes, then assert: one commit happened with the phase in the message, and
	// on mission-done Push+OpenPR fired and PRURL was stored.
	// Implementer: construct the store/queue as the existing engine tests do; create a
	// mission via CreateMission with a one-phase plan; SetRepo(id, "https://github.com/o/r","main","corralai/m1");
	// claim+complete the phase's task(s) so MissionDone is true; set e.Repo=&fakeRepo{},
	// e.Workspace=t.TempDir(); call e.Tick() (possibly twice — once to commit the done
	// phase, once for mission-done). Assert fake.commits has one entry containing the
	// phase name, fake.pushed is true, and s.Mission(id).PRURL != "".
}
```

> Implementer: flesh out the body using the EXISTING engine-test harness in `internal/mission/engine_test.go` (it already builds a store + queue and drives `Tick`). The fake above is complete; wire it via `e.Repo = &fakeRepo{}` and `e.Workspace = t.TempDir()`. A plain (no-repo) mission must NOT call the fake — add a one-line assertion that a no-repo mission leaves `fake.commits` empty.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/mission/ -run TestEnginePhaseCommit`
Expected: FAIL — `Engine.Repo` undefined.

- [ ] **Step 3: Implement**

In `internal/mission/engine.go`:
(a) Add the interface + fields:
```go
type RepoOps interface {
	Commit(ctx context.Context, dir, msg string) (bool, error)
	Push(ctx context.Context, dir, branch string) error
	OpenPR(ctx context.Context, owner, repo, head, base, title, body string) (string, error)
	RepoIdent(repoURL string) (owner, repo string, err error)
}
```
Add to `Engine`: `Repo RepoOps`, `Workspace string`, `committed map[int64]map[string]bool`. Initialize the map in `NewEngine` (`committed: map[int64]map[string]bool{}`). Add `"context"` (and `"log"` if not already imported). `engine.go` is in package `mission`, so it calls `MissionDir` (Task 3) directly — no `repo` import is needed (the `RepoOps` interface decouples it from the `repo` package).

(b) In `Tick`, for each running mission with a repo, after `PromoteReady` and before/around the done-check, commit newly-done phases:
```go
		if e.Repo != nil {
			if full, err := e.m.Mission(mi.ID); err == nil && full != nil && full.Repo != "" {
				e.commitDonePhases(full)
			}
		}
```
and after setting status to `"done"` for a non-review mission (or when review-accepted elsewhere completes it), push+PR:
```go
		} else {
			_ = e.m.SetMissionStatus(mi.ID, "done")
			if e.Repo != nil {
				e.finishRepoMission(mi.ID)
			}
		}
```

(c) The helpers:
```go
func (e *Engine) workdir(m *Mission) string { return MissionDir(e.Workspace, m.ID) }

func (e *Engine) commitDonePhases(m *Mission) {
	phases, err := e.m.Phases(m.ID)
	if err != nil {
		return
	}
	seen := e.committed[m.ID]
	if seen == nil {
		seen = map[string]bool{}
		e.committed[m.ID] = seen
	}
	for _, p := range phases {
		if p.Status != "done" || seen[p.Name] {
			continue
		}
		seen[p.Name] = true // mark first so an error doesn't re-commit
		if ok, err := e.Repo.Commit(context.Background(), e.workdir(m), p.Name+": "+m.Directive); err != nil {
			log.Printf("mission %d: commit phase %s: %v", m.ID, p.Name, err)
		} else if ok {
			log.Printf("mission %d: committed phase %s", m.ID, p.Name)
		}
	}
}

func (e *Engine) finishRepoMission(id int64) {
	m, err := e.m.Mission(id)
	if err != nil || m == nil || m.Repo == "" {
		return
	}
	e.commitDonePhases(m) // catch any final phase
	if err := e.Repo.Push(context.Background(), e.workdir(m), m.Branch); err != nil {
		log.Printf("mission %d: push: %v (local branch preserved)", id, err)
		return
	}
	owner, rp, err := e.Repo.RepoIdent(m.Repo)
	if err != nil {
		log.Printf("mission %d: repo ident: %v", id, err)
		return
	}
	url, err := e.Repo.OpenPR(context.Background(), owner, rp, m.Branch, m.Base, "corralai: "+m.Directive, "Built by the corralai swarm.")
	if err != nil {
		log.Printf("mission %d: open PR: %v", id, err)
		return
	}
	_ = e.m.SetPRURL(id, url)
	log.Printf("mission %d: PR opened: %s", id, url)
}
```
(`Phase` has `Name`/`Status`; confirm field names from `store.go`'s `Phase` type and adjust if different.)

- [ ] **Step 4: Run tests + commit**

Run: `go test ./internal/mission/ && go build ./...`
Expected: PASS; build OK.

```bash
git add internal/mission/engine.go internal/mission/engine_test.go
git commit -m "feat(mission): per-phase commit + push/PR on mission done (RepoOps)"
```

---

## Task 5: brain — create_mission provisioning + repo read tools

**Files:** Modify `internal/brain/missions.go`, `internal/brain/identity.go`, `internal/brain/server.go`; Create `internal/brain/repofiles.go`; Test `internal/brain/repofiles_test.go`

**Interfaces:**
- Consumes: `repo.Engine` (Tasks 1–2); `mission.SetRepo`, `mission.Mission` (Task 3); `queue.ClaimedMission`; `identity(req, ...)`.
- Produces: `Options.Repo *repo.Engine`, `Options.Workspace string`; `create_mission` gains `Repo`/`Base`; tools `read_repo`/`repo_tree`/`repo_grep`.

- [ ] **Step 1: Write the failing test (read tools, escape, no-claim)**

```go
// internal/brain/repofiles_test.go
package brain

// Construct a brain with Options{Queue:q, Missions:mstore, Repo:repo.New("",""), Workspace:ws}.
// Seed a repo mission whose working dir <ws>/<name> has a file. A bee claims the
// mission's task. Then read_repo over MCP returns the file; repo_grep finds a match;
// read_repo with "../x" is rejected; a caller with no claimed repo mission gets an error.
// Implementer: mirror the harness in internal/brain/memory_test.go / tasks_test.go
// (in-memory MCP transport). The key assertions:
//   - read_repo{path:"a.go"} (as the claiming bee) → contents
//   - read_repo{path:"../secret"} → tool error (escape)
//   - repo_grep{query:"Auth"} → a hit mentioning a.go
//   - read_repo as a bee with no claimed mission → "not on a repo mission" error
}
```

> Implementer: complete using the existing in-package MCP harness. Build the workspace dir + a repo mission (CreateMission + SetRepo with branch and a `Repo` URL), then create the file under `mission.MissionDir(ws, id)` (= `<ws>/m<id>`). Enqueue + claim its task as the bee (so `ClaimedMission` resolves). Assert the four behaviors above.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/brain/ -run TestRepoFiles`
Expected: FAIL — tools not registered.

- [ ] **Step 3: Add Options fields + provisioning + read tools**

(a) `internal/brain/identity.go` `Options`:
```go
	// Repo, when set, enables repo-work missions (git → PR) and the repo read tools.
	Repo *repo.Engine
	// Workspace is where repo clones live (the brain owns the working copy). 
	Workspace string
```
Add the import `github.com/pdbethke/corralai/internal/repo`.

(b) `internal/brain/missions.go` — `createMissionIn` gains:
```go
	Repo string `json:"repo,omitempty" jsonschema:"git repo URL to build in (omit for a workspace-only mission)"`
	Base string `json:"base,omitempty" jsonschema:"base branch to branch from (default main)"`
```
After `id, err := mission.CreateMission(...)` succeeds, provision when a repo is requested:
```go
			if in.Repo != "" {
				if opts.Repo == nil || opts.Workspace == "" {
					_ = store.DeleteMission(id) // refuse: can't land what we can't provision
					return nil, mission.MissionView{}, fmt.Errorf("repo missions not enabled on this brain")
				}
				base := in.Base
				if base == "" { base = "main" }
				branch := fmt.Sprintf("corralai/m%d", id)
				dest := mission.MissionDir(opts.Workspace, id)
				if err := opts.Repo.Clone(ctx, in.Repo, base, dest); err != nil {
					_ = store.DeleteMission(id)
					return nil, mission.MissionView{}, fmt.Errorf("clone %s: %w", in.Repo, err)
				}
				if err := opts.Repo.Checkout(ctx, dest, branch); err != nil {
					_ = store.DeleteMission(id)
					return nil, mission.MissionView{}, fmt.Errorf("checkout: %w", err)
				}
				_ = store.SetRepo(id, in.Repo, base, branch)
			}
```
Add the import `github.com/pdbethke/corralai/internal/repo` (for `*repo.Engine` in `Options`/`registerRepoFiles`). `mission.MissionDir` (Task 3) gives the clone dir — no `path/filepath` needed here. Use the handler's real `ctx` (the create_mission closure parameter — rename `_ context.Context` to `ctx context.Context`). **Add `mission.DeleteMission(id)`** if it doesn't exist (a small `DELETE FROM missions WHERE id=?` + its tasks) — implement it in this task if missing.

(c) Create `internal/brain/repofiles.go` with `registerRepoFiles(s *mcp.Server, q *queue.Store, m *mission.Store, eng *repo.Engine, workspace string)` registering the three tools. Each resolves the workdir from the caller's claimed mission:
```go
func repoDirFor(req *mcp.CallToolRequest, q *queue.Store, m *mission.Store, eng *repo.Engine, workspace, name string) (string, error) {
	mid, _ := q.ClaimedMission(identity(req, name))
	if mid == 0 {
		return "", fmt.Errorf("not on a repo mission")
	}
	mi, err := m.Mission(mid)
	if err != nil || mi == nil || mi.Repo == "" {
		return "", fmt.Errorf("not on a repo mission")
	}
	return mission.MissionDir(workspace, mid), nil
}
```
(`repofiles.go` imports `mission` for `MissionDir`; no `path/filepath`/`repo.DirName`.)
`read_repo{name,path}` → `eng.ReadFile(dir, path)`; `repo_tree{name,subdir}` → `eng.Tree`; `repo_grep{name,query,k}` → `eng.Grep`. (The `name` field is the agent identity the brain closure stamps, same as other tools.) Each returns its result or the error.

(d) `internal/brain/server.go`: after the other registrations, add:
```go
	if opts.Repo != nil && opts.Queue != nil && opts.Missions != nil {
		registerRepoFiles(s, opts.Queue, opts.Missions, opts.Repo, opts.Workspace)
	}
```

- [ ] **Step 4: Run tests + commit**

Run: `go test ./internal/brain/ && go build ./...`
Expected: PASS; build OK.

```bash
git add internal/brain/missions.go internal/brain/identity.go internal/brain/server.go internal/brain/repofiles.go internal/brain/repofiles_test.go internal/mission/store.go
git commit -m "feat(brain): create_mission repo provisioning + read_repo/repo_tree/repo_grep tools"
```

---

## Task 6: cmd/corral wiring + credential-boundary test + infra

**Files:** Modify `cmd/corral/main.go`, `internal/sandbox/sandbox_test.go`, `deploy/demo/Dockerfile.brain`, `deploy/demo/docker-compose.yml`

**Interfaces:**
- Consumes: `repo.New` (Task 1); `Engine.Repo`/`Workspace` (Task 4); `Options.Repo`/`Workspace` (Task 5).

- [ ] **Step 1: Write the credential-boundary failing test**

```go
// internal/sandbox/sandbox_test.go — add
func TestMinimalEnvHasNoGitToken(t *testing.T) {
	t.Setenv("CORRALAI_GIT_TOKEN", "supersecret")
	for _, kv := range MinimalEnv() {
		if strings.Contains(kv, "CORRALAI_GIT_TOKEN") || strings.Contains(kv, "supersecret") {
			t.Fatalf("the secret-free jail env must never carry the git token: %q", kv)
		}
	}
}
```
(import `strings` if not already.)

- [ ] **Step 2: Run to verify it passes already (it's a guard)**

Run: `go test ./internal/sandbox/ -run TestMinimalEnvHasNoGitToken`
Expected: PASS (MinimalEnv only allowlists PATH/HOME/LANG/LC_ALL/TMPDIR — the token can't appear). This test LOCKS the boundary so a future MinimalEnv change can't leak it.

- [ ] **Step 3: Wire cmd/corral**

In `cmd/corral/main.go`, near the mission engine construction (`engine := mission.NewEngine(...)`):
```go
	repoWorkspace := env("CORRALAI_REPO_WORKSPACE", filepath.Join(os.TempDir(), "corral-repos"))
	os.MkdirAll(repoWorkspace, 0o755)
	var repoEng *repo.Engine
	if tok := os.Getenv("CORRALAI_GIT_TOKEN"); tok != "" || os.Getenv("CORRALAI_REPO_ENABLE") == "1" {
		repoEng = repo.New(tok, env("CORRALAI_GITHUB_API", "https://api.github.com"))
		engine.Repo = repoEng
		engine.Workspace = repoWorkspace
	}
```
Add `github.com/pdbethke/corralai/internal/repo` to imports. Pass `Repo: repoEng, Workspace: repoWorkspace` into the `brain.Options{...}` literal (so the read tools + provisioning are enabled when a repo engine exists).

- [ ] **Step 4: Infra**

`deploy/demo/Dockerfile.brain` runtime stage — add `git` to the apt install line:
```dockerfile
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates libstdc++6 curl git \
    && rm -rf /var/lib/apt/lists/*
```
`deploy/demo/docker-compose.yml` brain service — mount the workspace + pass the token + repo env:
```yaml
    volumes: ["workspace:/workspace"]
    environment:
      # ... existing ...
      CORRALAI_REPO_WORKSPACE: /workspace
      CORRALAI_GIT_TOKEN: ${CORRALAI_GIT_TOKEN:-}
      CORRALAI_GITHUB_API: ${CORRALAI_GITHUB_API:-https://api.github.com}
```
(merge `volumes`/`environment` into the existing `brain:` service block; don't duplicate keys.)

- [ ] **Step 5: Build + commit**

Run: `go build ./... && go test ./internal/sandbox/ ./internal/repo/ ./internal/mission/ ./internal/brain/`
Expected: build OK; tests PASS.

```bash
git add cmd/corral/main.go internal/sandbox/sandbox_test.go deploy/demo/Dockerfile.brain deploy/demo/docker-compose.yml
git commit -m "feat(corral): wire repo engine + workspace; lock the git-token credential boundary; brain git + mount"
```

---

## Final verification

- [ ] `go build ./...` — OK
- [ ] `go test ./...` — all PASS
- [ ] `CGO_ENABLED=0 go build ./cmd/corral-agent` — agent CGO-free (untouched, confirm)
- [ ] Boundary grep: token referenced only in `internal/repo` + `cmd/corral` (`grep -rn CORRALAI_GIT_TOKEN internal/ cmd/` shows it NOT in sandbox/agent), and `.git/config` after a clone carries no token (the Task 1 test).
