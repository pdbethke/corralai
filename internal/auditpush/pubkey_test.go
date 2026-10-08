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
