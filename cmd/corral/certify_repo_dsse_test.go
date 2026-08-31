// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/secure-systems-lab/go-securesystemslib/dsse"

	"github.com/pdbethke/corralai/internal/certify"
)

// fixtureCertifyKey seeds CORRALAI_CERTIFY_KEY_FILE with a deterministic
// ed25519 key (a fixed 32-byte seed, hex-encoded — the exact format
// buildstore.LoadOrCreateSigningKey reads) so envelope-signing tests are
// reproducible: the SIGNATURE varies run to run (ed25519 is randomized), but
// the key, and therefore the public key a verifier checks against, does not.
func fixtureCertifyKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	path := filepath.Join(t.TempDir(), "fixture_certify_key")
	if err := os.WriteFile(path, []byte(hex.EncodeToString(seed)), 0o600); err != nil {
		t.Fatalf("seeding the fixture key: %v", err)
	}
	t.Setenv("CORRALAI_CERTIFY_KEY", "")
	t.Setenv("CORRALAI_CERTIFY_KEY_FILE", path)
	return priv
}

// TestWriteSignedStatementEnvelopeIsGoldenStructure pins requirement 3: the
// envelope's STRUCTURE is deterministic and verifiable even though the
// signature bytes themselves are randomized by ed25519 — payloadType,
// keyid, and the payload's decoded content are asserted directly, and the
// whole envelope is confirmed to verify under the fixture key's public half.
func TestWriteSignedStatementEnvelopeIsGoldenStructure(t *testing.T) {
	priv := fixtureCertifyKey(t)
	stmtPath := filepath.Join(t.TempDir(), "statement.json")
	stmt := map[string]any{"_type": "https://in-toto.io/Statement/v1", "predicateType": "https://corralai.dev/certify/audit/v1", "predicate": map[string]any{"passed": true}}

	envPath, err := writeSignedStatementEnvelope(stmtPath, stmt)
	if err != nil {
		t.Fatalf("writeSignedStatementEnvelope: %v", err)
	}
	if envPath != stmtPath+dsseEnvelopeSuffix {
		t.Fatalf("envelope path = %q, want %q", envPath, stmtPath+dsseEnvelopeSuffix)
	}
	raw, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("reading the envelope: %v", err)
	}

	var env dsse.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("the envelope is not valid DSSE JSON: %v", err)
	}
	if env.PayloadType != "application/vnd.in-toto+json" {
		t.Errorf("payloadType = %q, want application/vnd.in-toto+json", env.PayloadType)
	}
	if len(env.Signatures) != 1 {
		t.Fatalf("got %d signature(s), want 1", len(env.Signatures))
	}
	if env.Signatures[0].KeyID != transparencyKeyID {
		t.Errorf("keyid = %q, want %q", env.Signatures[0].KeyID, transparencyKeyID)
	}
	if env.Signatures[0].Sig == "" {
		t.Error("the signature itself is empty")
	}

	got, ok, verr := certify.VerifyDSSE(raw, priv.Public().(ed25519.PublicKey))
	if verr != nil || !ok {
		t.Fatalf("VerifyDSSE against the fixture key's own public half: ok=%v err=%v", ok, verr)
	}
	if got["predicateType"] != stmt["predicateType"] {
		t.Errorf("decoded predicateType = %v, want %v", got["predicateType"], stmt["predicateType"])
	}

	// A DIFFERENT key must not verify it — the golden structure is real
	// cryptography, not just JSON shape.
	wrongPub, _, _ := ed25519.GenerateKey(nil)
	if _, ok, _ := certify.VerifyDSSE(raw, wrongPub); ok {
		t.Error("the envelope verified against an unrelated public key")
	}
}

// TestLoadLocalCertifyKeyIfConfiguredRefusesWhenUnconfigured pins the
// no-silent-provisioning rule: with neither CORRALAI_CERTIFY_KEY nor
// CORRALAI_CERTIFY_KEY_FILE set, and no key at the (redirected, so this test
// cannot touch the real home directory) default path, it returns an error
// rather than creating a fresh signing identity nobody asked for.
func TestLoadLocalCertifyKeyIfConfiguredRefusesWhenUnconfigured(t *testing.T) {
	t.Setenv("CORRALAI_CERTIFY_KEY", "")
	t.Setenv("CORRALAI_CERTIFY_KEY_FILE", "")
	t.Setenv("HOME", t.TempDir()) // redirect the DEFAULT path so this can never see a real key

	if _, err := loadLocalCertifyKeyIfConfigured(); err == nil {
		t.Fatal("want an error when no signing key is configured, got nil")
	}
	if _, statErr := os.Stat(filepath.Join(os.Getenv("HOME"), ".claude", "corralai_certify_key")); statErr == nil {
		t.Error("a key was silently created at the default path — this must never happen unconfigured")
	}
}

// TestLoadLocalCertifyKeyIfConfiguredUsesTheConfiguredFile is the positive
// case: CORRALAI_CERTIFY_KEY_FILE set to an existing fixture key succeeds
// and returns exactly that key.
func TestLoadLocalCertifyKeyIfConfiguredUsesTheConfiguredFile(t *testing.T) {
	want := fixtureCertifyKey(t)
	got, err := loadLocalCertifyKeyIfConfigured()
	if err != nil {
		t.Fatalf("loadLocalCertifyKeyIfConfigured: %v", err)
	}
	if !got.Equal(want) {
		t.Error("returned a different key than the one CORRALAI_CERTIFY_KEY_FILE named")
	}
}

// TestLoadLocalCertifyKeyIfConfiguredRejectsACorruptFile: an explicitly
// configured but unreadable/corrupt key file is a real error, distinct from
// "unconfigured" — both refuse, but this one names the actual problem
// (surfaced through --transparency's guard as the reason it exits 2).
func TestLoadLocalCertifyKeyIfConfiguredRejectsACorruptFile(t *testing.T) {
	t.Setenv("CORRALAI_CERTIFY_KEY", "")
	path := filepath.Join(t.TempDir(), "corrupt_key")
	if err := os.WriteFile(path, []byte("not valid hex seed material"), 0o600); err != nil {
		t.Fatalf("seeding a corrupt key file: %v", err)
	}
	t.Setenv("CORRALAI_CERTIFY_KEY_FILE", path)

	if _, err := loadLocalCertifyKeyIfConfigured(); err == nil {
		t.Fatal("want an error for a corrupt configured key file, got nil")
	}
}
