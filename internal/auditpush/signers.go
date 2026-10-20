// SPDX-License-Identifier: Elastic-2.0

package auditpush

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

// signerShapeProblem is the KindSigner arm of EntryShapeProblem.
func signerShapeProblem(c *SignerChange) string {
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

// WriteSignerChange appends a KindSigner entry signed by root. It is the
// one writer of such entries, so the shape rule runs at the same door as
// every other kind (placeEntry enforces it).
func WriteSignerChange(dir string, c SignerChange, root LedgerSigner) (string, error) {
	return AppendLedgerEntry(dir, LedgerEntry{Kind: KindSigner, Signer: &c}, root)
}

// legacyKeyIDProblem is the one wording of "format 5 names keys by
// fingerprint", said by the writer (placeEntry refuses) and the verifier.
const legacyKeyIDProblem = `a corral-ledger-5 entry names its key by fingerprint, not "` + LegacyRootKeyID + `"`

// signerSet is the walk's state: who may sign at THIS point in the chain.
// It is advanced entry by entry inside verifyEntries — the one walk — so
// VerifyLedgerDir and ActiveSigners read the same set by construction and
// cannot disagree about who was registered when.
//
// The root is either the key the operator gave (--pub names only the root)
// or, with no key, the keyid of the chain's first signer entry. The second
// is a NAME, not a proof, and with no key every authority check is by name:
// a signer entry is accepted as the root's when its keyid string equals the
// root's fingerprint (or is "corral-certify" on a format <= 4 entry),
// whatever its signature bytes. The root's fingerprint is plaintext on
// every entry the root signed, so anyone who can write the directory can
// place a signer entry, at ANY position, that registers their own key on a
// keyless read — not only one who writes the first signer entry. Only a
// read under the root key (--pub) refuses it: there the signature is
// checked against the root, the forged entry is a Problem, and the set does
// not move. The keyless set is therefore a description of what the chain
// CLAIMS, never an authorization; see ActiveSigners.
//
// Seeding: active starts empty. A leading checkpoint that carries the set
// it replaced (Task 3 of the signers plan) seeds it here, in
// newSignerSet, and nowhere else.
type signerSet struct {
	rootID  string
	rootPub ed25519.PublicKey // nil when no key was given: nothing is signature-checked
	active  map[string]SignerKey
	order   []string // registration order, so ActiveSigners is deterministic
}

func newSignerSet(entries []LedgerEntry, pub ed25519.PublicKey) *signerSet {
	s := &signerSet{rootPub: pub, active: map[string]SignerKey{}}
	if pub != nil {
		s.rootID = KeyFingerprint(pub)
		return s
	}
	for _, e := range entries {
		if e.Kind == KindSigner {
			s.rootID = e.KeyID
			break
		}
	}
	return s
}

// isRoot reports whether e names the root: by fingerprint, or — on a
// format <= 4 entry only — by the legacy convention name every such entry
// carries. An empty rootID (a keyless read of a chain with no signer
// entries) names nobody, so an unkeyed entry is never mistaken for it.
func (s *signerSet) isRoot(e LedgerEntry) bool {
	if e.KeyID == LegacyRootKeyID {
		return e.Format != LedgerFileFormat
	}
	return s.rootID != "" && e.KeyID == s.rootID
}

// resolve names the key an entry's signature must verify under, and who it
// is ("root" or the registered name). key is nil when there is nothing to
// check against: an unsigned entry, or no --pub. problem is set when the
// entry names a key the chain does not hold at this point.
//
// With no key given, an unregistered keyid is NOT a problem. That read is
// what RequireIntactChain runs before every append, and a ledger that has
// never registered a signer — every ledger, today — is written by keys it
// cannot name; refusing them there would make the chain unappendable. The
// writer's own refusal of unregistered keys on a signer ledger is a
// separate rule at the append door.
func (s *signerSet) resolve(e LedgerEntry) (name string, key ed25519.PublicKey, problem string) {
	if e.Signature == "" {
		return "", nil, ""
	}
	if e.Format == LedgerFileFormat && e.KeyID == LegacyRootKeyID {
		return "", nil, legacyKeyIDProblem
	}
	if s.isRoot(e) {
		return "root", s.rootPub, ""
	}
	if k, ok := s.active[e.KeyID]; ok {
		if s.rootPub == nil {
			return k.Name, nil, ""
		}
		raw, _ := hex.DecodeString(k.PubKey) // shape-checked when it was registered
		return k.Name, ed25519.PublicKey(raw), ""
	}
	if s.rootPub == nil {
		return "", nil, ""
	}
	return "", nil, fmt.Sprintf("signed by %s, which is not a registered signer at this point in the chain", orNoKeyID(e.KeyID))
}

// changeProblem is the authority half of a signer entry, checked after its
// shape (EntryShapeProblem) and after its signature: the root signed it,
// and the change makes sense against the set as it stands. "" when the
// change may be applied.
func (s *signerSet) changeProblem(e LedgerEntry) string {
	if e.Signature == "" || !s.isRoot(e) {
		return "a signer entry must be signed by the ledger's root key"
	}
	c := e.Signer
	_, active := s.active[c.KeyID]
	switch {
	case c.Action == SignerRegister && c.KeyID == s.rootID:
		return "registers the ledger's own root key — the root is not registered, it signs registrations"
	case c.Action == SignerRegister && active:
		return fmt.Sprintf("registers %s, which is already a registered signer — revoke it first to re-register", c.KeyID)
	case c.Action == SignerRevoke && c.KeyID == s.rootID:
		return "the root cannot be revoked — a new root is a new ledger"
	case c.Action == SignerRevoke && !active:
		return fmt.Sprintf("revokes %s, which is not a registered signer at this point in the chain", c.KeyID)
	}
	return ""
}

// apply changes the set. The caller applies a signer entry only once the
// entry checked clean — hash, link, signature, shape, authority — so a
// tampered or forged change never moves the set.
func (s *signerSet) apply(c *SignerChange) {
	switch c.Action {
	case SignerRegister:
		s.active[c.KeyID] = SignerKey{KeyID: c.KeyID, PubKey: c.PubKey, Name: c.Name}
		s.order = append(s.order, c.KeyID)
	case SignerRevoke:
		delete(s.active, c.KeyID)
		for i, id := range s.order {
			if id == c.KeyID {
				s.order = append(s.order[:i:i], s.order[i+1:]...)
				break
			}
		}
	}
}

// changeNote says what a clean signer entry did, in words.
func changeNote(c *SignerChange) string {
	if c.Action == SignerRevoke {
		return fmt.Sprintf("revokes %s: %s", c.KeyID, c.Reason)
	}
	return fmt.Sprintf("registers %s as %s", c.KeyID, c.Name)
}

func (s *signerSet) list() []SignerKey {
	out := make([]SignerKey, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.active[id])
	}
	return out
}

func orNoKeyID(id string) string {
	if strings.TrimSpace(id) == "" {
		return "a key it does not name"
	}
	return id
}

// ActiveSigners is the signer set at the chain's head, as the verifier's
// own walk leaves it with no key: hashes, links, shape and the register /
// revoke rules are checked and a change that fails any of them is not
// applied, but NO SIGNATURE IS. A signer entry that merely names the root's
// keyid is accepted, so anyone who can write the directory can add their
// own key here (see signerSet). That makes it a CONVENIENCE for a writer —
// "would this append look right to a reader?" — and never an
// authorization: whether a key really was registered is answered only by
// VerifyLedgerDir under the root key. On a chain that verifies clean under
// the root key it is exactly the set that verify walked. rootKeyID is the
// KeyID of the chain's first signer entry, "" when the chain has none.
func ActiveSigners(entries []LedgerEntry) (rootKeyID string, active []SignerKey) {
	_, set := verifyEntries(entries, nil)
	return set.rootID, set.list()
}
