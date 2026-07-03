// SPDX-License-Identifier: Elastic-2.0

// provider_gitlab_test.go — TDD tests for gitlabProvider against an httptest mock.
// GitLab's REST API is genuinely different from GitHub/Gitea (different endpoints,
// PRIVATE-TOKEN auth, discussions → CHANGES_REQUESTED synthesis). These tests cover:
//   - PRIVATE-TOKEN auth header on every request
//   - Project path URL-encoding ("o/r" → "o%2Fr")
//   - AuthLogin (GET /user → username field, not login)
//   - ChangeRequestPath() == "merge_requests"
//   - PushCredURL: oauth2: form
//   - OpenPR (POST /merge_requests, web_url response)
//   - OpenPR on 409 conflict: find via GET ?source_branch=...&state=opened
//   - GetPR: state:merged → merged=true
//   - ListReviews: unresolved non-bot → CHANGES_REQUESTED
//   - ListReviews: resolved thread → zero reviews
//   - ListReviews: bot-only thread → zero reviews
//   - ListReviews: pagination across 2 pages → all threads considered
//   - PostComment (POST /notes)
//   - ListReviewComments (GET /notes with position)
//   - TestProviderForGitLabType: registry wires gitlabProvider for type==gitlab
package repo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestGitLabProvider creates a gitlabProvider pointed at the given test server URL.
func newTestGitLabProvider(baseURL, token string) *gitlabProvider {
	return &gitlabProvider{rc: restClient{
		base:    baseURL,
		token:   token,
		authKey: "PRIVATE-TOKEN",
	}}
}

// TestGitLabProviderAgainstMock exercises the full gitlabProvider contract
// against an httptest server replaying GitLab v4 API shapes.
func TestGitLabProviderAgainstMock(t *testing.T) {
	const tok = "gl-secret-token"
	const botUser = "corralai-bot"

	// assertPrivateToken verifies that every request carries the PRIVATE-TOKEN
	// header (GitLab's auth form) rather than Authorization: Bearer.
	assertPrivateToken := func(t *testing.T, r *http.Request) {
		t.Helper()
		if got := r.Header.Get("PRIVATE-TOKEN"); got != tok {
			t.Errorf("PRIVATE-TOKEN = %q, want %q", got, tok)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization header must be absent for GitLab, got %q", got)
		}
	}

	t.Run("AuthLogin", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assertPrivateToken(t, r)
			if r.URL.Path != "/user" {
				t.Errorf("unexpected path %s", r.URL.Path)
				w.WriteHeader(404)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"username": botUser})
		}))
		defer srv.Close()
		p := newTestGitLabProvider(srv.URL, tok)
		login, err := p.AuthLogin(context.Background())
		if err != nil {
			t.Fatalf("AuthLogin: %v", err)
		}
		if login != botUser {
			t.Errorf("login = %q, want %q", login, botUser)
		}
	})

	t.Run("ChangeRequestPath", func(t *testing.T) {
		p := newTestGitLabProvider("http://unused", tok)
		if got := p.ChangeRequestPath(); got != "merge_requests" {
			t.Errorf("ChangeRequestPath = %q, want %q", got, "merge_requests")
		}
	})

	t.Run("PushCredURL_oauth2_form", func(t *testing.T) {
		p := newTestGitLabProvider("http://unused", tok)
		got := p.PushCredURL("https://gitlab.com/o/r.git", "mytoken")
		want := "https://oauth2:mytoken@gitlab.com/o/r.git"
		if got != want {
			t.Errorf("PushCredURL = %q, want %q", got, want)
		}
	})

	t.Run("PushCredURL_empty_token_unchanged", func(t *testing.T) {
		p := newTestGitLabProvider("http://unused", "")
		got := p.PushCredURL("https://gitlab.com/o/r.git", "")
		if got != "https://gitlab.com/o/r.git" {
			t.Errorf("PushCredURL with empty token should be unchanged, got %q", got)
		}
	})

	t.Run("OpenPR", func(t *testing.T) {
		var gotPayload map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assertPrivateToken(t, r)
			// GitLab project path is URL-encoded: "o/r" → path segment "o%2Fr"
			wantPath := "/projects/o%2Fr/merge_requests"
			if r.URL.EscapedPath() != wantPath || r.Method != http.MethodPost {
				t.Errorf("unexpected %s %s (want POST %s)", r.Method, r.URL.EscapedPath(), wantPath)
				w.WriteHeader(404)
				return
			}
			json.NewDecoder(r.Body).Decode(&gotPayload)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"web_url": "https://gitlab.com/o/r/-/merge_requests/1"})
		}))
		defer srv.Close()
		p := newTestGitLabProvider(srv.URL, tok)
		webURL, err := p.OpenPR(context.Background(), "o", "r", "feature", "main", "My MR", "description")
		if err != nil {
			t.Fatalf("OpenPR: %v", err)
		}
		if webURL != "https://gitlab.com/o/r/-/merge_requests/1" {
			t.Errorf("OpenPR url = %q", webURL)
		}
		// Verify GitLab-specific body fields (not head/base as in GitHub)
		if gotPayload["source_branch"] != "feature" {
			t.Errorf("source_branch = %v, want %q", gotPayload["source_branch"], "feature")
		}
		if gotPayload["target_branch"] != "main" {
			t.Errorf("target_branch = %v, want %q", gotPayload["target_branch"], "main")
		}
		if gotPayload["description"] != "description" {
			t.Errorf("description = %v, want %q", gotPayload["description"], "description")
		}
	})

	t.Run("OpenPR_409_already_exists", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assertPrivateToken(t, r)
			switch {
			case r.URL.EscapedPath() == "/projects/o%2Fr/merge_requests" && r.Method == http.MethodPost:
				w.WriteHeader(409)
				json.NewEncoder(w).Encode(map[string]any{"message": "Another open merge request already exists"})
			case r.URL.EscapedPath() == "/projects/o%2Fr/merge_requests" && r.Method == http.MethodGet:
				// find-open fallback: return the existing MR
				json.NewEncoder(w).Encode([]map[string]any{
					{"web_url": "https://gitlab.com/o/r/-/merge_requests/5"},
				})
			default:
				t.Errorf("unexpected %s %s", r.Method, r.URL.EscapedPath())
				w.WriteHeader(404)
			}
		}))
		defer srv.Close()
		p := newTestGitLabProvider(srv.URL, tok)
		webURL, err := p.OpenPR(context.Background(), "o", "r", "feature", "main", "title", "body")
		if err != nil {
			t.Fatalf("OpenPR 409 recovery: %v", err)
		}
		if webURL != "https://gitlab.com/o/r/-/merge_requests/5" {
			t.Errorf("OpenPR 409 url = %q", webURL)
		}
	})

	t.Run("GetPR_merged", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assertPrivateToken(t, r)
			if r.URL.EscapedPath() != "/projects/o%2Fr/merge_requests/7" {
				t.Errorf("unexpected path %s", r.URL.EscapedPath())
				w.WriteHeader(404)
				return
			}
			// GitLab state enum: "merged" → merged=true (no separate .merged field)
			json.NewEncoder(w).Encode(map[string]any{"state": "merged"})
		}))
		defer srv.Close()
		p := newTestGitLabProvider(srv.URL, tok)
		state, merged, err := p.GetPR(context.Background(), "o", "r", 7)
		if err != nil {
			t.Fatalf("GetPR: %v", err)
		}
		if state != "merged" || !merged {
			t.Errorf("GetPR: state=%q merged=%v, want merged/true", state, merged)
		}
	})

	t.Run("GetPR_opened", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assertPrivateToken(t, r)
			json.NewEncoder(w).Encode(map[string]any{"state": "opened"})
		}))
		defer srv.Close()
		p := newTestGitLabProvider(srv.URL, tok)
		state, merged, err := p.GetPR(context.Background(), "o", "r", 3)
		if err != nil {
			t.Fatalf("GetPR opened: %v", err)
		}
		if state != "opened" || merged {
			t.Errorf("GetPR opened: state=%q merged=%v, want opened/false", state, merged)
		}
	})

	// glDiscussionFixture builds a GitLab discussion JSON object for test servers.
	glDiscussion := func(id string, resolvable, resolved bool, author string, notes ...map[string]any) map[string]any {
		return map[string]any{
			"id":         id,
			"resolvable": resolvable,
			"resolved":   resolved,
			"notes":      notes,
		}
	}
	glNote := func(author, body, createdAt string) map[string]any {
		return map[string]any{
			"id":         1,
			"body":       body,
			"created_at": createdAt,
			"system":     false,
			"author":     map[string]any{"username": author},
		}
	}

	// newDiscussionServer creates a test server that serves:
	//   GET /user → {username: botUser}
	//   GET /projects/o%2Fr/merge_requests/{iid}/discussions?... → discussions
	// The discussions function receives the page number and returns the batch.
	newDiscussionServer := func(t *testing.T, iid int, discussionsFn func(page string) []map[string]any) *httptest.Server {
		t.Helper()
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assertPrivateToken(t, r)
			switch {
			case r.URL.Path == "/user":
				json.NewEncoder(w).Encode(map[string]any{"username": botUser})
			case strings.Contains(r.URL.EscapedPath(), "discussions"):
				page := r.URL.Query().Get("page")
				if page == "" {
					page = "1"
				}
				batch := discussionsFn(page)
				json.NewEncoder(w).Encode(batch)
			default:
				t.Errorf("unexpected %s %s", r.Method, r.URL.EscapedPath())
				w.WriteHeader(404)
			}
		}))
	}

	// (a) Unresolved non-bot discussion → one CHANGES_REQUESTED review.
	t.Run("ListReviews_unresolved_nonbot_yields_CHANGES_REQUESTED", func(t *testing.T) {
		disc := glDiscussion("d1", true, false, "alice",
			glNote("alice", "please fix this", "2024-06-01T10:00:00Z"),
		)
		srv := newDiscussionServer(t, 7, func(page string) []map[string]any {
			if page == "1" {
				return []map[string]any{disc}
			}
			return nil
		})
		defer srv.Close()
		p := newTestGitLabProvider(srv.URL, tok)
		reviews, newEtag, notMod, err := p.ListReviews(context.Background(), "o", "r", 7, "")
		if err != nil {
			t.Fatalf("ListReviews: %v", err)
		}
		if notMod {
			t.Error("ListReviews: notModified must always be false for GitLab")
		}
		if newEtag != "" {
			t.Errorf("newEtag = %q, want empty (GitLab discussions unsupported)", newEtag)
		}
		if len(reviews) != 1 {
			t.Fatalf("len(reviews) = %d, want 1", len(reviews))
		}
		if reviews[0].State != "CHANGES_REQUESTED" {
			t.Errorf("review.State = %q, want CHANGES_REQUESTED", reviews[0].State)
		}
		if reviews[0].User != "alice" {
			t.Errorf("review.User = %q, want alice", reviews[0].User)
		}
		if reviews[0].Body != "please fix this" {
			t.Errorf("review.Body = %q", reviews[0].Body)
		}
		if reviews[0].SubmittedAt != "2024-06-01T10:00:00Z" {
			t.Errorf("review.SubmittedAt = %q", reviews[0].SubmittedAt)
		}
	})

	// (b) Resolved thread → zero reviews.
	t.Run("ListReviews_resolved_thread_yields_zero", func(t *testing.T) {
		disc := glDiscussion("d2", true, true, "alice", // resolved=true
			glNote("alice", "please fix this", "2024-06-01T10:00:00Z"),
		)
		srv := newDiscussionServer(t, 7, func(page string) []map[string]any {
			if page == "1" {
				return []map[string]any{disc}
			}
			return nil
		})
		defer srv.Close()
		p := newTestGitLabProvider(srv.URL, tok)
		reviews, _, _, err := p.ListReviews(context.Background(), "o", "r", 7, "")
		if err != nil {
			t.Fatalf("ListReviews: %v", err)
		}
		if len(reviews) != 0 {
			t.Errorf("len(reviews) = %d, want 0 (resolved thread excluded)", len(reviews))
		}
	})

	// (c) Bot-only discussion → zero reviews.
	t.Run("ListReviews_bot_discussion_yields_zero", func(t *testing.T) {
		disc := glDiscussion("d3", true, false, botUser, // authored by the bot itself
			glNote(botUser, "I posted this myself", "2024-06-01T10:00:00Z"),
		)
		srv := newDiscussionServer(t, 7, func(page string) []map[string]any {
			if page == "1" {
				return []map[string]any{disc}
			}
			return nil
		})
		defer srv.Close()
		p := newTestGitLabProvider(srv.URL, tok)
		reviews, _, _, err := p.ListReviews(context.Background(), "o", "r", 7, "")
		if err != nil {
			t.Fatalf("ListReviews: %v", err)
		}
		if len(reviews) != 0 {
			t.Errorf("len(reviews) = %d, want 0 (bot discussion excluded)", len(reviews))
		}
	})

	// (d) Pagination across 2 pages → all threads considered.
	// Page 1: 1 unresolved non-bot thread; Page 2: 1 unresolved non-bot thread;
	// Page 3: empty → stop. Expect 2 CHANGES_REQUESTED reviews.
	t.Run("ListReviews_pagination_two_pages", func(t *testing.T) {
		page1 := []map[string]any{
			glDiscussion("p1d1", true, false, "alice",
				glNote("alice", "fix p1", "2024-06-01T10:00:00Z"),
			),
		}
		page2 := []map[string]any{
			glDiscussion("p2d1", true, false, "bob",
				glNote("bob", "fix p2", "2024-06-02T10:00:00Z"),
			),
		}
		srv := newDiscussionServer(t, 7, func(page string) []map[string]any {
			switch page {
			case "1":
				return page1
			case "2":
				return page2
			default:
				return nil // empty → stop
			}
		})
		defer srv.Close()
		p := newTestGitLabProvider(srv.URL, tok)
		reviews, _, _, err := p.ListReviews(context.Background(), "o", "r", 7, "")
		if err != nil {
			t.Fatalf("ListReviews pagination: %v", err)
		}
		if len(reviews) != 2 {
			t.Fatalf("len(reviews) = %d, want 2 (one from each page)", len(reviews))
		}
		// Verify both users are represented
		users := map[string]bool{reviews[0].User: true, reviews[1].User: true}
		if !users["alice"] || !users["bob"] {
			t.Errorf("expected alice+bob reviews, got %v", users)
		}
		for _, rv := range reviews {
			if rv.State != "CHANGES_REQUESTED" {
				t.Errorf("review.State = %q, want CHANGES_REQUESTED", rv.State)
			}
		}
	})

	// SubmittedAt is the LATEST note's created_at (not the first).
	t.Run("ListReviews_submittedAt_is_latest_note", func(t *testing.T) {
		disc := glDiscussion("d5", true, false, "alice",
			glNote("alice", "first note", "2024-06-01T08:00:00Z"),
			glNote("alice", "follow-up note", "2024-06-01T12:00:00Z"), // later
		)
		srv := newDiscussionServer(t, 7, func(page string) []map[string]any {
			if page == "1" {
				return []map[string]any{disc}
			}
			return nil
		})
		defer srv.Close()
		p := newTestGitLabProvider(srv.URL, tok)
		reviews, _, _, err := p.ListReviews(context.Background(), "o", "r", 7, "")
		if err != nil {
			t.Fatalf("ListReviews: %v", err)
		}
		if len(reviews) != 1 {
			t.Fatalf("len(reviews) = %d, want 1", len(reviews))
		}
		if reviews[0].SubmittedAt != "2024-06-01T12:00:00Z" {
			t.Errorf("SubmittedAt = %q, want latest note time", reviews[0].SubmittedAt)
		}
		if reviews[0].Body != "follow-up note" {
			t.Errorf("Body = %q, want latest note body", reviews[0].Body)
		}
	})

	t.Run("PostComment", func(t *testing.T) {
		var gotBody string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assertPrivateToken(t, r)
			wantPath := "/projects/o%2Fr/merge_requests/7/notes"
			if r.URL.EscapedPath() != wantPath || r.Method != http.MethodPost {
				t.Errorf("unexpected %s %s (want POST %s)", r.Method, r.URL.EscapedPath(), wantPath)
				w.WriteHeader(404)
				return
			}
			var payload map[string]any
			json.NewDecoder(r.Body).Decode(&payload)
			gotBody = payload["body"].(string)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"id": 42})
		}))
		defer srv.Close()
		p := newTestGitLabProvider(srv.URL, tok)
		if err := p.PostComment(context.Background(), "o", "r", 7, "hello from gitlab"); err != nil {
			t.Fatalf("PostComment: %v", err)
		}
		if gotBody != "hello from gitlab" {
			t.Errorf("PostComment body = %q, want %q", gotBody, "hello from gitlab")
		}
	})

	t.Run("ListReviewComments", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assertPrivateToken(t, r)
			wantPath := "/projects/o%2Fr/merge_requests/7/notes"
			if r.URL.EscapedPath() != wantPath {
				t.Errorf("unexpected path %s (want %s)", r.URL.EscapedPath(), wantPath)
				w.WriteHeader(404)
				return
			}
			json.NewEncoder(w).Encode([]map[string]any{
				{
					"body":   "nit: rename this",
					"system": false,
					"author": map[string]any{"username": "carol"},
					"position": map[string]any{
						"new_path": "pkg/foo.go",
						"new_line": 42,
					},
				},
				{
					// system notes must be excluded
					"body":     "pushed 2 commits",
					"system":   true,
					"author":   map[string]any{"username": "system"},
					"position": nil,
				},
			})
		}))
		defer srv.Close()
		p := newTestGitLabProvider(srv.URL, tok)
		comments, err := p.ListReviewComments(context.Background(), "o", "r", 7)
		if err != nil {
			t.Fatalf("ListReviewComments: %v", err)
		}
		if len(comments) != 1 {
			t.Fatalf("len(comments) = %d, want 1 (system note excluded)", len(comments))
		}
		c := comments[0]
		if c.Path != "pkg/foo.go" || c.Line != 42 || c.Body != "nit: rename this" || c.User != "carol" {
			t.Errorf("comment = %+v", c)
		}
	})

	// Mixed discussion set: one qualifying + one resolved + one bot's own →
	// only the qualifying one becomes a review.
	t.Run("ListReviews_mixed_filters", func(t *testing.T) {
		discs := []map[string]any{
			glDiscussion("qualifying", true, false, "alice", glNote("alice", "fix me", "2024-06-01T10:00:00Z")),
			glDiscussion("resolved", true, true, "bob", glNote("bob", "already done", "2024-06-01T09:00:00Z")),
			glDiscussion("bot-own", true, false, botUser, glNote(botUser, "I said this", "2024-06-01T08:00:00Z")),
			glDiscussion("system-non-resolvable", false, false, "system", glNote("system", "pushed 1 commit", "2024-06-01T07:00:00Z")),
		}
		srv := newDiscussionServer(t, 9, func(page string) []map[string]any {
			if page == "1" {
				return discs
			}
			return nil
		})
		defer srv.Close()
		p := newTestGitLabProvider(srv.URL, tok)
		reviews, _, _, err := p.ListReviews(context.Background(), "o", "r", 9, "")
		if err != nil {
			t.Fatalf("ListReviews mixed: %v", err)
		}
		if len(reviews) != 1 {
			t.Fatalf("len(reviews) = %d, want 1 (only the qualifying discussion)", len(reviews))
		}
		if reviews[0].User != "alice" || reviews[0].State != "CHANGES_REQUESTED" {
			t.Errorf("review = %+v, want alice/CHANGES_REQUESTED", reviews[0])
		}
	})
}

// TestProviderForGitLabType verifies that providerFor constructs a gitlabProvider
// for type=="gitlab" with the correct auth key and ChangeRequestPath.
func TestProviderForGitLabType(t *testing.T) {
	e := NewWithForges("primary-tok", map[string]ForgeConfig{
		"gitlab.example.com": {Type: "gitlab", APIBase: "https://gitlab.example.com/api/v4", Token: "GITLABTOK"},
	})

	p, err := e.providerFor("gitlab.example.com")
	if err != nil {
		t.Fatalf("providerFor gitlab.example.com: %v", err)
	}
	gp, ok := p.(*gitlabProvider)
	if !ok {
		t.Fatalf("expected *gitlabProvider, got %T", p)
	}
	if gp.rc.authKey != "PRIVATE-TOKEN" {
		t.Errorf("gitlabProvider authKey = %q, want %q", gp.rc.authKey, "PRIVATE-TOKEN")
	}
	if gp.rc.authScheme != "" {
		t.Errorf("gitlabProvider authScheme = %q, want empty", gp.rc.authScheme)
	}
	if gp.rc.accept != "" {
		t.Errorf("gitlabProvider accept = %q, want empty (no vendor header)", gp.rc.accept)
	}
	if gp.ChangeRequestPath() != "merge_requests" {
		t.Errorf("ChangeRequestPath = %q, want %q", gp.ChangeRequestPath(), "merge_requests")
	}
}

// TestProviderForGitLabComDefault verifies that gitlab.com is pre-populated
// in the default forge registry and returns a gitlabProvider.
func TestProviderForGitLabComDefault(t *testing.T) {
	forges := ForgesFromEnv()
	cfg, ok := forges["gitlab.com"]
	if !ok {
		t.Fatal("gitlab.com not in default ForgesFromEnv registry")
	}
	if cfg.Type != "gitlab" {
		t.Errorf("gitlab.com type = %q, want gitlab", cfg.Type)
	}
	if cfg.APIBase != "https://gitlab.com/api/v4" {
		t.Errorf("gitlab.com APIBase = %q, want https://gitlab.com/api/v4", cfg.APIBase)
	}
}

// TestGitLabPushCredURLDRY verifies that github, gitea, and gitlab all use the
// shared pushCredURL helper — correct prefix per forge, no duplicate logic.
func TestGitLabPushCredURLDRY(t *testing.T) {
	cases := []struct {
		name    string
		p       Provider
		repoURL string
		wantPfx string
	}{
		{
			name:    "github_x-access-token",
			p:       &githubProvider{rc: restClient{}},
			repoURL: "https://github.com/o/r.git",
			wantPfx: "https://x-access-token:tok@github.com/o/r.git",
		},
		{
			name:    "gitea_x-access-token",
			p:       &giteaProvider{rc: restClient{}},
			repoURL: "https://gitea.example.com/o/r.git",
			wantPfx: "https://x-access-token:tok@gitea.example.com/o/r.git",
		},
		{
			name:    "gitlab_oauth2",
			p:       &gitlabProvider{rc: restClient{}},
			repoURL: "https://gitlab.com/o/r.git",
			wantPfx: "https://oauth2:tok@gitlab.com/o/r.git",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.p.PushCredURL(tc.repoURL, "tok")
			if got != tc.wantPfx {
				t.Errorf("PushCredURL = %q, want %q", got, tc.wantPfx)
			}
		})
	}
}
