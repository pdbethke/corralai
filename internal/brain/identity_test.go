// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"testing"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
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
