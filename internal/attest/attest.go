// SPDX-License-Identifier: Elastic-2.0

// Package attest provides cryptographic brain identity for cross-swarm coordination:
// an Ed25519 keypair per brain, signed intents, and a TOFU/allowlist trust registry.
// Verification is fail-closed — any error means "do not trust this intent."
package attest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
)

// KeyPair holds an Ed25519 private+public keypair for a brain.
// Priv NEVER leaves the brain process; it must not be logged or transmitted.
type KeyPair struct {
	Priv ed25519.PrivateKey
	Pub  ed25519.PublicKey
}

// LoadOrCreateKey loads or generates a brain Ed25519 keypair. Priority:
//  1. seedB64 (base64-encoded 32-byte seed, e.g. from env)
//  2. keyFile on disk (same base64 format, may have trailing newline)
//  3. generate fresh key; if keyFile != "" persist the seed at 0600
func LoadOrCreateKey(seedB64, keyFile string) (KeyPair, error) {
	if seedB64 == "" && keyFile != "" {
		if b, err := os.ReadFile(keyFile); err == nil { // #nosec G304 -- path is a server-configured location (db/config/own file), not attacker input
			seedB64 = string(b)
		}
	}
	if seedB64 != "" {
		seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(seedB64))
		if err != nil || len(seed) != ed25519.SeedSize {
			return KeyPair{}, fmt.Errorf("attest: bad brain key seed")
		}
		priv := ed25519.NewKeyFromSeed(seed)
		return KeyPair{Priv: priv, Pub: priv.Public().(ed25519.PublicKey)}, nil
	}
	// generate + persist
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return KeyPair{}, err
	}
	if keyFile != "" {
		// Fail LOUDLY if we cannot persist the identity: returning an unpersisted key
		// would cause the brain to generate a DIFFERENT identity on next restart, silently
		// churning its identity (TOFU Conflict against peers). Callers treat a load error as
		// "coordination disabled, log loudly" — that is the correct fail-closed handling.
		if err := os.WriteFile(keyFile, []byte(base64.StdEncoding.EncodeToString(priv.Seed())), 0o600); err != nil {
			return KeyPair{}, fmt.Errorf("attest: could not persist brain key to %s: %w", keyFile, err)
		}
	}
	return KeyPair{Priv: priv, Pub: pub}, nil
}

// PubB64 returns the base64-encoded public key (standard encoding, no padding strip).
func PubB64(pub ed25519.PublicKey) string { return base64.StdEncoding.EncodeToString(pub) }

// canonical builds an UNAMBIGUOUS message for signing/verification.
// Format: domain-tag (NUL-terminated) + length-prefixed fields (big-endian uint32 len + bytes).
// Length-prefixing each field prevents field-splitting/confusion attacks — no two distinct
// (brainID, kind, subject, ts, nonce, expiresTs) tuples can produce the same byte sequence.
//
// v2 change: expiresTs is now a signed field so an attacker with MotherDuck write access
// cannot resurrect a genuine signed row with a forged far-future expires_ts (the sig would
// cover the original expires_ts, not the attacker-supplied one). Old v1 signatures are not
// valid under v2 (domain tag changed), making the break clean and explicit.
func canonical(brainID, kind, subject string, ts float64, nonce string, expiresTs float64) []byte {
	var b []byte
	put := func(s string) {
		// Fail closed rather than let uint32(len(s)) wrap: a wrapped length prefix
		// would make two distinct inputs share a canonical encoding (a signing
		// ambiguity). Unreachable in practice — every field is a short identifier
		// (brainID/kind/subject/nonce/formatted-float) — but the crypto core asserts
		// it rather than assume it.
		if len(s) > math.MaxUint32 {
			panic("attest: field too large to length-prefix")
		}
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(s))) // #nosec G115 -- guarded: the preceding len(s) > math.MaxUint32 check makes the uint32 conversion safe; fields are short identifiers regardless
		b = append(b, n[:]...)
		b = append(b, s...)
	}
	b = append(b, "corralai-intent-v2\x00"...)
	put(brainID)
	put(kind)
	put(subject)
	put(strconv.FormatFloat(ts, 'f', -1, 64))
	put(nonce)
	put(strconv.FormatFloat(expiresTs, 'f', -1, 64))
	return b
}

// Sign signs a coordination intent for the given brain keypair.
// Returns a base64-encoded Ed25519 signature over the canonical encoding.
// expiresTs is included in the signed message (v2) so that a stored claim's
// lifetime cannot be extended without invalidating the signature.
func Sign(kp KeyPair, brainID, kind, subject string, ts float64, nonce string, expiresTs float64) string {
	return base64.StdEncoding.EncodeToString(
		ed25519.Sign(kp.Priv, canonical(brainID, kind, subject, ts, nonce, expiresTs)),
	)
}

// Verify checks the signature against pubB64 AND enforces freshness (|now-ts| ≤ skew).
// expiresTs must match the value that was signed — a tampered lifetime breaks the signature.
// Nonce non-reuse is enforced by the storage layer ((brain_id, nonce) uniqueness in Task 2).
// FAIL-CLOSED: returns a non-nil error on ANY doubt; callers must treat any error as "refuse."
func Verify(pubB64, brainID, kind, subject string, ts float64, nonce, sig string, expiresTs, now, skew float64) error {
	pub, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errors.New("attest: bad pubkey")
	}
	sigb, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return errors.New("attest: bad signature encoding")
	}
	// Reject non-finite ts: NaN would make both freshness comparisons false and silently
	// pass. Non-exploitable (still needs a valid sig over "NaN"), but reject explicitly.
	if math.IsNaN(ts) || math.IsInf(ts, 0) {
		return errors.New("attest: non-finite ts")
	}
	if d := now - ts; d > skew || d < -skew {
		return fmt.Errorf("attest: stale intent (|now-ts|=%.0f > %.0f)", absf(now-ts), skew)
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), canonical(brainID, kind, subject, ts, nonce, expiresTs), sigb) {
		return errors.New("attest: signature does not verify")
	}
	return nil
}

// Outcome is the result of a Register call.
type Outcome int

const (
	Registered     Outcome = iota // first time: pubkey pinned
	AlreadyTrusted                // same brain+pubkey seen before: no-op
	Conflict                      // different pubkey for already-pinned brain: REFUSED (never overwritten)
	Rejected                      // allowlist set and brain/key not on it
)

// BrainStore is a persistent store for brain_id → pubB64 mappings.
// Implementations must be safe for concurrent use if concurrent Register calls are expected.
type BrainStore interface {
	Get(brainID string) (pubB64 string, ok bool)
	Put(brainID, pubB64 string) error
}

// Register applies the trust policy and pins a brain's pubkey.
//
//   - allowlist != nil → ONLY brains+keys on the allowlist are accepted (TOFU disabled).
//   - allowlist == nil → TOFU: first pubkey for a brainID is pinned; a DIFFERENT key later
//     returns Conflict and the pinned key is NEVER overwritten.
func Register(store BrainStore, brainID, pubB64 string, allowlist map[string]string) (Outcome, error) {
	if allowlist != nil {
		want, ok := allowlist[brainID]
		if !ok || want != pubB64 {
			return Rejected, nil
		}
	}
	if cur, ok := store.Get(brainID); ok {
		if cur == pubB64 {
			return AlreadyTrusted, nil
		}
		// REFUSE — never overwrite a pinned key on conflict
		return Conflict, nil
	}
	if err := store.Put(brainID, pubB64); err != nil {
		return Rejected, err
	}
	return Registered, nil
}

func absf(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
