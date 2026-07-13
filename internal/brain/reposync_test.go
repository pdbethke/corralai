// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/repo"
)

func TestRepoSync(t *testing.T) {
	root := t.TempDir()

	cstore, err := coord.Open(filepath.Join(root, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cstore.Close() })

	q, err := queue.Open(filepath.Join(root, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })

	mstore, err := mission.Open(filepath.Join(root, "m.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mstore.Close() })

	ws := filepath.Join(root, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a repo mission.
	mid, err := mission.CreateMission(mstore, q, "sync directive", []mission.PhaseSpec{
		{Name: "build", Role: "builder", Count: 1, Instruction: "build it"},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := mstore.SetRepo(mid, "https://github.com/example/repo", "main", "corralai/m1"); err != nil {
		t.Fatal(err)
	}
	q.PromoteReady(mid)

	// Seed the mission working dir with a file and a real git repo (for base_rev).
	workDir := mission.MissionDir(ws, mid)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// git init + commit so HeadSHA returns a non-empty value.
	gitCmds := [][]string{
		{"git", "-C", workDir, "init"},
		{"git", "-C", workDir, "config", "user.email", "test@example.com"},
		{"git", "-C", workDir, "config", "user.name", "Test"},
		{"git", "-C", workDir, "add", "."},
		{"git", "-C", workDir, "commit", "-m", "init"},
	}
	for _, args := range gitCmds {
		if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("git setup %v: %v\n%s", args, err, out)
		}
	}

	eng := repo.New("", "")

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() {
		_ = NewServer(cstore, nil, Options{
			Queue:     q,
			Missions:  mstore,
			Repo:      eng,
			Workspace: ws,
		}).Run(ctx, serverT)
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "bee", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// Bee claims the task so ClaimedMission("Ada") returns mid.
	if _, err := q.ClaimNext("Ada", []string{"builder"}, 300); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}

	// --- Assertion 1: repo_snapshot → manifest covers main.go; base_rev non-empty; data decodes as gzip'd tar ---
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "repo_snapshot",
		Arguments: map[string]any{"name": "Ada"},
	})
	if err != nil {
		t.Fatalf("repo_snapshot: %v", err)
	}
	if res.IsError {
		t.Fatalf("repo_snapshot returned tool error: %+v", res.Content)
	}
	var snapOut snapshotOut
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, &snapOut); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snapOut.BaseRev == "" {
		t.Fatal("repo_snapshot: expected non-empty base_rev (git dir has a commit)")
	}
	if _, ok := snapOut.Manifest["main.go"]; !ok {
		t.Fatalf("repo_snapshot: manifest missing main.go; got %v", snapOut.Manifest)
	}
	if snapOut.DataB64 == "" {
		t.Fatal("repo_snapshot: data_b64 is empty")
	}
	// Decode and verify it's a valid gzip'd tar.
	raw, err := base64.StdEncoding.DecodeString(snapOut.DataB64)
	if err != nil {
		t.Fatalf("repo_snapshot: data_b64 decode: %v", err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("repo_snapshot: not a gzip stream: %v", err)
	}
	tr := tar.NewReader(gz)
	foundMain := false
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if hdr.Name == "main.go" {
			foundMain = true
		}
	}
	if !foundMain {
		t.Fatal("repo_snapshot: tar does not contain main.go")
	}

	// --- Assertion 2: repo_push with base_rev from snapshot → applied contains new.go; stale==false ---
	res2, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "repo_push",
		Arguments: map[string]any{
			"name":     "Ada",
			"files":    []map[string]any{{"path": "new.go", "content": "package x\n"}},
			"base_rev": snapOut.BaseRev,
		},
	})
	if err != nil {
		t.Fatalf("repo_push: %v", err)
	}
	if res2.IsError {
		t.Fatalf("repo_push returned tool error: %+v", res2.Content)
	}
	var pushRes pushOut
	b2, _ := json.Marshal(res2.StructuredContent)
	if err := json.Unmarshal(b2, &pushRes); err != nil {
		t.Fatalf("decode push: %v", err)
	}
	if len(pushRes.Applied) == 0 || pushRes.Applied[0] != "new.go" {
		t.Fatalf("repo_push: expected applied=[new.go], got %v", pushRes.Applied)
	}
	if pushRes.Stale {
		t.Fatalf("repo_push: expected stale==false when base_rev matches HEAD")
	}
	if _, err := os.Stat(filepath.Join(workDir, "new.go")); err != nil {
		t.Fatalf("repo_push: new.go not written to workDir: %v", err)
	}

	// --- Assertion 3: repo_push with stale base_rev → stale==true but still applied ---
	res3, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "repo_push",
		Arguments: map[string]any{
			"name":     "Ada",
			"files":    []map[string]any{{"path": "stale.go", "content": "package x\n"}},
			"base_rev": "deadbeef",
		},
	})
	if err != nil {
		t.Fatalf("repo_push stale: %v", err)
	}
	if res3.IsError {
		t.Fatalf("repo_push stale returned tool error: %+v", res3.Content)
	}
	var pushStale pushOut
	b3, _ := json.Marshal(res3.StructuredContent)
	if err := json.Unmarshal(b3, &pushStale); err != nil {
		t.Fatalf("decode push stale: %v", err)
	}
	if !pushStale.Stale {
		t.Fatal("repo_push: expected stale==true when base_rev differs from HEAD")
	}
	if len(pushStale.Applied) == 0 || pushStale.Applied[0] != "stale.go" {
		t.Fatalf("repo_push stale: expected applied=[stale.go], got %v", pushStale.Applied)
	}

	// --- Assertion 4: repo_snapshot as Bob (no claimed mission) → tool error "not on a repo mission" ---
	res4, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "repo_snapshot",
		Arguments: map[string]any{"name": "Bob"},
	})
	if err != nil {
		t.Fatalf("repo_snapshot Bob: %v", err)
	}
	if !res4.IsError {
		t.Fatalf("repo_snapshot with no claimed mission should return a tool error, got ok: %+v", res4.StructuredContent)
	}
	if msg := toolErrText(res4); !strings.Contains(msg, "not on a repo mission") {
		t.Fatalf("repo_snapshot no-claim: expected 'not on a repo mission', got %q", msg)
	}

	// --- Assertion 5: repo_push payload > 64 MiB → tool error, not applied ---
	big := strings.Repeat("a", 65<<20) // 65 MiB, over the 64 MiB cap
	res5, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "repo_push",
		Arguments: map[string]any{
			"name":     "Ada",
			"files":    []map[string]any{{"path": "huge.go", "content": big}},
			"base_rev": snapOut.BaseRev,
		},
	})
	if err != nil {
		t.Fatalf("repo_push oversized: %v", err)
	}
	if !res5.IsError {
		t.Fatalf("repo_push over the cap should return a tool error, got ok: %+v", res5.StructuredContent)
	}
	if _, err := os.Stat(filepath.Join(workDir, "huge.go")); !os.IsNotExist(err) {
		t.Fatalf("repo_push oversized: huge.go must NOT be written, stat err=%v", err)
	}

	// --- Assertion 6 (Fix 1c): repo_push with .git/config path is silently skipped ---
	// A compromised bee must not be able to overwrite .git/config and plant a hook
	// that would execute when the brain's git runner commits (brain-side RCE).
	res6, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "repo_push",
		Arguments: map[string]any{
			"name": "Ada",
			"files": []map[string]any{
				{"path": ".git/config", "content": "[core]\n  fsmonitor = evil-script\n"},
				{"path": "legit.go", "content": "package legit\n"},
			},
			"base_rev": snapOut.BaseRev,
		},
	})
	if err != nil {
		t.Fatalf("repo_push .git/config: %v", err)
	}
	if res6.IsError {
		t.Fatalf("repo_push .git/config: unexpected tool error: %+v", res6.Content)
	}
	var pushGit pushOut
	b6, _ := json.Marshal(res6.StructuredContent)
	if err := json.Unmarshal(b6, &pushGit); err != nil {
		t.Fatalf("decode push .git/config: %v", err)
	}
	// .git/config must NOT appear in applied — it was silently skipped
	for _, ap := range pushGit.Applied {
		if ap == ".git/config" || strings.HasPrefix(ap, ".git/") {
			t.Fatalf("repo_push: .git/config appeared in applied list — credential boundary violated")
		}
	}
	// .git/config must NOT have been written with the evil content
	gitCfgPath := filepath.Join(workDir, ".git", "config")
	if cfgBytes, err := os.ReadFile(gitCfgPath); err == nil {
		if strings.Contains(string(cfgBytes), "evil-script") {
			t.Fatal("repo_push wrote evil content into .git/config — credential boundary violated")
		}
	}
	// legit.go must have been applied
	found := false
	for _, ap := range pushGit.Applied {
		if ap == "legit.go" {
			found = true
		}
	}
	if !found {
		t.Fatalf("repo_push: legit.go missing from applied=%v", pushGit.Applied)
	}
}

// TestRepoSyncPushApplyCommit is an end-to-end seam test (Fix 3): it exercises
// the push→apply→commit path with real components — a real repo.Engine, a real
// git-init'd working directory, and the real repo_push MCP tool — so that the
// credential-boundary fix (ApplyFiles now rejects .git/* paths) is covered by a
// test that crosses the full apply seam, not just unit-level mocks.
func TestRepoSyncPushApplyCommit(t *testing.T) {
	root := t.TempDir()

	cstore, err := coord.Open(filepath.Join(root, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cstore.Close() })

	q, err := queue.Open(filepath.Join(root, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })

	mstore, err := mission.Open(filepath.Join(root, "m.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mstore.Close() })

	ws := filepath.Join(root, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}

	mid, err := mission.CreateMission(mstore, q, "seam test directive", []mission.PhaseSpec{
		{Name: "build", Role: "builder", Count: 1, Instruction: "build it"},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := mstore.SetRepo(mid, "https://github.com/example/seam", "main", "corralai/seam"); err != nil {
		t.Fatal(err)
	}
	q.PromoteReady(mid)

	// Seed the mission working dir with a real git repo.
	workDir := mission.MissionDir(ws, mid)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "seed.go"), []byte("package seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmds := [][]string{
		{"git", "-C", workDir, "init"},
		{"git", "-C", workDir, "config", "user.email", "test@example.com"},
		{"git", "-C", workDir, "config", "user.name", "Test"},
		{"git", "-C", workDir, "add", "."},
		{"git", "-C", workDir, "commit", "-m", "init"},
	}
	for _, args := range gitCmds {
		if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("git setup %v: %v\n%s", args, err, out)
		}
	}

	eng := repo.New("", "")

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() {
		_ = NewServer(cstore, nil, Options{
			Queue:     q,
			Missions:  mstore,
			Repo:      eng,
			Workspace: ws,
		}).Run(ctx, serverT)
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "seamBee", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// Bee claims the task.
	if _, err := q.ClaimNext("seamBee", []string{"builder"}, 300); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}

	// Step 1: snapshot to get base_rev.
	snapRes, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "repo_snapshot",
		Arguments: map[string]any{"name": "seamBee"},
	})
	if err != nil || snapRes.IsError {
		t.Fatalf("repo_snapshot: err=%v isError=%v content=%v", err, snapRes.IsError, snapRes.Content)
	}
	var snap snapshotOut
	sb, _ := json.Marshal(snapRes.StructuredContent)
	json.Unmarshal(sb, &snap)

	// Step 2: push a new file via the REAL repo_push tool.
	pushRes, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "repo_push",
		Arguments: map[string]any{
			"name":     "seamBee",
			"files":    []map[string]any{{"path": "seam_new.go", "content": "package seam\n"}},
			"base_rev": snap.BaseRev,
		},
	})
	if err != nil || pushRes.IsError {
		t.Fatalf("repo_push: err=%v isError=%v content=%v", err, pushRes.IsError, pushRes.Content)
	}
	var pushed pushOut
	pb, _ := json.Marshal(pushRes.StructuredContent)
	json.Unmarshal(pb, &pushed)
	if len(pushed.Applied) == 0 || pushed.Applied[0] != "seam_new.go" {
		t.Fatalf("repo_push: expected applied=[seam_new.go], got %v", pushed.Applied)
	}

	// Step 3: assert the file is present in the working copy on disk.
	if _, err := os.Stat(filepath.Join(workDir, "seam_new.go")); err != nil {
		t.Fatalf("seam_new.go not written to workDir after repo_push: %v", err)
	}

	// Step 4: drive Commit directly through the repo.Engine (crosses the apply→commit
	// seam). This mirrors the per-phase commit the mission engine used to make
	// on Tick() in the retired build-from-directive loop (that commitDonePhases
	// method has since been deleted).
	committed, err := eng.Commit(ctx, workDir, "build: seam test directive")
	if err != nil {
		t.Fatalf("Commit after push: %v", err)
	}
	if !committed {
		t.Fatal("expected a commit (seam_new.go is new), got no-op")
	}

	// Step 5: assert git show HEAD contains seam_new.go.
	out, err := exec.Command("git", "-C", workDir, "show", "--name-only", "--format=", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("git show HEAD: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "seam_new.go") {
		t.Fatalf("seam_new.go not in HEAD commit; git show output:\n%s", out)
	}
}

// toolErrText concatenates the text payload of an errored tool result.
func toolErrText(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}
