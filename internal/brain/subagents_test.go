// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/rolemodel"
)

// TestSpawnAppliesPolicyModelWhenAvailable verifies the full apply-on-spawn path:
// a brain with policy deployer=anthropic:claude-opus and a HostBook containing a
// live agent with that model returns Model+Backend in the spawn_subagent response.
func TestSpawnAppliesPolicyModelWhenAvailable(t *testing.T) {
	store, err := coord.Open(filepath.Join(t.TempDir(), "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	pol, _ := rolemodel.Parse("deployer=anthropic:claude-opus")
	book := NewHostBook()
	// Seed the pool: a live agent with the policy model is connected.
	book.Set(Host{
		Agent: "bee/deployer-1", Role: "deployer",
		Backend: "anthropic", Model: "claude-opus",
		TS: time.Now().Unix(),
	})

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	opts := Options{RoleModels: pol, HostBook: book}
	go func() { _ = NewServer(store, nil, opts).Run(ctx, serverT) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "spawn_subagent",
		Arguments: map[string]any{"name": "dep-1", "role": "deployer"},
	})
	if err != nil || res.IsError {
		t.Fatalf("spawn_subagent failed: err=%v isError=%v content=%v", err, res.IsError, res.Content)
	}
	var out spawnSubagentOut
	b, _ := json.Marshal(res.StructuredContent)
	_ = json.Unmarshal(b, &out)

	if out.Model != "claude-opus" {
		t.Errorf("expected Model=claude-opus, got %q", out.Model)
	}
	if out.Backend != "anthropic" {
		t.Errorf("expected Backend=anthropic, got %q", out.Backend)
	}
}

// TestSpawnFallsBackWhenModelUnavailable verifies degrade-never-block: the same
// policy but a pool WITHOUT the model → Model and Backend are empty in the
// spawn_subagent response (child inherits parent/default model).
func TestSpawnFallsBackWhenModelUnavailable(t *testing.T) {
	store, err := coord.Open(filepath.Join(t.TempDir(), "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	pol, _ := rolemodel.Parse("deployer=anthropic:claude-opus")
	book := NewHostBook() // empty pool — the model is not connected

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	opts := Options{RoleModels: pol, HostBook: book}
	go func() { _ = NewServer(store, nil, opts).Run(ctx, serverT) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "spawn_subagent",
		Arguments: map[string]any{"name": "dep-1", "role": "deployer"},
	})
	if err != nil || res.IsError {
		t.Fatalf("spawn_subagent failed unexpectedly (must not error): err=%v isError=%v", err, res.IsError)
	}
	var out spawnSubagentOut
	b, _ := json.Marshal(res.StructuredContent)
	_ = json.Unmarshal(b, &out)

	// Model unavailable → no override injected; child inherits parent/default.
	if out.Model != "" {
		t.Errorf("model unavailable: expected empty Model, got %q", out.Model)
	}
	if out.Backend != "" {
		t.Errorf("model unavailable: expected empty Backend, got %q", out.Backend)
	}
	// Spawn must still succeed — degrade-never-block.
	if out.Name == "" {
		t.Error("spawn_subagent must succeed even when model is unavailable")
	}
}

func TestSubagentsOverMCP(t *testing.T) {
	store, err := coord.Open(filepath.Join(t.TempDir(), "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	// Provide a MintToken so out_of_process spawn works (dev = no real auth).
	minted := ""
	opts := Options{MintToken: func(principal, subagent string, ttl time.Duration) (string, error) {
		minted = principal + "|" + subagent
		return "cdt_test", nil
	}}
	go func() { _ = NewServer(store, nil, opts).Run(ctx, serverT) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "smoke", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()

	call := func(tool string, args map[string]any, out any) {
		t.Helper()
		res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", tool, err)
		}
		if res.IsError {
			t.Fatalf("%s returned error: %+v", tool, res.Content)
		}
		if out != nil {
			b, _ := json.Marshal(res.StructuredContent)
			_ = json.Unmarshal(b, out)
		}
	}

	// Spawn an in-process subagent (dev => parent defaults to "agent").
	var sp struct {
		Name, Parent, Role, Token string
	}
	call("spawn_subagent", map[string]any{"name": "tester", "role": "tester", "task": "run tests"}, &sp)
	if sp.Name != "agent/tester" || sp.Parent != "agent" || sp.Role != "tester" {
		t.Fatalf("spawn result wrong: %+v", sp)
	}
	if sp.Token != "" {
		t.Fatal("in-process spawn should not mint a token")
	}

	// It shows up under list_subagents and in coordination_status with parent+role.
	var ls struct{ Subagents []coord.Agent }
	call("list_subagents", map[string]any{"parent": "agent"}, &ls)
	if len(ls.Subagents) != 1 || ls.Subagents[0].Role != "tester" {
		t.Fatalf("list_subagents wrong: %+v", ls.Subagents)
	}

	// Out-of-process spawn mints a delegation token via the injected minter.
	var sp2 struct{ Name, Token string }
	call("spawn_subagent", map[string]any{"name": "deployer", "role": "deployer", "out_of_process": true}, &sp2)
	if sp2.Token != "cdt_test" {
		t.Fatalf("out-of-process spawn should return a token, got %q", sp2.Token)
	}
	if minted != "agent|agent/deployer" {
		t.Fatalf("minter called with wrong identity: %q", minted)
	}

	// Despawn removes it.
	var ok struct{ OK bool }
	call("despawn_subagent", map[string]any{"name": "agent/tester"}, &ok)
	if !ok.OK {
		t.Fatal("despawn should succeed")
	}
	call("list_subagents", map[string]any{"parent": "agent"}, &ls)
	if len(ls.Subagents) != 1 || ls.Subagents[0].Name != "agent/deployer" {
		t.Fatalf("after despawn expected only deployer, got %+v", ls.Subagents)
	}
}

func TestSpawnBudgetDefaults(t *testing.T) {
	got := SpawnBudget{}.withDefaults()
	if got.MaxAgentsPerPrincipal != 64 || got.MaxSpawnDepth != 4 || got.MaxChildrenPerParent != 8 {
		t.Fatalf("defaults wrong: %+v", got)
	}
	// 0 means default, never unlimited:
	got = SpawnBudget{MaxSpawnDepth: 2}.withDefaults()
	if got.MaxSpawnDepth != 2 || got.MaxAgentsPerPrincipal != 64 {
		t.Fatalf("partial override wrong: %+v", got)
	}
}

func TestSpawnDepthOf(t *testing.T) {
	cases := map[string]int{"Bob": 1, "Bob/t1": 2, "a/b/c/d": 4}
	for name, want := range cases {
		if d := spawnDepthOf(name); d != want {
			t.Fatalf("depth(%q)=%d want %d", name, d, want)
		}
	}
}

func TestBudgetDecisionRefusals(t *testing.T) {
	b := SpawnBudget{MaxAgentsPerPrincipal: 2, MaxSpawnDepth: 3, MaxChildrenPerParent: 2}.withDefaults()
	// over depth (full name has 4 segments, cap 3)
	if err := budgetDecision(b, "a/b/c", "a", 0, 0); err == nil || !strings.Contains(err.Error(), "depth") {
		t.Fatalf("want depth refusal, got %v", err)
	}
	// over breadth (parent already has 2 children, cap 2)
	if err := budgetDecision(b, "a", "root", 2, 0); err == nil || !strings.Contains(err.Error(), "children") {
		t.Fatalf("want breadth refusal, got %v", err)
	}
	// over principal total (2 live, cap 2)
	if err := budgetDecision(b, "a", "root", 0, 2); err == nil || !strings.Contains(err.Error(), "principal") {
		t.Fatalf("want principal refusal, got %v", err)
	}
	// under all caps
	if err := budgetDecision(b, "a/b", "a", 1, 1); err != nil {
		t.Fatalf("under caps should pass, got %v", err)
	}
}

func TestSpawnSubagentRefusedOverBreadth(t *testing.T) {
	store, err := coord.Open(filepath.Join(t.TempDir(), "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	opts := Options{SpawnBudget: SpawnBudget{MaxChildrenPerParent: 1}}
	go func() { _ = NewServer(store, nil, opts).Run(ctx, serverT) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "smoke", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()

	spawn := func(name string) *mcp.CallToolResult {
		res, err := sess.CallTool(ctx, &mcp.CallToolParams{
			Name:      "spawn_subagent",
			Arguments: map[string]any{"name": name, "role": "tester"},
		})
		if err != nil {
			t.Fatalf("spawn %s transport error: %v", name, err)
		}
		return res
	}
	if res := spawn("t1"); res.IsError {
		t.Fatalf("first spawn should succeed: %+v", res.Content)
	}
	if res := spawn("t2"); !res.IsError {
		t.Fatal("second spawn should be refused (breadth cap 1)")
	}
}
