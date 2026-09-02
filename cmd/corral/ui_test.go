// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
	uiHandler(st).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/seal", nil))

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
	uiHandler(fakeSeal{err: io.ErrUnexpectedEOF}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/seal", nil))

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
	uiHandler(fakeSeal{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d serving the page, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"corral seal", "api/seal"} {
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
func TestEverySubcommandIsDispatchable(t *testing.T) {
	for _, name := range []string{
		"certify", "secret", "control", "scorecard", "criticscore", "matrix",
		"models", "scans", "seal", "ui", "eval", "mcp", "doctor", "demo", "verify",
	} {
		if got := subcommand([]string{name}); got != name {
			t.Errorf("subcommand(%q) = %q — the name is not in the allowlist, so corral would boot the coordination server instead of running it", name, got)
		}
	}
	if got := subcommand([]string{"definitely-not-a-subcommand"}); got != "" {
		t.Errorf("subcommand of an unknown name = %q, want \"\"", got)
	}
}
