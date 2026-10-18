// SPDX-License-Identifier: Elastic-2.0

package recordview

import (
	"crypto/ed25519"
	"encoding/hex"

	"github.com/pdbethke/corralai/internal/auditpush"
)

// Load reads the ledger directory, verifies the chain (signatures against
// pub; nil means unchecked, never failed), pairs each check to its entry by
// hash, and builds the site. The key is the caller's decision, resolved
// with auditpush.ResolveLedgerPubKey, so a malformed one is refused before
// anything renders.
func Load(dir string, pub ed25519.PublicKey) (Site, error) {
	entries, err := auditpush.ReadLedgerDir(dir)
	if err != nil {
		return Site{}, err
	}
	checks, err := auditpush.VerifyLedgerDir(dir, pub)
	if err != nil {
		return Site{}, err
	}
	s := Build(entries, checks, pub != nil)
	s.HasLedger = true
	if pub != nil {
		s.PubKeyHex = hex.EncodeToString(pub)
	}
	return s, nil
}
