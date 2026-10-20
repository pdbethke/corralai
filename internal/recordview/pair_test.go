// SPDX-License-Identifier: Elastic-2.0

package recordview

import (
	"bytes"
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
	if !got[0].Verified || !got[1].Verified || got[2].Verified {
		t.Fatalf("only an entry no check names is unverified, got %+v", got)
	}
	if got[2].Problem != "" || got[2].HashOK || got[2].LinkOK {
		t.Fatalf("an unverified entry carries no measured outcome, got %+v", got[2])
	}
}

// An entry appended between the read and the verifying pass was never
// checked. That is a could-not-measure, not a measured failure: no page may
// call its hash a MISMATCH, its link BROKEN, it unsigned, or count it as a
// chain problem. Each page says "not verified" and asks for a reload.
func TestAnEntryTheVerifierNeverSawIsNotVerifiedRatherThanFailed(t *testing.T) {
	seen := reviewEntry("aaaaaaaaaaaa", 0)
	late := warpLikeScan("ab12cd34ef56", true)
	entries := []auditpush.LedgerEntry{seen, late}
	for i := range entries {
		entries[i].File = entries[i].Hash + ".json.gz"
	}
	s := Build(entries, []auditpush.ChainCheck{okCheck(entries[0])}, true)
	if s.ChainProblems != 0 || s.Unverified != 1 {
		t.Errorf("ChainProblems=%d Unverified=%d, want 0 and 1", s.ChainProblems, s.Unverified)
	}
	p, _ := NewPages()
	for _, path := range s.Paths() {
		var b bytes.Buffer
		if err := p.Render(&b, s, path); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		page := b.String()
		for _, bad := range []string{"MISMATCH", "BROKEN", "problem(s)", ">unsigned<"} {
			if strings.Contains(page, bad) {
				t.Errorf("%s: an unverified entry renders as measured: %q", path, bad)
			}
		}
		switch path {
		case "index.html", "chain-1.html", "entry/ab12cd34ef56.html":
			if !strings.Contains(page, "not verified") {
				t.Errorf("%s does not say the entry was not verified", path)
			}
		}
	}
	var b bytes.Buffer
	_ = p.Render(&b, s, "index.html")
	if !strings.Contains(b.String(), "1 entry changed while being read") {
		t.Error("the index does not count what was not verified")
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
