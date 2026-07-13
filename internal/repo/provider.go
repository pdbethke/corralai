// SPDX-License-Identifier: Elastic-2.0

// provider.go defines the forge-agnostic Provider interface and the shared
// restClient HTTP helper used by all concrete provider implementations.
package repo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/netguard"
)

// forgeGuard defends every dial forgeHTTPClient makes against SSRF: by
// default it blocks loopback/private (RFC1918, ULA)/link-local (incl. the
// cloud metadata address 169.254.169.254) targets. Because CheckRedirect only
// strips auth headers on a cross-host hop — it never refuses the dial — a
// forge base URL or a forge-side (open) redirect could otherwise reach an
// internal address unguarded. An operator can sanction a self-hosted forge on
// a private network via CORRALAI_FORGE_ALLOWED_HOSTS (comma-separated
// hostnames/IPs). It's a package var (not inlined into forgeHTTPClient's
// initializer) so tests can substitute an allowlist that covers the httptest
// loopback servers they spin up.
var forgeGuard = netguard.NewGuard(splitForgeAllowlist(os.Getenv("CORRALAI_FORGE_ALLOWED_HOSTS")))

// splitForgeAllowlist parses a comma-separated env value into trimmed,
// non-empty items. (cmd/corral has an equivalent splitList, but that's
// package main and not importable here.)
func splitForgeAllowlist(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// forgeHTTPClient is the shared client for forge REST calls. It bounds request
// time, dials through forgeGuard (SSRF defense — see above), and on a redirect
// that crosses to a different host, strips the auth headers Go doesn't
// (notably GitLab's custom PRIVATE-TOKEN) so a forge redirect or open-redirect
// can't exfiltrate the token.
var forgeHTTPClient = &http.Client{
	Timeout:   30 * time.Second,
	Transport: forgeTransport(),
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		if len(via) > 0 && req.URL.Host != via[0].URL.Host {
			req.Header.Del("PRIVATE-TOKEN")
			req.Header.Del("Authorization")
		}
		return nil
	},
}

// forgeTransport clones http.DefaultTransport and overrides ONLY its DialContext
// with the SSRF guard. Cloning (rather than a bare &http.Transport{}) preserves
// Proxy: http.ProxyFromEnvironment, HTTP/2 (ForceAttemptHTTP2), and the tuned
// sub-timeouts (TLSHandshakeTimeout, ExpectContinueTimeout, IdleConnTimeout) a
// bare Transport silently dropped (F3). The guard still applies on every dial
// when no proxy is set — DialContext reads forgeGuard afresh each call so it
// re-validates each hop of a redirect chain and a test can swap forgeGuard after
// this var is initialized.
func forgeTransport() *http.Transport {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return forgeGuard.DialContext(ctx, network, addr)
	}
	return tr
}

// Provider is the forge-specific REST surface. One implementation per forge
// type (github, gitea, gitlab). The Engine selects the right Provider for a
// repo URL using the host-keyed ForgeConfig registry, then delegates.
type Provider interface {
	// OpenPR creates a change request. Returns the web URL of the new (or
	// existing, when 422 "already exists") CR. Idempotent.
	OpenPR(ctx context.Context, owner, repo, head, base, title, body string) (string, error)

	// ListReviews returns the reviews for a change request. When etag is
	// non-empty an If-None-Match header is sent; 304 → notModified=true.
	ListReviews(ctx context.Context, owner, repo string, cr int, etag string) (reviews []Review, newEtag string, notModified bool, err error)

	// ListReviewComments returns the inline review comments for a CR.
	ListReviewComments(ctx context.Context, owner, repo string, cr int) ([]ReviewComment, error)

	// GetPR returns the state and merged flag for a CR.
	GetPR(ctx context.Context, owner, repo string, cr int) (state string, merged bool, err error)

	// PostComment posts a comment on a CR (uses the issues/comments endpoint
	// on GitHub, the notes endpoint on GitLab, etc.).
	PostComment(ctx context.Context, owner, repo string, cr int, body string) error

	// AuthLogin returns the authenticated user's login name.
	AuthLogin(ctx context.Context) (string, error)

	// ListOpenPRs returns open change requests targeting base (all bases if base == "").
	ListOpenPRs(ctx context.Context, owner, repo, base string) ([]PRRef, error)
	// SetCommitStatus posts a commit status to sha. state ∈ {"pending","success","failure","error"}.
	SetCommitStatus(ctx context.Context, owner, repo, sha, context, state, targetURL, description string) error

	// ChangeRequestPath returns the URL path segment used in CR web URLs,
	// e.g. "pull" for GitHub/Gitea, "merge_requests" for GitLab.
	ChangeRequestPath() string

	// PushCredURL returns the HTTPS remote URL with credentials injected for a
	// one-shot git push. The returned URL must NEVER be stored or logged.
	PushCredURL(repoURL, token string) string
}

// PRRef identifies an open change request and its current head.
type PRRef struct {
	Number  int
	HeadSHA string
	HeadRef string
	Base    string
}

// restClient is a shared HTTP helper used by all concrete Provider implementations.
// It holds the API base URL, auth token, Accept header, and auth scheme.
type restClient struct {
	base       string // e.g. "https://api.github.com"
	token      string // PAT / app token; may be empty (public repos)
	accept     string // e.g. "application/vnd.github+json"; empty = omit header
	authScheme string // "Bearer" (default/empty) or "token" (Gitea's native form)
	authKey    string // when non-empty, used as the auth header key instead of "Authorization"
	// e.g. "PRIVATE-TOKEN" for GitLab; the token value is set directly (no scheme prefix).
}

// authHeader returns the full Authorization header value for this client.
// Falls back to "Bearer" when authScheme is unset, preserving backward compat.
func (rc *restClient) authHeader() string {
	scheme := rc.authScheme
	if scheme == "" {
		scheme = "Bearer"
	}
	return scheme + " " + rc.token
}

// setAuthHeader sets the appropriate authentication header on the request.
// When authKey is non-empty (e.g. "PRIVATE-TOKEN" for GitLab), it is used as the
// header name and the token is set directly. Otherwise, "Authorization" is used
// with the scheme-prefixed form (e.g. "Bearer <tok>", "token <tok>").
func (rc *restClient) setAuthHeader(req *http.Request) {
	if rc.token == "" {
		return
	}
	if rc.authKey != "" {
		req.Header.Set(rc.authKey, rc.token)
	} else {
		req.Header.Set("Authorization", rc.authHeader())
	}
}

// pushCredURL injects the token using the given credential prefix into an HTTPS
// remote URL, producing "https://<prefix>:<token>@<host>/..." for a one-shot git
// push. prefix is "x-access-token" (GitHub/Gitea) or "oauth2" (GitLab).
// The result carries a live secret and must NEVER be stored or logged.
func (rc *restClient) pushCredURL(prefix, repoURL, token string) string {
	if token == "" || !strings.HasPrefix(repoURL, "https://") {
		return repoURL
	}
	return "https://" + prefix + ":" + token + "@" + strings.TrimPrefix(repoURL, "https://")
}

// get performs a GET with auth and optional ETag caching. On 304 Not Modified,
// returns nil body and the original resp with no error. The token is redacted
// from any error message.
func (rc *restClient) get(ctx context.Context, url, etag string) (body []byte, resp *http.Response, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, err
	}
	if rc.accept != "" {
		req.Header.Set("Accept", rc.accept)
	}
	rc.setAuthHeader(req)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err = forgeHTTPClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode == http.StatusNotModified {
		_ = resp.Body.Close()
		return nil, resp, nil
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, resp, fmt.Errorf("GET %s: %s: %s", url, resp.Status, rc.redact(string(b)))
	}
	return b, resp, nil
}

// doPost is a shared POST helper for all GitHub-API-shaped forges. It sets the
// Content-Type, the forge's Accept header (if any), and the forge's auth header.
func (rc *restClient) doPost(ctx context.Context, url string, payload []byte) ([]byte, *http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if rc.accept != "" {
		req.Header.Set("Accept", rc.accept)
	}
	rc.setAuthHeader(req)
	resp, err := forgeHTTPClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
	return b, resp, nil
}

// redact scrubs the token from a string so it is safe to include in errors or logs.
func (rc *restClient) redact(s string) string {
	if rc.token == "" {
		return s
	}
	return strings.ReplaceAll(s, rc.token, "***")
}

// ---------------------------------------------------------------------------
// Shared GitHub-API-shaped operations — used by githubProvider and giteaProvider.
// ---------------------------------------------------------------------------

// rcOpenPR creates a PR via the GitHub REST API shape. Returns html_url.
// Idempotent: a 422 "already exists" falls back to rcFindOpenPR.
func (rc *restClient) rcOpenPR(ctx context.Context, owner, repo, head, base, title, body string) (string, error) {
	payload, _ := json.Marshal(map[string]any{"title": title, "head": head, "base": base, "body": body})
	url := rc.base + "/repos/" + owner + "/" + repo + "/pulls"
	b, resp, err := rc.doPost(ctx, url, payload)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 300 {
		origErr := fmt.Errorf("open PR: %s: %s", resp.Status, rc.redact(string(b)))
		if resp.StatusCode == 422 || strings.Contains(strings.ToLower(string(b)), "pull request already exists") {
			if existing, lookupErr := rc.rcFindOpenPR(ctx, owner, repo, head); lookupErr == nil && existing != "" {
				return existing, nil
			}
		}
		return "", origErr
	}
	var out struct {
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return "", fmt.Errorf("provider: parse PR URL response: %w", err)
	}
	return out.HTMLURL, nil
}

// rcFindOpenPR returns the html_url of the first open PR whose head matches, or "".
func (rc *restClient) rcFindOpenPR(ctx context.Context, owner, repo, head string) (string, error) {
	// Escape the query params — a branch/ref (head) can carry characters that
	// would otherwise break out of the query string ("&", space, "#").
	q := url.Values{"head": {owner + ":" + head}, "state": {"open"}}
	reqURL := rc.base + "/repos/" + owner + "/" + repo + "/pulls?" + q.Encode()
	b, _, err := rc.get(ctx, reqURL, "")
	if err != nil {
		return "", err
	}
	var prs []struct {
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(b, &prs); err != nil {
		return "", err
	}
	if len(prs) == 0 {
		return "", nil
	}
	return prs[0].HTMLURL, nil
}

// rcListReviews returns reviews with raw forge state strings (no normalization).
// Callers that require normalization (e.g. giteaProvider) post-process the result.
func (rc *restClient) rcListReviews(ctx context.Context, owner, repo string, prNumber int, etag string) ([]Review, string, bool, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/reviews", rc.base, owner, repo, prNumber)
	b, resp, err := rc.get(ctx, url, etag)
	if err != nil {
		return nil, "", false, err
	}
	if resp.StatusCode == http.StatusNotModified {
		return nil, etag, true, nil
	}
	var raw []struct {
		ID          int64  `json:"id"`
		State       string `json:"state"`
		Body        string `json:"body"`
		SubmittedAt string `json:"submitted_at"`
		User        struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, "", false, err
	}
	out := make([]Review, 0, len(raw))
	for _, r := range raw {
		out = append(out, Review{ID: r.ID, State: r.State, Body: r.Body, SubmittedAt: r.SubmittedAt, User: r.User.Login})
	}
	return out, resp.Header.Get("ETag"), false, nil
}

// rcListReviewComments returns the inline review comments for a PR.
func (rc *restClient) rcListReviewComments(ctx context.Context, owner, repo string, prNumber int) ([]ReviewComment, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/comments", rc.base, owner, repo, prNumber)
	b, _, err := rc.get(ctx, url, "")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Path string `json:"path"`
		Line int    `json:"line"`
		Body string `json:"body"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	out := make([]ReviewComment, 0, len(raw))
	for _, c := range raw {
		out = append(out, ReviewComment{Path: c.Path, Line: c.Line, Body: c.Body, User: c.User.Login})
	}
	return out, nil
}

// rcGetPR returns the state and merged flag for a PR.
func (rc *restClient) rcGetPR(ctx context.Context, owner, repo string, prNumber int) (string, bool, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", rc.base, owner, repo, prNumber)
	b, _, err := rc.get(ctx, url, "")
	if err != nil {
		return "", false, err
	}
	var out struct {
		State  string `json:"state"`
		Merged bool   `json:"merged"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return "", false, err
	}
	return out.State, out.Merged, nil
}

// rcPostComment posts a comment on a PR via the issues/comments endpoint.
func (rc *restClient) rcPostComment(ctx context.Context, owner, repo string, prNumber int, body string) error {
	payload, _ := json.Marshal(map[string]any{"body": body})
	url := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments", rc.base, owner, repo, prNumber)
	b, resp, err := rc.doPost(ctx, url, payload)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("post comment: %s: %s", resp.Status, rc.redact(string(b)))
	}
	return nil
}

// rcAuthLogin returns the authenticated user's login name.
func (rc *restClient) rcAuthLogin(ctx context.Context) (string, error) {
	b, _, err := rc.get(ctx, rc.base+"/user", "")
	if err != nil {
		return "", err
	}
	var out struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return "", err
	}
	return out.Login, nil
}

// rcListOpenPRs lists open PRs via the GitHub REST shape.
func (rc *restClient) rcListOpenPRs(ctx context.Context, owner, repo, base string) ([]PRRef, error) {
	// Escape the query params — a base branch can carry characters ("&",
	// space, "#") that would otherwise break out of the raw-concatenated
	// query string (mirrors the rcFindOpenPR escape fix above).
	q := url.Values{"state": {"open"}, "per_page": {"100"}}
	if base != "" {
		q.Set("base", base)
	}
	reqURL := rc.base + "/repos/" + owner + "/" + repo + "/pulls?" + q.Encode()
	b, _, err := rc.get(ctx, reqURL, "")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Number int `json:"number"`
		Head   struct {
			SHA string `json:"sha"`
			Ref string `json:"ref"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	out := make([]PRRef, 0, len(raw))
	for _, r := range raw {
		out = append(out, PRRef{Number: r.Number, HeadSHA: r.Head.SHA, HeadRef: r.Head.Ref, Base: r.Base.Ref})
	}
	return out, nil
}

// rcSetCommitStatus posts a commit status via the GitHub REST shape.
func (rc *restClient) rcSetCommitStatus(ctx context.Context, owner, repo, sha, context, state, targetURL, description string) error {
	payload, _ := json.Marshal(map[string]any{
		"state": state, "context": context, "target_url": targetURL, "description": description,
	})
	url := rc.base + "/repos/" + owner + "/" + repo + "/statuses/" + sha
	b, resp, err := rc.doPost(ctx, url, payload)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("set commit status: %s: %s", resp.Status, rc.redact(string(b)))
	}
	return nil
}
