// SPDX-License-Identifier: Elastic-2.0

package recordview

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/auditpush"
)

func appendOne(t *testing.T, dir string) {
	t.Helper()
	if _, err := auditpush.AppendLedgerEntry(dir, auditpush.LedgerEntry{Bundle: auditpush.Bundle{Scan: auditpush.ScanRow{Repo: "r", Commit: "abcdef1234567890", CorralVersion: "vtest"}}}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestLoadReadsVerifiesAndPairs(t *testing.T) {
	dir := t.TempDir()
	appendOne(t, dir)
	appendOne(t, dir)
	s, err := Load(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !s.HasLedger || s.LedgerEntries != 2 || len(s.Chain) != 2 || s.ChainProblems != 0 {
		t.Fatalf("got HasLedger=%v entries=%d chain=%d problems=%d", s.HasLedger, s.LedgerEntries, len(s.Chain), s.ChainProblems)
	}
	if s.SigsChecked {
		t.Fatal("no key was given, so signatures were not checked")
	}
}

// Review Focus 1: in live mode, each request loads afresh.
func TestLiveModeReadsTheLedgerFreshOnEveryRequest(t *testing.T) {
	dir := t.TempDir()
	appendOne(t, dir)
	h := Handler(func() (Site, error) { return Load(dir, nil) })
	get := func() string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		if rec.Code != 200 {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	first := get()
	appendOne(t, dir)
	second := get()
	want1 := `<div class="stat"><b>1</b><span>ledger entries</span>`
	want2 := `<div class="stat"><b>2</b><span>ledger entries</span>`
	if !strings.Contains(first, want1) || !strings.Contains(second, want2) {
		t.Fatal("the second request did not reflect the entry appended between requests")
	}
}
