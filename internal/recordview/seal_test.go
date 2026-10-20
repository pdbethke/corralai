// SPDX-License-Identifier: Elastic-2.0

package recordview

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestTheSealRanksProvenGapsFirstAndKeepsItsCaveats(t *testing.T) {
	s := Site{HasLedger: true}.WithSeal([]SealRow{
		{Repo: "r", Path: "a.go", KillRate: 0.2},
		{Repo: "r", Path: "b.go", KillRate: 0.9, ProvenMissed: 3},
		{Repo: "r", Path: "c.go", KillRate: 0.5, Caveat: "BASELINE-FAILED"},
	}, nil)
	if s.Seal[0].Path != "b.go" || s.Seal[1].Path != "a.go" {
		t.Fatalf("order = %v", []string{s.Seal[0].Path, s.Seal[1].Path, s.Seal[2].Path})
	}
	p, _ := NewPages()
	var b bytes.Buffer
	if err := p.Render(&b, s, "index.html"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "BASELINE-FAILED") {
		t.Fatal("a seal row's caveat was dropped: its kill rate reads at face value")
	}
}

func TestASealReadFailureIsShownAsOne(t *testing.T) {
	s := Site{HasLedger: true}.WithSeal(nil, errors.New("locked by another process"))
	p, _ := NewPages()
	var b bytes.Buffer
	_ = p.Render(&b, s, "index.html")
	if !strings.Contains(b.String(), "Could not read the seal: locked by another process") {
		t.Fatal("a failed seal read must not render as an empty seal")
	}
}

// Review Focus 5: with no ledger directory there is no chain to vouch for,
// on ANY page, not just the index.
func TestASealOnlyTargetMakesNoChainClaim(t *testing.T) {
	s := Site{HasLedger: false}.WithSeal([]SealRow{{Repo: "r", Path: "a.go", KillRate: 0.4}}, nil)
	p, _ := NewPages()
	for _, path := range s.Paths() {
		var b bytes.Buffer
		if err := p.Render(&b, s, path); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		page := b.String()
		for _, claim := range []string{"intact", "hash-linked", "The record is empty", "Nothing is waiting", `<span class="small">0</span>`} {
			if strings.Contains(page, claim) {
				t.Fatalf("%s: a warehouse-only target must make no claim about a chain it never read: %q", path, claim)
			}
		}
		if path != "index.html" && !strings.Contains(page, "Not a ledger directory") {
			t.Fatalf("%s: must say why there is no chain here", path)
		}
	}
	var b bytes.Buffer
	_ = p.Render(&b, s, "index.html")
	if !strings.Contains(b.String(), "Not a ledger directory") || !strings.Contains(b.String(), "a.go") {
		t.Fatal("index must explain the absence and still show the seal")
	}
}

func TestAStaticSiteHasNoSealSection(t *testing.T) {
	p, _ := NewPages()
	var b bytes.Buffer
	_ = p.Render(&b, Site{HasLedger: true}, "index.html")
	if strings.Contains(b.String(), "The seal") {
		t.Fatal("absent is not empty: a site never given a seal shows no section")
	}
}

func TestACaveatedRowIsNotRankedByItsFaceValueKillRate(t *testing.T) {
	s := Site{HasLedger: true}.WithSeal([]SealRow{
		{Path: "failed.go", KillRate: 0, Caveat: "BASELINE-FAILED"},
		{Path: "ok.go", KillRate: 0.8},
		{Path: "gap.go", KillRate: 0.9, ProvenMissed: 1},
	}, nil)
	got := []string{s.Seal[0].Path, s.Seal[1].Path, s.Seal[2].Path}
	if got[0] != "gap.go" || got[1] != "ok.go" || got[2] != "failed.go" {
		t.Fatalf("caveated rows must follow every graded row, got %v", got)
	}
}
