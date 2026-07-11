// SPDX-License-Identifier: Elastic-2.0

// provider_github_test.go — new tests for the multi-forge foundation:
// host-aware RepoIdent, ForgesFromEnv defaults + back-compat, and the
// githubProvider against an httptest mock (AuthLogin + full happy path).
package repo

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestRepoIdentMultiHost verifies the host-aware package-level RepoIdent
// across all supported URL forms.
func TestRepoIdentMultiHost(t *testing.T) {
	cases := []struct {
		url          string
		wantHost     string
		wantOwner    string
		wantRepo     string
		wantErrMatch string // non-empty → expect an error containing this
	}{
		{"https://github.com/o/r.git", "github.com", "o", "r", ""},
		{"https://github.com/o/r", "github.com", "o", "r", ""},
		{"https://gitlab.com/o/r.git", "gitlab.com", "o", "r", ""},
		{"git@github.com:o/r.git", "github.com", "o", "r", ""},
		{"https://gitea.example.com/o/r", "gitea.example.com", "o", "r", ""},
		{"https://gitea.example.com/o/r.git", "gitea.example.com", "o", "r", ""},
		{"git@gitea.example.com:o/r.git", "gitea.example.com", "o", "r", ""},
		{"https://github.com/pdbethke/corralai.git", "github.com", "pdbethke", "corralai", ""},
		// error cases
		{"not-a-url", "", "", "", "cannot parse"},
		{"https://github.com/onlyone", "", "", "", "cannot parse"},
	}
	for _, tc := range cases {
		host, owner, repo, err := RepoIdent(tc.url)
		if tc.wantErrMatch != "" {
			if err == nil {
				t.Errorf("RepoIdent(%q): expected error containing %q, got nil", tc.url, tc.wantErrMatch)
			}
			continue
		}
		if err != nil {
			t.Errorf("RepoIdent(%q): unexpected error: %v", tc.url, err)
			continue
		}
		if host != tc.wantHost || owner != tc.wantOwner || repo != tc.wantRepo {
			t.Errorf("RepoIdent(%q): got (%q, %q, %q), want (%q, %q, %q)",
				tc.url, host, owner, repo, tc.wantHost, tc.wantOwner, tc.wantRepo)
		}
	}
}

// TestForgesFromEnv verifies defaults, CORRALAI_FORGES parsing, and back-compat
// with CORRALAI_GIT_TOKEN + CORRALAI_GITHUB_API.
func TestForgesFromEnv(t *testing.T) {
	// Unset all forge-related env vars before each sub-test.
	clearForgeEnv := func() {
		os.Unsetenv("CORRALAI_GIT_TOKEN")
		os.Unsetenv("CORRALAI_GITHUB_API")
		os.Unsetenv("CORRALAI_FORGES")
	}

	t.Run("defaults", func(t *testing.T) {
		clearForgeEnv()
		forges := ForgesFromEnv()
		gh, ok := forges["github.com"]
		if !ok {
			t.Fatal("github.com must be present in defaults")
		}
		if gh.Type != "github" {
			t.Errorf("github.com type = %q, want %q", gh.Type, "github")
		}
		if gh.APIBase != "https://api.github.com" {
			t.Errorf("github.com apiBase = %q, want https://api.github.com", gh.APIBase)
		}
		if gh.Token != "" {
			t.Errorf("github.com token should be empty by default, got %q", gh.Token)
		}
		gl, ok := forges["gitlab.com"]
		if !ok {
			t.Fatal("gitlab.com must be present in defaults")
		}
		if gl.Type != "gitlab" || gl.APIBase != "https://gitlab.com/api/v4" {
			t.Errorf("gitlab.com config wrong: %+v", gl)
		}
	})

	t.Run("back_compat_token_and_api", func(t *testing.T) {
		clearForgeEnv()
		os.Setenv("CORRALAI_GIT_TOKEN", "mytoken")
		os.Setenv("CORRALAI_GITHUB_API", "https://ghes.example.com/api/v3")
		defer clearForgeEnv()
		forges := ForgesFromEnv()
		gh := forges["github.com"]
		if gh.Token != "mytoken" {
			t.Errorf("token = %q, want %q", gh.Token, "mytoken")
		}
		if gh.APIBase != "https://ghes.example.com/api/v3" {
			t.Errorf("apiBase = %q, want custom URL", gh.APIBase)
		}
	})

	t.Run("CORRALAI_FORGES_adds_host", func(t *testing.T) {
		clearForgeEnv()
		os.Setenv("CORRALAI_FORGES", "gitea.example.com=gitea,https://gitea.example.com/api/v1,TOK")
		defer clearForgeEnv()
		forges := ForgesFromEnv()
		gt, ok := forges["gitea.example.com"]
		if !ok {
			t.Fatal("gitea.example.com should be added by CORRALAI_FORGES")
		}
		if gt.Type != "gitea" || gt.APIBase != "https://gitea.example.com/api/v1" || gt.Token != "TOK" {
			t.Errorf("gitea config wrong: %+v", gt)
		}
		// Default hosts must still be present.
		if _, ok := forges["github.com"]; !ok {
			t.Error("github.com default must still be present after CORRALAI_FORGES")
		}
	})

	t.Run("CORRALAI_FORGES_multiple_entries", func(t *testing.T) {
		clearForgeEnv()
		os.Setenv("CORRALAI_FORGES", "a.example.com=gitea,https://a.example.com/api/v1,tokA;b.example.com=github,https://b.example.com/api/v3,tokB")
		defer clearForgeEnv()
		forges := ForgesFromEnv()
		if forges["a.example.com"].Token != "tokA" {
			t.Errorf("a.example.com token wrong: %+v", forges["a.example.com"])
		}
		if forges["b.example.com"].Token != "tokB" {
			t.Errorf("b.example.com token wrong: %+v", forges["b.example.com"])
		}
	})
}

// TestGitHubProviderAuthLogin exercises the AuthLogin method against an httptest
// server, completing the "TestGitHubProviderAgainstMock" contract from the brief.
func TestGitHubProviderAuthLogin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" {
			t.Errorf("AuthLogin: unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		if r.Header.Get("Authorization") != "Bearer mytoken" {
			t.Errorf("AuthLogin: bad auth header %q", r.Header.Get("Authorization"))
		}
		json.NewEncoder(w).Encode(map[string]any{"login": "corralai-bot"})
	}))
	defer srv.Close()
	p := newTestGitHubProvider(srv.URL, "mytoken")
	login, err := p.AuthLogin(context.Background())
	if err != nil {
		t.Fatalf("AuthLogin: %v", err)
	}
	if login != "corralai-bot" {
		t.Fatalf("login = %q, want %q", login, "corralai-bot")
	}
}

// TestGitHubProviderListReviewComments exercises inline comment parsing.
func TestGitHubProviderListReviewComments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/pulls/7/comments" {
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode([]map[string]any{
			{"path": "main.go", "line": 42, "body": "fix this", "user": map[string]any{"login": "alice"}},
		})
	}))
	defer srv.Close()
	p := newTestGitHubProvider(srv.URL, "tok")
	comments, err := p.ListReviewComments(context.Background(), "o", "r", 7)
	if err != nil {
		t.Fatalf("ListReviewComments: %v", err)
	}
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
	c := comments[0]
	if c.Path != "main.go" || c.Line != 42 || c.Body != "fix this" || c.User != "alice" {
		t.Errorf("comment fields wrong: %+v", c)
	}
}

// TestProviderForHostSelection verifies Engine.providerFor returns the correct
// provider type and errors for unknown/unimplemented hosts.
func TestProviderForHostSelection(t *testing.T) {
	e := New("tok", "https://api.github.com")

	// github.com → githubProvider, no error
	p, err := e.providerFor("github.com")
	if err != nil {
		t.Fatalf("providerFor github.com: %v", err)
	}
	if p == nil {
		t.Fatal("providerFor github.com: nil provider")
	}
	if p.ChangeRequestPath() != "pull" {
		t.Errorf("github ChangeRequestPath = %q, want %q", p.ChangeRequestPath(), "pull")
	}

	// Unknown host → error
	_, err = e.providerFor("unknown.example.com")
	if err == nil {
		t.Error("providerFor unknown host should return an error")
	}
}

// TestAuthLoginForgeRouted verifies that Engine.AuthLogin routes to the forge
// that hosts the given repoURL — a gitea-host mission must hit the gitea /user
// endpoint, NOT github's. This is the per-forge bot-identity fix: the review
// self-filter must compare against the correct forge's bot login.
func TestAuthLoginForgeRouted(t *testing.T) {
	var githubHits, giteaHits int
	githubSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user" {
			githubHits++
			json.NewEncoder(w).Encode(map[string]any{"login": "github-bot"})
			return
		}
		w.WriteHeader(404)
	}))
	defer githubSrv.Close()
	giteaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user" {
			giteaHits++
			json.NewEncoder(w).Encode(map[string]any{"login": "gitea-bot"})
			return
		}
		w.WriteHeader(404)
	}))
	defer giteaSrv.Close()

	e := NewWithForges("ghtok", map[string]ForgeConfig{
		"github.com":        {Type: "github", APIBase: githubSrv.URL, Token: "ghtok"},
		"gitea.example.com": {Type: "gitea", APIBase: giteaSrv.URL, Token: "giteatok"},
	})

	// A gitea-host mission must resolve its bot login via the gitea server.
	login, err := e.AuthLogin(context.Background(), "https://gitea.example.com/o/r")
	if err != nil {
		t.Fatalf("AuthLogin gitea: %v", err)
	}
	if login != "gitea-bot" {
		t.Errorf("gitea AuthLogin login = %q, want %q", login, "gitea-bot")
	}
	if giteaHits != 1 {
		t.Errorf("gitea /user hits = %d, want 1", giteaHits)
	}
	if githubHits != 0 {
		t.Errorf("github /user must NOT be hit for a gitea mission, got %d hits", githubHits)
	}

	// A github-host mission still resolves via github.
	login, err = e.AuthLogin(context.Background(), "https://github.com/o/r")
	if err != nil {
		t.Fatalf("AuthLogin github: %v", err)
	}
	if login != "github-bot" {
		t.Errorf("github AuthLogin login = %q, want %q", login, "github-bot")
	}
	if githubHits != 1 {
		t.Errorf("github /user hits = %d, want 1", githubHits)
	}
}

// TestPushCredRegistryStrict verifies the credential boundary: tokenURL injects
// ONLY the host's OWN registry token (in the provider-owned format), and NEVER
// falls back to another host's token. A foreign host with no token must not get
// the github PAT injected.
func TestPushCredRegistryStrict(t *testing.T) {
	e := NewWithForges("GHTOKEN", map[string]ForgeConfig{
		"github.com":        {Type: "github", APIBase: "https://api.github.com", Token: "GHTOKEN"},
		"gitea.example.com": {Type: "gitea", APIBase: "https://gitea.example.com/api/v1", Token: "GITEATOKEN"},
		// A host in the registry with NO token (e.g. gitlab.com default entry).
		"notoken.example.com": {Type: "gitea", APIBase: "https://notoken.example.com/api/v1", Token: ""},
	})

	// Own host, own token: injected in x-access-token form.
	got := e.tokenURL("https://github.com/o/r.git")
	if got != "https://x-access-token:GHTOKEN@github.com/o/r.git" {
		t.Errorf("github push URL = %q, want x-access-token:GHTOKEN form", got)
	}

	// Foreign host WITH its own token: gets THAT token, never github's.
	got = e.tokenURL("https://gitea.example.com/o/r.git")
	if !strings.Contains(got, "GITEATOKEN") {
		t.Errorf("gitea push URL should carry its own token: %q", got)
	}
	if strings.Contains(got, "GHTOKEN") {
		t.Errorf("CREDENTIAL LEAK: gitea push URL must NOT contain the github token: %q", got)
	}

	// Foreign host with NO token: URL unchanged, github's PAT NEVER injected.
	got = e.tokenURL("https://notoken.example.com/o/r.git")
	if got != "https://notoken.example.com/o/r.git" {
		t.Errorf("no-token host URL should be unchanged, got %q", got)
	}
	if strings.Contains(got, "GHTOKEN") {
		t.Errorf("CREDENTIAL LEAK: no-token host must NOT get the github token: %q", got)
	}

	// A host entirely absent from the registry: no injection either.
	got = e.tokenURL("https://unknown.example.com/o/r.git")
	if strings.Contains(got, "GHTOKEN") || strings.Contains(got, "GITEATOKEN") {
		t.Errorf("CREDENTIAL LEAK: unknown host must not get any token: %q", got)
	}
}

// TestEngineOpenPRDelegatesViaForgeRegistry verifies that Engine.OpenPR (which
// takes a repoURL) correctly routes through the registry to a githubProvider
// pointed at the test server.
func TestEngineOpenPRDelegatesViaForgeRegistry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/o/r/pulls" && r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"html_url": "https://github.com/o/r/pull/7"})
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	// Engine with the test server as the github.com API base.
	e := New("tok", srv.URL)
	url, err := e.OpenPR(context.Background(), "https://github.com/o/r", "corralai/m1", "main", "T", "B")
	if err != nil {
		t.Fatalf("Engine.OpenPR via registry: %v", err)
	}
	if url != "https://github.com/o/r/pull/7" {
		t.Fatalf("url = %q, want https://github.com/o/r/pull/7", url)
	}
}

// TestGithubSetCommitStatus verifies SetCommitStatus posts the correct
// payload to the GitHub commit-status endpoint.
func TestGithubSetCommitStatus(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.Method + " " + r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(201)
	}))
	defer srv.Close()
	p := &githubProvider{rc: restClient{base: srv.URL, accept: "application/vnd.github+json"}}
	if err := p.SetCommitStatus(context.Background(), "o", "r", "deadbeef", "corral/gate", "success", "http://x", "passed"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "POST /repos/o/r/statuses/deadbeef" {
		t.Errorf("path = %q", gotPath)
	}
	for _, want := range []string{`"state":"success"`, `"context":"corral/gate"`, `"target_url":"http://x"`, `"description":"passed"`} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("body %q missing %q", gotBody, want)
		}
	}
}
