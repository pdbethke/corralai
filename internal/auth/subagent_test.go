// SPDX-License-Identifier: Elastic-2.0

package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
)

// TestSubagentDetectsDelegationToken proves Subagent reads the exact claim a
// REAL minted delegation token carries — not a hand-built stub — by running a
// token all the way through EnableDelegation -> MintDelegation -> VerifyToken
// -> RequireBearerToken, the same path production bearer auth uses.
func TestSubagentDetectsDelegationToken(t *testing.T) {
	vf := &Verifier{}
	vf.EnableDelegation([]byte("test-delegation-key"))
	tok, err := vf.MintDelegation("boss@x.com", "boss@x.com/child", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	var gotSubagent bool
	handler := sdkauth.RequireBearerToken(vf.VerifyToken, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSubagent = Subagent(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if !gotSubagent {
		t.Fatal("Subagent(ctx) must be true for a delegation token")
	}
}

// TestSubagentFalseForHumanToken proves a human's own OIDC-shaped token (no
// subagent claim, only principal/email like oidc.go's VerifyToken sets) does
// NOT trip Subagent.
func TestSubagentFalseForHumanToken(t *testing.T) {
	verify := func(_ context.Context, token string, _ *http.Request) (*sdkauth.TokenInfo, error) {
		return &sdkauth.TokenInfo{
			Expiration: time.Now().Add(time.Hour),
			UserID:     token,
			Extra:      map[string]any{"principal": token, "email": token},
		}, nil
	}

	var gotSubagent bool
	handler := sdkauth.RequireBearerToken(verify, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSubagent = Subagent(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer human@x.com")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if gotSubagent {
		t.Fatal("Subagent(ctx) must be false for a human's OIDC-shaped token")
	}
}

func TestSubagentFalseWithNoToken(t *testing.T) {
	if Subagent(context.Background()) {
		t.Fatal("Subagent(ctx) with no TokenInfo in context must be false")
	}
}
