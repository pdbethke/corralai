// SPDX-License-Identifier: Elastic-2.0

package recordview

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/auditpush"
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
	c := ledger.ChainCheck{Hash: lHash, HashOK: true, LinkOK: true, Signed: true, SigChecked: signed, SigOK: signed, Signer: "root"}
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
	if got := none.Languages[1].Reason; got != "no qualifying run on this record" {
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

// Both an unchecked run and a below-floor run on the record: the unchecked
// reason wins, because a key could still turn it into proof.
func TestLanguagesPageUncheckedOutranksBelowFloor(t *testing.T) {
	a := langSite(t, "40", false, "")
	b := langSite(t, "39", true, "")
	b.entries[0].Hash, b.checks[0].Hash = "0123456789abcdef0123456789abcdef", "0123456789abcdef0123456789abcdef"
	a.entries = append(a.entries, b.entries...)
	a.checks = append(a.checks, b.checks...)
	s := a.WithLanguages(lLangs, lRunners, lSelf, "--self")
	if got := s.Languages[0].Reason; got != "signatures not checked: render with the ledger's public key" {
		t.Fatalf("reason = %q", got)
	}
}

func TestLanguagesPageStatesTheCIColumnRestsOnTheRunURL(t *testing.T) {
	s := langSite(t, "40", true, "").WithLanguages(lLangs, lRunners, lSelf, "--self")
	if !strings.Contains(renderPage(t, s, "languages.html"), "The CI column rests on each run's linked Actions URL") {
		t.Fatalf("the trust sentence is missing")
	}
}

func TestIndexLinksToLanguagesOnlyWhenBuilt(t *testing.T) {
	base := langSite(t, "40", true, "")
	if strings.Contains(renderPage(t, base, "index.html"), "languages.html") {
		t.Fatalf("index links a page that does not exist")
	}
	if !strings.Contains(renderPage(t, base.WithLanguages(lLangs, lRunners, lSelf, "--self"), "index.html"), `href="languages.html"`) {
		t.Fatalf("index does not link the Languages page")
	}
}

// loadedLedger writes one signed qualifying go run to a real ledger
// directory, so the tests below go through Load, not a hand-built Site.
func loadedLedger(t *testing.T, gen string) (dir string, pub ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dir = t.TempDir()
	d := false
	e := auditpush.LedgerEntry{Bundle: auditpush.Bundle{
		Scan: auditpush.ScanRow{Repo: "https://github.com/acme/widgets", RepoIdentity: "git", Commit: "c1",
			Runner: "go-test", VerdictGeneration: gen, TreeDirty: &d},
		Files: []auditpush.Row{{Lang: "go", Path: "a.go", Audited: 1, MutantsGraded: 5}},
	}}
	signer := auditpush.Ed25519LedgerSigner{KeyID: auditpush.KeyFingerprint(pub), Key: priv}
	if _, err := auditpush.AppendLedgerEntry(dir, e, signer); err != nil {
		t.Fatal(err)
	}
	return dir, pub
}

// The page is computed from what Load read: dropping that hand-off shows
// "no run" for every language in production while hand-built sites pass.
func TestLanguagesPageFromALoadedLedgerShowsItsRun(t *testing.T) {
	dir, pub := loadedLedger(t, "40")
	s, err := Load(dir, pub)
	if err != nil {
		t.Fatal(err)
	}
	s = s.WithLanguages(lLangs, lRunners, lSelf, "--self")
	if s.Languages[0].Local == nil {
		t.Fatalf("a signed qualifying run on disk is not on the page: %+v", s.Languages[0])
	}
}

func TestLanguagesPageFromALedgerLoadedWithoutAKeySaysNotChecked(t *testing.T) {
	dir, _ := loadedLedger(t, "40")
	s, err := Load(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	s = s.WithLanguages(lLangs, lRunners, lSelf, "--self")
	if got := s.Languages[0].Reason; got != "signatures not checked: render with the ledger's public key" {
		t.Fatalf("reason = %q", got)
	}
}

// A run below the floor with no signature check is unchecked, not "no run".
func TestLanguagesPageBelowFloorUncheckedSaysNotChecked(t *testing.T) {
	s := langSite(t, "39", false, "").WithLanguages(lLangs, lRunners, lSelf, "--self")
	if got := s.Languages[0].Reason; got != "signatures not checked: render with the ledger's public key" {
		t.Fatalf("reason = %q", got)
	}
}

// A language never measured has no gap verdict to give: "—", not "no".
func TestLanguagesPageUnmeasuredLanguageHasNoGapVerdict(t *testing.T) {
	s := langSite(t, "40", true, "").WithLanguages(lLangs, lRunners, lSelf, "--self")
	page := renderPage(t, s, "languages.html")
	i := strings.Index(page, "<td class=\"mono\">python</td>")
	if i < 0 {
		t.Fatalf("no python row")
	}
	row := page[i : i+strings.Index(page[i:], "</tr>")]
	if strings.Contains(row, "chip") {
		t.Fatalf("an unmeasured language shows a gap chip: %s", row)
	}
}

// unsignedSite is langSite with the entry carrying no signature at all.
func unsignedSite(t *testing.T, gen string) Site {
	t.Helper()
	s := langSite(t, gen, false, "")
	s.checks[0].Signed, s.checks[0].Signer = false, ""
	return s
}

// An unsigned entry is "unsigned", never "render with the key": a reader who
// already gave the key would be told to supply it.
func TestLanguagesPageUnsignedSaysUnsigned(t *testing.T) {
	s := unsignedSite(t, "40").WithLanguages(lLangs, lRunners, lSelf, "--self")
	if got := s.Languages[0].Reason; got != "runs on this record are unsigned" {
		t.Fatalf("reason = %q", got)
	}
	if strings.Contains(renderPage(t, s, "languages.html"), "public key") {
		t.Fatalf("an unsigned row tells the reader to supply a key")
	}
}

func TestLanguagesPageUnsignedOutranksBelowFloor(t *testing.T) {
	a := unsignedSite(t, "40")
	b := langSite(t, "39", true, "")
	b.entries[0].Hash, b.checks[0].Hash = "0123456789abcdef0123456789abcdef", "0123456789abcdef0123456789abcdef"
	a.entries = append(a.entries, b.entries...)
	a.checks = append(a.checks, b.checks...)
	if got := a.WithLanguages(lLangs, lRunners, lSelf, "--self").Languages[0].Reason; got != "runs on this record are unsigned" {
		t.Fatalf("reason = %q", got)
	}
}

func TestLanguagesPageUncheckedOutranksUnsigned(t *testing.T) {
	a := langSite(t, "40", false, "")
	b := unsignedSite(t, "40")
	b.entries[0].Hash, b.checks[0].Hash = "0123456789abcdef0123456789abcdef", "0123456789abcdef0123456789abcdef"
	a.entries = append(a.entries, b.entries...)
	a.checks = append(a.checks, b.checks...)
	if got := a.WithLanguages(lLangs, lRunners, lSelf, "--self").Languages[0].Reason; got != "signatures not checked: render with the ledger's public key" {
		t.Fatalf("reason = %q", got)
	}
}

func goRunCell(t *testing.T, s Site) string {
	t.Helper()
	page := renderPage(t, s.WithLanguages(lLangs, lRunners, lSelf, "--self"), "languages.html")
	i := strings.Index(page, `<td class="mono">go</td>`)
	if i < 0 {
		t.Fatalf("no go row")
	}
	return page[i : i+strings.Index(page[i:], "</tr>")]
}

// The run cell names the release that graded it.
func TestLanguagesPageRunCellShowsTheRelease(t *testing.T) {
	s := langSite(t, "40", true, "")
	s.entries[0].Bundle.Scan.CorralVersion = "v1.0.0-rc.31"
	if row := goRunCell(t, s); !strings.Contains(row, "v1.0.0-rc.31") {
		t.Fatalf("no release in %s", row)
	}
}

// A run whose runner is not this language's says so, not a blank.
func TestLanguagesPageRunCellSaysRunnerNotRecorded(t *testing.T) {
	s := langSite(t, "40", true, "")
	s.entries[0].Bundle.Scan.Runner = "pytest" // not a go runner: ProofRun.Runner stays blank
	row := goRunCell(t, s)
	if !strings.Contains(row, "runner not recorded") {
		t.Fatalf("blank runner not stated in %s", row)
	}
}

// A run with no start time shows no time, never 0001-01-01.
func TestLanguagesPageRunCellOmitsAZeroTime(t *testing.T) {
	s := langSite(t, "40", true, "")
	s.entries[0].Bundle.Scan.StartedAt = nil
	row := goRunCell(t, s)
	if strings.Contains(row, "0001") {
		t.Fatalf("zero time rendered in %s", row)
	}
	if !strings.Contains(goRunCell(t, langSite(t, "40", true, "")), "2026-11-01") {
		t.Fatalf("a recorded time is not shown")
	}
}
