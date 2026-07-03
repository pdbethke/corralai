# Multi-Forge Provider (GitHub / GitLab / Gitea) — Design

**Status:** design · **Date:** 2026-07-01 · **Sub-project:** F (git-forge provider abstraction)

## Problem

The repo-work engine (#15/#18) is GitHub-only. The **git-protocol layer** (`Clone`/`Checkout`/
`Commit`/`Push`/`ChangedFiles`/read/grep/snapshot in `internal/repo`) is already forge-neutral,
but the **REST-API layer** (`OpenPR`, `findOpenPR`, `ListReviews`, `ListReviewComments`, `GetPR`,
`PostIssueComment`, `AuthLogin`, the shared `ghGet`) is 100% GitHub REST, plus three GitHub
hardcodings: `RepoIdent` strips `github.com`, `ParsePRNumber` looks for `/pull/`, and the API base
defaults to `api.github.com`.

**The good news:** `mission.RepoOps` is ALREADY the clean seam — the mission engine (#15/#18)
only ever calls that interface, never the concrete `*repo.Engine`; `repoAdapter` in `cmd/corral`
is the single wiring point. So multi-forge = extract a `Provider`, add GitLab + Gitea
implementations, and select by host — with **zero changes to the mission engine**.

**Goal:** missions can target **GitHub, GitLab, and Gitea** repos (incl. self-hosted), with the
#18 review loop working on each, per-forge credentials, and the existing GitHub behavior preserved.

## First principles

1. **Reuse the existing seam.** `mission.RepoOps` doesn't change. The engine keeps calling it.
   The change is *behind* it: a `Provider` per forge, selected by the repo's host.
2. **Git stays shared; only the forge API varies.** `Clone`/`Push`/etc. are identical across
   forges (git is git); only PR/MR + review + auth operations are provider-specific.
3. **Normalize to the engine's existing model.** Each provider maps its native concepts to the
   engine's `ReviewInfo{State, SubmittedAt, User, Body}` with `State ∈ {APPROVED,
   CHANGES_REQUESTED, COMMENTED, DISMISSED}` — so the engine's `newestActionable`
   (`State=="CHANGES_REQUESTED"` triggers rework) is unchanged. The mapping lives in each provider.
4. **Credential boundary per forge (unchanged posture).** Each forge's token lives only in the
   brain, is scrubbed from env after load, injected into the push URL only for the one network
   call, and sent as the forge's auth header on API calls. `.git` never carries a token.

## Architecture

### The `Provider` interface (`internal/repo`)
Extract the forge-API subset (the GitHub-specific methods) into:
```go
type Provider interface {
    OpenPR(ctx, owner, repo, head, base, title, body string) (url string, err error)
    ListReviews(ctx, owner, repo string, cr int, etag string) ([]Review, newETag string, notModified bool, err error)
    ListReviewComments(ctx, owner, repo string, cr int) ([]ReviewComment, error)
    GetPR(ctx, owner, repo string, cr int) (state string, merged bool, err error)
    PostComment(ctx, owner, repo string, cr int, body string) error   // renamed from PostIssueComment (forge-neutral)
    AuthLogin(ctx) (login string, err error)
    ChangeRequestPath() string                                        // "pull" (GH/Gitea) | "merge_requests" (GitLab) — for URL parse/build
}
```
`repo.Engine` keeps the git ops and holds a `Provider` (chosen per repo host); its existing
`OpenPR`/`ListReviews`/... methods delegate to `p.Provider`. The `RepoOps` adapter is unchanged.

### The three providers (`internal/repo/provider_{github,gitea,gitlab}.go`)
- **GitHub** — the existing code, refactored behind `Provider` (endpoints, `application/vnd.github+json`,
  `Authorization: Bearer`, `/pull/`, native review states, `/issues/{n}/comments`).
- **Gitea** — Gitea's API deliberately mirrors GitHub's: same endpoints, `/pull/`, review states,
  `/issues/{n}/comments`. Differences: the host/API-base, and auth (`Authorization: token <tok>`
  or Bearer). Implemented as a thin variant of the GitHub provider (share a common REST core
  parameterized by base URL + auth header + Accept), NOT a fork.
- **GitLab** — genuinely different:
  - CR create: `POST /projects/{projID}/merge_requests` (`projID` = URL-encoded `owner/repo`).
  - find open: `GET /projects/{projID}/merge_requests?source_branch={head}&state=opened`.
  - state: `GET .../merge_requests/{iid}` → `state ∈ {opened,closed,locked,merged}`; **`merged`
    is a state, not a separate flag** → provider returns `(state, merged = state=="merged")`.
  - **reviews (the mapping):** GitLab has no review object. `ListReviews` calls
    `GET .../merge_requests/{iid}/discussions`; for each discussion thread that is **resolvable,
    UNRESOLVED, and authored by a non-bot**, synthesize a `Review{State:"CHANGES_REQUESTED",
    SubmittedAt: latest note's created_at, User: author, Body: note body}`. Resolved/only-bot
    discussions → no CHANGES_REQUESTED (treated as clear). This is the chosen "unresolved
    discussions = changes requested" model (portable to CE + EE).
  - comments: `POST .../merge_requests/{iid}/notes`.
  - auth: `PRIVATE-TOKEN: <token>` header (GitLab PAT).
  - `ChangeRequestPath()` = `"merge_requests"`.

### Forge selection + config
- `RepoIdent(repoURL)` becomes host-aware: parse `scheme://host/owner/repo(.git)` (and the
  `git@host:owner/repo` SSH form) for ANY host — return `(host, owner, repo)`.
- A **forge registry** maps host → `{type: github|gitlab|gitea, apiBase, token}`:
  - Defaults: `github.com`→github (`api.github.com`), `gitlab.com`→gitlab (`gitlab.com/api/v4`).
  - Self-hosted via `CORRALAI_FORGES` — entries of `host=type,apiBase,tokenEnvOrValue` (a brain
    can serve missions across multiple forges/instances). The per-host token is the credential.
  - Back-compat: the existing `CORRALAI_GIT_TOKEN` + `CORRALAI_GITHUB_API` continue to configure
    the `github.com` (or the default) provider.
- `cmd/corral`'s `repoAdapter`/engine wiring selects the provider from the mission's `Repo` host.
- `ParsePRNumber` uses the provider's `ChangeRequestPath()` (`/pull/` vs `/merge_requests/`).

### Push credential per forge
The token-in-URL push format varies: GitHub/Gitea `https://x-access-token:{tok}@host/...` (Gitea
also accepts `{user}:{tok}@`); GitLab `https://oauth2:{tok}@host/...`. The provider (or the host
config) supplies the credential-URL format; the token is injected only for the single push/clone
call and never persisted in `.git/config` (existing behavior).

## Error handling / edge cases
- **Unknown host / no forge config** → the repo can't be provisioned for forge ops; fail the
  mission provisioning with a clear "no forge configured for host X" error (git-only ops could
  still work, but a repo-work mission needs the PR/MR surface). Never a crash.
- **Missing per-forge token** → same as today's missing-token handling (mission repo ops disabled
  / clear error), per forge.
- **GitLab discussions pagination** → `ListReviews` must page the discussions endpoint (GitLab
  paginates) to not miss an unresolved thread. Bounded.
- **ETag** — GitHub/Gitea support conditional GET; GitLab may not on discussions → the provider
  returns `notModified=false` + empty etag (the engine tolerates this; just more polling).
- **Bot self-filter** — each provider's `AuthLogin` returns the bot's own login (GitHub/Gitea
  `/user`.login; GitLab `/user`.username) so the engine skips its own reviews/notes.
- **Existing GitHub missions unaffected** — the default provider + `CORRALAI_GIT_TOKEN`/
  `CORRALAI_GITHUB_API` path is preserved; a GitHub repo behaves exactly as before.

## Testing
- **Per-provider against a mock HTTP server** replaying each forge's real API shapes: GitHub
  (existing behavior preserved — refactor doesn't regress), Gitea (same shapes, its auth header),
  GitLab (MR create/find/state, discussions→reviews, notes).
- **The GitLab review mapping (load-bearing):** a mock MR with (a) an unresolved non-bot
  discussion → `ListReviews` yields a `CHANGES_REQUESTED` review (triggers the engine's rework);
  (b) all-resolved discussions → no CHANGES_REQUESTED; (c) only a bot's discussion → filtered;
  (d) `state:merged` → `GetPR` returns `merged=true`.
- **Forge selection by host:** `RepoIdent` parses github.com / gitlab.com / a self-hosted host
  from `CORRALAI_FORGES`; the right provider is chosen; an unknown host → clear error.
- **`ParsePRNumber`** handles `/pull/N` (GH/Gitea) and `/merge_requests/N` (GitLab).
- **Credential boundary:** each provider sends the correct auth header; the token isn't logged;
  the push URL uses the per-forge credential format; `.git/config` carries no token (existing test
  pattern).
- **GitHub regression:** the existing #15/#18 repo + review tests pass unchanged against the
  refactored GitHub provider.

## Out of scope (follow-ups)
- **Forges beyond GitHub/GitLab/Gitea** (Bitbucket, Azure DevOps, etc.) — the `Provider` interface
  makes them additive later.
- **GitLab EE approval-rule "request changes"** — v1 uses unresolved-discussions (works all tiers).
- **SSH-key auth** — HTTPS-token auth only (as today).
- **Per-mission forge override** — forge is derived from the repo host; no manual override.
- **GraphQL APIs** — REST only.
