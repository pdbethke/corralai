// SPDX-License-Identifier: Elastic-2.0

package recordview

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/auditpush"
)

// warpLikeScan is shaped on ledger entry 94d9296c5801 (Warp's fuzzy_match):
// 34 graded, 23 survived, 22 proven, the authored block landing in the file
// the source declares, and one proven survivor whose diff the page shows.
func warpLikeScan(hash string, sourcePushed bool) auditpush.LedgerEntry {
	return auditpush.LedgerEntry{
		Hash:   hash,
		Pushed: time.Date(2026, 10, 14, 13, 37, 12, 0, time.UTC),
		Bundle: auditpush.Bundle{
			SourcePushed: sourcePushed,
			Scan: auditpush.ScanRow{
				Repo: "https://github.com/warpdotdev/warp", Commit: "714c51f1e29f0863b8b7a3e0f460a8acc89b1e0e",
				CorralVersion: "v1.0.0-rc.19-16-g59facd91", Substrate: "workspace",
				InputTokens: 346555, OutputTokens: 13184, ModelCalls: 35,
				ModelSet:  "critic=codex [codex-cli 0.160.0],mutant-generator=gemini-3.8-flash,test-writer=gemini-3.8-flash",
				StartedAt: ptr(time.Date(2026, 10, 14, 13, 26, 28, 0, time.UTC)),
			},
			Files: []auditpush.Row{
				{Path: "Cargo.toml", Disposition: "rejected", Reason: "no-language"},
				{Path: "src/fuzzy_tests.rs", Disposition: "rejected", Reason: "is-test"},
				{
					Path: "src/lib.rs", Lang: "rust", Status: "needs-review",
					KillRate: ptr(11.0 / 34.0), Disposition: "audited", MutantsGraded: 34, Survivors: 23, ProvenMissed: 22,
					AuthoredLanding: "src/fuzzy_tests.rs",
					AuthoredTest:    "#[cfg(test)]\nmod corral_tests {\n    use super::super::*;\n}\n",
				}},
			Mutants: []auditpush.MutantRow{
				{Path: "src/lib.rs", MutantID: "s1/m1", Outcome: "survived", Proven: true, ProvenByAuthoredAlone: true, SpanStart: 226,
					Code: `{"search":"        if text.ends_with(suffix) {","replace":"        if text.starts_with(suffix) {"}`},
				{Path: "src/lib.rs", MutantID: "s0/m3", Outcome: "survived", SpanStart: 300,
					Code: `{"search":"a","replace":"b"}`},
				{Path: "src/lib.rs", MutantID: "s0/m9", Outcome: "killed", SpanStart: 10,
					Code: `{"search":"killed-before","replace":"killed-after"}`},
			},
		},
	}
}

func okCheck(e auditpush.LedgerEntry) auditpush.ChainCheck {
	return auditpush.ChainCheck{File: e.File, Hash: e.Hash, HashOK: true, LinkOK: true, Signed: true, SigOK: true}
}

func renderOne(t *testing.T, entries []auditpush.LedgerEntry) (index string, page func(short string) string) {
	t.Helper()
	var checks []auditpush.ChainCheck
	for i := range entries {
		entries[i].File = entries[i].Hash + ".json.gz"
		checks = append(checks, okCheck(entries[i]))
	}
	out := t.TempDir()
	if err := WriteStatic(out, Build(entries, checks, true)); err != nil {
		t.Fatal(err)
	}
	return read(t, filepath.Join(out, "index.html")), func(short string) string {
		return read(t, filepath.Join(out, "entry", short+".html"))
	}
}

// A scan entry was counted as "legacy" and never shown, so the first audit
// of someone else's code was invisible on the record page. It gets its own
// page, linked from the index, carrying what the run measured: the rate
// with its interval, the proven gaps, where the authored test lands, and
// the survivors as diffs.
func TestAScanEntryGetsItsOwnPage(t *testing.T) {
	const hash = "94d9296c5801c7b56dea3ccb0e7517f7254fded3441bcbeefccc01f550dce4dc"
	index, page := renderOne(t, []auditpush.LedgerEntry{warpLikeScan(hash, true)})

	if !strings.Contains(index, `href="entry/94d9296c5801.html"`) {
		t.Fatalf("the index does not link the scan's page")
	}
	if !strings.Contains(index, "0.32") {
		t.Errorf("the index does not print the scan's kill rate")
	}
	if !strings.Contains(index, "warpdotdev/warp") {
		t.Errorf("the index does not name the audited repository")
	}
	p := page("94d9296c5801")
	for _, want := range []string{
		"warpdotdev/warp",
		"src/lib.rs",
		"0.32",      // the kill rate
		"0.19–0.49", // its 95% interval, computed from the row's counts
		"11 of 34",  // killed of graded
		"22",        // proven missing tests
		"src/fuzzy_tests.rs",
		"mod corral_tests",
		"- if text.ends_with(suffix) {",
		"+ if text.starts_with(suffix) {",
		"s0/m3", // a survivor nobody proved is shown too, marked unproven
		"codex-cli 0.160.0",
	} {
		if !strings.Contains(p, htmlEscape(want)) {
			t.Errorf("the scan page is missing %q", want)
		}
	}
	if strings.Contains(p, "killed-after") {
		t.Errorf("a killed mutant's diff is not a gap and should not be listed with the survivors")
	}
}

// Source travels only with --push-source. When it did not, the page must say
// the authored test was withheld — not render an empty block that reads as
// "no test was written".
func TestAScanWithoutPushedSourceSaysTheTestWasWithheld(t *testing.T) {
	e := warpLikeScan("aaaaaaaaaaaa0000", false)
	e.Bundle.Files[2].AuthoredTest = ""
	for i := range e.Bundle.Mutants {
		e.Bundle.Mutants[i].Code = ""
	}
	_, page := renderOne(t, []auditpush.LedgerEntry{e})
	p := page("aaaaaaaaaaaa")
	if !strings.Contains(p, "withheld") {
		t.Errorf("a scan pushed without source must say its source was withheld")
	}
	if strings.Contains(p, "mod corral_tests") {
		t.Errorf("withheld source was rendered")
	}
}

// A file nothing was graded on has no kill rate. Printing 0.00 would turn a
// could-not-measure into a measured zero.
func TestAFileWithNothingGradedShowsNoRate(t *testing.T) {
	e := warpLikeScan("bbbbbbbbbbbb0000", true)
	e.Bundle.Files = []auditpush.Row{{Path: "src/x.rs", Lang: "rust", Disposition: "rejected", Reason: "test-landing-not-built"}}
	e.Bundle.Mutants = nil
	index, page := renderOne(t, []auditpush.LedgerEntry{e})
	p := page("bbbbbbbbbbbb")
	if strings.Contains(p, "0.00") || strings.Contains(index, "0.00") {
		t.Errorf("an ungraded file was given a rate of 0.00")
	}
	if !strings.Contains(p, "test-landing-not-built") {
		t.Errorf("the page does not say why the file was not audited")
	}
}

// A retracted scan stays in the chain and stops being the record. Its page
// still exists (the link must not break) but says so, with the reason.
func TestARetractedScanSaysSo(t *testing.T) {
	scan := warpLikeScan("cccccccccccc0000", true)
	retract := auditpush.LedgerEntry{
		Hash: "dddddddddddd0000", Kind: auditpush.KindRetract,
		Retracts: scan.Hash, Reason: "ran against a dirty tree",
		Pushed: scan.Pushed.Add(time.Hour),
	}
	index, page := renderOne(t, []auditpush.LedgerEntry{scan, retract})
	p := page("cccccccccccc")
	if !strings.Contains(p, "retracted") || !strings.Contains(p, "ran against a dirty tree") {
		t.Errorf("a retracted scan's page must say it was retracted, and why")
	}
	if !strings.Contains(index, "retracted") {
		t.Errorf("the index must mark the retracted scan")
	}
}

func ptr[T any](v T) *T { return &v }

// htmlEscape is what html/template does to text, so a want string can be
// matched against the page as written.
func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;", "'", "&#39;", "+", "&#43;").Replace(s)
}

// An audit scoped with --path names its scope wherever it names its
// repository: otherwise a page for one crate reads as an audit of Warp.
func TestAScopedScanNamesItsScope(t *testing.T) {
	e := warpLikeScan("eeeeeeeeeeee0000", true)
	e.Bundle.Scan.Scope = "crates/fuzzy_match"
	index, page := renderOne(t, []auditpush.LedgerEntry{e})
	if !strings.Contains(page("eeeeeeeeeeee"), "scoped to <span class=\"mono\">crates/fuzzy_match</span>") {
		t.Errorf("the audit page does not name the scope")
	}
	if !strings.Contains(index, "crates/fuzzy_match") {
		t.Errorf("the index does not name the scope")
	}
}

// The page's interval is over the row's DISTINCT measured edits when the row
// recorded them: 8 graded copies of 3 distinct edits, 1 surviving, is a band
// over n=3. A row from before that was recorded keeps its band over every
// graded mutant, labelled as such rather than passed off as the other.
func TestTheIntervalIsOverDistinctEdits(t *testing.T) {
	e := warpLikeScan("aaaabbbbccccdddd", true)
	three, one := 3, 1
	e.Bundle.Files[2].MutantsDistinct, e.Bundle.Files[2].MutantsDistinctSurvived = &three, &one
	_, page := renderOne(t, []auditpush.LedgerEntry{e})
	if p := page("aaaabbbbcccc"); !strings.Contains(p, "0.21–0.94") || strings.Contains(p, "0.19–0.49") {
		t.Errorf("the interval is not over the 3 distinct edits:\n%s", p)
	}
	_, page = renderOne(t, []auditpush.LedgerEntry{warpLikeScan("eeeeffff00001111", true)})
	if p := page("eeeeffff0000"); !strings.Contains(p, "0.19–0.49") || !strings.Contains(p, "duplicates counted") {
		t.Errorf("an older row's interval is not labelled as over every graded mutant:\n%s", p)
	}
}

// A signature that did not verify is only "not checked" when no key was
// given. With a key, it is a failure, and the entry says who the chain says
// held the key.
func TestEntryBannerSaysWhoSignedAndDoesNotCallAFailureUnchecked(t *testing.T) {
	render := func(sigsChecked bool, c auditpush.ChainCheck) string {
		entries := []auditpush.LedgerEntry{warpLikeScan("abcdef0123456789abcdef", true)}
		entries[0].File = entries[0].Hash + ".json.gz"
		c.File, c.Hash, c.HashOK, c.LinkOK = entries[0].File, entries[0].Hash, true, true
		out := t.TempDir()
		if err := WriteStatic(out, Build(entries, []auditpush.ChainCheck{c}, sigsChecked)); err != nil {
			t.Fatal(err)
		}
		return read(t, filepath.Join(out, "entry", shortHash(entries[0].Hash)+".html"))
	}
	ok := render(true, auditpush.ChainCheck{Signed: true, SigOK: true, Signer: "laptop"})
	if !strings.Contains(ok, "signed by laptop") {
		t.Error("the page does not say who signed")
	}
	bad := render(true, auditpush.ChainCheck{Signed: true, Problem: "key not registered"})
	if strings.Contains(bad, "not checked (no key given)") || !strings.Contains(bad, "signature does NOT verify") {
		t.Error("a signature that failed under a given key was rendered as unchecked")
	}
	nokey := render(false, auditpush.ChainCheck{Signed: true})
	if !strings.Contains(nokey, "not checked (no key given)") {
		t.Error("with no key the page must say signatures were not checked")
	}
}
