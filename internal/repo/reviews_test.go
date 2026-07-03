// SPDX-License-Identifier: Elastic-2.0

// internal/repo/reviews_test.go — regression tests for GitHub review/comment
// behavior, now exercised through githubProvider directly.
package repo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListReviewsAndConditional(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/repos/o/r/pulls/7/reviews" {
			if r.Header.Get("If-None-Match") == `"etag123"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"etag123"`)
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": 11, "state": "CHANGES_REQUESTED", "body": "fix the naming", "submitted_at": "2026-07-01T10:00:00Z", "user": map[string]any{"login": "alice"}},
			})
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	p := newTestGitHubProvider(srv.URL, "tok")
	revs, etag, nm, err := p.ListReviews(context.Background(), "o", "r", 7, "")
	if err != nil || nm || len(revs) != 1 {
		t.Fatalf("first list: revs=%v nm=%v err=%v", revs, nm, err)
	}
	if revs[0].State != "CHANGES_REQUESTED" || revs[0].User != "alice" || revs[0].ID != 11 {
		t.Fatalf("parsed wrong: %+v", revs[0])
	}
	if etag != `"etag123"` {
		t.Fatalf("etag not captured: %q", etag)
	}
	// second call WITH the etag → 304, no body
	_, _, nm2, err := p.ListReviews(context.Background(), "o", "r", 7, etag)
	if err != nil || !nm2 {
		t.Fatalf("conditional should be not-modified: nm=%v err=%v", nm2, err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 server hits, got %d", calls)
	}
}

func TestGetPRAndPostComment(t *testing.T) {
	var posted string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/o/r/pulls/7":
			json.NewEncoder(w).Encode(map[string]any{"state": "open", "merged": false})
		case r.Method == "POST" && r.URL.Path == "/repos/o/r/issues/7/comments":
			var in map[string]any
			json.NewDecoder(r.Body).Decode(&in)
			posted, _ = in["body"].(string)
			w.WriteHeader(201)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	p := newTestGitHubProvider(srv.URL, "tok")
	state, merged, err := p.GetPR(context.Background(), "o", "r", 7)
	if err != nil || state != "open" || merged {
		t.Fatalf("GetPR: %s %v %v", state, merged, err)
	}
	if err := p.PostComment(context.Background(), "o", "r", 7, "hello"); err != nil {
		t.Fatal(err)
	}
	if posted != "hello" {
		t.Fatalf("comment body = %q", posted)
	}
}
