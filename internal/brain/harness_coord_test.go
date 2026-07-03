// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/coord"
)

// Over real HTTP, N independent clients race to claim the same path; exactly one
// must win cleanly (relies on the atomic exclusive claim from Task 3).
func TestWireExclusiveRaceOneWinner(t *testing.T) {
	const n = 5
	c := newCohort(t, n)
	for i := 0; i < n; i++ {
		c.call(i, "bootstrap", map[string]any{"name": agentName(i)}, nil)
	}
	results := make([]coord.ClaimResult, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			res, err := c.sessions[i].CallTool(context.Background(), &mcp.CallToolParams{
				Name:      "claim_paths",
				Arguments: map[string]any{"name": agentName(i), "paths": []string{"src/app.go"}, "exclusive": true},
			})
			if err != nil || res.IsError {
				t.Errorf("claim over wire: %v %+v", err, res)
				return
			}
			decode(t, res, &results[i])
		}(i)
	}
	close(start)
	wg.Wait()

	winners := 0
	for _, r := range results {
		if len(r.Granted) == 1 && len(r.Conflicts) == 0 {
			winners++
		} else if len(r.Granted) != 0 {
			t.Fatalf("loser must not be granted: %+v", r)
		}
	}
	if winners != 1 {
		t.Fatalf("want exactly one winner over the wire, got %d", winners)
	}
}

func TestWirePresenceReflectsStatus(t *testing.T) {
	c := newCohort(t, 1)
	c.call(0, "bootstrap", map[string]any{"name": "alice"}, nil)
	c.call(0, "heartbeat", map[string]any{"name": "alice", "status": "awaiting_approval"}, nil)
	var out struct {
		Agents []coord.Agent `json:"agents"`
	}
	c.call(0, "list_active", map[string]any{}, &out)
	if len(out.Agents) != 1 || out.Agents[0].Status != "awaiting_approval" {
		t.Fatalf("presence must reflect status over the wire, got %+v", out.Agents)
	}
}

func TestWireInstructionRoundTrip(t *testing.T) {
	c := newCohort(t, 2)
	c.call(0, "bootstrap", map[string]any{"name": "boss"}, nil)
	c.call(1, "bootstrap", map[string]any{"name": "worker"}, nil)
	c.call(0, "send_instruction", map[string]any{"target": "worker", "text": "claim src/ and refactor"}, nil)

	var inbox struct {
		Instructions []struct {
			ID   int64  `json:"id"`
			Text string `json:"text"`
		} `json:"instructions"`
	}
	c.call(1, "check_instructions", map[string]any{"name": "worker"}, &inbox)
	if len(inbox.Instructions) != 1 || inbox.Instructions[0].Text == "" {
		t.Fatalf("worker should have 1 instruction, got %+v", inbox.Instructions)
	}
	c.call(1, "ack_instruction", map[string]any{"name": "worker", "id": inbox.Instructions[0].ID, "result": "done"}, nil)
	var inbox2 struct {
		Instructions []any `json:"instructions"`
	}
	c.call(1, "check_instructions", map[string]any{"name": "worker"}, &inbox2)
	if len(inbox2.Instructions) != 0 {
		t.Fatalf("acked instruction should clear from pending inbox, got %+v", inbox2.Instructions)
	}
}

func agentName(i int) string { return string(rune('a' + i)) }

func decode(t *testing.T, res *mcp.CallToolResult, out any) {
	t.Helper()
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("decode: %v", err)
	}
}
