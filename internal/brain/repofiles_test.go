// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/repo"
)

func TestRepoFiles(t *testing.T) {
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

	// Create a mission and mark it as a repo mission manually.
	ws := filepath.Join(root, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create mission directly via CreateMission (no repo provisioning — we're doing it manually for the test).
	mid, err := mission.CreateMission(mstore, q, "test directive", []mission.PhaseSpec{
		{Name: "build", Role: "builder", Count: 1, Instruction: "build it"},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	// Set it as a repo mission.
	if err := mstore.SetRepo(mid, "https://github.com/example/repo", "main", "corralai/m1"); err != nil {
		t.Fatal(err)
	}
	// Promote tasks so the bee can claim one.
	q.PromoteReady(mid)

	// Write a file into the mission working dir (simulating a clone).
	workDir := mission.MissionDir(ws, mid)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "a.go"), []byte("package main\n\n// Auth handles authentication\nfunc Auth() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Also write a secret file outside the mission dir to test escape prevention.
	if err := os.WriteFile(filepath.Join(root, "secret"), []byte("TOP SECRET"), 0o644); err != nil {
		t.Fatal(err)
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

	// --- Assertion 1: read_repo{path:"a.go"} returns the file contents ---
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "read_repo",
		Arguments: map[string]any{"name": "Ada", "path": "a.go"},
	})
	if err != nil {
		t.Fatalf("read_repo: %v", err)
	}
	if res.IsError {
		t.Fatalf("read_repo returned tool error: %+v", res.Content)
	}
	var readOut struct {
		Content string `json:"content"`
	}
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, &readOut); err != nil {
		t.Fatal(err)
	}
	if readOut.Content == "" {
		t.Fatalf("read_repo: expected file contents, got: %s", string(b))
	}
	if readOut.Content != "package main\n\n// Auth handles authentication\nfunc Auth() {}\n" {
		t.Fatalf("read_repo: unexpected content: %q", readOut.Content)
	}

	// --- Assertion 2: read_repo{path:"../secret"} → tool error (escape) ---
	res2, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "read_repo",
		Arguments: map[string]any{"name": "Ada", "path": "../secret"},
	})
	if err != nil {
		t.Fatalf("read_repo escape: %v", err)
	}
	if !res2.IsError {
		t.Fatalf("read_repo with ../secret should return a tool error (path escape), got ok: %+v", res2.StructuredContent)
	}

	// --- Assertion 3: repo_grep{query:"Auth"} → a hit mentioning a.go ---
	res3, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "repo_grep",
		Arguments: map[string]any{"name": "Ada", "query": "Auth"},
	})
	if err != nil {
		t.Fatalf("repo_grep: %v", err)
	}
	if res3.IsError {
		t.Fatalf("repo_grep returned tool error: %+v", res3.Content)
	}
	var grepOut struct {
		Hits []string `json:"hits"`
	}
	b3, _ := json.Marshal(res3.StructuredContent)
	if err := json.Unmarshal(b3, &grepOut); err != nil {
		t.Fatal(err)
	}
	if len(grepOut.Hits) == 0 {
		t.Fatalf("repo_grep: expected hits for Auth, got none; raw=%s", string(b3))
	}
	foundAGo := false
	for _, h := range grepOut.Hits {
		if strings.HasPrefix(h, "a.go:") {
			foundAGo = true
			break
		}
	}
	if !foundAGo {
		t.Fatalf("repo_grep: expected a hit in a.go, got %v", grepOut.Hits)
	}

	// --- Assertion 4: read_repo as Bob (no claimed mission) → "not on a repo mission" error ---
	res4, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "read_repo",
		Arguments: map[string]any{"name": "Bob", "path": "a.go"},
	})
	if err != nil {
		t.Fatalf("read_repo Bob: %v", err)
	}
	if !res4.IsError {
		t.Fatalf("read_repo with no claimed mission should return a tool error, got ok: %+v", res4.StructuredContent)
	}
}
