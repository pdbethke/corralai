// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/review"
	"github.com/pdbethke/corralai/internal/scanstore"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

type fakeSeal struct {
	rows []sealRow
	err  error
}

func (f fakeSeal) SealRows(context.Context, string) ([]sealRow, error) { return f.rows, f.err }
func (f fakeSeal) UncoveredPaths(context.Context, string) (map[string]bool, error) {
	return nil, nil
}
func (f fakeSeal) ImportOnlyPaths(context.Context, string) (map[string]bool, error) {
	return nil, nil
}
func (f fakeSeal) Close() error { return nil }

// uiGet serves one GET through h and returns the response.
func uiGet(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// TestUIServesTheSealAndRanksProvenGapsFirst: the page's whole job is to put
// the earned findings at the top. A proven gap is a survivor the pool wrote a
// test for and killed by execution; a bare survivor is a question. Sorting by
// kill rate alone would bury a file with 12 proven gaps under one with a worse
// rate and nothing proven.
func TestUIServesTheSealAndRanksProvenGapsFirst(t *testing.T) {
	st := fakeSeal{rows: []sealRow{
		{Repo: "r", Path: "low-rate-nothing-proven.go", KillRate: 0.20, Survivors: 9, ProvenMissed: 0, TS: time.Now()},
		{Repo: "r", Path: "proven.go", KillRate: 0.75, Survivors: 12, ProvenMissed: 12, TS: time.Now()},
	}}
	rec := uiGet(t, uiHandler(st, ""), "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	proven, low := strings.Index(body, "proven.go"), strings.Index(body, "low-rate-nothing-proven.go")
	if proven < 0 || low < 0 {
		t.Fatalf("the seal is missing a row (proven at %d, low-rate at %d)", proven, low)
	}
	if proven > low {
		t.Errorf("proven.go renders after low-rate-nothing-proven.go — proven gaps are the earned findings and must rank above a worse kill rate that proved nothing")
	}
}

// TestUIServesTheEmbeddedPage: the pages ship IN the binary. No CDN, no
// network — the same posture as the jail.
func TestUIServesTheEmbeddedPage(t *testing.T) {
	rec := uiGet(t, uiHandler(fakeSeal{}, ""), "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d serving the page, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
	body := rec.Body.String()
	if strings.Contains(body, "https://cdn") || strings.Contains(body, "unpkg") {
		t.Error("the page loads something from a CDN — it must be self-contained, like everything else corral ships")
	}
}

// TestUIRefusesToCallANonLoopbackAddressLocal guards the warning, not the bind:
// an operator may genuinely want to serve this on a LAN, and corral does not
// stop them. What it must not do is stay silent, because the ledger names
// repositories, file paths and their weakest files.
func TestUIRefusesToCallANonLoopbackAddressLocal(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:8787": true, "localhost:8787": true, "[::1]:8787": true,
		"0.0.0.0:8787": false, "192.168.1.10:8787": false, ":8787": false,
		"example.com:8787": false,
	} {
		if got := isLoopback(addr); got != want {
			t.Errorf("isLoopback(%q) = %v, want %v", addr, got, want)
		}
	}
}

// TestEverySubcommandIsDispatchable pins two hand-maintained lists to each
// other.
//
// subcommand() holds an allowlist, and a name missing from it does not error —
// it falls through to booting the coordination server. `corral ui -h` printed
// the BRAIN's usage until "ui" was added to that list, with no other symptom.
// That is the two-products problem in miniature: the default path for anything
// unrecognised is the other product.
func TestDocsEveryDocumentedVerbIsDispatched(t *testing.T) {
	// Derived, not enumerated: the verbs are read from the usage text
	// (every "  corral <verb>" line) and the dispatch's case labels are read
	// from main.go's own source. A verb documented but not dispatched — the
	// shape of `corral ledger`, which was documented, tested through
	// runLedger, and in the shipped binary fell through to the server —
	// fails here by name; so does a case label the usage does not mention.
	dispatched := map[string]bool{}
	for _, n := range dispatchableSubcommands(t) {
		dispatched[n] = true
	}
	documented := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^  corral ([a-z][a-z-]*)\b`).FindAllStringSubmatch(usageText(), -1) {
		documented[m[1]] = true
	}
	if len(dispatched) < 10 || len(documented) < 10 {
		t.Fatalf("read %d dispatched and %d documented verbs — the parse is not covering the surface", len(dispatched), len(documented))
	}
	for v := range documented {
		if !dispatched[v] {
			t.Errorf("`corral %s` is documented in the usage text but has no case in main's dispatch — the shipped binary would refuse it (or, before the pointer, start the server)", v)
		}
	}
	for v := range dispatched {
		if !documented[v] {
			t.Errorf("main dispatches `corral %s` but the usage text never mentions it", v)
		}
	}
	// And the parser itself: subcommand hands back any bare word, never a
	// flag, and never invents one.
	if got := subcommand([]string{"ledger", "verify", "x"}); got != "ledger" {
		t.Errorf("subcommand = %q, want ledger", got)
	}
	if got := subcommand([]string{"-h"}); got != "" {
		t.Errorf("subcommand of a flag = %q, want \"\"", got)
	}
	if got := subcommand(nil); got != "" {
		t.Errorf("subcommand of nothing = %q, want \"\"", got)
	}
}

// TestUIShowsTheChainTheReviewsAndTheVerdicts: the record's second half.
// Over a ledger directory, the chain page carries every entry with its
// chain check and marks the retracted one, and the review's page carries
// its findings with the verifier's refutation and the person's verdict;
// over anything that is not a directory it says so instead of pretending.
func TestUIShowsTheChainTheReviewsAndTheVerdicts(t *testing.T) {
	t.Setenv("CORRALAI_CERTIFY_KEY_FILE", filepath.Join(t.TempDir(), "certify_key"))
	dir := filepath.Join(t.TempDir(), "ledger")
	writeCacheTestEntry(t, dir, []scanstore.File{{Path: "a.go", Disposition: "audited", Gradable: true, KillRate: ptrF(0.5)}})
	signer, _, _ := ledgerSignerFromLocalKey()
	// The UI verifies against an EXPLICIT key only; hand it the one that signed.
	lk, kerr := loadLocalCertifyKeyIfConfigured()
	if kerr != nil {
		t.Fatal(kerr)
	}
	t.Setenv(auditpush.LedgerPubKeyEnv, hex.EncodeToString(lk.Public().(ed25519.PublicKey)))
	code := 0
	r := review.Review{Repo: "acme/r", Commit: "abc", Scope: "pkg", ReviewerModel: "reviewer-model-x", VerifierModel: "verifier-model-y", Opinion: "it leaks",
		Findings: []review.Finding{{ID: "R1", Claim: "leaks the key", Declared: review.TierReproduced, Tier: review.TierReproduced, Script: "exit 0", ExitCode: &code,
			Refutation: &review.Refutation{Model: "verifier-model-y", Verdict: review.VerdictStands, Argument: "could not refute"}}},
		Sound: []string{"the parser"}}
	if _, err := auditpush.WriteReview(dir, r, signer); err != nil {
		t.Fatal(err)
	}
	entries, _ := auditpush.ReadLedgerDir(dir)
	revHash := entries[1].Hash
	if _, err := auditpush.WriteAdjudication(dir, revHash+"#R1", auditpush.VerdictConfirmed, "pdb", "it does", signer); err != nil {
		t.Fatal(err)
	}
	if _, err := auditpush.WriteRetraction(dir, entries[0].Hash, "wrong commit", signer); err != nil {
		t.Fatal(err)
	}

	h := uiHandler(fakeSeal{}, dir)
	chain := uiGet(t, h, "/chain-1.html")
	if chain.Code != http.StatusOK {
		t.Fatalf("chain status %d: %s", chain.Code, chain.Body.String())
	}
	cb := chain.Body.String()
	// Four rows, newest first, each with its kind, every signature checked
	// against the explicit key, and the retraction marked on the SCAN's row
	// (the retract row's own note names the reason too, so a page-wide
	// substring would prove nothing).
	rows := chainRows(cb)
	wantKinds := []string{"retract", "adjudication", "review", "scan"}
	if len(rows) != len(wantKinds) {
		t.Fatalf("chain page has %d rows, want %d: %+v", len(rows), len(wantKinds), rows)
	}
	for i, k := range wantKinds {
		if rows[i].Kind != k || rows[i].Pos != strconv.Itoa(len(wantKinds)-i) {
			t.Errorf("row %d = #%s %s, want #%d %s (newest first)", i, rows[i].Pos, rows[i].Kind, len(wantKinds)-i, k)
		}
		if !strings.Contains(rows[i].Sig, `<span class="chip ok">verified</span>`) {
			t.Errorf("row #%s (%s) signature = %q, want verified", rows[i].Pos, rows[i].Kind, rows[i].Sig)
		}
	}
	if !strings.HasPrefix(rows[3].Note, "RETRACTED: wrong commit") {
		t.Errorf("the retracted scan's chain row must say so: %q", rows[3].Note)
	}
	for _, r := range rows[:3] {
		if strings.Contains(r.Note, "RETRACTED") {
			t.Errorf("row #%s (%s) is marked retracted but stands: %q", r.Pos, r.Kind, r.Note)
		}
	}
	if !strings.Contains(cb, "4/4 verified") {
		t.Error("the chain banner does not say 4/4 verified")
	}
	for _, bad := range []string{"does NOT verify", `chip bad">problem`, "not checked"} {
		if strings.Contains(cb, bad) {
			t.Errorf("the chain page says %q over an intact, verified chain", bad)
		}
	}
	rev := uiGet(t, h, "/entry/"+revHash[:12]+".html")
	if rev.Code != http.StatusOK {
		t.Fatalf("review page status %d", rev.Code)
	}
	rb := rev.Body.String()
	for _, want := range []string{"reviewer-model-x", "leaks the key", "it leaks", "the parser",
		`<span class="chip ok">reproduced</span>`,
		`refutation — verifier-model-y <span class="chip mute">` + review.VerdictStands + `</span>`, "could not refute",
		`<span class="chip ok">confirmed by a person</span>`, "<strong>confirmed</strong> by pdb — it does"} {
		if !strings.Contains(rb, want) {
			t.Errorf("the review page is missing %q", want)
		}
	}

	// Not a directory: the chain page says so rather than inventing a chain.
	none := uiGet(t, uiHandler(fakeSeal{}, ""), "/chain-1.html")
	if none.Code != http.StatusOK || !strings.Contains(none.Body.String(), "Not a ledger directory") {
		t.Errorf("no directory: %d %s", none.Code, none.Body.String())
	}
}

// chainRow is one row of the chain page's table, as rendered.
type chainRow struct{ Pos, Kind, Note, Sig string }

var chainRowRE = regexp.MustCompile(`(?s)<tr>\s*<td>(\d+)</td><td class="nowrap">[^<]*</td>\s*<td>(?:<a [^>]*>)?([a-z]+)(?:</a>)?</td>\s*<td class="small">(.*?)</td>\s*<td>(.*?)</td>\s*</tr>`)

// chainRows reads the chain table's rows in page order, so a test can
// assert on one row's kind, note and signature rather than on a substring
// that page chrome or another row could satisfy.
func chainRows(page string) []chainRow {
	var out []chainRow
	for _, m := range chainRowRE.FindAllStringSubmatch(page, -1) {
		out = append(out, chainRow{Pos: m[1], Kind: m[2], Note: strings.TrimSpace(m[3]), Sig: strings.TrimSpace(m[4])})
	}
	return out
}

// The UI is a third door onto "which key checks the ledger": with a local
// certify key configured and no explicit key it must NOT claim verification
// against the local key, and a malformed explicit key is an error.
func TestUILedgerNeverVerifiesAgainstTheLocalKeyImplicitly(t *testing.T) {
	t.Setenv("CORRALAI_CERTIFY_KEY_FILE", filepath.Join(t.TempDir(), "certify_key"))
	t.Setenv(auditpush.LedgerPubKeyEnv, "")
	dir := filepath.Join(t.TempDir(), "ledger")
	writeCacheTestEntry(t, dir, []scanstore.File{{Path: "a.go", Disposition: "audited", Gradable: true, KillRate: ptrF(0.5)}})
	h := uiHandler(fakeSeal{}, dir)
	rec := uiGet(t, h, "/chain-1.html")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "signed, not checked") || strings.Contains(body, `chip ok">verified`) {
		t.Fatalf("with no explicit key the chain must read \"signed, not checked\" and never verified:\n%s", body)
	}
	t.Setenv(auditpush.LedgerPubKeyEnv, "garbage")
	if rec := uiGet(t, h, "/chain-1.html"); rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), auditpush.LedgerPubKeyEnv) {
		t.Fatalf("garbage key must be a read failure naming the variable, got %d %s", rec.Code, rec.Body.String())
	}
}
