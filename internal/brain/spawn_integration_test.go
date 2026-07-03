// SPDX-License-Identifier: Elastic-2.0

// internal/brain/spawn_integration_test.go
package brain

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/telemetry"
)

func TestSpawnRefusalRecorded(t *testing.T) {
	dir := t.TempDir()
	store, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	tel, err := telemetry.Open(filepath.Join(dir, "t.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tel.Close() })

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	opts := Options{Telemetry: tel, SpawnBudget: SpawnBudget{MaxChildrenPerParent: 1}}
	go func() { _ = NewServer(store, nil, opts).Run(ctx, serverT) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "smoke", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()

	mk := func(name string) {
		_, _ = sess.CallTool(ctx, &mcp.CallToolParams{
			Name:      "spawn_subagent",
			Arguments: map[string]any{"name": name, "role": "tester"},
		})
	}
	mk("t1") // ok (first child)
	mk("t2") // refused: breadth cap 1 → records a spawn_refused event

	tl, err := tel.AgentTimeline("agent", 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range tl {
		if e.Kind == "spawn_refused" {
			found = true
		}
	}
	if !found {
		t.Fatal("a spawn_refused telemetry event should have been recorded")
	}
}
