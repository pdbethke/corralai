// SPDX-License-Identifier: Elastic-2.0

package recordview

import (
	"bytes"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/auditpush"
)

func TestTheQueueListsOnlyFindingsWithNoStandingVerdict(t *testing.T) {
	s := buildOK([]auditpush.LedgerEntry{
		reviewEntry("aaaaaaaaaaaa", 0),
		reviewEntry("bbbbbbbbbbbb", 1),
		adjudicationEntry("cccccccccccc", "bbbbbbbbbbbb#R1", auditpush.VerdictRefuted),
	})
	got := s.Awaiting()
	if len(got) != 1 || got[0].Finding.Ref != "aaaaaaaaaaaa#R1" {
		t.Fatalf("awaiting = %+v, want only aaaaaaaaaaaa#R1", got)
	}
}

func TestStaticPagesCarryNoWriteControls(t *testing.T) {
	s := buildOK([]auditpush.LedgerEntry{reviewEntry("aaaaaaaaaaaa", 0)})
	p, _ := NewPages()
	for _, path := range []string{"verdicts.html", "entry/aaaaaaaaaaaa.html"} {
		var b bytes.Buffer
		if err := p.Render(&b, s, path); err != nil {
			t.Fatal(err)
		}
		for _, mustNot := range []string{"api/adjudicate", "Authorization", "data-verdict", "<script"} {
			if strings.Contains(b.String(), mustNot) {
				t.Fatalf("%s: a read-only page carries %q", path, mustNot)
			}
		}
	}
}

// Carried from cmd/corral's TestUIPageHandlesTheTokenOnlyTheWaySpecified:
// the token comes from the fragment, lives in sessionStorage, is stripped
// from the address bar, and is sent only as a Bearer header.
func TestWriteControlsHandleTheTokenOnlyTheWaySpecified(t *testing.T) {
	s := buildOK([]auditpush.LedgerEntry{reviewEntry("aaaaaaaaaaaa", 0)})
	s.Write = true
	p, _ := NewPages()
	for _, path := range []string{"verdicts.html", "entry/aaaaaaaaaaaa.html"} {
		var b bytes.Buffer
		if err := p.Render(&b, s, path); err != nil {
			t.Fatal(err)
		}
		page := b.String()
		for _, must := range []string{
			`location.hash`, `sessionStorage`, `history.replaceState`, `'Authorization': 'Bearer '`,
			`api/adjudicate`, `api/recheck`, `api/cited`, `cited by a commit message, not verified`,
			`could not check citations`, `.disabled = true`,
			`verdict recorded; reload the page to refresh`,
			`No launch token in this tab`, // Review Focus 4: a tab without the token says why nothing works
			`data-ref="aaaaaaaaaaaa#R1"`,
		} {
			if !strings.Contains(page, must) {
				t.Errorf("%s is missing %q", path, must)
			}
		}
		for _, mustNot := range []string{`localStorage`, `?t=`} {
			if strings.Contains(page, mustNot) {
				t.Errorf("%s must not use %q (the token outlives the tab / reaches the server)", path, mustNot)
			}
		}
	}
}

func TestTheQueueIsAPathAndAnAdjudicatedFindingGetsNoControls(t *testing.T) {
	s := buildOK([]auditpush.LedgerEntry{
		reviewEntry("aaaaaaaaaaaa", 0),
		adjudicationEntry("cccccccccccc", "aaaaaaaaaaaa#R1", auditpush.VerdictRefuted),
	})
	s.Write = true
	if got := s.Paths(); len(got) < 2 || got[1] != "verdicts.html" {
		t.Fatalf("Paths = %v, want verdicts.html right after index.html", got)
	}
	p, _ := NewPages()
	var b bytes.Buffer
	if err := p.Render(&b, s, "entry/aaaaaaaaaaaa.html"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), `data-verdict="confirm"`) {
		t.Fatal("a finding that already has a standing verdict offers another one")
	}
	b.Reset()
	if err := p.Render(&b, s, "verdicts.html"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "Nothing is waiting") {
		t.Fatal("an empty queue does not say so")
	}
}

// A tokenless tab must send nothing: the citation lookup (which runs git log
// on the operator's machine) comes after the no-token return.
func TestATokenlessTabSendsNoRequestBeforeItReturns(t *testing.T) {
	i, j := strings.Index(writeJS, "No launch token"), strings.Index(writeJS, "api/cited")
	if i < 0 || j < 0 || i > j {
		t.Fatalf("No launch token at %d must precede api/cited at %d", i, j)
	}
	if w := strings.Index(writeJS, "api/whoami"); w < 0 || !strings.Contains(writeJS[w-20:w], "write(") {
		t.Fatal("api/whoami must go through the POST write helper")
	}
}

func TestWriteModeKeepsWhatUIWebDid(t *testing.T) {
	for _, must := range []string{
		`api/whoami`, `WRITE mode`, `Write mode unavailable: `, `Open the URL`,
		`lastwrite`, `suggest`, `data-verifier`,
	} {
		if !strings.Contains(writeJS+controlsTmpl, must) {
			t.Errorf("write mode lost %q", must)
		}
	}
}

func TestRecheckIsOfferedOnlyWhenThereIsAScript(t *testing.T) {
	scripted := reviewEntry("aaaaaaaaaaaa", 0)
	bare := reviewEntry("bbbbbbbbbbbb", 0)
	bare.Review.Findings[0].Script = ""
	bare.Review.Findings[0].ExitCode = nil
	bare.Review.Findings[0].File, bare.Review.Findings[0].Line = "x.go", 7
	bare.Review.Commit = "0123456789abcdef"
	s := buildOK([]auditpush.LedgerEntry{scripted, bare})
	s.Write = true
	p, _ := NewPages()
	for path, want := range map[string]bool{"entry/aaaaaaaaaaaa.html": true, "entry/bbbbbbbbbbbb.html": false} {
		var b bytes.Buffer
		if err := p.Render(&b, s, path); err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(b.String(), `<button data-recheck>`); got != want {
			t.Errorf("%s: recheck button = %v, want %v", path, got, want)
		}
		if !want && !strings.Contains(b.String(), "no script: judge from the code at <code>x.go:7</code> (commit <code>0123456789</code>)") {
			t.Errorf("%s: a scriptless finding does not say to judge from the code", path)
		}
	}
	var b bytes.Buffer
	if err := p.Render(&b, s, "verdicts.html"); err != nil {
		t.Fatal(err)
	}
	if strings.Count(b.String(), `<button data-recheck>`) != 1 || !strings.Contains(b.String(), "no script: judge from the code at <code>x.go:7</code> (commit <code>0123456789</code>)") {
		t.Error("the queue must offer recheck only on the scripted card")
	}
}

func TestQueueCardsLinkToTheirEntryPage(t *testing.T) {
	s := buildOK([]auditpush.LedgerEntry{reviewEntry("aaaaaaaaaaaa", 0)})
	p, _ := NewPages()
	var b bytes.Buffer
	if err := p.Render(&b, s, "verdicts.html"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), `href="entry/aaaaaaaaaaaa.html#R1"`) {
		t.Fatal("a queue card does not link to its entry page")
	}
}

func TestInFlightAdjudicateTextIsShown(t *testing.T) {
	if !strings.Contains(writeJS, "running corral review adjudicate") {
		t.Fatal("no in-flight progress text")
	}
}
