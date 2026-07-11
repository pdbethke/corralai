// SPDX-License-Identifier: Elastic-2.0

// provider_gitlab.go implements Provider for the GitLab REST API v4.
//
// GitLab has a genuinely different REST shape from GitHub/Gitea:
//
//   - Auth: PRIVATE-TOKEN: <token> header (not Authorization: Bearer).
//   - Project ID: URL-encoded "owner/repo" path, e.g. "myorg%2Fmyrepo".
//   - Change requests: called "merge requests" at "merge_requests" in URLs.
//   - No first-class review object: unresolved resolvable MR discussions authored
//     by non-bot users are synthesized as CHANGES_REQUESTED so the #18 rework
//     loop triggers unchanged. Resolved threads, system notes, and the bot's own
//     discussions produce no review.
//   - GetPR: merged = (state == "merged"); there is no separate .merged field.
//   - PushCredURL: oauth2:<token>@host (not x-access-token like GitHub/Gitea).
//   - Pagination: GET /discussions uses ?per_page=100&page=N; stop when empty.
//   - ETag: GitLab discussions endpoint does not support If-None-Match — always
//     returns ("", false) for (newEtag, notModified).
package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// gitlabProvider implements Provider for the GitLab REST API v4.
// rc.authKey is set to "PRIVATE-TOKEN" so setAuthHeader sends the correct
// GitLab auth header instead of "Authorization: Bearer <tok>".
type gitlabProvider struct {
	rc restClient // authKey:"PRIVATE-TOKEN"; no Accept header; no authScheme
}

// ChangeRequestPath returns "merge_requests" — the GitLab URL segment for MRs.
func (p *gitlabProvider) ChangeRequestPath() string { return "merge_requests" }

// PushCredURL injects the token as oauth2:<tok>@host into an HTTPS remote URL
// for a one-shot git push. GitLab accepts the "oauth2:" form for token-in-URL auth.
// The result must NEVER be stored or logged.
func (p *gitlabProvider) PushCredURL(repoURL, token string) string {
	return p.rc.pushCredURL("oauth2", repoURL, token)
}

// glProjectID returns the URL-path-escaped project ID for GitLab v4 endpoints.
// GitLab accepts the URL-encoded "namespace/project" string as the project identifier.
// Example: "myorg/myrepo" → "myorg%2Fmyrepo".
func glProjectID(owner, repo string) string {
	return url.PathEscape(owner + "/" + repo)
}

// OpenPR creates a merge request on GitLab. Returns the web_url of the new (or
// already-existing) MR. Idempotent: on 400/409/422 "already exists" falls back
// to finding the first open MR from the source branch.
func (p *gitlabProvider) OpenPR(ctx context.Context, owner, repo, head, base, title, body string) (string, error) {
	projID := glProjectID(owner, repo)
	payload, _ := json.Marshal(map[string]any{
		"source_branch": head,
		"target_branch": base,
		"title":         title,
		"description":   body,
	})
	u := p.rc.base + "/projects/" + projID + "/merge_requests"
	b, resp, err := p.rc.doPost(ctx, u, payload)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 300 {
		origErr := fmt.Errorf("open MR: %s: %s", resp.Status, p.rc.redact(string(b)))
		// GitLab returns 409 Conflict or 400/422 with a body containing "already exists"
		// when an open MR for the source branch already exists.
		bodyStr := strings.ToLower(string(b))
		alreadyExists := strings.Contains(bodyStr, "already exists") ||
			strings.Contains(bodyStr, "another open merge request")
		if resp.StatusCode == 409 || (resp.StatusCode == 422 && alreadyExists) ||
			(resp.StatusCode == 400 && alreadyExists) {
			if existing, lookupErr := p.glFindOpenMR(ctx, projID, head); lookupErr == nil && existing != "" {
				return existing, nil
			}
		}
		return "", origErr
	}
	var out struct {
		WebURL string `json:"web_url"`
	}
	_ = json.Unmarshal(b, &out)
	return out.WebURL, nil
}

// glFindOpenMR returns the web_url of the first open MR whose source_branch
// matches the given head branch, or "" if none is found.
func (p *gitlabProvider) glFindOpenMR(ctx context.Context, projID, sourceBranch string) (string, error) {
	u := p.rc.base + "/projects/" + projID + "/merge_requests" +
		"?source_branch=" + url.QueryEscape(sourceBranch) + "&state=opened"
	b, _, err := p.rc.get(ctx, u, "")
	if err != nil {
		return "", err
	}
	var mrs []struct {
		WebURL string `json:"web_url"`
	}
	if err := json.Unmarshal(b, &mrs); err != nil {
		return "", err
	}
	if len(mrs) == 0 {
		return "", nil
	}
	return mrs[0].WebURL, nil
}

// GetPR returns the state and merged flag for a GitLab MR.
// GitLab MR state enum: opened | closed | locked | merged.
// There is no separate "merged" field; merged = (state == "merged").
func (p *gitlabProvider) GetPR(ctx context.Context, owner, repo string, iid int) (string, bool, error) {
	projID := glProjectID(owner, repo)
	u := fmt.Sprintf("%s/projects/%s/merge_requests/%d", p.rc.base, projID, iid)
	b, _, err := p.rc.get(ctx, u, "")
	if err != nil {
		return "", false, err
	}
	var out struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return "", false, err
	}
	return out.State, out.State == "merged", nil
}

// glDiscussion is the GitLab API v4 shape for a single MR discussion thread.
// Resolvable=true means the thread can be resolved (i.e., it is a diff note
// thread, not a system event). System notes (e.g. "pushed 2 commits") are
// not resolvable. The Notes slice contains the individual comments.
type glDiscussion struct {
	ID         string `json:"id"`
	Resolvable bool   `json:"resolvable"`
	Resolved   bool   `json:"resolved"`
	Notes      []struct {
		ID        int64  `json:"id"`
		Body      string `json:"body"`
		CreatedAt string `json:"created_at"` // RFC3339 / ISO 8601
		System    bool   `json:"system"`
		Author    struct {
			Username string `json:"username"`
		} `json:"author"`
	} `json:"notes"`
}

// ListReviews synthesizes CHANGES_REQUESTED reviews from unresolved non-bot MR
// discussions. GitLab has no first-class review object; the engine's #18 rework
// loop is driven by this synthesis:
//
//   - A discussion is included iff: resolvable && not resolved && first-note
//     author is not the bot.
//   - The synthesized Review carries: State="CHANGES_REQUESTED", User=author of
//     the first note, Body=body of the latest note, SubmittedAt=created_at of
//     the latest note (RFC3339 sorts lexically so max is straightforward).
//
// Pagination: GET /discussions?per_page=100&page=N, increment until empty.
// ETag: GitLab discussions does not support If-None-Match; always returns
// ("", false) for (newEtag, notModified) — the engine polls on each cycle.
func (p *gitlabProvider) ListReviews(ctx context.Context, owner, repo string, iid int, _ string) ([]Review, string, bool, error) {
	projID := glProjectID(owner, repo)

	// Resolve the bot's username once, to filter self-authored discussions.
	botUsername, err := p.AuthLogin(ctx)
	if err != nil {
		return nil, "", false, fmt.Errorf("gitlab ListReviews: resolve bot username: %w", err)
	}

	// Collect all discussion threads, following pagination.
	var all []glDiscussion
	const maxPages = 100 // guard against runaway servers
	for page := 1; page <= maxPages; page++ {
		u := fmt.Sprintf("%s/projects/%s/merge_requests/%d/discussions?per_page=100&page=%d",
			p.rc.base, projID, iid, page)
		b, _, err := p.rc.get(ctx, u, "")
		if err != nil {
			return nil, "", false, err
		}
		var batch []glDiscussion
		if err := json.Unmarshal(b, &batch); err != nil {
			return nil, "", false, err
		}
		if len(batch) == 0 {
			break
		}
		all = append(all, batch...)
	}

	// Synthesize CHANGES_REQUESTED reviews from qualifying discussions.
	var reviews []Review
	for _, d := range all {
		// Only resolvable (diff-note) threads that are still open.
		if !d.Resolvable || d.Resolved || len(d.Notes) == 0 {
			continue
		}
		// Skip the bot's own discussions.
		firstAuthor := d.Notes[0].Author.Username
		if firstAuthor == botUsername {
			continue
		}
		// Find the latest note's created_at and body (RFC3339 sorts lexically).
		latest := d.Notes[0].CreatedAt
		latestBody := d.Notes[0].Body
		for _, n := range d.Notes[1:] {
			if n.CreatedAt > latest {
				latest = n.CreatedAt
				latestBody = n.Body
			}
		}
		reviews = append(reviews, Review{
			State:       "CHANGES_REQUESTED",
			SubmittedAt: latest,
			User:        firstAuthor,
			Body:        latestBody,
		})
	}

	return reviews, "", false, nil
}

// ListReviewComments returns inline diff note comments for a GitLab MR.
// GitLab MR notes with a position field are diff-anchored; others are general
// comments. System notes (automated events) are excluded. Position is
// best-effort mapped to (Path, Line).
func (p *gitlabProvider) ListReviewComments(ctx context.Context, owner, repo string, iid int) ([]ReviewComment, error) {
	projID := glProjectID(owner, repo)
	u := fmt.Sprintf("%s/projects/%s/merge_requests/%d/notes?per_page=100", p.rc.base, projID, iid)
	b, _, err := p.rc.get(ctx, u, "")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Body   string `json:"body"`
		System bool   `json:"system"`
		Author struct {
			Username string `json:"username"`
		} `json:"author"`
		Position *struct {
			NewPath string `json:"new_path"`
			NewLine int    `json:"new_line"`
		} `json:"position"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	var out []ReviewComment
	for _, n := range raw {
		if n.System {
			continue
		}
		c := ReviewComment{Body: n.Body, User: n.Author.Username}
		if n.Position != nil {
			c.Path = n.Position.NewPath
			c.Line = n.Position.NewLine
		}
		out = append(out, c)
	}
	return out, nil
}

// PostComment posts a comment on a GitLab MR via the MR notes endpoint.
func (p *gitlabProvider) PostComment(ctx context.Context, owner, repo string, iid int, body string) error {
	projID := glProjectID(owner, repo)
	payload, _ := json.Marshal(map[string]any{"body": body})
	u := fmt.Sprintf("%s/projects/%s/merge_requests/%d/notes", p.rc.base, projID, iid)
	b, resp, err := p.rc.doPost(ctx, u, payload)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("post MR note: %s: %s", resp.Status, p.rc.redact(string(b)))
	}
	return nil
}

// AuthLogin returns the authenticated GitLab user's username.
// GitLab's GET /user returns "username" (not "login" like GitHub).
func (p *gitlabProvider) AuthLogin(ctx context.Context) (string, error) {
	b, _, err := p.rc.get(ctx, p.rc.base+"/user", "")
	if err != nil {
		return "", err
	}
	var out struct {
		Username string `json:"username"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return "", err
	}
	return out.Username, nil
}

// ListOpenPRs is not yet implemented for GitLab. Callers get an honest
// errors.ErrUnsupported rather than a silent no-op.
func (p *gitlabProvider) ListOpenPRs(ctx context.Context, owner, repo, base string) ([]PRRef, error) {
	return nil, fmt.Errorf("gitlab: ListOpenPRs: %w", errors.ErrUnsupported)
}

// SetCommitStatus is not yet implemented for GitLab. Callers get an honest
// errors.ErrUnsupported rather than a silent no-op.
func (p *gitlabProvider) SetCommitStatus(ctx context.Context, owner, repo, sha, context, state, targetURL, description string) error {
	return fmt.Errorf("gitlab: SetCommitStatus: %w", errors.ErrUnsupported)
}
