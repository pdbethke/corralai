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

	"github.com/pdbethke/corralai/internal/matrixstore"
)

type fakeMatrix struct {
	rows       []matrixstore.Row
	candidates []matrixstore.Row
}

func (f fakeMatrix) Matrix(context.Context) ([]matrixstore.Row, []matrixstore.Row, error) {
	return f.rows, f.candidates, nil
}

// TestMatrixListTableRendersRowsAndDeleteCandidates proves `corral matrix
// list` renders the per-test adequacy rows AND a delete-candidate section
// carrying the honest caveat verbatim — a zero-kill result is relative to
// this run's own mutant set, not proof the test is dead weight.
func TestMatrixListTableRendersRowsAndDeleteCandidates(t *testing.T) {
	f := fakeMatrix{
		rows: []matrixstore.Row{
			{RecordID: 1, Repo: "corralai", Commit: "abc1234", TestSelector: "TestFoo", TestFile: "foo_test.go", Kills: 3, MutantsTotal: 5, DeleteCandidate: false},
			{RecordID: 1, Repo: "corralai", Commit: "abc1234", TestSelector: "TestBar", TestFile: "foo_test.go", Kills: 0, MutantsTotal: 5, DeleteCandidate: true},
		},
		candidates: []matrixstore.Row{
			{RecordID: 1, Repo: "corralai", Commit: "abc1234", TestSelector: "TestBar", TestFile: "foo_test.go", Kills: 0, MutantsTotal: 5, DeleteCandidate: true},
		},
	}

	var out bytes.Buffer
	if rc := runMatrix([]string{"list"}, f, &out, &out); rc != 0 {
		t.Fatalf("rc=%d: %s", rc, out.String())
	}
	got := out.String()
	if !strings.Contains(got, "TestFoo") || !strings.Contains(got, "TestBar") {
		t.Fatalf("table missing rows:\n%s", got)
	}
	if !strings.Contains(got, "TestBar") {
		t.Fatalf("delete-candidate section missing TestBar:\n%s", got)
	}
	if !strings.Contains(got, "Relative to this mutant set") {
		t.Fatalf("delete-candidate section missing the honest caveat:\n%s", got)
	}
}

// TestMatrixListJSONEmitsRawRows proves --json emits the raw rows (not just
// the delete-candidate subset), so a scripted consumer sees every scored
// test.
func TestMatrixListJSONEmitsRawRows(t *testing.T) {
	f := fakeMatrix{
		rows: []matrixstore.Row{
			{RecordID: 1, TestSelector: "TestFoo", MutantsTotal: 5, Kills: 3},
		},
	}
	var out bytes.Buffer
	if rc := runMatrix([]string{"list", "--json"}, f, &out, &out); rc != 0 {
		t.Fatalf("rc=%d: %s", rc, out.String())
	}
	if !strings.Contains(out.String(), `"TestSelector": "TestFoo"`) {
		t.Fatalf("json missing raw row:\n%s", out.String())
	}
}

// TestHTTPMatrixReaderDecodesRows verifies httpMatrixReader against a canned
// /api/matrix response body — the same JSON shape internal/ui's matrix
// handler serves — and that it sends the bearer token. DuckDB is
// single-process (the running brain holds the matrix store file read-write),
// so `corral matrix list` must read over HTTP, never open the file itself.
func TestHTTPMatrixReaderDecodesRows(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/api/matrix" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"rows": []map[string]any{
				{"TestSelector": "TestFoo", "TestFile": "foo_test.go", "Kills": 3, "MutantsTotal": 5, "DeleteCandidate": false},
			},
			"delete_candidates": []map[string]any{},
		})
	}))
	defer srv.Close()

	r := newHTTPMatrixReader(srv.URL, "test-token")
	rows, candidates, err := r.Matrix(context.Background())
	if err != nil {
		t.Fatalf("Matrix: %v", err)
	}
	if len(rows) != 1 || rows[0].TestSelector != "TestFoo" {
		t.Fatalf("unexpected rows: %+v", rows)
	}
	if len(candidates) != 0 {
		t.Fatalf("unexpected candidates: %+v", candidates)
	}
	if gotAuth != "Bearer test-token" {
		t.Fatalf("expected bearer auth, got %q", gotAuth)
	}
}

// TestHTTPMatrixReaderErrorStatus verifies a non-200 response surfaces as a
// clean error rather than a decode panic or silent empty matrix.
func TestHTTPMatrixReaderErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	r := newHTTPMatrixReader(srv.URL, "bad-token")
	if _, _, err := r.Matrix(context.Background()); err == nil {
		t.Fatal("expected error on non-200 status, got nil")
	}
}
