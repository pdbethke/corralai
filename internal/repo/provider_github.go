// SPDX-License-Identifier: Elastic-2.0

// provider_github.go implements Provider for the GitHub REST API.
// All HTTP mechanics and JSON parsing live in restClient (provider.go);
// this file is a thin wrapper that configures the GitHub-specific settings
// (Bearer auth, vnd.github+json Accept, "pull" CR path) and delegates.
package repo

import (
	"context"
)

// githubProvider implements Provider for the GitHub REST API.
type githubProvider struct {
	rc restClient
}

// ChangeRequestPath returns "pull" — the GitHub URL segment for PRs.
func (p *githubProvider) ChangeRequestPath() string { return "pull" }

// PushCredURL injects the token as x-access-token into an HTTPS remote URL
// for a one-shot git push. The result must NEVER be stored or logged.
func (p *githubProvider) PushCredURL(repoURL, token string) string {
	return p.rc.pushCredURL("x-access-token", repoURL, token)
}

// OpenPR delegates to the shared restClient implementation.
func (p *githubProvider) OpenPR(ctx context.Context, owner, repo, head, base, title, body string) (string, error) {
	return p.rc.rcOpenPR(ctx, owner, repo, head, base, title, body)
}

// ListReviews delegates to the shared restClient implementation.
// GitHub uses "CHANGES_REQUESTED" natively — no normalization needed.
func (p *githubProvider) ListReviews(ctx context.Context, owner, repo string, prNumber int, etag string) ([]Review, string, bool, error) {
	return p.rc.rcListReviews(ctx, owner, repo, prNumber, etag)
}

// ListReviewComments delegates to the shared restClient implementation.
func (p *githubProvider) ListReviewComments(ctx context.Context, owner, repo string, prNumber int) ([]ReviewComment, error) {
	return p.rc.rcListReviewComments(ctx, owner, repo, prNumber)
}

// GetPR delegates to the shared restClient implementation.
func (p *githubProvider) GetPR(ctx context.Context, owner, repo string, prNumber int) (string, bool, error) {
	return p.rc.rcGetPR(ctx, owner, repo, prNumber)
}

// PostComment delegates to the shared restClient implementation.
func (p *githubProvider) PostComment(ctx context.Context, owner, repo string, prNumber int, body string) error {
	return p.rc.rcPostComment(ctx, owner, repo, prNumber, body)
}

// AuthLogin delegates to the shared restClient implementation.
func (p *githubProvider) AuthLogin(ctx context.Context) (string, error) {
	return p.rc.rcAuthLogin(ctx)
}
