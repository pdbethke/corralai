// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

// TestLocalCriticScoreRoundTrip pins the offline corpus: a finding recorded by
// a --local run must be listable, showable and adjudicable with NO brain
// running.
//
// Before this, criticscore refused without CORRAL_BRAIN — so `certify --local`,
// the command the quickstart and README tell people to run, produced critic
// findings that printed once and vanished. scorecard's C-PREC column, which
// measures the critic's execution-checked precision from human verdicts, could
// never be filled by anyone without a daemon.
func TestLocalCriticScoreRoundTrip(t *testing.T) {
	store, err := criticscore.Open(filepath.Join(t.TempDir(), "cs.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.Record(ctx, []criticscore.Finding{{
		ID: "31:1", TS: 1, RecordID: 31, RecordHead: "abc",
		Repo: "local", Commit: "c0ffee", Model: "claude-haiku-4-5",
		TargetTest: "TokenManager schedules a refresh", TestFile: "src/auth/__tests__/TokenManager.test.ts",
		Scope: "dead-check", Evidence: "the setTimeout branch is never exercised",
		Severity: "high", Adjudication: "unadjudicated", Source: "auto",
	}}); err != nil {
		t.Fatal(err)
	}

	local := localCriticScore{store: store}

	pending, err := local.ListPending(ctx)
	if err != nil || len(pending) != 1 {
		t.Fatalf("expected 1 pending finding offline, got %d (%v)", len(pending), err)
	}
	if _, err := local.Get(ctx, "31:1"); err != nil {
		t.Fatalf("show should work offline: %v", err)
	}
	msg, err := local.Adjudicate(ctx, "31:1", "confirmed")
	if err != nil {
		t.Fatalf("confirm should work offline: %v", err)
	}
	if !strings.Contains(msg, "confirmed") {
		t.Fatalf("adjudication message should name the verdict, got %q", msg)
	}

	// Confirmed findings leave the pending queue — that is what makes the
	// corpus a worklist rather than an ever-growing log.
	after, err := local.ListPending(ctx)
	if err != nil || len(after) != 0 {
		t.Fatalf("an adjudicated finding must leave the pending list, got %d (%v)", len(after), err)
	}
	// And the verdict must be attributed: a row that cannot say who decided is
	// worse than one that says "someone at this machine".
	f, _, err := store.Get(ctx, "31:1")
	if err != nil {
		t.Fatal(err)
	}
	if f.Source != "human" || f.AdjudicatedBy == "" {
		t.Fatalf("a local adjudication must be attributed to a human: %+v", f)
	}
}

// TestLocalCriticScoreUnknownID: adjudicating something that does not exist
// must say so rather than silently succeeding.
func TestLocalCriticScoreUnknownID(t *testing.T) {
	store, err := criticscore.Open(filepath.Join(t.TempDir(), "cs.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	local := localCriticScore{store: store}
	if _, err := local.Adjudicate(context.Background(), "99:9", "confirmed"); err == nil {
		t.Fatal("adjudicating an unknown finding must fail loudly")
	}
}
