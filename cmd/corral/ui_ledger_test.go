// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/auditpush"
)

// Review 786e8a0c675e#R1: the ledger is read twice — once to verify, once to
// render — so a check is paired with an entry by HASH, never by position. An
// entry the verifying pass did not see (appended between the reads) is a
// counted problem, not a silent "unsigned"; a reordered pass cannot lend one
// entry's good signature to another.
func TestUILedgerPairsChecksByHash(t *testing.T) {
	entries := []auditpush.LedgerEntry{{Hash: "a"}, {Hash: "b"}, {Hash: "c"}}
	checks := []auditpush.ChainCheck{
		{Hash: "b", Signed: true, SigOK: false, Problem: "bad signature"},
		{Hash: "a", Signed: true, SigOK: true},
	}
	got := checksByEntry(entries, checks)
	if !got[0].SigOK || got[1].SigOK || got[1].Problem != "bad signature" {
		t.Fatalf("checks paired by position, not hash: %+v", got)
	}
	if got[2].Problem == "" || !strings.Contains(got[2].Problem, "not verified") {
		t.Fatalf("an entry the verifying pass never saw must be a problem, got %+v", got[2])
	}
}

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
