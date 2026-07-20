// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/criticscore"
)

var errBoom = errors.New("boom")

type fakeCriticLister struct {
	findings []criticscore.Finding
	err      error
}

func (f fakeCriticLister) ListPending(context.Context) ([]criticscore.Finding, error) {
	return f.findings, f.err
}

type fakeCriticAdmin struct {
	finding criticscore.Finding
	getErr  error
	message string
	adjErr  error
}

func (f fakeCriticAdmin) Get(context.Context, string) (criticscore.Finding, error) {
	return f.finding, f.getErr
}

func (f fakeCriticAdmin) Adjudicate(context.Context, string, string) (string, error) {
	return f.message, f.adjErr
}

func TestRunCriticScoreListPrintsPendingTable(t *testing.T) {
	lister := fakeCriticLister{findings: []criticscore.Finding{
		{ID: "42:5", Model: "haiku", TargetTest: "TestFoo", Scope: "whole-test", Severity: "high"},
	}}
	var out, errOut bytes.Buffer
	rc := runCriticScore([]string{"list"}, lister, fakeCriticAdmin{}, &out, &errOut)
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errOut.String())
	}
	if !strings.Contains(out.String(), "42:5") || !strings.Contains(out.String(), "haiku") || !strings.Contains(out.String(), "TestFoo") {
		t.Fatalf("list output missing expected fields:\n%s", out.String())
	}
}

func TestRunCriticScoreListEmpty(t *testing.T) {
	var out, errOut bytes.Buffer
	rc := runCriticScore([]string{"list"}, fakeCriticLister{}, fakeCriticAdmin{}, &out, &errOut)
	if rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if !strings.Contains(out.String(), "no pending") {
		t.Fatalf("expected an explicit empty message, got:\n%s", out.String())
	}
}

func TestRunCriticScoreShowPrintsFinding(t *testing.T) {
	admin := fakeCriticAdmin{finding: criticscore.Finding{ID: "42:5", Model: "haiku", TargetTest: "TestFoo", Evidence: "the mutant survived", Adjudication: "unadjudicated"}}
	var out, errOut bytes.Buffer
	rc := runCriticScore([]string{"show", "42:5"}, fakeCriticLister{}, admin, &out, &errOut)
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errOut.String())
	}
	if !strings.Contains(out.String(), "the mutant survived") || !strings.Contains(out.String(), "TestFoo") {
		t.Fatalf("show output missing finding detail:\n%s", out.String())
	}
}

func TestRunCriticScoreConfirmAndRefute(t *testing.T) {
	for _, verdict := range []string{"confirm", "refute"} {
		admin := fakeCriticAdmin{message: "42:5 adjudicated " + verdict + "d"}
		var out, errOut bytes.Buffer
		rc := runCriticScore([]string{verdict, "42:5"}, fakeCriticLister{}, admin, &out, &errOut)
		if rc != 0 {
			t.Fatalf("%s: rc=%d stderr=%s", verdict, rc, errOut.String())
		}
		if !strings.Contains(out.String(), "42:5") {
			t.Fatalf("%s: output missing confirmation message:\n%s", verdict, out.String())
		}
	}
}

func TestRunCriticScoreAdjudicateErrorSurfacesAndFails(t *testing.T) {
	admin := fakeCriticAdmin{adjErr: errBoom}
	var out, errOut bytes.Buffer
	rc := runCriticScore([]string{"confirm", "42:5"}, fakeCriticLister{}, admin, &out, &errOut)
	if rc == 0 {
		t.Fatalf("expected non-zero rc on adjudicate error")
	}
	if !strings.Contains(errOut.String(), "boom") {
		t.Fatalf("expected error surfaced on stderr, got:\n%s", errOut.String())
	}
}

func TestRunCriticScoreUsageOnBadArgs(t *testing.T) {
	var out, errOut bytes.Buffer
	rc := runCriticScore(nil, fakeCriticLister{}, fakeCriticAdmin{}, &out, &errOut)
	if rc != 2 {
		t.Fatalf("expected rc=2 on missing subcommand, got %d", rc)
	}
	rc = runCriticScore([]string{"confirm"}, fakeCriticLister{}, fakeCriticAdmin{}, &out, &errOut)
	if rc != 2 {
		t.Fatalf("expected rc=2 on missing id, got %d", rc)
	}
}

// TestHTTPCriticScoreListerDecodesPending verifies the HTTP client against a
// canned /api/criticscore response — the same JSON shape internal/ui's
// criticScorePending handler serves — and that it sends the bearer token.
func TestHTTPCriticScoreListerDecodesPending(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/api/criticscore" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"findings": []map[string]any{{"id": "42:5", "model": "haiku"}},
		})
	}))
	defer srv.Close()

	r := newHTTPCriticScoreLister(srv.URL, "test-token")
	findings, err := r.ListPending(context.Background())
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(findings) != 1 || findings[0].ID != "42:5" || findings[0].Model != "haiku" {
		t.Fatalf("unexpected findings: %+v", findings)
	}
	if gotAuth != "Bearer test-token" {
		t.Fatalf("expected bearer auth, got %q", gotAuth)
	}
}
