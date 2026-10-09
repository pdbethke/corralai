// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

// loadExistingCertifyKey is --transparency's guard: the key that will sign
// into a public, permanent log must already exist. It is
// loadLocalCertifyKeyIfConfigured minus that function's one bootstrap — a
// CORRALAI_CERTIFY_KEY_FILE naming a missing file, which `corral certify`
// and `corral review --attest` treat as a deliberate first run. Here it is a
// typo or an unprovisioned CI secret, and minting would sign the entry with
// an identity nobody holds the other half of; --transparency's help promises
// it "never mints a fresh key just to have one", and this is what keeps that
// true (review e1608f971235#R1). The guard runs before any work, so by the
// time the envelope is signed the key it names exists and nothing is minted.
func loadExistingCertifyKey() (ed25519.PrivateKey, error) {
	if strings.TrimSpace(os.Getenv("CORRALAI_CERTIFY_KEY")) == "" {
		if p := strings.TrimSpace(os.Getenv("CORRALAI_CERTIFY_KEY_FILE")); p != "" {
			if _, err := os.Stat(p); err != nil { // #nosec G703 -- the operator's own key path, only stat'd: nothing is read, created or written here
				return nil, fmt.Errorf("CORRALAI_CERTIFY_KEY_FILE names %s, which does not exist (%w) — --transparency never creates a signing key; create it deliberately first", p, err)
			}
		}
	}
	return loadLocalCertifyKeyIfConfigured()
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
// function that produces this envelope, so writeStatement's call and any direct test of the signing
// behavior exercise identical logic — see TestWriteSignedStatementEnvelopeIsGoldenStructure.
//
// Returns an error (never partial output) when no key is configured
// (loadLocalCertifyKeyIfConfigured), when signing fails, or when the write
// fails. writeStatement treats all of these as non-fatal to the PLAIN
// file it already wrote and hands the error back: each caller decides (an
// audit stays silent, a review reports it). --transparency's own early guard is
// what actually gates a run on key availability, before any of this work runs.
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

// writeStatement is the ONE writer of an in-toto statement and its DSSE
// envelope, for audits and reviews alike. The two used to be separate and
// each held half the fixes: one made the directory (2026-09-08), the other
// removed an envelope that no longer matched. Order matters: the stale
// envelope goes BEFORE signing, so a failed or keyless sign leaves no
// envelope rather than a wrong one.
//
// The plain bytes are json.MarshalIndent(stmt, "", "  ") with no trailing
// newline — the form both callers produced before they were merged, and the
// form whose sha256 the signed ledger already carries. Do not change it.
//
// signErr is returned, not acted on: a review reports it; an audit stays
// silent, because an ordinary --attest run has no local key by design.
//
// A stale envelope that cannot be removed fails the write with NOTHING left
// at path: not the new statement, and not an old one. The Action attests
// whatever statement it finds, so a file left behind by a write that
// reported failure was attested while the run said "nothing will be
// attested" (review e1608f971235#R3 — the plain file used to be written
// before the removal was tried).
func writeStatement(path string, stmt map[string]any) (sha, envPath string, signErr, err error) {
	b, err := json.MarshalIndent(stmt, "", "  ")
	if err != nil {
		return "", "", nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", "", nil, err
	}
	if rmErr := os.Remove(dsseEnvelopePathFor(path)); rmErr != nil && !os.IsNotExist(rmErr) {
		if oldErr := os.Remove(path); oldErr != nil && !os.IsNotExist(oldErr) {
			return "", "", nil, fmt.Errorf("removing the stale envelope %s: %w — and the old statement at %s could not be removed either, so it may still be attested: %v", dsseEnvelopePathFor(path), rmErr, path, oldErr)
		}
		return "", "", nil, fmt.Errorf("removing the stale envelope %s: %w", dsseEnvelopePathFor(path), rmErr)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return "", "", nil, err
	}
	sum := sha256.Sum256(b)
	envPath, signErr = writeSignedStatementEnvelope(path, stmt)
	return hex.EncodeToString(sum[:]), envPath, signErr, nil
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
		transparencyFailed(stderr, fmt.Errorf("reading the signed envelope at %s: %w", envelopePath, err))
		return transparency.LogEntry{}, false
	}
	entry, err := logger.Upload(ctx, envelope, pubKeyPEM)
	if err != nil {
		transparencyFailed(stderr, fmt.Errorf("uploading to rekor: %w", err))
		return transparency.LogEntry{}, false
	}
	fmt.Fprintf(stdout, "  attestation logged: rekor index %d (uuid %s)\n", entry.LogIndex, entry.UUID)
	return entry, true
}

// logStatementToTransparency is --transparency's step after the statement is
// written: report a sign failure as itself, else load the public key and
// upload the envelope. It used to sit inline in runCertifyRepo, where the
// sign error had already been dropped by writeAuditStatement — so a failed
// envelope write surfaced only as "no such file" on the read that followed
// (review e1608f971235#R4). Fails OPEN like uploadToTransparencyLog: never
// an exit code, always a line, and on a runner an annotation.
func logStatementToTransparency(attestPath string, signErr error, stdout, stderr io.Writer) (transparency.LogEntry, bool) {
	if signErr != nil {
		transparencyFailed(stderr, fmt.Errorf("the statement was not signed, so there is nothing to log: %w", signErr))
		return transparency.LogEntry{}, false
	}
	pubKeyPEM, err := transparencyPublicKeyPEM()
	if err != nil {
		transparencyFailed(stderr, err)
		return transparency.LogEntry{}, false
	}
	return uploadToTransparencyLog(context.Background(), newTransparencyLogger(rekorBaseURL()), dsseEnvelopePathFor(attestPath), pubKeyPEM, stdout, stderr)
}

// transparencyFailed is every --transparency failure's one report: the
// stderr line, and on a runner the workflow annotation a failed statement
// write and a failed push already raise (review e1608f971235#R2 — this door
// printed only the line, and the job went green with NULL receipt columns).
func transparencyFailed(stderr io.Writer, err error) {
	fmt.Fprintf(stderr, "corral certify --repo: --transparency: %v\n", err)
	runnerWarning(stderr, "corral transparency failed", "the audit ran, but its statement was not logged to the public transparency log: %v", err)
}
