// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/learn"
)

// TestWorkerSessionMarksExpire proves the tracker sheds dead sessions: the
// go-sdk offers no exported per-session close hook, and every agent reconnect
// mints a fresh session ID, so without expiry a long-lived brain's map grows
// forever. Marks expire workerSessionTTL after their last touch (Mark or an
// Is hit both refresh), swept lazily during Mark — and a session still
// calling tools is never evicted while alive.
func TestWorkerSessionMarksExpire(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	cur := base
	ws := NewWorkerSessions()
	ws.now = func() time.Time { return cur }

	ws.mark("stale")     // queried later, after its TTL
	ws.mark("abandoned") // never queried again — only the lazy sweep can shed it
	ws.mark("active")    // keeps calling tools

	// Half a TTL later the active worker is still calling admin-gated tools;
	// each Is hit refreshes its last-touched time.
	cur = base.Add(workerSessionTTL / 2)
	if !ws.isMarked("active") {
		t.Fatal("an active session's mark must survive at TTL/2")
	}

	// Past the original TTL: the stale mark is gone (checked and evicted),
	// the refreshed one survives (its TTL restarted at the Is hit).
	cur = base.Add(workerSessionTTL + time.Minute)
	if ws.isMarked("stale") {
		t.Fatal("a mark untouched for over workerSessionTTL must expire")
	}
	if !ws.isMarked("active") {
		t.Fatal("a refreshed mark must survive — no premature eviction of a live worker")
	}

	// The map itself shrinks: a new Mark sweeps expired entries, including
	// ones nobody ever asks about again.
	ws.mark("newcomer")
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if _, ok := ws.ids["abandoned"]; ok {
		t.Fatal("an abandoned session's entry must be swept during Mark")
	}
	if _, ok := ws.ids["stale"]; ok {
		t.Fatal("an expired, queried entry must stay evicted")
	}
	if len(ws.ids) != 2 { // active + newcomer
		t.Fatalf("map must shrink to the live entries, got %d: %v", len(ws.ids), ws.ids)
	}
}

// TestDevModeWorkerSessionRefusedAtHumanGate is the dev-mode half of the
// human gate, end to end over real streamable-HTTP (so each connecting
// client is a genuinely distinct MCP session — required to prove per-session
// marking and that marks don't leak): a session that names itself
// "corral-agent" at the handshake, or that calls bootstrap before trying to
// approve, is refused; a fresh corral-admin-shaped session that never
// bootstraps passes; and the mark on one session never leaks to another.
func TestDevModeWorkerSessionRefusedAtHumanGate(t *testing.T) {
	dir := t.TempDir()
	cstore, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cstore.Close() })
	lstore, err := learn.Open(filepath.Join(dir, "l.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lstore.Close() })

	ws := NewWorkerSessions()
	srv := NewServer(cstore, nil, Options{Learn: lstore, WorkerSessions: ws})
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{DisableLocalhostProtection: true},
	)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	ctx := context.Background()

	connect := func(name string) *mcp.ClientSession {
		t.Helper()
		cl := mcp.NewClient(&mcp.Implementation{Name: name, Version: "0"}, nil)
		sess, err := cl.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL}, nil)
		if err != nil {
			t.Fatalf("connect %s: %v", name, err)
		}
		t.Cleanup(func() { sess.Close() })
		return sess
	}
	seed := func(sig string) int64 {
		t.Helper()
		p, _, err := lstore.Upsert(sig, "finding", "builder", []string{"a"})
		if err != nil {
			t.Fatal(err)
		}
		return p.ID
	}

	// Signal 1: ClientInfo.Name == "corral-agent" — refused immediately, no
	// bootstrap call needed.
	agentID := seed("missing-req|go.mod")
	agentSess := connect("corral-agent")
	res, err := agentSess.CallTool(ctx, &mcp.CallToolParams{Name: "approve_proposal", Arguments: map[string]any{"id": agentID}})
	if err != nil {
		t.Fatalf("approve_proposal (ClientInfo signal) call: %v", err)
	}
	if !res.IsError {
		t.Fatal("a corral-agent-named session must be refused at the human gate")
	}

	// Signal 2: behavior — a neutrally-named session that calls bootstrap
	// first is marked a worker, mirroring every shipped corral-agent (whose
	// first call is always bootstrap).
	behaviorID := seed("missing-req|package.json")
	behaviorSess := connect("neutral-client")
	if _, err := behaviorSess.CallTool(ctx, &mcp.CallToolParams{Name: "bootstrap", Arguments: map[string]any{"name": "worker1"}}); err != nil {
		t.Fatalf("bootstrap call: %v", err)
	}
	res2, err := behaviorSess.CallTool(ctx, &mcp.CallToolParams{Name: "approve_proposal", Arguments: map[string]any{"id": behaviorID}})
	if err != nil {
		t.Fatalf("approve_proposal (behavioral signal) call: %v", err)
	}
	if !res2.IsError {
		t.Fatal("a session that called bootstrap must be refused at the human gate")
	}

	// A fresh corral-admin-shaped session that never bootstraps passes.
	adminID := seed("missing-req|Cargo.toml")
	adminSess := connect("corral-admin")
	var ap approveProposalOut
	callTask(t, adminSess, "approve_proposal", map[string]any{"id": adminID}, &ap)
	if !ap.OK {
		t.Fatalf("a corral-admin-shaped session must pass the human gate: %+v", ap)
	}

	// No leakage: a second fresh, unmarked session also passes even though
	// behaviorSess (same test process) was marked.
	freshID := seed("missing-req|requirements.txt")
	freshSess := connect("another-neutral-client")
	var ap2 approveProposalOut
	callTask(t, freshSess, "approve_proposal", map[string]any{"id": freshID}, &ap2)
	if !ap2.OK {
		t.Fatalf("a fresh unmarked session must pass: %+v", ap2)
	}
}
