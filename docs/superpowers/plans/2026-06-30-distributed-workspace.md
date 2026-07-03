# Distributed Workspace (snapshot-bracket) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remote bees do repo work with no shared filesystem: the brain owns the per-mission working copy (`.git` + token), a bee pulls a `.git`-free snapshot into its own jail, works locally as it does today, and pushes changed files back for the brain to commit.

**Architecture:** Extend the `internal/repo` engine (from #15) with `Snapshot`/`ApplyFiles`/`HeadSHA`. Add brain MCP tools `repo_snapshot`/`repo_push` that resolve the caller's claimed mission to `mission.MissionDir(workspace, id)`. Teach the agent to mirror a mission's snapshot into `<AGENT_WORKSPACE>/m<id>`, track the paths it writes, and push exactly those on `complete_task`.

**Tech Stack:** Go 1.26; stdlib `archive/tar` + `compress/gzip` + `crypto/sha256`; the `internal/repo` engine, mission/queue stores, and `corral-agent` from #15.

## Global Constraints

- **Builds on #15 (repo-work mode).** Assumes `internal/repo.Engine` with `safeJoin` + the `skip(name)` helper (`read.go`), `mission.MissionDir(workspace, id)`, `Options.Repo *repo.Engine` + `Options.Workspace`, the brain's claimed-mission→workdir resolution, and the `corral-agent` write/run path all exist. Do #15 first.
- **`.git` never reaches a bee.** `Snapshot` excludes `.git` (and `node_modules`/`vendor`); a test asserts the bee mirror has no `.git`. This is the credential boundary — the token surface stays brain-side.
- **Brain owns the working copy; bees never share a volume.** All file movement is bee→brain over MCP (request/response), mirroring the existing `sync_pull` base64 precedent.
- **Push only the bee-written paths.** The agent tracks `write_file`/`edit_file` targets and pushes exactly those — never build artifacts or toolchain caches.
- **Path-confined apply.** `ApplyFiles` rejects `..`/absolute via the existing `safeJoin`; rejected files are skipped and absent from the returned `applied` list (visible discrepancy), never written outside the dir.
- **Graceful + serialized.** A mission's phases are sequential, so a push is a clean file-level apply; a stale `base_rev` is surfaced (`stale:true`) and logged, not fatal. A non-repo mission keeps today's single-workspace behavior untouched.
- `go build ./...` stays clean each task; the agent stays CGO-free (`CGO_ENABLED=0 go build ./cmd/corral-agent`).

---

## File Structure

- `internal/repo/snapshot.go` (create) — `Snapshot`/`ApplyFiles`/`HeadSHA` + `FileWrite`.
- `internal/repo/snapshot_test.go` (create) — round-trip, `.git` excluded, escape skip, size cap, HeadSHA.
- `internal/brain/reposync.go` (create) — `repo_snapshot`/`repo_push` MCP tools + `registerRepoSync`.
- `internal/brain/reposync_test.go` (create) — snapshot manifest, push apply + stale, not-on-mission error.
- `internal/brain/server.go` (modify) — register the sync tools alongside the read tools.
- `cmd/corral-agent/repomirror.go` (create) — `ensureMirror`/`trackWrite`/`pushMission` (testable, take a `call` func).
- `cmd/corral-agent/repomirror_test.go` (create) — mirror lays down (no `.git`), tracked write pushed, artifact not pushed.
- `cmd/corral-agent/main.go` (modify) — per-mission workdir; wire mirror pull, write tracking, push on `complete_task`.

---

## Task 1: `internal/repo` — Snapshot, ApplyFiles, HeadSHA

**Files:** Create `internal/repo/snapshot.go`, `internal/repo/snapshot_test.go`

**Interfaces:**
- Consumes (from #15): `Engine`, `safeJoin(dir, rel string) (string, error)`, `skip(name string) bool`, `e.git(ctx, dir, args...)` (the internal git runner).
- Produces:
  - `type FileWrite struct{ Path, Content string }`
  - `func (e *Engine) Snapshot(dir string) (data []byte, manifest map[string]string, err error)` — gzip'd tar of `dir`, excluding `.git`/`node_modules`/`vendor`; `manifest` is `path → sha256-hex`.
  - `func (e *Engine) ApplyFiles(dir string, writes []FileWrite) (applied []string, err error)` — writes each under `dir`; skips path escapes (absent from `applied`); `err` only on a real IO failure.
  - `func (e *Engine) HeadSHA(ctx context.Context, dir string) (string, error)` — `git rev-parse HEAD`; returns `""` (no error) on an unborn branch.

- [ ] **Step 1: Write the failing test**

```go
// internal/repo/snapshot_test.go
package repo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotRoundTripExcludesGit(t *testing.T) {
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "pkg"), 0o755)
	os.MkdirAll(filepath.Join(src, ".git"), 0o755)
	os.MkdirAll(filepath.Join(src, "node_modules", "x"), 0o755)
	os.WriteFile(filepath.Join(src, "pkg", "a.go"), []byte("package pkg\n"), 0o644)
	os.WriteFile(filepath.Join(src, "README.md"), []byte("hi\n"), 0o644)
	os.WriteFile(filepath.Join(src, ".git", "config"), []byte("[core]\n"), 0o644)
	os.WriteFile(filepath.Join(src, "node_modules", "x", "p.js"), []byte("x\n"), 0o644)

	e := New("", "")
	data, manifest, err := e.Snapshot(src)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := manifest["pkg/a.go"]; !ok {
		t.Fatalf("manifest missing pkg/a.go: %v", manifest)
	}
	for bad := range manifest {
		if strings.HasPrefix(bad, ".git/") || strings.HasPrefix(bad, "node_modules/") {
			t.Fatalf("snapshot leaked excluded path: %s", bad)
		}
	}
	// untar into a fresh dir via ApplyFiles-equivalent: use the engine's own untar test helper.
	dst := t.TempDir()
	if err := untarGz(dst, data); err != nil { // test helper below
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dst, "pkg", "a.go"))
	if string(got) != "package pkg\n" {
		t.Fatalf("round-trip content wrong: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dst, ".git")); !os.IsNotExist(err) {
		t.Fatalf(".git must not be in the snapshot")
	}
}

func TestApplyFilesEscapeSkipped(t *testing.T) {
	dir := t.TempDir()
	e := New("", "")
	applied, err := e.ApplyFiles(dir, []FileWrite{
		{Path: "ok.go", Content: "package x\n"},
		{Path: "../escape.go", Content: "nope\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || applied[0] != "ok.go" {
		t.Fatalf("expected only ok.go applied, got %v", applied)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.go")); !os.IsNotExist(err) {
		t.Fatal("escape file was written outside dir")
	}
	got, _ := os.ReadFile(filepath.Join(dir, "ok.go"))
	if string(got) != "package x\n" {
		t.Fatalf("ok.go content wrong: %q", got)
	}
}

func TestHeadSHA(t *testing.T) {
	bare := makeBareRepoWithCommit(t) // from repo_test.go (Task 1 of #15)
	dest := filepath.Join(t.TempDir(), "w")
	e := New("", "")
	if err := e.Clone(context.Background(), bare, "main", dest); err != nil {
		t.Fatal(err)
	}
	sha, err := e.HeadSHA(context.Background(), dest)
	if err != nil || len(sha) < 7 {
		t.Fatalf("HeadSHA = %q err=%v", sha, err)
	}
}

// untarGz is a test-only helper that expands a gzip'd tar into dir.
func untarGz(dir string, data []byte) error {
	return extractTarGz(dir, data) // exported-for-test? no — same package, use unexported helper
}
```

> NOTE: `makeBareRepoWithCommit` already exists in `internal/repo/repo_test.go` (#15 Task 1) — same package, reuse it. Implement `extractTarGz(dir string, data []byte) error` as an **unexported package function in `snapshot.go`** (the agent needs the same untar in its own package, but keep this one here for the round-trip test; the agent gets its own copy in Task 3 since packages can't share unexported helpers). The test's `untarGz` simply calls it.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/repo/ -run 'Snapshot|ApplyFiles|HeadSHA'`
Expected: FAIL — `Snapshot`/`ApplyFiles`/`HeadSHA`/`extractTarGz` undefined.

- [ ] **Step 3: Implement `internal/repo/snapshot.go`**

```go
package repo

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// snapshotCap bounds the uncompressed bytes a Snapshot will read (pathological repos).
const snapshotCap = 64 << 20 // 64 MiB

type FileWrite struct {
	Path    string
	Content string
}

// Snapshot returns a gzip'd tar of dir (excluding .git/node_modules/vendor) and a
// path→sha256 manifest. The tar carries no .git, so a bee that expands it never holds
// the credential surface.
func (e *Engine) Snapshot(dir string) ([]byte, map[string]string, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	manifest := map[string]string{}
	var total int64

	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skip(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		total += int64(len(b))
		if total > snapshotCap {
			return fmt.Errorf("snapshot exceeds %d bytes", int64(snapshotCap))
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		sum := sha256.Sum256(b)
		manifest[rel] = hex.EncodeToString(sum[:])
		if err := tw.WriteHeader(&tar.Header{Name: rel, Mode: 0o644, Size: int64(len(b))}); err != nil {
			return err
		}
		if _, err := tw.Write(b); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, nil, err
	}
	return buf.Bytes(), manifest, nil
}

// ApplyFiles writes each FileWrite under dir, creating parents. Path escapes
// (../ or absolute) are skipped and absent from the returned applied list; err is
// returned only on a real IO failure.
func (e *Engine) ApplyFiles(dir string, writes []FileWrite) ([]string, error) {
	var applied []string
	for _, w := range writes {
		full, err := safeJoin(dir, w.Path)
		if err != nil {
			continue // escape — skip, leave out of applied
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return applied, err
		}
		if err := os.WriteFile(full, []byte(w.Content), 0o644); err != nil {
			return applied, err
		}
		applied = append(applied, filepath.ToSlash(w.Path))
	}
	return applied, nil
}

// HeadSHA is the working copy's current commit (empty string on an unborn branch).
func (e *Engine) HeadSHA(ctx context.Context, dir string) (string, error) {
	out, err := e.git(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", nil // unborn branch / no commit yet — not an error for our purposes
	}
	return strings.TrimSpace(out), nil
}

// extractTarGz expands a gzip'd tar (from Snapshot) into dir, rejecting path escapes.
func extractTarGz(dir string, data []byte) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		full, err := safeJoin(dir, h.Name)
		if err != nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(full, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, io.LimitReader(tr, snapshotCap)); err != nil {
			f.Close()
			return err
		}
		f.Close()
	}
	return nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/repo/`
Expected: PASS — round-trip excludes `.git`/`node_modules`; `ApplyFiles` skips the escape; `HeadSHA` returns a sha.

- [ ] **Step 5: Commit**

```bash
git add internal/repo/snapshot.go internal/repo/snapshot_test.go
git commit -m "feat(repo): Snapshot/ApplyFiles/HeadSHA for the snapshot-bracket workspace"
```

---

## Task 2: brain — repo_snapshot + repo_push MCP tools

**Files:** Create `internal/brain/reposync.go`, `internal/brain/reposync_test.go`; Modify `internal/brain/server.go`

**Interfaces:**
- Consumes: `repo.Engine.{Snapshot,ApplyFiles,HeadSHA}` + `repo.FileWrite` (Task 1); `mission.MissionDir`, `mission.Mission` (#15 Task 3); `queue.ClaimedMission`; `identity(req, name)`; `Options.Repo`/`Options.Workspace`/`Options.Queue`/`Options.Missions` (#15 Task 5).
- Produces: tools `repo_snapshot{name}` → `{data_b64, manifest, base_rev}`; `repo_push{name, files:[{path,content}], base_rev}` → `{applied, stale}`; `func registerRepoSync(s *mcp.Server, opts Options)`.

- [ ] **Step 1: Write the failing test**

```go
// internal/brain/reposync_test.go
package brain

// Build a brain with Options{Queue:q, Missions:m, Repo:repo.New("",""), Workspace:ws},
// seed a repo mission whose MissionDir(ws,id) holds a file, have a bee claim its task,
// then over MCP:
//   - repo_snapshot{name:bee} → manifest covers the seeded file; base_rev non-empty
//     iff the dir is a git repo (seed it as one); data_b64 decodes to a gzip'd tar.
//   - repo_push{name:bee, files:[{path:"new.go",content:"package x\n"}], base_rev:<from snapshot>}
//     → applied contains "new.go" and the file exists under MissionDir; stale==false.
//   - repo_push with base_rev:"deadbeef" → stale==true but still applied.
//   - repo_snapshot as a bee with NO claimed repo mission → tool error "not on a repo mission".
//
// Implementer: mirror the in-package MCP harness used by repofiles_test.go (#15 Task 5)
// — same brain construction, same claim flow. Use repo.New("","") (no token needed; the
// MissionDir is a plain dir, optionally `git init`'d via the engine's git ops for base_rev).
}
```

> Implementer: complete the body using the existing in-package MCP test harness (see `repofiles_test.go` from #15). The four assertions above are the contract. To give `base_rev` a value, `git init` + one commit in `MissionDir(ws,id)` using the engine (or `os/exec` in the test). The `name` field is the agent identity the brain closure stamps, same as the read tools.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/brain/ -run TestRepoSync`
Expected: FAIL — tools not registered.

- [ ] **Step 3: Implement `internal/brain/reposync.go`**

```go
package brain

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/repo"
)

type snapshotIn struct {
	Name string `json:"name"`
}
type snapshotOut struct {
	DataB64  string            `json:"data_b64"`
	Manifest map[string]string `json:"manifest"`
	BaseRev  string            `json:"base_rev"`
}
type pushFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}
type pushIn struct {
	Name    string     `json:"name"`
	Files   []pushFile `json:"files"`
	BaseRev string     `json:"base_rev"`
}
type pushOut struct {
	Applied []string `json:"applied"`
	Stale   bool     `json:"stale"`
}

// repoMissionDir resolves the caller's claimed repo mission to its working copy.
func repoMissionDir(opts Options, req *mcp.CallToolRequest, name string) (string, error) {
	mid, _ := opts.Queue.ClaimedMission(identity(req, name))
	if mid == 0 {
		return "", fmt.Errorf("not on a repo mission")
	}
	mi, err := opts.Missions.Mission(mid)
	if err != nil || mi == nil || mi.Repo == "" {
		return "", fmt.Errorf("not on a repo mission")
	}
	return mission.MissionDir(opts.Workspace, mid), nil
}

func registerRepoSync(s *mcp.Server, opts Options) {
	mcp.AddTool(s, &mcp.Tool{Name: "repo_snapshot",
		Description: "Pull a .git-free snapshot of your mission's working copy (tar.gz, base64) plus a path→sha manifest and the current base_rev."},
		func(ctx context.Context, req *mcp.CallToolRequest, in snapshotIn) (*mcp.CallToolResult, snapshotOut, error) {
			dir, err := repoMissionDir(opts, req, in.Name)
			if err != nil {
				return nil, snapshotOut{}, err
			}
			data, manifest, err := opts.Repo.Snapshot(dir)
			if err != nil {
				return nil, snapshotOut{}, err
			}
			rev, _ := opts.Repo.HeadSHA(ctx, dir)
			return nil, snapshotOut{DataB64: base64.StdEncoding.EncodeToString(data), Manifest: manifest, BaseRev: rev}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "repo_push",
		Description: "Push changed files back to your mission's working copy. The brain applies them and commits gate-passed phases."},
		func(ctx context.Context, req *mcp.CallToolRequest, in pushIn) (*mcp.CallToolResult, pushOut, error) {
			dir, err := repoMissionDir(opts, req, in.Name)
			if err != nil {
				return nil, pushOut{}, err
			}
			writes := make([]repo.FileWrite, 0, len(in.Files))
			for _, f := range in.Files {
				writes = append(writes, repo.FileWrite{Path: f.Path, Content: f.Content})
			}
			applied, err := opts.Repo.ApplyFiles(dir, writes)
			if err != nil {
				return nil, pushOut{}, err
			}
			cur, _ := opts.Repo.HeadSHA(ctx, dir)
			stale := in.BaseRev != "" && cur != "" && in.BaseRev != cur
			return nil, pushOut{Applied: applied, Stale: stale}, nil
		})
}
```

(Match the exact `mcp.AddTool` signature shape used by the neighboring tools in `internal/brain` — copy the form from `repofiles.go`/`reference.go` if the handler return tuple differs in this SDK version.)

- [ ] **Step 4: Register in `server.go`**

Next to the `registerRepoFiles(...)` call from #15, add:
```go
	if opts.Repo != nil && opts.Queue != nil && opts.Missions != nil {
		registerRepoSync(s, opts)
	}
```
(If #15 already guards `registerRepoFiles` with that exact condition, fold both calls under one `if`.)

- [ ] **Step 5: Run tests + commit**

Run: `go test ./internal/brain/ && go build ./...`
Expected: PASS; build OK.

```bash
git add internal/brain/reposync.go internal/brain/reposync_test.go internal/brain/server.go
git commit -m "feat(brain): repo_snapshot/repo_push — bees mirror + push without a shared volume"
```

---

## Task 3: agent — per-mission mirror, tracked writes, push on complete

**Files:** Create `cmd/corral-agent/repomirror.go`, `cmd/corral-agent/repomirror_test.go`; Modify `cmd/corral-agent/main.go`

**Interfaces:**
- Consumes: the agent's brain-call closure (rename/adapt to `type brainCall func(tool string, args map[string]any) (map[string]any, error)`); the agent's `write_file`/`edit_file`/`run_command`/`complete_task` dispatch (`main.go`).
- Produces (testable helpers, package `main`):
  - `type mirror struct{ root string; writes map[int64]map[string]bool; base map[int64]string; isRepo map[int64]bool }` + `newMirror(root string) *mirror`
  - `func (m *mirror) ensure(call brainCall, missionID int64) (workdir string, isRepo bool, err error)`
  - `func (m *mirror) track(missionID int64, path string)`
  - `func (m *mirror) push(call brainCall, missionID int64) ([]string, error)`
  - `func untarGzInto(dir string, data []byte) error` (agent-local copy of the repo untar).

- [ ] **Step 1: Write the failing test**

```go
// cmd/corral-agent/repomirror_test.go
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

// fakeBrain returns a snapshot for mission 7 and records pushes.
func fakeBrain(t *testing.T) (brainCall, *[]pushedFile) {
	var pushed []pushedFile
	// build a tiny tar.gz with one file
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("package calc\n")
	tw.WriteHeader(&tar.Header{Name: "calc.go", Mode: 0o644, Size: int64(len(body))})
	tw.Write(body)
	tw.Close()
	gz.Close()
	b64 := base64.StdEncoding.EncodeToString(buf.Bytes())

	call := func(tool string, args map[string]any) (map[string]any, error) {
		switch tool {
		case "repo_snapshot":
			return map[string]any{"data_b64": b64, "manifest": map[string]any{"calc.go": "x"}, "base_rev": "abc123"}, nil
		case "repo_push":
			for _, f := range args["files"].([]map[string]any) {
				pushed = append(pushed, pushedFile{f["path"].(string), f["content"].(string)})
			}
			return map[string]any{"applied": []any{}, "stale": false}, nil
		}
		return map[string]any{}, nil
	}
	return call, &pushed
}

type pushedFile struct{ path, content string }

func TestMirrorPullTrackPush(t *testing.T) {
	root := t.TempDir()
	call, pushed := fakeBrain(t)
	m := newMirror(root)

	wd, isRepo, err := m.ensure(call, 7)
	if err != nil || !isRepo {
		t.Fatalf("ensure: wd=%s isRepo=%v err=%v", wd, isRepo, err)
	}
	if wd != filepath.Join(root, "m7") {
		t.Fatalf("workdir = %s", wd)
	}
	if _, err := os.Stat(filepath.Join(wd, "calc.go")); err != nil {
		t.Fatalf("snapshot not laid down: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wd, ".git")); !os.IsNotExist(err) {
		t.Fatal("mirror must contain no .git (credential boundary)")
	}

	// simulate a bee edit + a build artifact
	os.WriteFile(filepath.Join(wd, "calc.go"), []byte("package calc\n// edited\n"), 0o644)
	m.track(7, "calc.go")
	os.WriteFile(filepath.Join(wd, "calc"), []byte("ELF-binary"), 0o755) // build artifact, NOT tracked

	if _, err := m.push(call, 7); err != nil {
		t.Fatal(err)
	}
	if len(*pushed) != 1 || (*pushed)[0].path != "calc.go" {
		t.Fatalf("expected only calc.go pushed, got %v", *pushed)
	}
	if (*pushed)[0].content != "package calc\n// edited\n" {
		t.Fatalf("pushed stale content: %q", (*pushed)[0].content)
	}
}

func TestEnsureNonRepoMission(t *testing.T) {
	root := t.TempDir()
	call := func(tool string, args map[string]any) (map[string]any, error) {
		if tool == "repo_snapshot" {
			return nil, errNotRepoMission // sentinel below
		}
		return map[string]any{}, nil
	}
	m := newMirror(root)
	_, isRepo, err := m.ensure(call, 3)
	if err != nil || isRepo {
		t.Fatalf("non-repo mission should be isRepo=false, no error; got isRepo=%v err=%v", isRepo, err)
	}
}
```

> NOTE: `errNotRepoMission` is a sentinel the agent uses to recognize the brain's "not on a repo mission" reply. Since the brain returns it as a tool error string, `ensure` treats any `repo_snapshot` error whose message contains `"not on a repo mission"` as "plain mission" (isRepo=false, no error) — define the test sentinel as `var errNotRepoMission = errors.New("not on a repo mission")` in the test file.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd/corral-agent/ -run 'Mirror|Ensure'`
Expected: FAIL — `newMirror`/`mirror`/`untarGzInto` undefined.

- [ ] **Step 3: Implement `cmd/corral-agent/repomirror.go`**

```go
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type brainCall func(tool string, args map[string]any) (map[string]any, error)

// mirror tracks each repo mission's local working copy and the paths the bee wrote.
type mirror struct {
	root   string
	writes map[int64]map[string]bool
	base   map[int64]string
	isRepo map[int64]bool
	known  map[int64]bool // ensure() already ran for this mission
}

func newMirror(root string) *mirror {
	return &mirror{root: root, writes: map[int64]map[string]bool{}, base: map[int64]string{}, isRepo: map[int64]bool{}, known: map[int64]bool{}}
}

func (m *mirror) dir(missionID int64) string {
	return filepath.Join(m.root, "m"+itoa(missionID))
}

// ensure pulls the mission's snapshot once and lays it down. For a non-repo mission
// (brain replies "not on a repo mission") it returns isRepo=false with no error.
func (m *mirror) ensure(call brainCall, missionID int64) (string, bool, error) {
	if m.known[missionID] {
		return m.dir(missionID), m.isRepo[missionID], nil
	}
	m.known[missionID] = true
	res, err := call("repo_snapshot", map[string]any{})
	if err != nil {
		if strings.Contains(err.Error(), "not on a repo mission") {
			m.isRepo[missionID] = false
			return "", false, nil
		}
		return "", false, err
	}
	wd := m.dir(missionID)
	if err := os.MkdirAll(wd, 0o755); err != nil {
		return "", false, err
	}
	data, err := base64.StdEncoding.DecodeString(asString(res["data_b64"]))
	if err != nil {
		return "", false, err
	}
	if err := untarGzInto(wd, data); err != nil {
		return "", false, err
	}
	m.base[missionID] = asString(res["base_rev"])
	m.isRepo[missionID] = true
	m.writes[missionID] = map[string]bool{}
	return wd, true, nil
}

func (m *mirror) track(missionID int64, path string) {
	if m.writes[missionID] == nil {
		m.writes[missionID] = map[string]bool{}
	}
	m.writes[missionID][filepath.ToSlash(path)] = true
}

// push sends exactly the tracked files (read from the mirror) back to the brain.
func (m *mirror) push(call brainCall, missionID int64) ([]string, error) {
	tracked := m.writes[missionID]
	if len(tracked) == 0 {
		return nil, nil
	}
	files := make([]map[string]any, 0, len(tracked))
	for p := range tracked {
		b, err := os.ReadFile(filepath.Join(m.dir(missionID), p))
		if err != nil {
			continue // a tracked file that vanished — skip
		}
		files = append(files, map[string]any{"path": p, "content": string(b)})
	}
	res, err := call("repo_push", map[string]any{"files": files, "base_rev": m.base[missionID]})
	if err != nil {
		return nil, err
	}
	m.writes[missionID] = map[string]bool{} // clear after a successful push
	var applied []string
	if a, ok := res["applied"].([]any); ok {
		for _, x := range a {
			applied = append(applied, asString(x))
		}
	}
	return applied, nil
}

func untarGzInto(dir string, data []byte) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		// confine to dir (reject ../ and absolute)
		clean := filepath.Clean("/" + h.Name)
		full := filepath.Join(dir, clean)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(full, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, tr); err != nil {
			f.Close()
			return err
		}
		f.Close()
	}
	return nil
}
```

> NOTE: `itoa`, `asString` are tiny helpers — if the agent already has equivalents use them; otherwise add `func itoa(n int64) string { return strconv.FormatInt(n, 10) }` and `func asString(v any) string { s, _ := v.(string); return s }` (import `strconv`). The `mcp` mapping of `[]map[string]any` for `files` must match what `pushIn.Files` expects on the brain — the SDK marshals the map through JSON, so the field names `path`/`content` line up with `pushFile`'s json tags.

- [ ] **Step 4: Run helper tests**

Run: `go test ./cmd/corral-agent/`
Expected: PASS — mirror lays down (no `.git`), only the tracked file is pushed with fresh content, non-repo mission returns `isRepo=false`.

- [ ] **Step 5: Wire into `main.go`**

Read `cmd/corral-agent/main.go` and make these changes (the function names are from the current structure — `runQueueLoop` → `runTask`/`runTicket` → `dispatch`):

(a) Construct one `*mirror` at startup next to `ws`: `mir := newMirror(ws)`. Adapt the existing brain closure to a `brainCall` (a thin wrapper returning `(map[string]any, error)`).

(b) At the **start of a repo task** (in `runTask`, once the `MissionID` is known), call
`wd, isRepo, err := mir.ensure(brainCall, missionID)`. If `isRepo`, use `wd` as the task's working dir (the `ws` passed into `dispatch`) instead of the global `ws`; otherwise keep the global `ws` (today's behavior).

(c) In `dispatch`, in the `write_file` and `edit_file` cases, after a successful write call `mir.track(missionID, p)` (thread `missionID` into `dispatch`, or close over it).

(d) In the `complete_task` path, **before** reporting completion to the brain, if the mission is a repo mission call `mir.push(brainCall, missionID)` and log the applied list (and any error — a push failure is logged, not fatal; the brain keeps the prior state).

(e) `run_command`'s `sandbox.Options{Workspace: wd}` now uses the per-mission `wd` so builds compile the mirrored tree.

- [ ] **Step 6: Build (agent stays CGO-free) + commit**

Run: `CGO_ENABLED=0 go build ./cmd/corral-agent && go build ./... && go test ./cmd/corral-agent/`
Expected: build OK; tests PASS.

```bash
git add cmd/corral-agent/repomirror.go cmd/corral-agent/repomirror_test.go cmd/corral-agent/main.go
git commit -m "feat(agent): per-mission snapshot mirror + tracked writes + push on complete"
```

---

## Final verification

- [ ] `go build ./...` — OK
- [ ] `CGO_ENABLED=0 go build ./cmd/corral-agent` — agent CGO-free
- [ ] `go test ./internal/repo/ ./internal/brain/ ./cmd/corral-agent/` — all PASS
- [ ] Credential boundary: a laid-down mirror (`<AGENT_WORKSPACE>/m<id>`) contains no `.git` (Task 3 test) — the token surface never leaves the brain.
- [ ] No shared volume: the bee never reads `<brain workspace>`; all file movement is `repo_snapshot`/`repo_push` over MCP.
