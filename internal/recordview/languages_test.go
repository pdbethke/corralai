// SPDX-License-Identifier: Elastic-2.0

package recordview

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/ledger"
)

const lHash = "abcdef0123456789abcdef0123456789"

var (
	lSelf    = []string{"github.com/pdbethke/corralai"}
	lRunners = map[string][]string{"go-test": {"go"}}
	lLangs   = []string{"go", "python", "rust"}
)

// langSite is a site over one qualifying go run (CI when runURL is set).
func langSite(t *testing.T, gen string, signed bool, runURL string) Site {
	t.Helper()
	d := false
	st := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	e := ledger.Entry{Hash: lHash}
	e.Bundle.Scan = ledger.ScanRow{Repo: "https://github.com/acme/widgets", RepoIdentity: "git", Commit: "c1",
		RunURL: runURL, Runner: "go-test", VerdictGeneration: gen, TreeDirty: &d, StartedAt: &st}
	e.Bundle.Files = []ledger.Row{{Lang: "go", Path: "a.go", Audited: 1, MutantsGraded: 5}}
	c := ledger.ChainCheck{Hash: lHash, HashOK: true, LinkOK: true, SigChecked: signed, SigOK: signed, Signer: "root"}
	s := Build([]ledger.Entry{e}, []ledger.ChainCheck{c})
	s.entries, s.checks = []ledger.Entry{e}, []ledger.ChainCheck{c}
	return s
}

func renderPage(t *testing.T, s Site, path string) string {
	t.Helper()
	p, err := NewPages()
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := p.Render(&b, s, path); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestLanguagesPageRowsFollowTheRegistry(t *testing.T) {
	s := langSite(t, "40", true, "").WithLanguages(lLangs, lRunners, lSelf, "--self")
	if len(s.Languages) != 3 {
		t.Fatalf("rows = %d, want 3", len(s.Languages))
	}
	for i, l := range lLangs {
		if s.Languages[i].Lang != l {
			t.Fatalf("row %d = %s, want %s", i, s.Languages[i].Lang, l)
		}
	}
	if s.Languages[0].Local == nil || s.Languages[1].Local != nil {
		t.Fatalf("go should have a local run and python none: %+v", s.Languages)
	}
}

func TestLanguagesPageReasons(t *testing.T) {
	none := langSite(t, "40", true, "").WithLanguages(lLangs, lRunners, lSelf, "--self")
	if got := none.Languages[1].Reason; got != "no run on this record" {
		t.Fatalf("reason = %q", got)
	}
	if none.Languages[0].Reason != "" {
		t.Fatalf("a proven row has no reason: %q", none.Languages[0].Reason)
	}
	old := langSite(t, "39", true, "").WithLanguages(lLangs, lRunners, lSelf, "--self")
	if got := old.Languages[0].Reason; got != "every run on this record predates generation 40" {
		t.Fatalf("reason = %q", got)
	}
	unc := langSite(t, "40", false, "").WithLanguages(lLangs, lRunners, lSelf, "--self")
	if got := unc.Languages[0].Reason; got != "signatures not checked: render with the ledger's public key" {
		t.Fatalf("reason = %q", got)
	}
}

func TestLanguagesPageSaysNotCheckedWithoutKey(t *testing.T) {
	s := langSite(t, "40", false, "").WithLanguages(lLangs, lRunners, lSelf, "--self")
	if s.Languages[0].Local != nil || s.Languages[0].CI != nil {
		t.Fatalf("an unchecked run counted")
	}
	if !strings.Contains(renderPage(t, s, "languages.html"), "signatures not checked") {
		t.Fatalf("page does not say signatures were not checked")
	}
	if strings.Contains(s.Languages[0].Reason, "no run") {
		t.Fatalf("an unchecked row said no run")
	}
}

func TestLanguagesPageRefusesWithoutSelf(t *testing.T) {
	base := langSite(t, "40", true, "")
	s := base.WithLanguages(lLangs, lRunners, nil, "--self-repo")
	if !strings.Contains(s.LanguagesRefusal, "--self-repo") {
		t.Fatalf("refusal = %q", s.LanguagesRefusal)
	}
	found := false
	for _, p := range s.Paths() {
		found = found || p == "languages.html"
	}
	if !found {
		t.Fatalf("languages.html missing from Paths")
	}
	if !strings.Contains(renderPage(t, s, "languages.html"), "--self-repo") {
		t.Fatalf("refusal not rendered")
	}
	for _, p := range []string{"verdicts.html", "audits-1.html", "chain-1.html"} {
		if renderPage(t, s, p) != renderPage(t, base, p) {
			t.Fatalf("%s changed by WithLanguages", p)
		}
	}
}

func TestLanguagesPageRefusesOnAnyOtherError(t *testing.T) {
	s := langSite(t, "40", true, "")
	s.entries = nil
	// A non-numeric floor cannot be injected, so provoke the self error path
	// through a blank repository name, which normalises to nothing.
	s = s.WithLanguages(lLangs, lRunners, []string{" "}, "--self")
	if s.LanguagesRefusal == "" || !s.LanguagesBuilt {
		t.Fatalf("an error must become a refusal, built=%v refusal=%q", s.LanguagesBuilt, s.LanguagesRefusal)
	}
}

func TestLanguagesPageLinksEntryAndRunURL(t *testing.T) {
	const url = "https://github.com/acme/widgets/actions/runs/1"
	s := langSite(t, "40", true, url).WithLanguages(lLangs, lRunners, lSelf, "--self")
	page := renderPage(t, s, "languages.html")
	if !strings.Contains(page, `href="entry/`+shortHash(lHash)+`.html"`) {
		t.Fatalf("no entry link in %s", page)
	}
	if !strings.Contains(page, `href="`+url+`"`) {
		t.Fatalf("no run URL link")
	}
}

func TestWarehouseSiteHasNoLanguagesPage(t *testing.T) {
	s := Site{}.WithLanguages(lLangs, lRunners, lSelf, "--self")
	if s.LanguagesBuilt {
		t.Fatalf("a warehouse site built a languages page")
	}
	for _, p := range s.Paths() {
		if p == "languages.html" {
			t.Fatalf("languages.html in Paths")
		}
	}
}
