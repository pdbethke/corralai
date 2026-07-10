// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"crypto/ed25519"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCertifyPubkeyHandler locks the shape of GET /api/certify/pubkey: a
// 200, text/plain body that is exactly the hex-encoded public key — no
// wrapping JSON, no auth required (this route exists so a third party with
// no brain credentials can verify a stored certify signature).
func TestCertifyPubkeyHandler(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	h := CertifyPubkeyHandler(pub)

	req := httptest.NewRequest(http.MethodGet, "/api/certify/pubkey", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	ct := rr.Header().Get("Content-Type")
	if ct != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want text/plain", ct)
	}
	want := hex.EncodeToString(pub)
	if rr.Body.String() != want {
		t.Fatalf("body = %q, want %q", rr.Body.String(), want)
	}
}
