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

	"github.com/pdbethke/corralai/internal/auth"
	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/principals"
	"github.com/pdbethke/corralai/internal/telemetry"
)

// TestMissionAnalyticsAdHocSQLIsAHumanDoor: a delegation token minted for a
// subagent under a superuser passes isAdmin and fails isHumanAdmin; the
// ad-hoc SQL door used to gate on isAdmin, so such a token could read the
// whole telemetry store. This test IS the reviewer's reproduction script
// (`corral review --scope internal/brain`, claude-sonnet-5, 2026-09-07),
// inverted: the call must be refused. It fails on the unfixed code.
func TestMissionAnalyticsAdHocSQLIsAHumanDoor(t *testing.T) {
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
	tel, err := telemetry.Open(filepath.Join(dir, "t.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tel.Close() })

	vf := &auth.Verifier{}
	vf.EnableDelegation([]byte("test-delegation-key-that-is-32-bytes!!"))
	tok, err := vf.MintDelegation("boss@x.com", "boss@x.com/child", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	srv := NewServer(cstore, nil, Options{Principals: pstore, Telemetry: tel})
	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{DisableLocalhostProtection: true},
	)
	handler := sdkauth.RequireBearerToken(vf.VerifyToken, nil)(mcpHandler)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	ctx := context.Background()

	cl := mcp.NewClient(&mcp.Implementation{Name: "delegated-subagent", Version: "0"}, nil)
	sess, err := cl.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   ts.URL,
		HTTPClient: &http.Client{Transport: bearerRT{token: tok}},
	}, nil)
	if err != nil {
		t.Fatalf("connect delegated subagent: %v", err)
	}
	t.Cleanup(func() { sess.Close() })

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "mission_analytics", Arguments: map[string]any{
		"sql": "SELECT 1",
	}})
	if err != nil {
		t.Fatalf("mission_analytics (delegation token) call: %v", err)
	}
	if !res.IsError {
		t.Fatalf("a delegation token ran ad-hoc SQL — the door gates on isAdmin, not isHumanAdmin: %+v", res)
	}
}
