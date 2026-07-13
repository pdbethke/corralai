// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/principals"
)

// TestSendInstructionNamespaceGuard proves send_instruction cannot cross a
// principal boundary: an authenticated non-admin principal may only steer agents
// in its OWN namespace (itself or its "p/..." subagents), never another
// principal's agent. A superuser may instruct anyone; unauthenticated dev is
// unrestricted (covered elsewhere). Without the guard, one principal could queue
// commands into another principal's agent inbox and drive its work loop.
func TestSendInstructionNamespaceGuard(t *testing.T) {
	dir := t.TempDir()
	cstore, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cstore.Close() })
	pstore, err := principals.Open(filepath.Join(dir, "p.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pstore.Close() })
	if err := pstore.CreateSuperuser("boss@x.com", "test"); err != nil {
		t.Fatal(err)
	}
	if err := pstore.AddMember("alice@x.com", "test"); err != nil {
		t.Fatal(err)
	}
	if err := pstore.AddMember("bob@x.com", "test"); err != nil {
		t.Fatal(err)
	}

	tokens := map[string]string{
		"alice-tok": "alice@x.com",
		"bob-tok":   "bob@x.com",
		"boss-tok":  "boss@x.com",
	}
	verify := func(_ context.Context, token string, _ *http.Request) (*sdkauth.TokenInfo, error) {
		if p, ok := tokens[token]; ok {
			return &sdkauth.TokenInfo{UserID: p, Expiration: time.Now().Add(time.Hour)}, nil
		}
		return nil, sdkauth.ErrInvalidToken
	}

	srv := NewServer(cstore, nil, Options{Principals: pstore})
	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{DisableLocalhostProtection: true},
	)
	handler := sdkauth.RequireBearerToken(verify, nil)(mcpHandler)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	ctx := context.Background()

	connect := func(token string) *mcp.ClientSession {
		t.Helper()
		cl := mcp.NewClient(&mcp.Implementation{Name: "corral-cli", Version: "0"}, nil)
		sess, err := cl.Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint:   ts.URL,
			HTTPClient: &http.Client{Transport: bearerRT{token: token}},
		}, nil)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		t.Cleanup(func() { sess.Close() })
		return sess
	}

	// alice may NOT instruct bob's agent.
	aliceSess := connect("alice-tok")
	res, err := aliceSess.CallTool(ctx, &mcp.CallToolParams{Name: "send_instruction", Arguments: map[string]any{
		"target": "bob@x.com/worker-1", "text": "drop your task",
	}})
	if err != nil {
		t.Fatalf("cross-principal send_instruction call: %v", err)
	}
	if !res.IsError {
		t.Fatal("alice must be refused send_instruction to bob's agent (cross-principal)")
	}
	if ins, _ := cstore.PendingInstructions("bob@x.com/worker-1"); len(ins) != 0 {
		t.Fatalf("refused instruction must not be queued, got %d", len(ins))
	}

	// alice MAY instruct her own subagent.
	var okOut sendInstructionOut
	callTask(t, aliceSess, "send_instruction", map[string]any{
		"target": "alice@x.com/worker-1", "text": "carry on",
	}, &okOut)
	if !okOut.OK {
		t.Fatalf("alice must be able to instruct her own subagent: %+v", okOut)
	}
	if ins, _ := cstore.PendingInstructions("alice@x.com/worker-1"); len(ins) != 1 {
		t.Fatalf("in-namespace instruction should be queued, got %d", len(ins))
	}

	// A superuser may instruct anyone.
	var bossOut sendInstructionOut
	callTask(t, connect("boss-tok"), "send_instruction", map[string]any{
		"target": "bob@x.com/worker-1", "text": "status?",
	}, &bossOut)
	if !bossOut.OK {
		t.Fatalf("a superuser must be able to instruct any agent: %+v", bossOut)
	}
}
