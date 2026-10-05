// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestDocsQuoteThePublishedLedgerKey: the ledger's public key is published in
// ONE place, LEDGER_PUBKEY at the repository root, and quoted wherever a
// reader is told how to verify corral's record. A quoted copy that drifted
// from the file would send a stranger to verify every signature against the
// wrong key, and every entry would "fail" for a reason that is not in the
// record. Every `--pub <hex>` in the docs must be the published key, and at
// least one doc must quote it, so this cannot pass by finding nothing.
func TestDocsQuoteThePublishedLedgerKey(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join(repoRoot, "LEDGER_PUBKEY"))
	if err != nil {
		t.Fatalf("LEDGER_PUBKEY: %v", err)
	}
	key := strings.TrimSpace(string(raw))
	if b, err := hex.DecodeString(key); err != nil || len(b) != 32 {
		t.Fatalf("LEDGER_PUBKEY is not a 32-byte hex Ed25519 public key: %q", key)
	}
	quoted := regexp.MustCompile(`--pub\s+([0-9a-fA-F]{64})`)
	found := 0
	for doc, body := range docsAdvertisingAnActionRef(t, repoRoot) {
		for _, m := range quoted.FindAllStringSubmatch(body, -1) {
			found++
			if !strings.EqualFold(m[1], key) {
				t.Errorf("%s quotes --pub %s, but the published ledger key is %s", doc, m[1], key)
			}
		}
	}
	if found == 0 {
		t.Fatal("no doc quotes the published ledger key with --pub; a reader has no command to verify the record's signatures")
	}
}
