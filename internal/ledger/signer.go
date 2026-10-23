// SPDX-License-Identifier: Elastic-2.0

package ledger

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// KindSigner is the entry kind that changes WHO may sign this ledger: a
// register adds a key, a revoke retires one. The entry is itself signed (by
// the root key, once the verifier walk lands), so the set of signers is
// part of the record rather than a file beside it.
const KindSigner = "signer"

// The two things a signer entry can do, and the key id a format <= 4 entry
// carries when the root key signed it. Before corral-ledger-5 keyid was a
// convention name, not a key; "corral-certify" therefore means "the root"
// on those entries, and a format-5 entry must name its key by fingerprint.
const (
	SignerRegister  = "register"
	SignerRevoke    = "revoke"
	LegacyRootKeyID = "corral-certify"
)

// SignerChange is the payload of a KindSigner entry. PubKey is carried in
// full (hex) and KeyID must equal its fingerprint, so a reader can check
// the pairing without any outside table; Reason is required on a revoke
// because a retired key with no stated cause is a record nobody can weigh.
type SignerChange struct {
	Action string `json:"action"`
	KeyID  string `json:"keyid"`
	PubKey string `json:"pubkey"` // hex Ed25519 public key
	Name   string `json:"name"`
	Reason string `json:"reason,omitempty"`
}

// SignerKey is one registered key as a reader holds it after the walk.
type SignerKey struct {
	KeyID  string `json:"keyid"`
	PubKey string `json:"pubkey"`
	Name   string `json:"name"`
}

// KeyFingerprint names a public key: "ed25519:" + the first 16 hex chars
// of sha256(pubkey). Derived from the key, never chosen, so a keyid cannot
// claim a key it is not. 64 bits is a label that is hard to confuse by
// accident, not a collision-resistant identity: the full PubKey travels in
// the register entry and is what signatures are checked against.
func KeyFingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return "ed25519:" + hex.EncodeToString(sum[:])[:16]
}

// SignerShapeProblem is the KindSigner arm of EntryShapeProblem.
func SignerShapeProblem(c *SignerChange) string {
	if c == nil {
		return "a signer entry that carries no signer change"
	}
	if c.Action != SignerRegister && c.Action != SignerRevoke {
		return fmt.Sprintf("a signer change's action is %s or %s, not %q", SignerRegister, SignerRevoke, c.Action)
	}
	raw, err := hex.DecodeString(c.PubKey)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return "a signer change's pubkey is a 32-byte Ed25519 key in hex"
	}
	if want := KeyFingerprint(ed25519.PublicKey(raw)); c.KeyID != want {
		return fmt.Sprintf("a signer change's keyid is its pubkey's fingerprint (%s), not %q", want, c.KeyID)
	}
	if c.Action == SignerRegister && strings.TrimSpace(c.Name) == "" {
		return "a signer registration names the key"
	}
	if c.Action == SignerRevoke && strings.TrimSpace(c.Reason) == "" {
		return "a signer revocation states a reason"
	}
	return ""
}

// CheckpointSignersProblem is the shape rule for a checkpoint's carried
// set. Each key must pass the very rule a registration does
// (SignerShapeProblem) — a wrong-length key seeded here would otherwise
// reach ed25519.Verify, which panics on one — and a key may appear once.
func CheckpointSignersProblem(keys []SignerKey) string {
	seen := map[string]bool{}
	for _, k := range keys {
		reg := SignerChange{Action: SignerRegister, KeyID: k.KeyID, PubKey: k.PubKey, Name: k.Name}
		if p := SignerShapeProblem(&reg); p != "" {
			return "a checkpoint's carried signer: " + p
		}
		if seen[k.KeyID] {
			return fmt.Sprintf("a checkpoint carries signer %s twice", k.KeyID)
		}
		seen[k.KeyID] = true
	}
	return ""
}
