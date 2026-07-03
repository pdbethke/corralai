// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/coord"
)

func TestBrainOverMCP(t *testing.T) {
	store, err := coord.Open(filepath.Join(t.TempDir(), "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() { _ = NewServer(store, nil, Options{ExecRing: NewExecRing()}).Run(ctx, serverT) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "smoke", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()

	lt, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(lt.Tools) != 22 { // 8 coord + 3 inbox + 7 admin + 3 subagent (whoami + 5 role tools + mint_observer) + 1 exec
		names := make([]string, len(lt.Tools))
		for i, tl := range lt.Tools {
			names[i] = tl.Name
		}
		t.Fatalf("want 22 tools, got %d: %v", len(lt.Tools), names)
	}

	for _, name := range []string{"BlueLake", "GreenCastle"} {
		res, err := sess.CallTool(ctx, &mcp.CallToolParams{
			Name: "bootstrap", Arguments: map[string]any{"name": name, "task": "smoke"}})
		if err != nil {
			t.Fatalf("bootstrap %s: %v", name, err)
		}
		if res.IsError {
			t.Fatalf("bootstrap %s tool-error: %+v", name, res.Content)
		}
	}

	claim := func(name string) coord.ClaimResult {
		res, err := sess.CallTool(ctx, &mcp.CallToolParams{
			Name: "claim_paths", Arguments: map[string]any{"name": name, "paths": []string{"src/auth.go"}}})
		if err != nil {
			t.Fatalf("claim %s: %v", name, err)
		}
		if res.IsError {
			t.Fatalf("claim %s tool-error: %+v", name, res.Content)
		}
		var cr coord.ClaimResult
		b, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(b, &cr); err != nil {
			t.Fatalf("decode claim result: %v (%s)", err, b)
		}
		return cr
	}

	if first := claim("BlueLake"); len(first.Conflicts) != 0 {
		t.Fatalf("first claim should be clean: %+v", first)
	}
	second := claim("GreenCastle")
	if len(second.Granted) != 0 || len(second.Conflicts) != 1 || second.Conflicts[0].HeldBy != "BlueLake" {
		t.Fatalf("second exclusive claim must be blocked (not granted) WITH a conflict held by BlueLake: %+v", second)
	}
}
