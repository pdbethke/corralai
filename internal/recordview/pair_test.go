// SPDX-License-Identifier: Elastic-2.0

package recordview

import (
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/auditpush"
)

// Moved from cmd/corral (review 786e8a0c675e#R1): checks are paired by the
// entry's hash. Position and file name are where an entry sits, not which
// entry it is.
func TestChecksPairByHash(t *testing.T) {
	entries := []auditpush.LedgerEntry{{Hash: "a", File: "x.json.gz"}, {Hash: "b", File: "y.json.gz"}, {Hash: "c"}}
	checks := []auditpush.ChainCheck{
		{Hash: "b", File: "x.json.gz", Signed: true, SigOK: false, Problem: "bad signature"},
		{Hash: "a", File: "y.json.gz", Signed: true, SigOK: true},
	}
	got := PairChecks(entries, checks)
	if !got[0].SigOK || got[1].SigOK || got[1].Problem != "bad signature" {
		t.Fatalf("checks paired by position or file, not hash: %+v", got)
	}
	if !strings.Contains(got[2].Problem, "not verified") {
		t.Fatalf("an entry the verifying pass never saw must be a problem, got %+v", got[2])
	}
}

func TestTheVerifiersNoteReachesTheEntryPage(t *testing.T) {
	e := warpLikeScan("ab12cd34ef56", true)
	e.File = e.Hash + ".json.gz"
	c := okCheck(e)
	c.Note = "UNSIGNED, in a chain whose other entries are signed"
	c.Signed, c.SigOK = false, false
	out := t.TempDir()
	if err := WriteStatic(out, Build([]auditpush.LedgerEntry{e}, []auditpush.ChainCheck{c}, true)); err != nil {
		t.Fatal(err)
	}
	page := read(t, out+"/entry/ab12cd34ef56.html")
	if !strings.Contains(page, "UNSIGNED, in a chain whose other entries are signed") {
		t.Fatal("the chain check's note was dropped on the way to the page")
	}
}

// A signature checked under a GIVEN key that fails is not "not checked".
// It is a failure, and the page must say so in the signature chip itself,
// not only in a problem line beneath it.
func TestASignatureThatFailsUnderAGivenKeySaysItDoesNotVerify(t *testing.T) {
	e := warpLikeScan("ab12cd34ef56", true)
	e.File = e.Hash + ".json.gz"
	c := okCheck(e)
	c.SigOK, c.Problem = false, "signature does not verify under the given key"
	out := t.TempDir()
	if err := WriteStatic(out, Build([]auditpush.LedgerEntry{e}, []auditpush.ChainCheck{c}, true)); err != nil {
		t.Fatal(err)
	}
	page := read(t, out+"/entry/ab12cd34ef56.html")
	if strings.Contains(page, "not checked") {
		t.Fatal("a signature that FAILED under a given key was rendered as not checked")
	}
	if !strings.Contains(page, "signature does NOT verify") {
		t.Fatal("the signature chip does not say the signature failed")
	}
}

// With no key given, a signed entry is signed and unchecked. It is never
// "verified", whatever its self-reported key name says.
func TestWithNoKeyASignedEntryIsSignedAndUnchecked(t *testing.T) {
	e := warpLikeScan("ab12cd34ef56", true)
	e.File = e.Hash + ".json.gz"
	c := okCheck(e)
	c.SigOK, c.KeyID = false, "corral-certify"
	out := t.TempDir()
	if err := WriteStatic(out, Build([]auditpush.LedgerEntry{e}, []auditpush.ChainCheck{c}, false)); err != nil {
		t.Fatal(err)
	}
	page := read(t, out+"/entry/ab12cd34ef56.html")
	if !strings.Contains(page, "signed, not checked (no key given)") || strings.Contains(page, ">signature verified<") {
		t.Fatal("an unchecked signature must read as signed and unchecked")
	}
}
