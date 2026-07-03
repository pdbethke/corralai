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
	"github.com/pdbethke/corralai/internal/repoindex"
)

func TestRepoSearch(t *testing.T) {
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

	// Open a repoindex store (nil embedder — keyword floor is sufficient for exact token).
	idx, err := repoindex.Open(filepath.Join(root, "rc.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idx.Close() })

	// Create a repo mission.
	mid, err := mission.CreateMission(mstore, q, "search directive", []mission.PhaseSpec{
		{Name: "build", Role: "builder", Count: 1, Instruction: "build it"},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := mstore.SetRepo(mid, "https://github.com/example/repo", "main", "corralai/m1"); err != nil {
		t.Fatal(err)
	}
	q.PromoteReady(mid)

	// Seed the mission working dir with auth.go containing "Authenticate".
	workDir := mission.MissionDir(ws, mid)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	authContent := `package auth

// Authenticate verifies the given token against the OIDC provider.
func Authenticate(token string) error {
	return nil
}
`
	if err := os.WriteFile(filepath.Join(workDir, "auth.go"), []byte(authContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Index the file directly (the initial-index provisioning path is tested in missions_test.go).
	if err := idx.IndexPaths(mid, workDir, []string{"auth.go"}); err != nil {
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
			Index:     idx,
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

	// --- Assertion 1: repo_search for "Authenticate" → hit for auth.go ---
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "repo_search",
		Arguments: map[string]any{"name": "Ada", "query": "Authenticate"},
	})
	if err != nil {
		t.Fatalf("repo_search: %v", err)
	}
	if res.IsError {
		t.Fatalf("repo_search returned tool error: %+v", res.Content)
	}
	var out repoSearchOut
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode repo_search result: %v", err)
	}
	if len(out.Hits) == 0 {
		t.Fatal("repo_search: expected at least one hit for 'Authenticate', got none")
	}
	foundAuth := false
	for _, h := range out.Hits {
		if h.Path == "auth.go" {
			foundAuth = true
		}
	}
	if !foundAuth {
		t.Fatalf("repo_search: expected a hit for auth.go, got %+v", out.Hits)
	}

	// --- Assertion 2: repo_search as Bob (no claimed mission) → "not on a repo mission" error ---
	res2, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "repo_search",
		Arguments: map[string]any{"name": "Bob", "query": "Authenticate"},
	})
	if err != nil {
		t.Fatalf("repo_search Bob: %v", err)
	}
	if !res2.IsError {
		t.Fatalf("repo_search with no claimed mission should return a tool error, got ok: %+v", res2.StructuredContent)
	}
	if msg := toolErrText(res2); !strings.Contains(msg, "not on a repo mission") {
		t.Fatalf("repo_search no-claim: expected 'not on a repo mission', got %q", msg)
	}
}
