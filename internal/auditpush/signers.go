// SPDX-License-Identifier: Elastic-2.0

package auditpush

import (
	"crypto/ed25519"

	"github.com/pdbethke/corralai/internal/ledger"
)

// The signer payloads and the pure rules over them live in internal/ledger.
const (
	KindSigner      = ledger.KindSigner
	SignerRegister  = ledger.SignerRegister
	SignerRevoke    = ledger.SignerRevoke
	LegacyRootKeyID = ledger.LegacyRootKeyID
)

type (
	SignerChange = ledger.SignerChange
	SignerKey    = ledger.SignerKey
)

// KeyFingerprint names a public key; see ledger.KeyFingerprint.
func KeyFingerprint(pub ed25519.PublicKey) string { return ledger.KeyFingerprint(pub) }

// WriteSignerChange appends a KindSigner entry signed by root. It is the
// one writer of such entries, so the shape rule runs at the same door as
// every other kind (placeEntry enforces it).
func WriteSignerChange(dir string, c SignerChange, root LedgerSigner) (string, error) {
	return AppendLedgerEntry(dir, LedgerEntry{Kind: KindSigner, Signer: &c}, root)
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
// KeyID of the chain's first signer entry — or of a leading checkpoint that
// carries a set — and "" when the chain has neither.
func ActiveSigners(entries []LedgerEntry) (rootKeyID string, active []SignerKey) {
	_, set := verifyEntries(entries, nil)
	return set.RootID(), set.List()
}
