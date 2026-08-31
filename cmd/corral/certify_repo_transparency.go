// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/pdbethke/corralai/internal/certify"
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

// dsseEnvelopeSuffix names the signed DSSE envelope writeAuditStatement
// writes BESIDE the plain --attest statement, when a local signing key is
// available — see writeSignedStatementEnvelope. The plain file at the
// operator's own --attest path is untouched either way: this is a sibling,
// never a replacement, so GitHub's actions/attest flow (the plain file's
// existing consumer) keeps working exactly as it does today.
const dsseEnvelopeSuffix = ".dsse.json"

// dsseEnvelopePathFor returns the envelope path for a given --attest path.
// Deterministic, so no caller needs writeAuditStatement to report it back.
func dsseEnvelopePathFor(attestPath string) string { return attestPath + dsseEnvelopeSuffix }

// transparencyKeyID is the DSSE signature's keyid for a locally-signed
// --attest statement — the same spelling cmd/corral's OTHER certify path
// (signBuildLocally, certify_change.go) already uses for the same
// underlying key: both are the one local corral signing identity.
const transparencyKeyID = "corral-certify"

// loadLocalCertifyKeyIfConfigured loads the local certify key WITHOUT ever
// silently provisioning one. loadLocalCertifyKey (buildstore.LoadOrCreateSigningKey
// underneath) auto-creates a key at whatever path it is given — the
// established, already-shipped behavior for `corral certify`'s own signing,
// where an EXPLICITLY configured path is a deliberate first-run bootstrap.
// This function preserves that for an explicit CORRALAI_CERTIFY_KEY or
// CORRALAI_CERTIFY_KEY_FILE, but refuses outright — never touching disk —
// when NEITHER is set and no key already exists at the default path.
//
// That refusal is the point: a plain `--attest` run has no local key by
// design (its whole purpose is GitHub's KEYLESS actions/attest — see
// writeAuditStatement's doc), and it must keep writing exactly the file it
// writes today, with no new key material appearing on disk as a side
// effect. And a feature that signs entries into a PUBLIC, PERMANENT log
// must not spring a fresh, never-requested signing identity on an operator
// just because they passed --transparency; --transparency's own guard
// (runCertifyRepo) calls this and exits 2, naming CORRALAI_CERTIFY_KEY_FILE,
// rather than silently minting one.
func loadLocalCertifyKeyIfConfigured() (ed25519.PrivateKey, error) {
	if strings.TrimSpace(os.Getenv("CORRALAI_CERTIFY_KEY")) != "" {
		return loadLocalCertifyKey()
	}
	if strings.TrimSpace(os.Getenv("CORRALAI_CERTIFY_KEY_FILE")) != "" {
		return loadLocalCertifyKey()
	}
	if _, err := os.Stat(localCertifyKeyPath()); err != nil {
		return nil, fmt.Errorf("no local signing key is configured — set CORRALAI_CERTIFY_KEY_FILE (or CORRALAI_CERTIFY_KEY) to sign the statement before it can be logged")
	}
	return loadLocalCertifyKey()
}

// transparencyPublicKeyPEM returns the PEM-encoded public half of the SAME
// local certify key writeSignedStatementEnvelope signs with, for
// --transparency's upload to hand Rekor alongside the envelope bytes.
func transparencyPublicKeyPEM() ([]byte, error) {
	priv, err := loadLocalCertifyKeyIfConfigured()
	if err != nil {
		return nil, fmt.Errorf("loading the local certify key: %w", err)
	}
	der, err := x509.MarshalPKIXPublicKey(priv.Public())
	if err != nil {
		return nil, fmt.Errorf("marshaling the certify public key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}

// writeSignedStatementEnvelope signs stmt (the same in-toto statement map
// writeAuditStatement built, before it was marshaled to the plain JSON file)
// into a DSSE envelope (application/vnd.in-toto+json) using the local
// certify key, and writes it to stmtPath's envelope path. It is the ONE
// function that produces this envelope, so writeAuditStatement's
// best-effort call and any direct test of the signing behavior exercise
// identical logic — see TestWriteSignedStatementEnvelopeIsGoldenStructure.
//
// Returns an error (never partial output) when no key is configured
// (loadLocalCertifyKeyIfConfigured), when signing fails, or when the write
// fails. writeAuditStatement treats all of these as non-fatal to the PLAIN
// file it already wrote; --transparency's own early guard is what actually
// gates a run on key availability, before any of this work runs.
func writeSignedStatementEnvelope(stmtPath string, stmt map[string]any) (string, error) {
	priv, err := loadLocalCertifyKeyIfConfigured()
	if err != nil {
		return "", err
	}
	envelope, err := certify.SignDSSE(stmt, priv, transparencyKeyID)
	if err != nil {
		return "", fmt.Errorf("signing the DSSE envelope: %w", err)
	}
	envPath := dsseEnvelopePathFor(stmtPath)
	if err := os.WriteFile(envPath, envelope, 0o600); err != nil {
		return "", fmt.Errorf("writing the DSSE envelope to %s: %w", envPath, err)
	}
	return envPath, nil
}

// uploadToTransparencyLog is --transparency's whole job, factored out of
// runCertifyRepo so it is unit-testable on its own: read the EXACT bytes at
// path — the SIGNED DSSE envelope writeSignedStatementEnvelope wrote,
// never re-serialized — upload them, and print the receipt.
//
// Fails OPEN, always: any error here (reading the file, uploading) is
// printed as ONE stderr line and reported back as ok=false. The caller's
// exit code is never touched by this function — see the doc at its call
// site in runCertifyRepo. A MISSING signing key is refused earlier and
// louder (exit 2, before any real work runs) by --transparency's own guard
// in runCertifyRepo — this function's fail-open contract is for UPLOAD
// failures only (an unreachable log, a rejected entry), never for that.
func uploadToTransparencyLog(ctx context.Context, logger transparency.Logger, envelopePath string, pubKeyPEM []byte, stdout, stderr io.Writer) (transparency.LogEntry, bool) {
	envelope, err := os.ReadFile(envelopePath) // #nosec G304 -- envelopePath is derived from the operator's own --attest path, just written by this same process
	if err != nil {
		fmt.Fprintf(stderr, "corral certify --repo: --transparency: reading the signed envelope at %s: %v\n", envelopePath, err)
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
