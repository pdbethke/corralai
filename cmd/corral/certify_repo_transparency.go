// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"os"

	"github.com/pdbethke/corralai/internal/transparency"
)

// rekorBaseURL resolves the Rekor instance --transparency uploads to:
// CORRALAI_REKOR_URL when set (an operator's own instance), else the public
// default (defaultRekorURL, defined in verify.go — the same default `corral
// certify verify` and the brain's own anchoring already use).
func rekorBaseURL() string {
	if u := os.Getenv("CORRALAI_REKOR_URL"); u != "" {
		return u
	}
	return defaultRekorURL
}

// newTransparencyLogger constructs the real Logger --transparency uploads
// through. A package-level var, like collectSelectionEvidence above, so
// tests can substitute a transparency.FakeLogger and exercise the upload
// path with no network.
var newTransparencyLogger = func(baseURL string) transparency.Logger {
	return transparency.NewRekor(baseURL)
}

// transparencyPublicKeyPEM returns the PEM-encoded public half of the local
// certify key (the same CORRALAI_CERTIFY_KEY_FILE-resolved key
// signBuildLocally signs with; loadLocalCertifyKey creates one on first use
// rather than erroring), for --transparency's upload to hand Rekor alongside
// the statement bytes.
func transparencyPublicKeyPEM() ([]byte, error) {
	priv, err := loadLocalCertifyKey()
	if err != nil {
		return nil, fmt.Errorf("loading the local certify key: %w", err)
	}
	der, err := x509.MarshalPKIXPublicKey(priv.Public())
	if err != nil {
		return nil, fmt.Errorf("marshaling the certify public key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}

// uploadToTransparencyLog is --transparency's whole job, factored out of
// runCertifyRepo so it is unit-testable on its own: read the EXACT bytes
// writeAuditStatement wrote to attestPath (never re-serialized — a
// re-marshal could reorder keys or change whitespace, and then the bytes
// Rekor logs would not be the bytes on disk), upload them, and print the
// receipt.
//
// Fails OPEN, always: any error here (reading the file, uploading) is
// printed as ONE stderr line and reported back as ok=false. The caller's
// exit code is never touched by this function — see the doc at its call
// site in runCertifyRepo.
func uploadToTransparencyLog(ctx context.Context, logger transparency.Logger, attestPath string, pubKeyPEM []byte, stdout, stderr io.Writer) (transparency.LogEntry, bool) {
	envelope, err := os.ReadFile(attestPath) // #nosec G304 -- attestPath is the operator's own --attest path, just written by this same process
	if err != nil {
		fmt.Fprintf(stderr, "corral certify --repo: --transparency: reading the attest statement at %s: %v\n", attestPath, err)
		return transparency.LogEntry{}, false
	}
	entry, err := logger.Upload(ctx, envelope, pubKeyPEM)
	if err != nil {
		fmt.Fprintf(stderr, "corral certify --repo: --transparency: uploading to rekor: %v\n", err)
		return transparency.LogEntry{}, false
	}
	fmt.Fprintf(stdout, "  attestation logged: rekor index %d (uuid %s)\n", entry.LogIndex, entry.UUID)
	return entry, true
}
