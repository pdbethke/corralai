// SPDX-License-Identifier: Elastic-2.0

package recordview

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/ledger"
)

const (
	declaredNote = "declared by the commit"
	runNote      = "the run's own account"
)

// A review entry names the audited party from the commit's own words. The
// page must say they are declared, not measured, wherever it prints them.
func TestTierReviewAuditedPartyIsLabelledDeclared(t *testing.T) {
	e := reviewEntry("aaaaaaaaaaaa", 0)
	e.Review.Author = "Ada <ada@example.com>"
	e.Review.CoAuthors = "Bot <bot@example.com>"
	e.Review.GeneratedWith = "SomeTool"
	_, out := renderAll(t, []auditpush.LedgerEntry{e})
	page := read(t, filepath.Join(out, "entry", "aaaaaaaaaaaa.html"))
	i := strings.Index(page, "audited party")
	if i < 0 {
		t.Fatal("the page has no audited party row")
	}
	row := page[i:]
	row = row[:strings.Index(row, "</dd>")]
	if !strings.Contains(row, declaredNote) {
		t.Fatalf("the audited party row does not say it is declared by the commit:\n%s", row)
	}
	for _, v := range []string{"Ada", "Bot", "SomeTool"} {
		if !strings.Contains(row, v) {
			t.Errorf("the row lost %q", v)
		}
	}
}

// Negative control, and the statement that events are not rendered: a scan
// with a timeline and no identity prints neither label, because recordview's
// scan page shows no event field and no commit identity.
func TestTierScanPageRendersNeitherLabelWithoutIdentityOrEvents(t *testing.T) {
	e := warpLikeScan("94d9296c5801c7b56dea3ccb0e7517f7254fded3441bcbeefccc01f550dce4dc", true)
	e.Bundle.Events = []auditpush.EventRow{{Kind: "scan-start", Actor: "orchestrator", Detail: "UNIQUE-EVENT-DETAIL", TS: time.Unix(0, 0)}}
	_, page := renderOne(t, []auditpush.LedgerEntry{e})
	p := page("94d9296c5801")
	if strings.Contains(p, "UNIQUE-EVENT-DETAIL") {
		t.Fatal("events are now rendered: assert they sit under the run's own account")
	}
	for _, label := range []string{declaredNote, runNote} {
		if strings.Contains(p, label) {
			t.Errorf("a scan with no identity and no rendered events prints %q", label)
		}
	}
}

// Driven by the table: every declared or meta field a template prints must
// carry its note. A field is "printed" when a template names it as .Field on
// one of the record types the views hold; the table is walked, never a list
// of fields typed here, so a field added to the table and rendered without
// its note fails. Refutation.Declared is exempt: its name is shared with
// Finding.Declared, which is printed, and Refutation's is not.
func TestTierEveryRenderedDeclaredOrMetaFieldCarriesItsNote(t *testing.T) {
	held := map[string]bool{"Review": true, "Finding": true, "Refutation": true, "Adjudication": true,
		"Entry": true, "Row": true, "ScanRow": true}
	exempt := map[string]bool{"Refutation.Declared": true}
	var src strings.Builder
	for _, tpl := range []string{indexTmpl, entryBanner, entryTmpl, scanTmpl, rowTmpls, listTmpl, chainTmpl, controlsTmpl, verdictsTmpl} {
		src.WriteString(tpl)
	}
	all := src.String()
	checked := 0
	for _, ft := range ledger.FieldTiers() {
		if !held[ft.Row] || (ft.Tier != ledger.TierDeclared && ft.Tier != ledger.TierMeta) || exempt[ft.Row+"."+ft.Field] {
			continue
		}
		if !regexp.MustCompile(`\.` + ft.Field + `\b`).MatchString(all) {
			continue
		}
		checked++
		if !strings.Contains(all, `tierNote "`+ft.Row+`" "`+ft.Field+`"`) {
			t.Errorf("a template prints %s.%s (%s) without tierNote %q %q", ft.Row, ft.Field, ft.Tier, ft.Row, ft.Field)
		}
		if ledger.NoteOf(ft.Row, ft.Field) == "" {
			t.Errorf("%s.%s is %s and has no note", ft.Row, ft.Field, ft.Tier)
		}
	}
	// A floor: a walk that matched nothing would pass vacuously.
	if checked < 10 {
		t.Fatalf("only %d rendered declared/meta fields found; the walk is not reaching the templates", checked)
	}
}

// A person's adjudication and a retraction reason are declared by that person.
func TestTierAdjudicationAndRetractionReasonsAreLabelled(t *testing.T) {
	_, out := renderAll(t, []auditpush.LedgerEntry{
		reviewEntry("aaaaaaaaaaaa", 0),
		adjudicationEntry("bbbbbbbbbbbb", "aaaaaaaaaaaa#R1", auditpush.VerdictConfirmed),
	})
	page := read(t, filepath.Join(out, "entry", "aaaaaaaaaaaa.html"))
	if !strings.Contains(page, ledger.NoteOf("Adjudication", "Reason")) {
		t.Error("the human verdict is not labelled as declared by the adjudicator")
	}
	_, out = renderAll(t, []auditpush.LedgerEntry{reviewEntry("aaaaaaaaaaaa", 0), retractEntry("cccccccccccc", "aaaaaaaaaaaa")})
	page = read(t, filepath.Join(out, "entry", "aaaaaaaaaaaa.html"))
	if !strings.Contains(page, ledger.NoteOf("Entry", "Reason")) {
		t.Error("the retraction reason is not labelled as a person's words")
	}
	chain := read(t, filepath.Join(out, "chain-1.html"))
	if !strings.Contains(chain, "wrong scope") || !strings.Contains(chain, ledger.NoteOf("Entry", "Reason")) {
		t.Error("the chain row's retraction reason is not labelled as a person's words")
	}
}
