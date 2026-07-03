// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/gateway"
)

// TestPromoteEndpointRequiresHumanAdmin proves promote_endpoint gates on
// isHumanAdmin, not just isAdmin: making a personal upstream endpoint
// fleet-public (or scoped) is the same class of behavior-shaping write as
// publishing a skill or vetting a proposal, so a worker session must be
// refused exactly like it is for the other gated admin writes. A fresh,
// never-bootstrapped session still passes.
func TestPromoteEndpointRequiresHumanAdmin(t *testing.T) {
	dir := t.TempDir()
	cstore, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cstore.Close() })
	gstore, err := gateway.Open(filepath.Join(dir, "g.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gstore.Close() })
	if err := gstore.Register(gateway.Endpoint{Name: "upstream1", Transport: "http", Endpoint: "http://example.invalid", Enabled: true},
		gateway.Auth{}, "owner@x.com"); err != nil {
		t.Fatal(err)
	}

	ws := NewWorkerSessions()
	srv := NewServer(cstore, nil, Options{Gateway: gstore, WorkerSessions: ws})
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

	// A worker session (marked by calling bootstrap, exactly like every
	// shipped corral-agent) is refused promote_endpoint — even though,
	// pre-fix, isAdmin alone would have let it through in dev mode.
	workerSess := connect("neutral-client")
	if _, err := workerSess.CallTool(ctx, &mcp.CallToolParams{Name: "bootstrap", Arguments: map[string]any{"name": "worker1"}}); err != nil {
		t.Fatalf("bootstrap call: %v", err)
	}
	res, err := workerSess.CallTool(ctx, &mcp.CallToolParams{Name: "promote_endpoint", Arguments: map[string]any{
		"name": "upstream1", "public": true,
	}})
	if err != nil {
		t.Fatalf("promote_endpoint (worker session) call: %v", err)
	}
	if !res.IsError {
		t.Fatal("a worker session must be refused promote_endpoint at the human gate")
	}

	// A fresh, never-bootstrapped session (human/dev-shaped, unmarked) passes.
	humanSess := connect("corral-admin")
	var out okMsg
	callTask(t, humanSess, "promote_endpoint", map[string]any{"name": "upstream1", "public": true}, &out)
	if !out.OK {
		t.Fatalf("a human/dev session must be able to promote_endpoint: %+v", out)
	}
}
