// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/brain"
	"github.com/pdbethke/corralai/internal/bugcatch"
)

type fakeScore struct{ cells []brain.ScorecardCell }

func (f fakeScore) Scorecard(context.Context) ([]brain.ScorecardCell, error) { return f.cells, nil }

func TestScorecardTableAndJSON(t *testing.T) {
	r := 0.5
	f := fakeScore{cells: []brain.ScorecardCell{{Cell: bugcatch.Cell{Model: "claude-sonnet-5", Role: "test-writer", Catches: 1, Opportunities: 2, Recall: &r, Runs: 2}, Provisional: true}}}

	var table bytes.Buffer
	if rc := runScorecard(nil, f, &table); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if !strings.Contains(table.String(), "claude-sonnet-5") || !strings.Contains(table.String(), "test-writer") {
		t.Fatalf("table missing model/role:\n%s", table.String())
	}
	if !strings.Contains(table.String(), "provisional") { // runs=2 < 3
		t.Fatalf("thin cell must be marked provisional:\n%s", table.String())
	}
	if !strings.Contains(table.String(), "C-PREC") {
		t.Fatalf("table missing C-PREC header:\n%s", table.String())
	}
	if !strings.Contains(table.String(), "—") {
		t.Fatalf("non-critic role must show a dash in the C-PREC column:\n%s", table.String())
	}

	var j bytes.Buffer
	if rc := runScorecard([]string{"--json"}, f, &j); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if !strings.Contains(j.String(), `"recall"`) || !strings.Contains(j.String(), "0.5") {
		t.Fatalf("json missing recall:\n%s", j.String())
	}
}

// TestScorecardTableShowsCriticPrecisionColumn proves the C-PREC column
// renders a real percentage + provisional tilde for the test-critic role,
// carrying the raw counts through in --json.
func TestScorecardTableShowsCriticPrecisionColumn(t *testing.T) {
	p := 2.0 / 3.0
	f := fakeScore{cells: []brain.ScorecardCell{{
		Cell:                bugcatch.Cell{Model: "haiku", Role: advpool.RoleTestCritic, Runs: 5},
		Provisional:         false,
		CriticConfirmed:     2,
		CriticRefuted:       1,
		CriticUnadjudicated: 0,
		CriticPrecision:     &p,
	}}}

	var table bytes.Buffer
	if rc := runScorecard(nil, f, &table); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if !strings.Contains(table.String(), "67%") {
		t.Fatalf("table missing critic precision %%:\n%s", table.String())
	}

	var j bytes.Buffer
	if rc := runScorecard([]string{"--json"}, f, &j); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if !strings.Contains(j.String(), `"critic_confirmed": 2`) || !strings.Contains(j.String(), `"critic_refuted": 1`) {
		t.Fatalf("json missing raw critic counts:\n%s", j.String())
	}
}

// TestHTTPScorecardReaderDecodesCells verifies httpScorecardReader against a
// canned /api/bugcatch response body — the same JSON shape internal/ui's
// bugcatch handler serves — and that it sends the bearer token.
func TestHTTPScorecardReaderDecodesCells(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/api/bugcatch" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"cells": []map[string]any{
				{"model": "claude-sonnet-5", "role": "test-writer", "catches": 1, "opportunities": 2, "recall": 0.5, "runs": 2, "provisional": true},
			},
		})
	}))
	defer srv.Close()

	r := newHTTPScorecardReader(srv.URL, "test-token")
	cells, err := r.Scorecard(context.Background())
	if err != nil {
		t.Fatalf("Scorecard: %v", err)
	}
	if len(cells) != 1 || cells[0].Model != "claude-sonnet-5" || cells[0].Role != "test-writer" {
		t.Fatalf("unexpected cells: %+v", cells)
	}
	if cells[0].Recall == nil || *cells[0].Recall != 0.5 {
		t.Fatalf("unexpected recall: %+v", cells[0].Recall)
	}
	if gotAuth != "Bearer test-token" {
		t.Fatalf("expected bearer auth, got %q", gotAuth)
	}
}

// TestHTTPScorecardReaderErrorStatus verifies a non-200 response surfaces as
// a clean error rather than a decode panic or silent empty scorecard.
func TestHTTPScorecardReaderErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	r := newHTTPScorecardReader(srv.URL, "bad-token")
	if _, err := r.Scorecard(context.Background()); err == nil {
		t.Fatal("expected error on non-200 status, got nil")
	}
}
