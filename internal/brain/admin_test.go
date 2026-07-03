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
)

// TestPrincipalWritesRequireHumanAdmin proves add_member, set_superuser, and
// remove_principal gate on isHumanAdmin, not just isAdmin: a session that
// carries a REAL superuser's verified bearer token (so isAdmin passes) but
// self-identifies as a worker (ClientInfo.Name == "corral-agent" — the same
// truthfulness-guardrail signal dev mode uses, which also applies when auth
// is on) must still be refused. A plain, non-worker session on the SAME
// superuser token passes all three.
//
// This exercises the WorkerSessions half of isHumanAdmin under real auth
// (not just dev's unauthenticated-fallback case), since add_member,
// set_superuser, and remove_principal all hard-require opts.Principals != nil
// and so can never exercise dev mode's isAdmin-open fallback.
func TestPrincipalWritesRequireHumanAdmin(t *testing.T) {
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
	if err := pstore.AddMember("target@x.com", "test"); err != nil {
		t.Fatal(err)
	}

	const humanToken = "boss-human-token"
	verify := func(ctx context.Context, token string, r *http.Request) (*sdkauth.TokenInfo, error) {
		if token == humanToken {
			return &sdkauth.TokenInfo{UserID: "boss@x.com", Expiration: time.Now().Add(time.Hour), Extra: map[string]any{"principal": "boss@x.com"}}, nil
		}
		return nil, sdkauth.ErrInvalidToken
	}

	ws := NewWorkerSessions()
	srv := NewServer(cstore, nil, Options{Principals: pstore, WorkerSessions: ws})
	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{DisableLocalhostProtection: true},
	)
	handler := sdkauth.RequireBearerToken(verify, nil)(mcpHandler)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	ctx := context.Background()

	connectAs := func(clientName string) *mcp.ClientSession {
		t.Helper()
		cl := mcp.NewClient(&mcp.Implementation{Name: clientName, Version: "0"}, nil)
		sess, err := cl.Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint:   ts.URL,
			HTTPClient: &http.Client{Transport: bearerRT{token: humanToken}},
		}, nil)
		if err != nil {
			t.Fatalf("connect %s: %v", clientName, err)
		}
		t.Cleanup(func() { sess.Close() })
		return sess
	}

	// The superuser's own token, but the session announces itself as a
	// worker at the MCP handshake — refused all three, same as a delegation
	// token, even though isAdmin alone passes.
	workerSess := connectAs("corral-agent")

	addRes, err := workerSess.CallTool(ctx, &mcp.CallToolParams{Name: "add_member", Arguments: map[string]any{"email": "new@x.com"}})
	if err != nil {
		t.Fatalf("add_member (worker-shaped session) call: %v", err)
	}
	if !addRes.IsError {
		t.Fatal("a worker-shaped session must be refused add_member at the human gate")
	}

	setRes, err := workerSess.CallTool(ctx, &mcp.CallToolParams{Name: "set_superuser", Arguments: map[string]any{
		"email": "target@x.com", "is_superuser": true,
	}})
	if err != nil {
		t.Fatalf("set_superuser (worker-shaped session) call: %v", err)
	}
	if !setRes.IsError {
		t.Fatal("a worker-shaped session must be refused set_superuser at the human gate")
	}

	rmRes, err := workerSess.CallTool(ctx, &mcp.CallToolParams{Name: "remove_principal", Arguments: map[string]any{"email": "target@x.com"}})
	if err != nil {
		t.Fatalf("remove_principal (worker-shaped session) call: %v", err)
	}
	if !rmRes.IsError {
		t.Fatal("a worker-shaped session must be refused remove_principal at the human gate")
	}

	// The SAME token over a plain (non-worker-shaped) session passes.
	humanSess := connectAs("corral-admin")
	var out principalOut
	callTask(t, humanSess, "add_member", map[string]any{"email": "new@x.com"}, &out)
	if !out.OK {
		t.Fatalf("a human session must be able to add_member: %+v", out)
	}
}

// TestSetSuperuserRefusesRealDelegationToken is the auth-on half of Finding
// 2: a REAL minted delegation token (Verifier.MintDelegation -> VerifyToken,
// wired through the same sdkauth.RequireBearerToken middleware production
// uses) for a subagent spawned under a superuser principal must be refused
// set_superuser, even though it still carries the superuser's rolled-up
// UserID and would pass isAdmin.
func TestSetSuperuserRefusesRealDelegationToken(t *testing.T) {
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
	if err := pstore.AddMember("target@x.com", "test"); err != nil {
		t.Fatal(err)
	}

	vf := &auth.Verifier{}
	vf.EnableDelegation([]byte("test-delegation-key"))
	tok, err := vf.MintDelegation("boss@x.com", "boss@x.com/child", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	srv := NewServer(cstore, nil, Options{Principals: pstore})
	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{DisableLocalhostProtection: true},
	)
	// Wire real bearer verification directly (bypassing Verifier.Wrap's
	// `enabled` gate, which requires live OIDC discovery unavailable in a
	// unit test) — the same sdkauth.RequireBearerToken call Wrap makes.
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

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "set_superuser", Arguments: map[string]any{
		"email": "target@x.com", "is_superuser": true,
	}})
	if err != nil {
		t.Fatalf("set_superuser (delegation token) call: %v", err)
	}
	if !res.IsError {
		t.Fatal("a delegation token rolled up to a superuser must be refused set_superuser")
	}
}

// TestCreateSuperuserBootstrapUnaffected proves the isAdmin -> isHumanAdmin
// swap on create_superuser doesn't break the dev first-run bootstrap: when no
// superuser exists yet, an UNAUTHENTICATED, worker-shaped caller still
// succeeds (the bootstrap branch never consults isHumanAdmin or isAdmin at
// all — it only checks opts.Principals.SuperuserCount() == 0). Once a
// superuser exists, a delegation token minted for a subagent under that new
// superuser is refused a second create_superuser, proving the non-bootstrap
// branch now gates on isHumanAdmin (it would have passed under the old
// isAdmin-only check, since a delegation token still rolls UserID up to its
// principal).
func TestCreateSuperuserBootstrapUnaffected(t *testing.T) {
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
	if pstore.SuperuserCount() != 0 {
		t.Fatal("precondition: fresh store must have zero superusers")
	}

	ws := NewWorkerSessions()
	srv := NewServer(cstore, nil, Options{Principals: pstore, WorkerSessions: ws})
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{DisableLocalhostProtection: true},
	)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	ctx := context.Background()

	cl := mcp.NewClient(&mcp.Implementation{Name: "corral-agent", Version: "0"}, nil)
	sess, err := cl.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { sess.Close() })

	// A worker-shaped, UNAUTHENTICATED session still bootstraps the first
	// superuser — the bootstrap branch is open regardless of caller when
	// none exists yet, exactly like `manage.py createsuperuser` on a fresh
	// database.
	var out principalOut
	callTask(t, sess, "create_superuser", map[string]any{"email": "first-admin@x.com"}, &out)
	if !out.OK || !out.Bootstrap {
		t.Fatalf("bootstrap create_superuser must succeed for any caller: %+v", out)
	}
	if pstore.SuperuserCount() != 1 {
		t.Fatalf("want 1 superuser after bootstrap, got %d", pstore.SuperuserCount())
	}

	// Now that a superuser exists, a delegation token minted for a subagent
	// under that new superuser is refused a second create_superuser.
	vf := &auth.Verifier{}
	vf.EnableDelegation([]byte("test-delegation-key"))
	tok, err := vf.MintDelegation("first-admin@x.com", "first-admin@x.com/child", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	delegHandler := sdkauth.RequireBearerToken(vf.VerifyToken, nil)(handler)
	delegTS := httptest.NewServer(delegHandler)
	t.Cleanup(delegTS.Close)

	delegCl := mcp.NewClient(&mcp.Implementation{Name: "delegated-subagent", Version: "0"}, nil)
	delegSess, err := delegCl.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   delegTS.URL,
		HTTPClient: &http.Client{Transport: bearerRT{token: tok}},
	}, nil)
	if err != nil {
		t.Fatalf("connect delegated subagent: %v", err)
	}
	t.Cleanup(func() { delegSess.Close() })

	res, err := delegSess.CallTool(ctx, &mcp.CallToolParams{Name: "create_superuser", Arguments: map[string]any{"email": "second-admin@x.com"}})
	if err != nil {
		t.Fatalf("create_superuser (post-bootstrap, delegation token) call: %v", err)
	}
	if !res.IsError {
		t.Fatal("post-bootstrap, a delegation token must be refused create_superuser")
	}
}

// bearerRT injects an Authorization: Bearer header on every request — the
// minimal transport needed to drive a real bearer-verified MCP session in a
// test without pulling in the full oidc client-credential flow.
type bearerRT struct{ token string }

func (b bearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}
