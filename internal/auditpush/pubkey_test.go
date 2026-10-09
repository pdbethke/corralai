// SPDX-License-Identifier: Elastic-2.0
package auditpush

import (
	"crypto/ed25519"
	"encoding/hex"
	"strings"
	"testing"
)

func TestResolveLedgerPubKeyOrderIsFlagThenEnvThenNothing(t *testing.T) {
	a, _, _ := ed25519.GenerateKey(nil)
	b, _, _ := ed25519.GenerateKey(nil)
	t.Setenv(LedgerPubKeyEnv, hex.EncodeToString(b))

	pub, src, err := ResolveLedgerPubKey(hex.EncodeToString(a))
	if err != nil || !pub.Equal(a) || src != "the --pub flag" {
		t.Fatalf("flag must win: pub=%x src=%q err=%v", pub, src, err)
	}
	pub, src, err = ResolveLedgerPubKey("")
	if err != nil || !pub.Equal(b) || src != LedgerPubKeyEnv {
		t.Fatalf("env is second: pub=%x src=%q err=%v", pub, src, err)
	}
	t.Setenv(LedgerPubKeyEnv, "")
	pub, src, err = ResolveLedgerPubKey("")
	if err != nil || pub != nil || src != "" {
		t.Fatalf("no explicit key means NOT CHECKED, never an implicit one: pub=%x src=%q err=%v", pub, src, err)
	}
}

func TestResolveLedgerPubKeyRefusesGarbageLoudly(t *testing.T) {
	if _, _, err := ResolveLedgerPubKey("not-hex"); err == nil || !strings.Contains(err.Error(), "--pub") {
		t.Fatalf("bad flag must be an error naming the flag, got %v", err)
	}
	t.Setenv(LedgerPubKeyEnv, "abcd")
	if _, _, err := ResolveLedgerPubKey(""); err == nil || !strings.Contains(err.Error(), LedgerPubKeyEnv) {
		t.Fatalf("bad env must be an error naming the env var, got %v", err)
	}
}

// Review e5fd4c8d1b50#R1: a key that is SET but blank is not "no key". The
// doc above ResolveLedgerPubKey says whoever set a key meant to check; a
// blank one used to read as unset and downgrade to NOT CHECKED, and a blank
// --pub fell through to the environment's key.
func TestResolveLedgerPubKeyRefusesASetButBlankKey(t *testing.T) {
	a, _, _ := ed25519.GenerateKey(nil)
	t.Setenv(LedgerPubKeyEnv, hex.EncodeToString(a))
	if pub, _, err := ResolveLedgerPubKey("  "); err == nil || !strings.Contains(err.Error(), "--pub") {
		t.Fatalf("a blank --pub must be refused, not fall through to the environment: pub=%x err=%v", pub, err)
	}
	t.Setenv(LedgerPubKeyEnv, " \t")
	if pub, _, err := ResolveLedgerPubKey(""); err == nil || !strings.Contains(err.Error(), LedgerPubKeyEnv) {
		t.Fatalf("a blank %s must be refused, not read as unset: pub=%x err=%v", LedgerPubKeyEnv, pub, err)
	}
}
