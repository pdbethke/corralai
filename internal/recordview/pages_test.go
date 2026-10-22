// SPDX-License-Identifier: Elastic-2.0

package recordview

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/auditpush"
)

func manyScans(n int) []auditpush.LedgerEntry {
	var out []auditpush.LedgerEntry
	for i := 0; i < n; i++ {
		e := warpLikeScan(fmt.Sprintf("%012x", i+1), true)
		e.Pushed = e.Pushed.Add(time.Duration(i) * time.Hour)
		out = append(out, e)
	}
	return out
}

func buildOK(entries []auditpush.LedgerEntry) Site {
	var checks []auditpush.ChainCheck
	for i := range entries {
		entries[i].File = entries[i].Hash + ".json.gz"
		checks = append(checks, okCheck(entries[i]))
	}
	return Build(entries, checks)
}

func TestTheIndexShowsTheNewestAndPagesTheRest(t *testing.T) {
	s := buildOK(manyScans(IndexRows + ListRows + 3))
	p, err := NewPages()
	if err != nil {
		t.Fatal(err)
	}
	var idx bytes.Buffer
	if err := p.Render(&idx, s, "index.html"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(idx.String(), `href="entry/`); got != IndexRows {
		t.Fatalf("index links %d audits, want the newest %d", got, IndexRows)
	}
	if !strings.Contains(idx.String(), `href="audits-1.html"`) {
		t.Fatal("index does not link the full list")
	}
	var p2 bytes.Buffer
	if err := p.Render(&p2, s, "audits-2.html"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(p2.String(), `href="entry/`); got != IndexRows+3 {
		t.Fatalf("page 2 holds %d audits, want %d", got, IndexRows+3)
	}
	if err := p.Render(&bytes.Buffer{}, s, "audits-3.html"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a page past the end must be ErrNotFound, got %v", err)
	}
}

func TestEveryPathTheSiteListsRenders(t *testing.T) {
	s := buildOK(append(manyScans(3), reviewEntry("dddddddddddd", 0)))
	p, err := NewPages()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range s.Paths() {
		if err := p.Render(&bytes.Buffer{}, s, path); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	for _, want := range []string{"index.html", "chain-1.html", "audits-1.html", "reviews-1.html", "entry/dddddddddddd.html"} {
		if !contains(s.Paths(), want) {
			t.Fatalf("Paths() is missing %s: %v", want, s.Paths())
		}
	}
}

func TestTheChainListsEveryKindWithItsNote(t *testing.T) {
	s := buildOK([]auditpush.LedgerEntry{
		reviewEntry("aaaaaaaaaaaa", 0),
		adjudicationEntry("bbbbbbbbbbbb", "aaaaaaaaaaaa#R1", auditpush.VerdictConfirmed),
	})
	s.Chain[0].Note = "confirmed R1 by peter: checked"
	p, _ := NewPages()
	var b bytes.Buffer
	if err := p.Render(&b, s, "chain-1.html"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"adjudication", "review", "confirmed R1 by peter: checked"} {
		if !strings.Contains(b.String(), want) {
			t.Fatalf("chain page is missing %q", want)
		}
	}
}

func TestAnEmptyLedgerRendersAndSaysItIsEmpty(t *testing.T) {
	s := Build(nil, nil)
	p, _ := NewPages()
	var b bytes.Buffer
	if err := p.Render(&b, s, "index.html"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "The record is empty") {
		t.Fatal("an empty ledger must say it is empty")
	}
	if strings.Contains(b.String(), "every entry's hash matches its bytes") {
		t.Fatal("zero entries must not be presented as an intact chain")
	}
	for _, path := range s.Paths() {
		var pb bytes.Buffer
		if err := p.Render(&pb, s, path); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for _, bad := range []string{"every entry's hash matches its bytes", "intact"} {
			if strings.Contains(pb.String(), bad) {
				t.Fatalf("%s: an empty ledger page says %q", path, bad)
			}
		}
	}
}

// A full list with nothing in it says so, rather than drawing a bare table
// that reads like a rendering fault.
func TestAnEmptyListSaysNoneRecordedYet(t *testing.T) {
	p, _ := NewPages()
	for _, c := range []struct {
		s    Site
		path string
	}{
		{buildOK([]auditpush.LedgerEntry{reviewEntry("aaaaaaaaaaaa", 0)}), "audits-1.html"},
		{buildOK(manyScans(1)), "reviews-1.html"},
		{buildOK(manyScans(1)), "index.html"},
	} {
		var b bytes.Buffer
		if err := p.Render(&b, c.s, c.path); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(b.String(), "None recorded yet.") {
			t.Errorf("%s with nothing to list does not say so", c.path)
		}
	}
}

// A chain row with a problem names it once, in the row's note; the
// signature column says what the verifier said about the signature.
func TestAChainRowNamesItsProblemOnce(t *testing.T) {
	e := reviewEntry("aaaaaaaaaaaa", 0)
	e.File = e.Hash + ".json.gz"
	c := okCheck(e)
	c.HashOK, c.Problem = false, "hash does not match the entry's bytes"
	s := Build([]auditpush.LedgerEntry{e}, []auditpush.ChainCheck{c})
	p, _ := NewPages()
	var b bytes.Buffer
	if err := p.Render(&b, s, "chain-1.html"); err != nil {
		t.Fatal(err)
	}
	page := b.String()
	if n := strings.Count(page, "hash does not match the entry&#39;s bytes"); n != 1 {
		t.Errorf("the problem appears %d times, want once", n)
	}
	if strings.Contains(page, `chip bad">problem<`) {
		t.Error("the row repeats its problem as a bare chip in the signature column")
	}
}

// Site carries no count that nothing renders: an unrendered number is the
// one most likely to be wrong unnoticed (Adjudications counted retracted
// verdicts for as long as it existed).
func TestSiteCarriesNoUnrenderedAdjudicationCount(t *testing.T) {
	if _, ok := reflect.TypeOf(Site{}).FieldByName("Adjudications"); ok {
		t.Fatal("Site.Adjudications is computed and never rendered")
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// Whether a signature was checked is a fact of each entry, not of the page:
// a keyless entry is checked against its repository with no root key at
// all, and must read as verified; an ed25519 entry beside it, with no root
// key given, is the only thing the banner may call unchecked.
func TestAKeylessEntryCheckedWithoutARootKeyReadsVerified(t *testing.T) {
	k := reviewEntry("aaaaaaaaaaaa", 0)
	k.File = k.Hash + ".json.gz"
	kc := okCheck(k)
	kc.Keyless, kc.KeyID, kc.Signer = true, "sigstore", "https://github.com/acme/widgets"
	render := func(s Site, path string) string {
		p, _ := NewPages()
		var b bytes.Buffer
		if err := p.Render(&b, s, path); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	s := Build([]auditpush.LedgerEntry{k}, []auditpush.ChainCheck{kc})
	chain, idx := render(s, "chain-1.html"), render(s, "index.html")
	if strings.Contains(chain, "signed, not checked") || !strings.Contains(chain, `chip ok">verified`) {
		t.Errorf("a verified keyless entry is not shown verified on the chain page:\n%s", chain)
	}
	if strings.Contains(idx, "not checked") || !strings.Contains(idx, "1/1 verified") {
		t.Errorf("the banner does not say the one keyless signature was verified:\n%s", idx)
	}

	e := reviewEntry("bbbbbbbbbbbb", 1)
	e.File = e.Hash + ".json.gz"
	ec := okCheck(e)
	ec.SigOK, ec.SigChecked = false, false
	s = Build([]auditpush.LedgerEntry{k, e}, []auditpush.ChainCheck{kc, ec})
	idx = render(s, "index.html")
	if !strings.Contains(idx, "no root key given: 1 ed25519 signature not checked") || !strings.Contains(idx, "1/2 verified") {
		t.Errorf("a mixed page must name the unchecked ed25519 signature and count the verified keyless one:\n%s", idx)
	}
}
