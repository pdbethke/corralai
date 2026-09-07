// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"encoding/json"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/review"
	"github.com/pdbethke/corralai/internal/scanstore"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
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
	rec := httptest.NewRecorder()
	uiHandler(st, "").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/seal", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got []sealRow
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding: %v (body %q)", err, rec.Body.String())
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}
	if got[0].Path != "proven.go" {
		t.Errorf("first row = %q, want proven.go — proven gaps are the earned findings and must rank above a worse kill rate that proved nothing", got[0].Path)
	}
}

// TestUIReportsAReadFailureRatherThanAnEmptyLedger is the honesty rule this
// project applies everywhere else, applied to a web page: an empty table reads
// as "this codebase has no audited files", which is a far more comforting
// claim than "the ledger could not be read". They must not look alike.
func TestUIReportsAReadFailureRatherThanAnEmptyLedger(t *testing.T) {
	rec := httptest.NewRecorder()
	uiHandler(fakeSeal{err: io.ErrUnexpectedEOF}, "").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/seal", nil))

	if rec.Code == http.StatusOK {
		t.Fatalf("status = 200 on a failed read — the page would render an empty table and an operator would read it as a clean codebase")
	}
	if !strings.Contains(rec.Body.String(), "error") {
		t.Errorf("body = %q, want the failure named", rec.Body.String())
	}
}

// TestUIServesTheEmbeddedPage: the page ships IN the binary. No CDN, no
// network — the same posture as the jail. If this regresses, `corral ui` serves
// an API and a 404.
func TestUIServesTheEmbeddedPage(t *testing.T) {
	rec := httptest.NewRecorder()
	uiHandler(fakeSeal{}, "").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d serving the page, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"The seal", "api/seal", "The chain", "api/ledger"} {
		if !strings.Contains(body, want) {
			t.Errorf("the embedded page does not mention %q", want)
		}
	}
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

// TestUILedgerAPIShowsTheChainTheReviewsAndTheVerdicts: the page's second
// half. Over a ledger directory, /api/ledger carries every entry with its
// chain check, each review with its findings, the verifier's refutation
// and the newest adjudication attached, and retractions; over anything
// that is not a directory it says so instead of pretending.
func TestUILedgerAPIShowsTheChainTheReviewsAndTheVerdicts(t *testing.T) {
	t.Setenv("CORRALAI_CERTIFY_KEY_FILE", filepath.Join(t.TempDir(), "certify_key"))
	dir := filepath.Join(t.TempDir(), "ledger")
	writeCacheTestEntry(t, dir, []scanstore.File{{Path: "a.go", Disposition: "audited", Gradable: true, KillRate: ptrF(0.5)}})
	signer, _ := ledgerSignerFromLocalKey()
	code := 0
	r := review.Review{Repo: "acme/r", Commit: "abc", Scope: "pkg", ReviewerModel: "rev", VerifierModel: "ver", Opinion: "it leaks",
		Findings: []review.Finding{{ID: "R1", Claim: "leaks the key", Declared: review.TierReproduced, Tier: review.TierReproduced, Script: "exit 0", ExitCode: &code,
			Refutation: &review.Refutation{Model: "ver", Verdict: review.VerdictStands, Argument: "could not refute"}}},
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

	rec := httptest.NewRecorder()
	uiHandler(fakeSeal{}, dir).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/ledger", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var l uiLedger
	if err := json.Unmarshal(rec.Body.Bytes(), &l); err != nil {
		t.Fatal(err)
	}
	if l.Dir != dir || len(l.Entries) != 4 || l.Problems != 0 || l.Verified == "" {
		t.Fatalf("ledger = dir %q, %d entries, %d problems, verified %q", l.Dir, len(l.Entries), l.Problems, l.Verified)
	}
	// Newest first: the retraction, the adjudication, the review, the scan.
	if l.Entries[0].Kind != "retract" || l.Entries[3].Kind != "scan" || !l.Entries[3].Signed || !l.Entries[3].Verified {
		t.Errorf("entries: %+v", l.Entries)
	}
	if !strings.HasPrefix(l.Entries[3].Note, "RETRACTED: wrong commit") {
		t.Errorf("the retracted scan must say so in the chain: %+v", l.Entries[3])
	}
	if len(l.Reviews) != 1 || l.Reviews[0].Reviewer != "rev" || l.Reviews[0].Verifier != "ver" || l.Reviews[0].Reproduced != 1 {
		t.Fatalf("reviews: %+v", l.Reviews)
	}
	f := l.Reviews[0].Findings[0]
	if f.Refutation == nil || f.Refutation.Verdict != review.VerdictStands || f.Adjudication == nil || f.Adjudication.Verdict != auditpush.VerdictConfirmed || f.Adjudication.By != "pdb" {
		t.Errorf("the finding must carry the verifier's answer and the person's verdict: %+v", f)
	}
	if len(l.Retracts) != 1 || l.Retracts[0].Reason != "wrong commit" {
		t.Errorf("retractions: %+v", l.Retracts)
	}

	// Not a directory: the API says so rather than inventing a chain.
	rec = httptest.NewRecorder()
	uiHandler(fakeSeal{}, "").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/ledger", nil))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"dir":""}` {
		t.Errorf("no directory: %d %s", rec.Code, rec.Body.String())
	}
	// And the page itself carries the ledger sections.
	rec = httptest.NewRecorder()
	uiHandler(fakeSeal{}, dir).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(rec.Body.String(), "api/ledger") || !strings.Contains(rec.Body.String(), "The chain") {
		t.Error("the page does not render the ledger half")
	}
}
