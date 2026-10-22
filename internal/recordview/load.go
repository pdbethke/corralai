// SPDX-License-Identifier: Elastic-2.0

package recordview

import (
	"crypto/ed25519"
	"encoding/hex"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/ledger"
)

// Load reads the ledger directory, verifies the chain (signatures against
// pub; nil means unchecked, never failed), pairs each check to its entry by
// hash, and builds the site. The key is the caller's decision, resolved
// with auditpush.ResolveLedgerPubKey, so a malformed one is refused before
// anything renders.
func Load(dir string, pub ed25519.PublicKey) (Site, error) {
	return LoadTrust(dir, ledger.Trust{RootPub: pub})
}

// LoadTrust is Load with the whole trust a reader holds, so a ledger with
// keyless entries renders them checked against the repository and Sigstore
// root the caller named instead of failing every one.
func LoadTrust(dir string, t ledger.Trust) (Site, error) {
	entries, err := auditpush.ReadLedgerDir(dir)
	if err != nil {
		return Site{}, err
	}
	checks, err := auditpush.VerifyLedgerDirTrust(dir, t)
	if err != nil {
		return Site{}, err
	}
	s := Build(entries, checks, t.RootPub != nil)
	s.HasLedger = true
	if t.RootPub != nil {
		s.PubKeyHex = hex.EncodeToString(t.RootPub)
	}
	return s, nil
}
