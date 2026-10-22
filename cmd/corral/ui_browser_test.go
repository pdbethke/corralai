// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http/httptest"
	"os/user"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/review"
)

// The write page's token handling, run in a real browser rather than read as
// strings. TestUIPageHandlesTheTokenOnlyTheWaySpecified proves the page
// CARRIES sessionStorage, replaceState and the Bearer header; only a browser
// proves the script does what they say once the URL `corral ui --write`
// prints is opened.
//
// It is the same handler `corral ui --write` serves (uiHandlerWith), over a
// throwaway ledger, with a token this test mints, so it runs on every CI pass
// without touching anyone's record.
//
// It skips, by name, when the machine has no Chrome or Chromium; it never
// downloads one. CI's runner image has Chrome, so the census shows it ran.
func TestUIWritePageInARealBrowser(t *testing.T) {
	bin, ok := launcher.LookPath()
	if !ok {
		t.Skip("no Chrome or Chromium on this machine; the write page's browser check did not run")
	}

	ledger := t.TempDir()
	exit0 := 0
	r := review.Review{Repo: "r", Commit: "0123456789abcdef0123456789abcdef01234567", Scope: "pkg", ReviewerModel: "m",
		Findings: []review.Finding{{ID: "R1", Claim: "awaiting a verdict", Script: "exit 0", ExitCode: &exit0}}}
	file, err := auditpush.WriteReview(ledger, r, nil)
	if err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	w := testWriter(t, addr)
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	w.token = hex.EncodeToString(raw) // the page only captures a 64-hex token
	w.ledgerDir, w.cli = ledger, inProcessCLI(t)
	srv := httptest.NewUnstartedServer(uiHandlerWith(fakeSeal{}, ledger, "", w))
	srv.Listener.Close() //nolint:errcheck
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	base := "http://" + addr

	u, err := launcher.New().Bin(bin).Headless(true).NoSandbox(true).Leakless(false).Launch()
	if err != nil {
		t.Fatalf("launching %s: %v", bin, err)
	}
	browser := rod.New().ControlURL(u)
	if err := browser.Connect(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { browser.Close() }) //nolint:errcheck
	browser = browser.Timeout(30 * time.Second)

	me := ""
	if cu, err := user.Current(); err == nil {
		me = cu.Username
	}
	writeBar := func(p *rod.Page) string {
		t.Helper()
		el := p.MustElementR("div", "WRITE mode|Write mode unavailable")
		return el.MustText()
	}

	// The printed URL: the token arrives in the fragment, moves to
	// sessionStorage, and leaves the address bar.
	page := browser.MustPage(base + "/#t=" + w.token)
	page.MustWaitLoad()
	if bar := writeBar(page); !strings.Contains(bar, "verdicts are recorded as "+me) {
		t.Errorf("the WRITE bar does not name the operator %q: %q", me, bar)
	}
	if href := page.MustEval(`() => location.href`).String(); strings.Contains(href, w.token) || strings.Contains(href, "#t=") {
		t.Errorf("the token is still in the address bar: %s", href)
	}
	if got := page.MustEval(`() => sessionStorage.getItem('corral-ui-token')`).String(); got != w.token {
		t.Errorf("sessionStorage holds %q, want the launch token", got)
	}
	if got := page.MustEval(`() => localStorage.length`).Int(); got != 0 {
		t.Errorf("localStorage holds %d item(s); the token must not outlive the tab", got)
	}

	// The next page in the same tab, with no fragment: the bar still names
	// the operator and the verdict buttons are live.
	page.MustNavigate(base + "/verdicts.html").MustWaitLoad()
	if bar := writeBar(page); !strings.Contains(bar, "verdicts are recorded as "+me) {
		t.Errorf("on the verdicts page the WRITE bar does not name the operator: %q", bar)
	}
	boxes := page.MustElements(".controls[data-ref]")
	if len(boxes) != 1 {
		t.Fatalf("the verdicts page shows %d control box(es), want the one awaiting finding", len(boxes))
	}
	box := boxes[0]
	if ref := *box.MustAttribute("data-ref"); !regexp.MustCompile(`^[0-9a-f]{64}#R1$`).MatchString(ref) {
		t.Errorf("the control box is for %q, want the review's hash and finding R1 (written as %s)", ref, file)
	}
	btns := box.MustElements("button[data-verdict]")
	if len(btns) != 2 {
		t.Fatalf("the control box shows %d verdict button(s), want Confirm and Refute", len(btns))
	}
	for _, b := range btns {
		if b.MustProperty("disabled").Bool() {
			t.Errorf("verdict button %q is disabled with a token in the tab", b.MustText())
		}
	}

	// A tab that never saw the URL has no token: nothing can write. Checked
	// before the click below, which takes the finding off this page.
	fresh := browser.MustIncognito().MustPage(base + "/verdicts.html")
	fresh.MustWaitLoad()
	freshBtns := fresh.MustElements("button[data-verdict]")
	if len(freshBtns) != 2 {
		t.Fatalf("the no-token tab shows %d verdict button(s), want Confirm and Refute", len(freshBtns))
	}
	for _, b := range freshBtns {
		if !b.MustProperty("disabled").Bool() {
			t.Errorf("verdict button %q is live in a tab with no launch token", b.MustText())
		}
	}
	if out := fresh.MustElement(".controls .out").MustText(); !strings.Contains(out, "No launch token") {
		t.Errorf("a tab with no token does not say so: %q", out)
	}

	// A click sends the token as a Bearer header and the server records a
	// real adjudication in the throwaway ledger.
	box.MustElement("textarea").MustInput("checked in a real browser")
	box.MustElement(`button[data-verdict="confirm"]`).MustClick()
	out := box.MustElement(".out")
	out.MustWait(`() => this.textContent !== '' && !this.textContent.startsWith('running')`)
	if txt := out.MustText(); !strings.Contains(txt, "verdict recorded") {
		t.Fatalf("the click did not record a verdict: %q", txt)
	}
	entries, err := auditpush.ReadLedgerDir(ledger)
	if err != nil {
		t.Fatal(err)
	}
	last := entries[len(entries)-1]
	if last.Kind != auditpush.KindAdjudication || last.Adjudication == nil || last.Adjudication.Verdict != auditpush.VerdictConfirmed {
		t.Errorf("the click did not record a confirmed adjudication: last entry %+v", last)
	}
}
