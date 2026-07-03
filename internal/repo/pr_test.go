// SPDX-License-Identifier: Elastic-2.0

// internal/repo/pr_test.go — regression tests for GitHub PR behavior,
// now exercised through githubProvider directly (the logic lives there after
// the multi-forge refactor). Endpoint paths, auth headers, JSON shapes, and
// the idempotent 422 recovery are all preserved verbatim.
package repo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestGitHubProvider creates a githubProvider pointed at the given test
// server URL with the supplied token.
func newTestGitHubProvider(baseURL, token string) *githubProvider {
	return &githubProvider{rc: restClient{
		base:   baseURL,
		token:  token,
		accept: "application/vnd.github+json",
	}}
}

func TestOpenPR(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/pulls" {
			t.Errorf("PR posted to wrong path: %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		gotBody, _ = in["head"].(string)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"html_url": "https://github.com/o/r/pull/7"})
	}))
	defer srv.Close()
	p := newTestGitHubProvider(srv.URL, "tok123")
	url, err := p.OpenPR(context.Background(), "o", "r", "corralai/m1", "main", "T", "B")
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://github.com/o/r/pull/7" {
		t.Fatalf("pr url: %s", url)
	}
	if !strings.Contains(gotAuth, "tok123") || gotBody != "corralai/m1" {
		t.Fatalf("request wrong: auth=%q head=%q", gotAuth, gotBody)
	}
}

func TestOpenPRError(t *testing.T) {
	// POST 422 for a real validation failure; the existing-PR lookup (GET) finds
	// nothing, so OpenPR must surface the original error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[]`))
			return
		}
		w.WriteHeader(422)
		w.Write([]byte(`{"message":"Validation Failed"}`))
	}))
	defer srv.Close()
	p := newTestGitHubProvider(srv.URL, "tok")
	if _, err := p.OpenPR(context.Background(), "o", "r", "h", "main", "T", "B"); err == nil {
		t.Fatal("expected error on 422 when no existing PR is found")
	}
}

// TestOpenPRAlreadyExists: a 422 "pull request already exists" on POST followed
// by a one-element array on the GET lookup must resolve to that PR's html_url
// with no error — so the reconcile loop records the URL and stops retrying.
func TestOpenPRAlreadyExists(t *testing.T) {
	var sawGet bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			sawGet = true
			if got := r.URL.Query().Get("head"); got != "o:corralai/m1" {
				t.Errorf("lookup head = %q, want %q", got, "o:corralai/m1")
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode([]map[string]any{
				{"html_url": "https://github.com/o/r/pull/42"},
			})
			return
		}
		w.WriteHeader(422)
		w.Write([]byte(`{"message":"Validation Failed","errors":[{"message":"A pull request already exists for o:corralai/m1."}]}`))
	}))
	defer srv.Close()
	p := newTestGitHubProvider(srv.URL, "tok")
	url, err := p.OpenPR(context.Background(), "o", "r", "corralai/m1", "main", "T", "B")
	if err != nil {
		t.Fatalf("expected success via existing-PR lookup, got err: %v", err)
	}
	if url != "https://github.com/o/r/pull/42" {
		t.Fatalf("expected existing PR url, got %q", url)
	}
	if !sawGet {
		t.Fatal("expected a follow-up GET lookup for the existing PR")
	}
}
