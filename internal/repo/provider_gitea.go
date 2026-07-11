// SPDX-License-Identifier: Elastic-2.0

// provider_gitea.go implements Provider for Gitea's REST API.
//
// Gitea deliberately mirrors GitHub's REST shape (same endpoints, same JSON),
// so all HTTP mechanics live in the shared restClient (provider.go). This file
// is a thin wrapper with three Gitea-specific differences:
//
//  1. Auth header: "Authorization: token <tok>" — Gitea's native form.
//     (Bearer also works on Gitea, but "token" is the canonical Gitea form
//     and makes self-hosted Gitea's audit log more readable.)
//
//  2. No vendor Accept header — Gitea does not accept "application/vnd.github+json".
//
//  3. Review-state normalization: Gitea returns "REQUEST_CHANGES" where GitHub
//     returns "CHANGES_REQUESTED". ListReviews normalizes to "CHANGES_REQUESTED"
//     so the engine's approval/block loop triggers correctly on both forges.
//
// PushCredURL uses the same "x-access-token:<tok>@" form as GitHub — Gitea
// accepts this for token-in-URL authentication, and it keeps the credential
// form consistent across forges.
package repo

import (
	"context"
	"errors"
	"fmt"
)

// giteaProvider implements Provider for Gitea (GitHub-API-compatible REST).
type giteaProvider struct {
	rc restClient // authScheme:"token", accept:"" (no vendor header)
}

// ChangeRequestPath returns "pull" — the Gitea URL segment for PRs.
func (p *giteaProvider) ChangeRequestPath() string { return "pull" }

// PushCredURL injects the token as x-access-token into an HTTPS remote URL
// for a one-shot git push. Gitea accepts this GitHub-compatible form.
// The result must NEVER be stored or logged.
func (p *giteaProvider) PushCredURL(repoURL, token string) string {
	return p.rc.pushCredURL("x-access-token", repoURL, token)
}

// OpenPR delegates to the shared restClient implementation.
func (p *giteaProvider) OpenPR(ctx context.Context, owner, repo, head, base, title, body string) (string, error) {
	return p.rc.rcOpenPR(ctx, owner, repo, head, base, title, body)
}

// ListReviews fetches reviews and normalizes Gitea's "REQUEST_CHANGES" state
// to "CHANGES_REQUESTED" so the engine's review-loop logic is forge-agnostic.
//
// Gitea API ref: GET /repos/{owner}/{repo}/pulls/{index}/reviews
// State enum: APPROVED | REQUEST_CHANGES | COMMENT | PENDING
func (p *giteaProvider) ListReviews(ctx context.Context, owner, repo string, prNumber int, etag string) ([]Review, string, bool, error) {
	reviews, newEtag, notMod, err := p.rc.rcListReviews(ctx, owner, repo, prNumber, etag)
	if err != nil || notMod {
		return reviews, newEtag, notMod, err
	}
	for i := range reviews {
		if reviews[i].State == "REQUEST_CHANGES" {
			reviews[i].State = "CHANGES_REQUESTED"
		}
	}
	return reviews, newEtag, notMod, nil
}

// ListReviewComments delegates to the shared restClient implementation.
func (p *giteaProvider) ListReviewComments(ctx context.Context, owner, repo string, prNumber int) ([]ReviewComment, error) {
	return p.rc.rcListReviewComments(ctx, owner, repo, prNumber)
}

// GetPR delegates to the shared restClient implementation.
func (p *giteaProvider) GetPR(ctx context.Context, owner, repo string, prNumber int) (string, bool, error) {
	return p.rc.rcGetPR(ctx, owner, repo, prNumber)
}

// PostComment delegates to the shared restClient implementation.
func (p *giteaProvider) PostComment(ctx context.Context, owner, repo string, prNumber int, body string) error {
	return p.rc.rcPostComment(ctx, owner, repo, prNumber, body)
}

// AuthLogin delegates to the shared restClient implementation.
func (p *giteaProvider) AuthLogin(ctx context.Context) (string, error) {
	return p.rc.rcAuthLogin(ctx)
}

// ListOpenPRs is not yet implemented for Gitea. Callers get an honest
// errors.ErrUnsupported rather than a silent no-op.
func (p *giteaProvider) ListOpenPRs(ctx context.Context, owner, repo, base string) ([]PRRef, error) {
	return nil, fmt.Errorf("gitea: ListOpenPRs: %w", errors.ErrUnsupported)
}

// SetCommitStatus is not yet implemented for Gitea. Callers get an honest
// errors.ErrUnsupported rather than a silent no-op.
func (p *giteaProvider) SetCommitStatus(ctx context.Context, owner, repo, sha, context, state, targetURL, description string) error {
	return fmt.Errorf("gitea: SetCommitStatus: %w", errors.ErrUnsupported)
}
