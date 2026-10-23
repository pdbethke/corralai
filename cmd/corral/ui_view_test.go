// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/auditpush"
)

func TestSealRowsForViewCarriesEveryShownField(t *testing.T) {
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	got := sealRowsForView([]sealRow{{Repo: "r", Path: "p", KillRate: 0.4, Survivors: 2, ProvenMissed: 1, AuditedAt: &at, BaselineFailed: true}})[0]
	if got.Repo != "r" || got.Path != "p" || got.KillRate != 0.4 || got.Survivors != 2 || got.ProvenMissed != 1 || !got.AuditedAt.Equal(at) || got.Caveat != "BASELINE-FAILED" {
		t.Fatalf("a field did not survive the converter: %+v", got)
	}
}

func TestUIServesTheRecordPages(t *testing.T) {
	dir := t.TempDir()
	if _, err := auditpush.AppendLedgerEntry(dir, auditpush.LedgerEntry{Bundle: auditpush.Bundle{Scan: auditpush.ScanRow{Repo: "r", Commit: "abcdef1234567890", CorralVersion: "vtest"}}}, nil); err != nil {
		t.Fatal(err)
	}
	h := uiHandler(fakeSeal{rows: []sealRow{{Repo: "r", Path: "a.go", KillRate: 0.5}}}, dir, "")
	for _, p := range []string{"/", "/chain-1.html", "/audits-1.html", "/verdicts.html"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", p, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(rec.Body.String(), "The seal") || !strings.Contains(rec.Body.String(), "a.go") {
		t.Fatal("corral ui's index does not carry the seal")
	}
}

func TestUIShowsASealReadFailure(t *testing.T) {
	h := uiHandler(fakeSeal{err: errors.New("locked")}, "", "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(rec.Body.String(), "Could not read the seal: locked") {
		t.Fatalf("got %q", rec.Body.String())
	}
}
