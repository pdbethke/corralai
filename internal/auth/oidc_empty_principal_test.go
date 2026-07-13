// SPDX-License-Identifier: Elastic-2.0

package auth

import (
	"context"
	"testing"
	"time"
)

// TestVerifyTokenRejectsEmptyPrincipal covers audit finding M: a token that
// verifies (valid sig/iss/aud/exp) but carries none of email/preferred_username/
// client_id/azp used to yield TokenInfo{UserID:""} with err == nil. With an
// empty allowlist, Allowed("") returns true, so the token authenticated as
// anonymous. VerifyToken must reject such tokens instead of returning a
// TokenInfo with an empty principal.
func TestVerifyTokenRejectsEmptyPrincipal(t *testing.T) {
	idp := newFakeIdP(t)
	vf, err := NewVerifier(context.Background(), []Pair{{Issuer: idp.issuer, Audience: "corral-svc"}})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}

	now := time.Now()
	tok := idp.sign(t, map[string]any{
		"iss": idp.issuer,
		"aud": "corral-svc",
		"sub": "user-123",
		"iat": now.Unix(),
		"exp": now.Add(time.Hour).Unix(),
		// deliberately no email / preferred_username / client_id / azp
	})

	if ti, err := vf.VerifyToken(context.Background(), tok, nil); err == nil {
		t.Fatalf("token with no principal claim must be rejected, got TokenInfo{UserID:%q}, nil", ti.UserID)
	}
}
