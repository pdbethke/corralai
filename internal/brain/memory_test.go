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
	"github.com/pdbethke/corralai/internal/memory"
)

func TestBrainMemoryOverMCP(t *testing.T) {
	root := t.TempDir()
	mem := filepath.Join(root, "x-alpha", "memory")
	if err := os.MkdirAll(mem, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(mem, "reference_duck.md"),
		[]byte("---\nname: reference_duck\ndescription: \"duckdb is the analytics engine\"\nmetadata:\n  type: reference\n---\n\nDuckDB powers the memory half via BM25 FTS.\n"), 0o644)

	cstore, err := coord.Open(filepath.Join(root, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cstore.Close() })
	mstore, err := memory.Open(filepath.Join(root, "m.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mstore.Close() })
	if _, err := mstore.Build([]string{mem}); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() { _ = NewServer(cstore, mstore, Options{}).Run(ctx, serverT) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "smoke", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	lt, _ := sess.ListTools(ctx, nil)
	if len(lt.Tools) != 26 { // 8 coord + 3 inbox + 7 admin + 3 subagent + 5 memory
		t.Fatalf("want 26 tools with memory enabled, got %d", len(lt.Tools))
	}

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "search_memory", Arguments: map[string]any{"query": "duckdb analytics", "scope": "default"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("search_memory tool-error: %+v", res.Content)
	}
	var out struct {
		Hits []memory.Hit `json:"hits"`
	}
	b, _ := json.Marshal(res.StructuredContent)
	json.Unmarshal(b, &out)
	if len(out.Hits) == 0 || out.Hits[0].Slug != "reference_duck" {
		t.Fatalf("expected reference_duck hit, got %+v", out.Hits)
	}
}

func TestAddMemoryStampsAuthor(t *testing.T) {
	root := t.TempDir()
	cstore, err := coord.Open(filepath.Join(root, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cstore.Close() })
	mstore, err := memory.Open(filepath.Join(root, "m.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mstore.Close() })

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() { _ = NewServer(cstore, mstore, Options{}).Run(ctx, serverT) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "smoke", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// The agent's brain() closure stamps "author"=<agent name> for add_memory; here
	// we pass it directly. "name" is the entry SLUG (not the agent).
	if _, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "add_memory", Arguments: map[string]any{
		"name": "eval-vuln", "author": "Hawk", "body": "eval() on unsanitized parser input",
		"description": "a vuln", "type": "lesson", "shared": true,
	}}); err != nil {
		t.Fatal(err)
	}

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "search_memory", Arguments: map[string]any{"query": "eval unsanitized"}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res.StructuredContent)
	if !strings.Contains(string(b), `"author":"Hawk"`) {
		t.Fatalf("search_memory hit should carry author Hawk: %s", string(b))
	}
}
