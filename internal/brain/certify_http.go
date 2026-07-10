// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"crypto/ed25519"
	"encoding/hex"
	"net/http"
)

// CertifyPubkeyHandler serves the brain's certify Ed25519 public key as
// hex-encoded plain text. It is deliberately unauthenticated (mirroring
// /healthz): the whole point of publishing the key is to let a third party
// — someone with only a persisted build_records row, no brain credentials —
// independently verify a corral certify signature with
// certify.VerifyStatement. Only the public key is ever handled here; the
// private signing key never reaches this code path.
func CertifyPubkeyHandler(pub ed25519.PublicKey) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(hex.EncodeToString(pub)))
	}
}
