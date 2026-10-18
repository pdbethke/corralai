// SPDX-License-Identifier: Elastic-2.0

package recordview

import (
	"bytes"
	"errors"
	"fmt"
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
	return Build(entries, checks, true)
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
	s := Build(nil, nil, false)
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

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
