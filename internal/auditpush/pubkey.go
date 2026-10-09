// SPDX-License-Identifier: Elastic-2.0
package auditpush

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// LedgerPubKeyEnv names the one environment variable that may carry the key
// a ledger is verified against.
const LedgerPubKeyEnv = "CORRALAI_LEDGER_PUBKEY"

// ResolveLedgerPubKey is the ONE answer to "which key verifies this ledger",
// for the CLI and the public site alike: the --pub flag, else the
// environment variable, else nothing — and nothing means signatures are NOT
// CHECKED, said as such. Trust is never inferred: not from a key file
// published beside the record it vouches for, not from whatever certify key
// happens to sit on this machine. Two verifiers that inferred differently
// once reached different verdicts on the same record. A malformed key from
// either source is an error, not a fallthrough: whoever set it meant to
// check, and a quiet downgrade to "unverified" would hide that they did not.
//
// "Set" means non-empty BEFORE trimming: an empty value is how a CI file or a
// shell clears a variable, so it is unset; a value of only whitespace was
// typed by someone and is malformed (review e5fd4c8d1b50#R1 — it used to read
// as unset, and a blank --pub fell through to the environment's key).
func ResolveLedgerPubKey(flagHex string) (ed25519.PublicKey, string, error) {
	if flagHex != "" {
		return parseLedgerPubKey(strings.TrimSpace(flagHex), "the --pub flag")
	}
	if v := os.Getenv(LedgerPubKeyEnv); v != "" {
		return parseLedgerPubKey(strings.TrimSpace(v), LedgerPubKeyEnv)
	}
	return nil, "", nil
}

func parseLedgerPubKey(h, source string) (ed25519.PublicKey, string, error) {
	b, err := hex.DecodeString(h)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, "", fmt.Errorf("%s is not a %d-byte hex-encoded Ed25519 public key", source, ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(b), source, nil
}
