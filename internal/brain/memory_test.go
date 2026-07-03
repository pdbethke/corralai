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
	"github.com/pdbethke/corralai/internal/principals"
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

// TestAddMemorySharedAndPromoteMemoryRequireAdmin proves shared=true writes
// and promote_memory are admin-gated: an unauthenticated caller against a
// Principals store with a real superuser seeded is refused; the same calls
// against a dev-mode server (Principals nil) succeed.
func TestAddMemorySharedAndPromoteMemoryRequireAdmin(t *testing.T) {
	dir := t.TempDir()
	cstore, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cstore.Close() })
	mstore, err := memory.Open(filepath.Join(dir, "m.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mstore.Close() })
	pstore, err := principals.Open(filepath.Join(dir, "p.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pstore.Close() })
	if err := pstore.CreateSuperuser("real-admin@example.com", "test"); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// --- non-admin server (Principals seeded, unauthenticated caller) ---
	clientT1, serverT1 := mcp.NewInMemoryTransports()
	go func() {
		_ = NewServer(cstore, mstore, Options{Principals: pstore}).Run(ctx, serverT1)
	}()
	client1 := mcp.NewClient(&mcp.Implementation{Name: "t1", Version: "0"}, nil)
	sess1, err := client1.Connect(ctx, clientT1, nil)
	if err != nil {
		t.Fatalf("connect non-admin: %v", err)
	}
	defer sess1.Close()

	res, err := sess1.CallTool(ctx, &mcp.CallToolParams{Name: "add_memory", Arguments: map[string]any{
		"name": "team-note", "body": "shared fact", "shared": true,
	}})
	if err != nil {
		t.Fatalf("add_memory shared non-admin call: %v", err)
	}
	if !res.IsError {
		t.Fatal("want tool error for non-admin add_memory(shared=true), got success")
	}

	res2, err := sess1.CallTool(ctx, &mcp.CallToolParams{Name: "promote_memory", Arguments: map[string]any{
		"name": "team-note", "shared": true,
	}})
	if err != nil {
		t.Fatalf("promote_memory non-admin call: %v", err)
	}
	if !res2.IsError {
		t.Fatal("want tool error for non-admin promote_memory, got success")
	}

	// --- admin server (Principals nil => unauthenticated = admin, dev mode) ---
	clientT2, serverT2 := mcp.NewInMemoryTransports()
	go func() {
		_ = NewServer(cstore, mstore, Options{}).Run(ctx, serverT2)
	}()
	client2 := mcp.NewClient(&mcp.Implementation{Name: "t2", Version: "0"}, nil)
	sess2, err := client2.Connect(ctx, clientT2, nil)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer sess2.Close()

	var added addOut
	callTask(t, sess2, "add_memory", map[string]any{
		"name": "team-note", "body": "shared fact", "shared": true,
	}, &added)
	if added.Slug == "" {
		t.Fatalf("add_memory(shared=true) by admin failed: %+v", added)
	}
	var promoted okMsg
	callTask(t, sess2, "promote_memory", map[string]any{"name": "team-note", "shared": false}, &promoted)
	if !promoted.OK {
		t.Fatalf("promote_memory by admin failed: %+v", promoted)
	}
}
