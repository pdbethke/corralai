// SPDX-License-Identifier: Elastic-2.0

// Package repo is the brain-side git/PR engine. The brain (trusted plane) owns all
// privileged git — clone, commit, push, PR — and the only copy of the token. The
// token is injected into the HTTPS remote only for the network call and is NEVER
// persisted in the working copy's .git/config (the jail bind-mounts the working copy
// and a bee could read it), and never logged.
package repo

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
)

// Engine drives git operations (clone/commit/push) and REST operations (OpenPR,
// ListReviews, etc.) for the brain. REST operations are delegated to a
// forge-specific Provider selected by the repo URL's host via the forge registry.
type Engine struct {
	// token is the default git credential used by tokenURL/Push for the primary
	// forge. Kept here (rather than only in the forges registry) so that git
	// push — which knows only the remote URL, not a Provider — can inject it.
	token  string
	forges map[string]ForgeConfig // host → forge config; populated by New / ForgesFromEnv
}

// New returns an Engine configured for a single GitHub-compatible forge.
// token is the PAT used for git push (x-access-token form) AND for REST API calls.
// apiBase is the GitHub REST API base (default "https://api.github.com").
//
// For multi-forge setups, call New("","") and populate via ForgesFromEnv().
func New(token, apiBase string) *Engine {
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}
	return &Engine{
		token: token,
		forges: map[string]ForgeConfig{
			"github.com": {Type: "github", APIBase: apiBase, Token: token},
		},
	}
}

// NewWithForges returns an Engine backed by an explicit forge registry.
// Use this when you want more than the default github.com entry.
func NewWithForges(primaryToken string, forges map[string]ForgeConfig) *Engine {
	return &Engine{token: primaryToken, forges: forges}
}

// RepoIdent parses the host, owner, and repo name from an HTTPS or SSH git URL.
// It is the forge-agnostic URL parser used by the Engine to select a Provider.
//
// Supported forms:
//
//	https://github.com/owner/repo(.git)
//	https://gitlab.com/owner/repo(.git)
//	https://gitea.example.com/owner/repo(.git)
//	git@github.com:owner/repo.git
//	git@gitea.example.com:owner/repo.git
func RepoIdent(repoURL string) (host, owner, repo string, err error) {
	s := strings.TrimSuffix(repoURL, ".git")

	// SSH form: git@host:owner/repo
	if strings.HasPrefix(s, "git@") {
		s = strings.TrimPrefix(s, "git@")
		colon := strings.Index(s, ":")
		if colon < 0 {
			return "", "", "", fmt.Errorf("cannot parse owner/repo from SSH URL %q", repoURL)
		}
		host = s[:colon]
		rest := strings.Trim(s[colon+1:], "/")
		parts := strings.SplitN(rest, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return "", "", "", fmt.Errorf("cannot parse owner/repo from SSH URL %q", repoURL)
		}
		return host, parts[0], parts[1], nil
	}

	// HTTPS form: https://host/owner/repo
	u, parseErr := url.Parse(s)
	if parseErr != nil || u.Host == "" {
		return "", "", "", fmt.Errorf("cannot parse owner/repo from %q: %v", repoURL, parseErr)
	}
	host = u.Host
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || parts[len(parts)-2] == "" || parts[len(parts)-1] == "" {
		return "", "", "", fmt.Errorf("cannot parse owner/repo from %q", repoURL)
	}
	return host, parts[len(parts)-2], parts[len(parts)-1], nil
}

// RepoIdent is a backward-compat engine method that calls the package-level
// RepoIdent and returns (owner, repo, err) without the host. The engine's own
// REST methods no longer call this; it exists for callers that predate the
// multi-forge refactor.
func (e *Engine) RepoIdent(repoURL string) (owner, repo string, err error) {
	_, owner, repo, err = RepoIdent(repoURL)
	return
}

// --- Engine REST wrappers: delegate to forge Providers via host-based registry ---
// All REST methods take a repoURL (not owner/repo) so the engine can select the
// correct Provider from the registry without the caller knowing the host.

// OpenPR creates a pull request on the forge for the given repo URL.
func (e *Engine) OpenPR(ctx context.Context, repoURL, head, base, title, body string) (string, error) {
	p, owner, repo, err := e.resolveProvider(repoURL)
	if err != nil {
		return "", err
	}
	return p.OpenPR(ctx, owner, repo, head, base, title, body)
}

// ListReviews returns reviews for the PR identified by (repoURL, prNumber).
func (e *Engine) ListReviews(ctx context.Context, repoURL string, prNumber int, etag string) ([]Review, string, bool, error) {
	p, owner, repo, err := e.resolveProvider(repoURL)
	if err != nil {
		return nil, "", false, err
	}
	return p.ListReviews(ctx, owner, repo, prNumber, etag)
}

// ListReviewComments returns the inline comments for the PR identified by (repoURL, prNumber).
func (e *Engine) ListReviewComments(ctx context.Context, repoURL string, prNumber int) ([]ReviewComment, error) {
	p, owner, repo, err := e.resolveProvider(repoURL)
	if err != nil {
		return nil, err
	}
	return p.ListReviewComments(ctx, owner, repo, prNumber)
}

// GetPR returns the state and merged flag for the PR identified by (repoURL, prNumber).
func (e *Engine) GetPR(ctx context.Context, repoURL string, prNumber int) (string, bool, error) {
	p, owner, repo, err := e.resolveProvider(repoURL)
	if err != nil {
		return "", false, err
	}
	return p.GetPR(ctx, owner, repo, prNumber)
}

// PostComment posts a comment on the PR identified by (repoURL, prNumber).
func (e *Engine) PostComment(ctx context.Context, repoURL string, prNumber int, body string) error {
	p, owner, repo, err := e.resolveProvider(repoURL)
	if err != nil {
		return err
	}
	return p.PostComment(ctx, owner, repo, prNumber, body)
}

// PostIssueComment is a backward-compat alias for PostComment. Deprecated: use PostComment.
func (e *Engine) PostIssueComment(ctx context.Context, repoURL string, prNumber int, body string) error {
	return e.PostComment(ctx, repoURL, prNumber, body)
}

// AuthLogin returns the authenticated user's login name on the forge that hosts
// repoURL. Routed by host so a mission on one forge never resolves its bot
// identity against a different forge — which would break the review self-filter
// (a same-named foreign reviewer silently dropped, or a self-response loop).
func (e *Engine) AuthLogin(ctx context.Context, repoURL string) (string, error) {
	p, _, _, err := e.resolveProvider(repoURL)
	if err != nil {
		return "", err
	}
	return p.AuthLogin(ctx)
}

// ListOpenPRs returns open PRs for repoURL targeting base (all bases if
// base == "").
func (e *Engine) ListOpenPRs(ctx context.Context, repoURL, base string) ([]PRRef, error) {
	p, owner, repo, err := e.resolveProvider(repoURL)
	if err != nil {
		return nil, err
	}
	return p.ListOpenPRs(ctx, owner, repo, base)
}

// SetCommitStatus posts a commit status for repoURL@sha.
func (e *Engine) SetCommitStatus(ctx context.Context, repoURL, sha, context, state, targetURL, description string) error {
	p, owner, repo, err := e.resolveProvider(repoURL)
	if err != nil {
		return err
	}
	return p.SetCommitStatus(ctx, owner, repo, sha, context, state, targetURL, description)
}

// resolveProvider parses repoURL to get (host, owner, repo), looks up the
// forge config for host, and returns the Provider + owner + repo.
func (e *Engine) resolveProvider(repoURL string) (Provider, string, string, error) {
	host, owner, repo, err := RepoIdent(repoURL)
	if err != nil {
		return nil, "", "", err
	}
	p, err := e.providerFor(host)
	if err != nil {
		return nil, "", "", err
	}
	return p, owner, repo, nil
}

// tokenURL injects the host's OWN registry credential into an HTTPS URL for a
// one-shot git network call, in the provider-owned format (github/gitea
// x-access-token:, gitlab oauth2:). It is registry-STRICT: a host with no token
// (or no provider) gets the URL returned UNCHANGED — the credential of one forge
// is NEVER injected into another forge's URL (credential boundary). The git op
// then fails with a clear "no credential" error rather than leaking a PAT.
//
// The returned URL carries a live secret and must NEVER be stored or logged.
func (e *Engine) tokenURL(u string) string {
	if !strings.HasPrefix(u, "https://") {
		return u // non-HTTPS (file://, ssh) — no credential injection
	}
	parsed, err := url.Parse(u)
	if err != nil || parsed.Host == "" {
		return u
	}
	cfg, ok := e.forges[parsed.Host]
	if !ok || cfg.Token == "" {
		return u // no credential for this host — do NOT fall back to another host's token
	}
	p, perr := e.providerFor(parsed.Host)
	if perr != nil {
		return u // no provider for this host (e.g. unimplemented type) — no injection
	}
	// Provider owns the credential FORMAT for its forge.
	return p.PushCredURL(u, cfg.Token)
}

// redact removes all known forge tokens from a string for safe logging/errors.
func (e *Engine) redact(s string) string {
	// Redact primary token.
	if e.token != "" {
		s = strings.ReplaceAll(s, e.token, "***")
	}
	// Redact any per-forge tokens that differ from the primary.
	for _, cfg := range e.forges {
		if cfg.Token != "" && cfg.Token != e.token {
			s = strings.ReplaceAll(s, cfg.Token, "***")
		}
	}
	return s
}

func (e *Engine) git(ctx context.Context, dir string, args ...string) (string, error) {
	// Defense-in-depth: disable hooks and fsmonitor for every brain git invocation.
	// This protects against a malicious .git/config arriving via a compromised or
	// hostile-cloned repo, not just via ApplyFiles. Hooks in /dev/null never exist;
	// fsmonitor=false prevents spawning an external monitor process.
	full := append([]string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}, args...)
	c := exec.CommandContext(ctx, "git", full...) // #nosec G204 -- corral runs git by design; command is the constant "git", args are constructed from internal code paths; agent command execution is separately sandboxed (bwrap)
	if dir != "" {
		c.Dir = dir
	}
	// deterministic identity for commits; no global config dependency.
	c.Env = append([]string{
		"GIT_AUTHOR_NAME=corralai", "GIT_AUTHOR_EMAIL=corralai@local",
		"GIT_COMMITTER_NAME=corralai", "GIT_COMMITTER_EMAIL=corralai@local",
		"GIT_TERMINAL_PROMPT=0",
	}, envPassthrough()...)
	var buf bytes.Buffer
	c.Stdout, c.Stderr = &buf, &buf
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("git %s: %v: %s", e.redact(strings.Join(args, " ")), err, e.redact(buf.String()))
	}
	return e.redact(buf.String()), nil
}

// envPassthrough passes PATH/HOME/etc. so git finds itself + ssl certs; NO secrets.
func envPassthrough() []string {
	var out []string
	for _, k := range []string{"PATH", "HOME", "SSL_CERT_FILE", "SSL_CERT_DIR", "GIT_SSL_CAINFO"} {
		if v := os.Getenv(k); v != "" {
			out = append(out, k+"="+v)
		}
	}
	return out
}

func (e *Engine) Clone(ctx context.Context, repoURL, base, destDir string) error {
	if base == "" {
		base = "main"
	}
	if _, err := e.git(ctx, "", "clone", "--branch", base, "--single-branch", e.tokenURL(repoURL), destDir); err != nil {
		return err
	}
	// CRITICAL: never leave the token in the working copy's config (jail-readable).
	_, err := e.git(ctx, destDir, "remote", "set-url", "origin", repoURL)
	return err
}

func (e *Engine) Checkout(ctx context.Context, dir, branch string) error {
	_, err := e.git(ctx, dir, "checkout", "-b", branch)
	return err
}

func (e *Engine) Commit(ctx context.Context, dir, message string) (bool, error) {
	if _, err := e.git(ctx, dir, "add", "-A"); err != nil {
		return false, err
	}
	out, err := e.git(ctx, dir, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(out) == "" {
		return false, nil // empty diff → no commit
	}
	if _, err := e.git(ctx, dir, "commit", "-m", message); err != nil {
		return false, err
	}
	return true, nil
}

func (e *Engine) Push(ctx context.Context, dir, branch string) error {
	// one-shot token URL on the command line; never stored in config. Read origin's
	// clean URL, push with the token-injected form.
	origin, err := e.git(ctx, dir, "remote", "get-url", "origin")
	if err != nil {
		return err
	}
	_, err = e.git(ctx, dir, "push", e.tokenURL(strings.TrimSpace(origin)), "HEAD:refs/heads/"+branch)
	return err
}
