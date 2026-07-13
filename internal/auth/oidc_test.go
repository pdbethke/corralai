// SPDX-License-Identifier: Elastic-2.0

package auth

import (
	"context"
	"testing"
)

// TestNewVerifierRefusesEmptyAudienceByDefault covers H-3: an empty configured
// Audience used to silently set oidc.Config.SkipClientIDCheck = true, letting
// ANY client's token from the trusted issuer through (within-issuer token
// confusion). NewVerifier must now refuse to build a verifier for an empty
// audience unless the operator explicitly opts in via
// CORRALAI_OIDC_ALLOW_EMPTY_AUDIENCE=1.
func TestNewVerifierRefusesEmptyAudienceByDefault(t *testing.T) {
	idp := newFakeIdP(t)

	if _, err := NewVerifier(context.Background(), []Pair{{Issuer: idp.issuer, Audience: ""}}); err == nil {
		t.Fatal("empty audience must be refused without explicit opt-in")
	}

	t.Setenv("CORRALAI_OIDC_ALLOW_EMPTY_AUDIENCE", "1")
	if _, err := NewVerifier(context.Background(), []Pair{{Issuer: idp.issuer, Audience: ""}}); err != nil {
		t.Fatalf("opt-in should allow empty audience: %v", err)
	}
}
