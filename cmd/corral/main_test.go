// SPDX-License-Identifier: Elastic-2.0

package main

import "testing"

// TestInsecureBindRefused proves the H-3 startup interlock: running with auth
// DISABLED (empty CORRALAI_OIDC_ISSUER) while bound to a non-loopback address
// is an open control plane and must be refused unless the operator explicitly
// opts in via CORRALAI_ALLOW_INSECURE=1. Loopback dev binds and auth-ON
// configs are never refused.
func TestInsecureBindRefused(t *testing.T) {
	// auth off + non-loopback + no override => refuse
	if err := insecureBindRefused(false, "0.0.0.0:9019", false); err == nil {
		t.Fatal("must refuse auth-off on 0.0.0.0")
	}
	// loopback is fine
	if err := insecureBindRefused(false, "127.0.0.1:9019", false); err != nil {
		t.Fatalf("loopback ok: %v", err)
	}
	// all-interfaces bind (no host, e.g. ":9019") is refused too
	if err := insecureBindRefused(false, ":9019", false); err == nil {
		t.Fatal("must refuse auth-off on all-interfaces bind")
	}
	// override allows it
	if err := insecureBindRefused(false, "0.0.0.0:9019", true); err != nil {
		t.Fatalf("override ok: %v", err)
	}
	// auth on => never refused
	if err := insecureBindRefused(true, "0.0.0.0:9019", false); err != nil {
		t.Fatalf("auth-on ok: %v", err)
	}
}
