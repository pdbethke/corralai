// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/queue"
)

// The demo deadlock of 2026-07-02, at the MCP surface: a bee's claim lands but
// the reply is lost; the bee then heartbeats idle forever while presence keeps
// it un-reapable. Two brain-side rules must recover it:
//  1. claim_task with the SAME name+instance re-issues the orphaned claim, and
//  2. an idle heartbeat requeues the bee's expired-lease claims (slacker rule).
func TestOrphanedClaimRecovery(t *testing.T) {
	dir := t.TempDir()
	cs, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if err := q.Enqueue(1, []queue.TaskSpec{
		{Key: "build", Role: "builder", Title: "build", Instruction: "do build"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.PromoteReady(1); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	go func() {
		_ = NewServer(cs, nil, Options{
			Queue:                   q,
			TaskLeaseSeconds:        0.05, // expires almost immediately
			IdleReclaimGraceSeconds: 0.01,
		}).Run(ctx, st)
	}()
	cl := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	sess, err := cl.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	call := func(tool string, args map[string]any) map[string]any {
		t.Helper()
		res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", tool, err)
		}
		b, _ := json.Marshal(res.StructuredContent)
		var out map[string]any
		_ = json.Unmarshal(b, &out)
		return out
	}

	call("bootstrap", map[string]any{"name": "Bob"})

	// Bob claims; the reply is "lost" (we ignore it). Same name+instance polls
	// again: the brain must re-issue the same task, flagged reissued.
	first := call("claim_task", map[string]any{"name": "Bob", "roles": []string{"builder"}, "instance": "host-1"})
	if first["task"] == nil {
		t.Fatal("first claim returned no task")
	}
	again := call("claim_task", map[string]any{"name": "Bob", "roles": []string{"builder"}, "instance": "host-1"})
	tk, _ := again["task"].(map[string]any)
	if tk == nil || tk["reissued"] != true {
		t.Fatalf("same-instance re-poll should re-issue the orphaned claim, got %v", again)
	}

	// Slacker rule: let the tiny lease + grace expire, then heartbeat idle —
	// the brain requeues Bob's claim and another bee can pick it up.
	time.Sleep(120 * time.Millisecond)
	call("heartbeat", map[string]any{"name": "Bob", "status": "idle"})
	got := call("claim_task", map[string]any{"name": "Tess", "roles": []string{"builder"}, "instance": "host-2"})
	tk2, _ := got["task"].(map[string]any)
	if tk2 == nil {
		t.Fatalf("after idle-heartbeat reclaim the task should be claimable by Tess, got %v", got)
	}
	if tk2["reissued"] == true {
		t.Fatalf("Tess's fresh claim must not be flagged reissued: %v", got)
	}
}
