// SPDX-License-Identifier: Elastic-2.0

package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// fakeIdP is a test-only OpenID Connect provider: an httptest server that serves
// the two documents go-oidc needs to verify a JWT end-to-end — a discovery doc at
// /.well-known/openid-configuration and a JWKS at /keys — backed by a single RSA
// keypair. It lets auth tests mint a REAL signed JWT and run it through the real
// coreos/go-oidc verification path (JWKS fetch, signature check, exp/aud/iss
// enforcement) instead of stubbing VerifyToken.
type fakeIdP struct {
	srv    *httptest.Server
	issuer string // == srv.URL; go-oidc requires discovery.issuer to equal the fetch URL
	priv   *rsa.PrivateKey
	kid    string
}

// newFakeIdP starts a fake IdP server (torn down via t.Cleanup) and returns it.
// The discovery doc's "issuer" is set to the server's own base URL — go-oidc
// rejects any provider whose advertised issuer differs from where it fetched the
// discovery doc, so this MUST equal srv.URL. jwks_uri points back at the same
// server's /keys handler, and the JWKS advertises the signing key under kid so a
// token minted by sign() (whose header carries the same kid) resolves to it.
func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	idp := &fakeIdP{priv: priv, kid: "test-key-1"}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                idp.issuer,
			"jwks_uri":                              idp.issuer + "/keys",
			"authorization_endpoint":                idp.issuer + "/auth",
			"token_endpoint":                        idp.issuer + "/token",
			"response_types_supported":              []string{"id_token"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		jwks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key:       priv.Public(),
			KeyID:     idp.kid,
			Algorithm: string(jose.RS256),
			Use:       "sig",
		}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwks)
	})

	idp.srv = httptest.NewServer(mux)
	idp.issuer = idp.srv.URL
	t.Cleanup(idp.srv.Close)
	return idp
}

// sign mints a compact-serialized RS256 JWT over the given claims, signed with the
// IdP's private key. The JWT header carries kid (via the JSONWebKey wrapper), so
// go-oidc resolves it against the matching key published at /keys. Callers supply
// the full claim set (iss/aud/exp/iat/sub/email/...) so a test can exercise each
// verification branch (bad aud, wrong iss, expired, email_verified, ...) by
// varying the map.
func (i *fakeIdP) sign(t *testing.T, claims map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(
		jose.SigningKey{
			Algorithm: jose.RS256,
			Key:       jose.JSONWebKey{Key: i.priv, KeyID: i.kid, Algorithm: string(jose.RS256)},
		},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	jws, err := signer.Sign(payload)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	tok, err := jws.CompactSerialize()
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return tok
}

// TestFakeIdPMintAndVerify is the harness smoke test: it proves a REAL signed JWT,
// minted by the fake IdP and verified end-to-end through coreos/go-oidc (discovery
// + JWKS fetch + RS256 signature + exp/aud/iss checks), lands as a TokenInfo whose
// UserID is the email principal. Not a stub — VerifyToken runs the production path.
func TestFakeIdPMintAndVerify(t *testing.T) {
	idp := newFakeIdP(t)
	ctx := context.Background()

	vf, err := NewVerifier(ctx, []Pair{{Issuer: idp.issuer, Audience: "corral-svc"}})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	if !vf.Enabled() {
		t.Fatal("verifier should be enabled with a real issuer")
	}

	now := time.Now()
	tok := idp.sign(t, map[string]any{
		"iss":            idp.issuer,
		"aud":            "corral-svc",
		"sub":            "user-123",
		"email":          "a@b.co",
		"email_verified": true,
		"iat":            now.Unix(),
		"exp":            now.Add(time.Hour).Unix(),
	})

	ti, err := vf.VerifyToken(ctx, tok, nil)
	if err != nil {
		t.Fatalf("VerifyToken rejected a valid signed JWT: %v", err)
	}
	if ti.UserID != "a@b.co" {
		t.Fatalf("UserID = %q, want %q", ti.UserID, "a@b.co")
	}
}
