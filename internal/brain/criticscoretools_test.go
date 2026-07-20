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
	"github.com/pdbethke/corralai/internal/criticscore"
	"github.com/pdbethke/corralai/internal/memory"
	"github.com/pdbethke/corralai/internal/principals"
)

// TestAdjudicateCriticFindingRequiresAdmin mirrors
// TestPromoteControlRequiresAdmin exactly (verbatim copy of the admin gate):
// an unauthenticated in-memory-transport caller against a Principals store
// with a real superuser seeded is refused a tool error, not let through.
func TestAdjudicateCriticFindingRequiresAdmin(t *testing.T) {
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
	cscore, err := criticscore.Open(filepath.Join(dir, "cs.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cscore.Close() })

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() {
		_ = NewServer(cstore, mstore, Options{Principals: pstore, CriticScore: cscore}).Run(ctx, serverT)
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "t1", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect non-admin: %v", err)
	}
	defer sess.Close()

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "adjudicate_critic_finding", Arguments: map[string]any{
		"id": "1:1", "verdict": "confirmed",
	}})
	if err != nil {
		t.Fatalf("adjudicate_critic_finding non-admin call: %v", err)
	}
	if !res.IsError {
		t.Fatal("want tool error for non-admin adjudicate_critic_finding, got success")
	}
}

// TestAdjudicateCriticFindingFlipsAndDropsFromPending proves the admin path
// end to end, under REAL bearer auth (mirroring
// TestPrincipalWritesRequireHumanAdmin's httptest+RequireBearerToken rig,
// since isHumanAdmin's dev-mode fallback only opens for an UNauthenticated
// caller and these tools need an authenticated superuser to pass): seed a
// pending finding via Record, adjudicate it as the superuser, then confirm
// list_pending_critic_findings no longer returns it — a human verdict must
// both flip the row and clear it off the pending queue.
func TestAdjudicateCriticFindingFlipsAndDropsFromPending(t *testing.T) {
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
	const admin = "boss@example.com"
	if err := pstore.CreateSuperuser(admin, "test"); err != nil {
		t.Fatal(err)
	}
	cscore, err := criticscore.Open(filepath.Join(dir, "cs.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cscore.Close() })

	if err := cscore.Record(context.Background(), []criticscore.Finding{{
		ID: "9:1", RecordID: 9, RecordHead: "head", Model: "llama3.2:3b",
		TargetTest: "TestFoo", Scope: "whole-test", Adjudication: "unadjudicated", Source: "auto",
	}}); err != nil {
		t.Fatal(err)
	}

	const humanToken = "boss-human-token"
	verify := func(_ context.Context, token string, _ *http.Request) (*sdkauth.TokenInfo, error) {
		if token == humanToken {
			return &sdkauth.TokenInfo{UserID: admin, Expiration: time.Now().Add(time.Hour), Extra: map[string]any{"principal": admin}}, nil
		}
		return nil, sdkauth.ErrInvalidToken
	}

	srv := NewServer(cstore, mstore, Options{Principals: pstore, CriticScore: cscore})
	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{DisableLocalhostProtection: true},
	)
	handler := sdkauth.RequireBearerToken(verify, nil)(mcpHandler)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	ctx := context.Background()

	cl := mcp.NewClient(&mcp.Implementation{Name: "corral-admin", Version: "0"}, nil)
	sess, err := cl.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   ts.URL,
		HTTPClient: &http.Client{Transport: bearerRT{token: humanToken}},
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { sess.Close() })

	var out okMsg
	callTask(t, sess, "adjudicate_critic_finding", map[string]any{"id": "9:1", "verdict": "confirmed"}, &out)
	if !out.OK {
		t.Fatalf("adjudicate_critic_finding as admin should succeed: %+v", out)
	}

	f, ok, err := cscore.Get(context.Background(), "9:1")
	if err != nil || !ok {
		t.Fatalf("finding 9:1 should still exist: ok=%v err=%v", ok, err)
	}
	if f.Adjudication != "confirmed" || f.Source != "human" {
		t.Fatalf("adjudication/source = %q/%q, want confirmed/human", f.Adjudication, f.Source)
	}

	pend, err := cscore.ListPending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pend {
		if p.ID == "9:1" {
			t.Fatal("9:1 must no longer be pending after adjudication")
		}
	}
}
