// SPDX-License-Identifier: Elastic-2.0

// provider_gitea_test.go — TDD tests for giteaProvider against an httptest mock.
// Gitea's REST API mirrors GitHub's endpoints/JSON, so the test shapes are
// nearly identical to the GitHub tests, with two key differences:
//   - Auth header: "Authorization: token <tok>" (Gitea's native form)
//   - Review state: Gitea returns "REQUEST_CHANGES"; provider normalizes to "CHANGES_REQUESTED"
package repo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestGiteaProvider creates a giteaProvider pointed at the given test server URL.
func newTestGiteaProvider(baseURL, token string) *giteaProvider {
	return &giteaProvider{rc: restClient{
		base:       baseURL,
		token:      token,
		accept:     "", // Gitea does not require a vendor Accept header
		authScheme: "token",
	}}
}

// TestGiteaProviderAgainstMock exercises the full giteaProvider contract:
// OpenPR, ListReviews (with state normalization), GetPR, PostComment, AuthLogin,
// ListReviewComments. Asserts "Authorization: token <tok>" on every request.
func TestGiteaProviderAgainstMock(t *testing.T) {
	const tok = "gitea-secret"
	assertAuth := func(t *testing.T, r *http.Request) {
		t.Helper()
		want := "token " + tok
		got := r.Header.Get("Authorization")
		if got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
	}

	t.Run("AuthLogin", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assertAuth(t, r)
			if r.URL.Path != "/user" {
				t.Errorf("unexpected path %s", r.URL.Path)
				w.WriteHeader(404)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"login": "gitea-bot"})
		}))
		defer srv.Close()
		p := newTestGiteaProvider(srv.URL, tok)
		login, err := p.AuthLogin(context.Background())
		if err != nil {
			t.Fatalf("AuthLogin: %v", err)
		}
		if login != "gitea-bot" {
			t.Errorf("login = %q, want %q", login, "gitea-bot")
		}
	})

	t.Run("ChangeRequestPath", func(t *testing.T) {
		p := newTestGiteaProvider("http://unused", tok)
		if got := p.ChangeRequestPath(); got != "pull" {
			t.Errorf("ChangeRequestPath = %q, want %q", got, "pull")
		}
	})

	t.Run("PushCredURL_injects_x-access-token", func(t *testing.T) {
		p := newTestGiteaProvider("http://unused", tok)
		got := p.PushCredURL("https://gitea.example.com/o/r.git", "mytoken")
		want := "https://x-access-token:mytoken@gitea.example.com/o/r.git"
		if got != want {
			t.Errorf("PushCredURL = %q, want %q", got, want)
		}
	})

	t.Run("PushCredURL_empty_token_unchanged", func(t *testing.T) {
		p := newTestGiteaProvider("http://unused", "")
		got := p.PushCredURL("https://gitea.example.com/o/r.git", "")
		if got != "https://gitea.example.com/o/r.git" {
			t.Errorf("PushCredURL with empty token should be unchanged, got %q", got)
		}
	})

	t.Run("OpenPR", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assertAuth(t, r)
			if r.URL.Path == "/repos/o/r/pulls" && r.Method == http.MethodPost {
				w.WriteHeader(http.StatusCreated)
				json.NewEncoder(w).Encode(map[string]any{"html_url": "https://gitea.example.com/o/r/pulls/3"})
				return
			}
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}))
		defer srv.Close()
		p := newTestGiteaProvider(srv.URL, tok)
		url, err := p.OpenPR(context.Background(), "o", "r", "feature", "main", "title", "body")
		if err != nil {
			t.Fatalf("OpenPR: %v", err)
		}
		if url != "https://gitea.example.com/o/r/pulls/3" {
			t.Errorf("OpenPR url = %q", url)
		}
	})

	t.Run("OpenPR_422_already_exists", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assertAuth(t, r)
			switch {
			case r.URL.Path == "/repos/o/r/pulls" && r.Method == http.MethodPost:
				w.WriteHeader(422)
				json.NewEncoder(w).Encode(map[string]any{"message": "pull request already exists"})
			case r.URL.Path == "/repos/o/r/pulls" && r.Method == http.MethodGet:
				json.NewEncoder(w).Encode([]map[string]any{
					{"html_url": "https://gitea.example.com/o/r/pulls/1"},
				})
			default:
				t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
				w.WriteHeader(404)
			}
		}))
		defer srv.Close()
		p := newTestGiteaProvider(srv.URL, tok)
		url, err := p.OpenPR(context.Background(), "o", "r", "feature", "main", "title", "body")
		if err != nil {
			t.Fatalf("OpenPR 422 recovery: %v", err)
		}
		if url != "https://gitea.example.com/o/r/pulls/1" {
			t.Errorf("OpenPR 422 url = %q", url)
		}
	})

	// ListReviews: Gitea sends "REQUEST_CHANGES" where GitHub sends "CHANGES_REQUESTED".
	// The provider must normalize to "CHANGES_REQUESTED" so the engine's loop triggers correctly.
	t.Run("ListReviews_normalizes_REQUEST_CHANGES", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assertAuth(t, r)
			if r.URL.Path != "/repos/o/r/pulls/5/reviews" {
				t.Errorf("unexpected path %s", r.URL.Path)
				w.WriteHeader(404)
				return
			}
			json.NewEncoder(w).Encode([]map[string]any{
				{
					"id":           101,
					"state":        "REQUEST_CHANGES", // Gitea's enum value
					"body":         "needs work",
					"submitted_at": "2024-01-01T12:00:00Z",
					"user":         map[string]any{"login": "alice"},
				},
				{
					"id":           102,
					"state":        "APPROVED",
					"body":         "",
					"submitted_at": "2024-01-02T12:00:00Z",
					"user":         map[string]any{"login": "bob"},
				},
			})
		}))
		defer srv.Close()
		p := newTestGiteaProvider(srv.URL, tok)
		reviews, _, notMod, err := p.ListReviews(context.Background(), "o", "r", 5, "")
		if err != nil {
			t.Fatalf("ListReviews: %v", err)
		}
		if notMod {
			t.Fatal("ListReviews: unexpected notModified")
		}
		if len(reviews) != 2 {
			t.Fatalf("len(reviews) = %d, want 2", len(reviews))
		}
		// REQUEST_CHANGES → CHANGES_REQUESTED normalization
		if reviews[0].State != "CHANGES_REQUESTED" {
			t.Errorf("review[0].State = %q, want CHANGES_REQUESTED (normalized from REQUEST_CHANGES)", reviews[0].State)
		}
		if reviews[0].User != "alice" || reviews[0].Body != "needs work" {
			t.Errorf("review[0] fields wrong: %+v", reviews[0])
		}
		// APPROVED stays unchanged
		if reviews[1].State != "APPROVED" {
			t.Errorf("review[1].State = %q, want APPROVED", reviews[1].State)
		}
	})

	t.Run("ListReviews_etag_304", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assertAuth(t, r)
			if r.Header.Get("If-None-Match") == `"etag123"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.WriteHeader(500)
		}))
		defer srv.Close()
		p := newTestGiteaProvider(srv.URL, tok)
		_, retEtag, notMod, err := p.ListReviews(context.Background(), "o", "r", 5, `"etag123"`)
		if err != nil {
			t.Fatalf("ListReviews 304: %v", err)
		}
		if !notMod {
			t.Error("ListReviews 304: expected notModified=true")
		}
		if retEtag != `"etag123"` {
			t.Errorf("ListReviews 304: etag = %q, want %q", retEtag, `"etag123"`)
		}
	})

	t.Run("ListReviewComments", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assertAuth(t, r)
			if r.URL.Path != "/repos/o/r/pulls/5/comments" {
				t.Errorf("unexpected path %s", r.URL.Path)
				w.WriteHeader(404)
				return
			}
			json.NewEncoder(w).Encode([]map[string]any{
				{"path": "foo.go", "line": 10, "body": "nit", "user": map[string]any{"login": "carol"}},
			})
		}))
		defer srv.Close()
		p := newTestGiteaProvider(srv.URL, tok)
		comments, err := p.ListReviewComments(context.Background(), "o", "r", 5)
		if err != nil {
			t.Fatalf("ListReviewComments: %v", err)
		}
		if len(comments) != 1 {
			t.Fatalf("len(comments) = %d, want 1", len(comments))
		}
		c := comments[0]
		if c.Path != "foo.go" || c.Line != 10 || c.Body != "nit" || c.User != "carol" {
			t.Errorf("comment fields wrong: %+v", c)
		}
	})

	t.Run("GetPR", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assertAuth(t, r)
			if r.URL.Path != "/repos/o/r/pulls/5" {
				t.Errorf("unexpected path %s", r.URL.Path)
				w.WriteHeader(404)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"state": "closed", "merged": true})
		}))
		defer srv.Close()
		p := newTestGiteaProvider(srv.URL, tok)
		state, merged, err := p.GetPR(context.Background(), "o", "r", 5)
		if err != nil {
			t.Fatalf("GetPR: %v", err)
		}
		if state != "closed" || !merged {
			t.Errorf("GetPR: state=%q merged=%v, want closed/true", state, merged)
		}
	})

	t.Run("PostComment", func(t *testing.T) {
		var gotBody string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assertAuth(t, r)
			if r.URL.Path != "/repos/o/r/issues/5/comments" || r.Method != http.MethodPost {
				t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
				w.WriteHeader(404)
				return
			}
			var payload map[string]any
			json.NewDecoder(r.Body).Decode(&payload)
			gotBody = payload["body"].(string)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"id": 99})
		}))
		defer srv.Close()
		p := newTestGiteaProvider(srv.URL, tok)
		if err := p.PostComment(context.Background(), "o", "r", 5, "hello from gitea"); err != nil {
			t.Fatalf("PostComment: %v", err)
		}
		if gotBody != "hello from gitea" {
			t.Errorf("PostComment body = %q, want %q", gotBody, "hello from gitea")
		}
	})

	// Verify no GitHub vendor Accept header is sent to Gitea.
	t.Run("NoGitHubAcceptHeader", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.Header.Get("Accept"), "vnd.github") {
				t.Errorf("Gitea request must not include GitHub Accept header, got %q", r.Header.Get("Accept"))
			}
			json.NewEncoder(w).Encode(map[string]any{"login": "bot"})
		}))
		defer srv.Close()
		p := newTestGiteaProvider(srv.URL, tok)
		p.AuthLogin(context.Background()) //nolint:errcheck
	})
}

// TestProviderForGiteaType verifies that providerFor constructs a giteaProvider
// (not a githubProvider) for type=="gitea" and that it has the correct auth scheme.
func TestProviderForGiteaType(t *testing.T) {
	e := NewWithForges("ghtok", map[string]ForgeConfig{
		"gitea.example.com": {Type: "gitea", APIBase: "https://gitea.example.com/api/v1", Token: "GITEATOK"},
	})

	p, err := e.providerFor("gitea.example.com")
	if err != nil {
		t.Fatalf("providerFor gitea.example.com: %v", err)
	}
	gp, ok := p.(*giteaProvider)
	if !ok {
		t.Fatalf("expected *giteaProvider, got %T", p)
	}
	if gp.rc.authScheme != "token" {
		t.Errorf("giteaProvider authScheme = %q, want %q", gp.rc.authScheme, "token")
	}
	if gp.rc.accept != "" {
		t.Errorf("giteaProvider accept = %q, want empty (no vendor header)", gp.rc.accept)
	}
	if gp.ChangeRequestPath() != "pull" {
		t.Errorf("giteaProvider ChangeRequestPath = %q, want %q", gp.ChangeRequestPath(), "pull")
	}
}
