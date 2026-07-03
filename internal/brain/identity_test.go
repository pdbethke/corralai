// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/auth"
	"github.com/pdbethke/corralai/internal/principals"
)

func reqWith(principal, tenant string) *mcp.CallToolRequest {
	return &mcp.CallToolRequest{Extra: &mcp.RequestExtra{TokenInfo: &sdkauth.TokenInfo{
		UserID: principal,
		Extra:  map[string]any{"tenant_id": tenant},
	}}}
}

func TestIdentityIsAuthoritative(t *testing.T) {
	// A verified principal overrides whatever name the client supplied.
	if got := identity(reqWith("real@x.com", ""), "FakeName"); got != "real@x.com" {
		t.Errorf("identity = %q, want verified principal to win", got)
	}
	// No token (dev mode) falls back to the client-supplied name.
	if got := identity(nil, "Fallback"); got != "Fallback" {
		t.Errorf("identity(nil) = %q, want fallback", got)
	}
	if got := identity(&mcp.CallToolRequest{}, "Fallback"); got != "Fallback" {
		t.Errorf("identity(no token) = %q, want fallback", got)
	}
	// Tenant rides along.
	if _, tn := actor(reqWith("u@x.com", "acme")); tn != "acme" {
		t.Errorf("tenant = %q, want acme", tn)
	}
}

func TestMemoryOwnerGate(t *testing.T) {
	open := Options{}
	if !open.isMemoryOwner(reqWith("a@x.com", "")) {
		t.Error("empty owners => memory open to any authorized caller")
	}
	o := Options{MemoryOwners: map[string]bool{"owner@x.com": true}}
	if !o.isMemoryOwner(reqWith("owner@x.com", "")) {
		t.Error("owner must be allowed")
	}
	if o.isMemoryOwner(reqWith("other@x.com", "")) {
		t.Error("non-owner must be denied")
	}
	if o.isMemoryOwner(nil) {
		t.Error("no identity must be denied when owners are set")
	}
}

// TestIsHumanAdminRefusesDelegationToken is the human gate's auth-on unit
// test: a REAL minted delegation token (EnableDelegation -> MintDelegation ->
// VerifyToken — the same path production bearer auth uses, not a hand-built
// TokenInfo) for a subagent spawned under a superuser principal must still
// pass isAdmin (the gap this feature closes — UserID rolls up to the
// principal) but must be refused by isHumanAdmin.
func TestIsHumanAdminRefusesDelegationToken(t *testing.T) {
	pstore, err := principals.Open(filepath.Join(t.TempDir(), "p.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pstore.Close() })
	if err := pstore.CreateSuperuser("boss@x.com", "test"); err != nil {
		t.Fatal(err)
	}
	o := Options{Principals: pstore}

	// The superuser's own token: no subagent claim, passes both gates.
	human := reqWith("boss@x.com", "")
	if !o.isAdmin(human) || !o.isHumanAdmin(human) {
		t.Fatal("the superuser's own token must pass both isAdmin and isHumanAdmin")
	}

	// A real delegation token minted for a subagent under that superuser.
	vf := &auth.Verifier{}
	vf.EnableDelegation([]byte("test-delegation-key"))
	tok, err := vf.MintDelegation("boss@x.com", "boss@x.com/child", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ti, err := vf.VerifyToken(context.Background(), tok, nil)
	if err != nil {
		t.Fatal(err)
	}
	delegated := &mcp.CallToolRequest{Extra: &mcp.RequestExtra{TokenInfo: ti}}

	if !o.isAdmin(delegated) {
		t.Fatal("a delegation token rolled up to a superuser must still pass isAdmin (the gap this feature closes)")
	}
	if o.isHumanAdmin(delegated) {
		t.Fatal("isHumanAdmin must refuse a delegation token even when isAdmin passes")
	}
	if subagentOf(delegated) != "boss@x.com/child" {
		t.Fatalf("subagentOf = %q, want boss@x.com/child", subagentOf(delegated))
	}
}
