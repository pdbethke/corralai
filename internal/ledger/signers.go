// SPDX-License-Identifier: Elastic-2.0

package ledger

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"strings"
)

// LegacyKeyIDProblem is the one wording of "format 5 names keys by
// fingerprint", said by the writer (auditpush's placeEntry refuses) and the
// verifier.
const LegacyKeyIDProblem = `a corral-ledger-5 entry names its key by fingerprint, not "` + LegacyRootKeyID + `"`

// SignerSet is the walk's state: who may sign at THIS point in the chain.
// It is advanced entry by entry inside VerifyWithSigners — the one walk — so
// Verify and ActiveSigners read the same set by construction and
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
// it replaced seeds it (seed, called by the walk once the checkpoint checked
// clean). Under --pub the checkpoint must be signed by the root, or anyone
// who could write a genesis could hand themselves signing keys; the pruned
// register entries were the proof of those keys, and the root's signature
// on the checkpoint is what stands in for them. With no pub the authority
// check is by NAME, exactly as for a signer entry: a checkpoint whose keyid
// string equals the root's passes, whatever its signature bytes, so a keyless
// read of a checkpoint-led chain describes what the chain CLAIMS. For such a
// chain the root is the checkpoint's own keyid.
type SignerSet struct {
	rootID  string
	rootPub ed25519.PublicKey // nil when no key was given: nothing is signature-checked
	active  map[string]SignerKey
	order   []string // registration order, so ActiveSigners is deterministic
}

func newSignerSet(entries []Entry, pub ed25519.PublicKey) *SignerSet {
	s := &SignerSet{rootPub: pub, active: map[string]SignerKey{}}
	if pub != nil {
		s.rootID = KeyFingerprint(pub)
		return s
	}
	if len(entries) > 0 && entries[0].Kind == KindCheckpoint && entries[0].Checkpoint != nil && len(entries[0].Checkpoint.Signers) > 0 {
		s.rootID = entries[0].KeyID
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
func (s *SignerSet) isRoot(e Entry) bool {
	if e.KeyID == LegacyRootKeyID {
		return e.Format == Format4 || e.Format == Format3 || e.Format == Format2
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
func (s *SignerSet) resolve(e Entry) (name string, key ed25519.PublicKey, problem string) {
	if e.Signature == "" {
		return "", nil, ""
	}
	if e.Format == FileFormat && e.KeyID == LegacyRootKeyID {
		return "", nil, LegacyKeyIDProblem
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
func (s *SignerSet) changeProblem(e Entry) string {
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
func (s *SignerSet) apply(c *SignerChange) {
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

// List is the active keys in registration order.
func (s *SignerSet) List() []SignerKey {
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

const errCheckpointNotRoot = "a checkpoint that carries signers must be signed by the root"

// seedProblem is the authority half of a leading checkpoint's carried set,
// checked after its shape: it must be signed by the root, and may not list
// the root among the registered keys (the root signs registrations, it is
// not one). "" when there is nothing to object to, including a checkpoint
// that carries no signers.
func (s *SignerSet) seedProblem(e Entry) string {
	keys := e.Checkpoint.Signers
	if len(keys) == 0 {
		return ""
	}
	if e.Signature == "" || !s.isRoot(e) {
		return errCheckpointNotRoot
	}
	for _, k := range keys {
		if k.KeyID == s.rootID {
			return "a checkpoint registers the ledger's own root key — the root is not registered, it signs registrations"
		}
	}
	return ""
}

// seed installs a clean leading checkpoint's carried set.
func (s *SignerSet) seed(keys []SignerKey) {
	for _, k := range keys {
		s.active[k.KeyID] = k
		s.order = append(s.order, k.KeyID)
	}
}

// RootID is the keyid of the ledger's root: the key the operator named, or
// with none, the chain's first signer entry or leading checkpoint. "" when
// the chain has neither.
func (s *SignerSet) RootID() string { return s.rootID }
