// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"strings"
	"testing"
)

// Review 786e8a0c675e#R2: a retraction note is ADDED to the verifier's note,
// not written over it — the self-reported-signer caveat on a legacy entry is
// the only thing qualifying the "verified · <keyid>" chip beside it.
func TestUILedgerKeepsTheVerifierNoteBesideARetraction(t *testing.T) {
	got := withNote("signer name is self-reported", "RETRACTED: wrong scope")
	if !strings.Contains(got, "RETRACTED: wrong scope") || !strings.Contains(got, "signer name is self-reported") {
		t.Fatalf("the caveat was overwritten: %q", got)
	}
	if withNote("", "RETRACTED: x") != "RETRACTED: x" {
		t.Fatalf("no caveat, no separator: %q", withNote("", "RETRACTED: x"))
	}
}
