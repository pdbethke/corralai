// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/coord"
)

func TestHeartbeatSetsStatus(t *testing.T) {
	dir := t.TempDir()
	cs, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	go func() { _ = NewServer(cs, nil, Options{}).Run(ctx, st) }()
	cl := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	sess, err := cl.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	if _, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "bootstrap", Arguments: map[string]any{"name": "alice"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "heartbeat", Arguments: map[string]any{"name": "alice", "status": "awaiting_approval"}}); err != nil {
		t.Fatal(err)
	}
	agents, _ := cs.ListActive(100000)
	if len(agents) != 1 || agents[0].Status != "awaiting_approval" {
		t.Fatalf("status not recorded via heartbeat: %+v", agents)
	}
}
