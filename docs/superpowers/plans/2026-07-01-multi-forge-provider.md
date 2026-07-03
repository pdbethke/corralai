# Multi-Forge Provider (GitHub / GitLab / Gitea) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Missions can target GitHub, GitLab, and Gitea (incl. self-hosted) — a `repo.Provider` per forge behind the existing `mission.RepoOps` seam, selected by repo host, with the #18 review loop working on each and per-forge credentials.

**Architecture:** T1 extracts the `Provider` interface, refactors the existing GitHub code behind it, makes URL parsing host-aware, adds the forge registry + host selection, and threads the repo URL through `RepoOps` so selection works — preserving GitHub behavior. T2 adds Gitea (a thin GitHub-compatible variant). T3 adds GitLab (merge requests + notes + the unresolved-discussions→CHANGES_REQUESTED mapping).

**Tech Stack:** Go 1.26; `net/http` REST clients; `internal/repo`, `internal/mission`, `cmd/corral`.

## Global Constraints (bind every task)

- **The mission engine's review LOGIC is unchanged.** `newestActionable` (`State=="CHANGES_REQUESTED"` triggers rework), the watermark, the bot self-filter — all stay. Each provider NORMALIZES its native concepts to `ReviewInfo{State, SubmittedAt, User, Body}`. (The `RepoOps` method *signatures* change minimally to carry the repo URL/host — that's plumbing, not logic.)
- **Credential boundary per forge (unchanged posture):** each forge's token lives only in the brain, is scrubbed from env after load, injected into the push/clone URL only for the one network call (per-forge format), sent as the forge's auth header on API calls, never in `.git/config`, never logged.
- **GitHub behavior must not regress** — the existing #15/#18 repo + review tests pass against the refactored GitHub provider (T1). Back-compat: `CORRALAI_GIT_TOKEN` + `CORRALAI_GITHUB_API` still configure the default GitHub provider.
- **Git ops stay shared** (`Clone`/`Checkout`/`Commit`/`Push`/`ChangedFiles`/read/grep/snapshot) — forge-neutral, untouched except the push-credential URL format.
- **Fail-clear:** unknown host / no forge config / missing per-forge token → a clear error at mission provisioning, never a crash.
- `go build ./...` + `go test ./...` stay green each task.

## Per-forge REST summary (from the exploration)
| Op | GitHub / Gitea | GitLab |
|---|---|---|
| create CR | `POST /repos/{o}/{r}/pulls` | `POST /projects/{o%2Fr}/merge_requests` |
| find open | `GET .../pulls?head={o}:{h}&state=open` | `GET .../merge_requests?source_branch={h}&state=opened` |
| state | `GET .../pulls/{n}` → `{state, merged}` | `GET .../merge_requests/{iid}` → `state∈{opened,closed,locked,merged}` (merged := state=="merged") |
| reviews | `GET .../pulls/{n}/reviews` (native states) | `GET .../merge_requests/{iid}/discussions` → synth CHANGES_REQUESTED from unresolved non-bot threads |
| comment | `POST /repos/{o}/{r}/issues/{n}/comments` | `POST .../merge_requests/{iid}/notes` |
| whoami | `GET /user` → `login` | `GET /user` → `username` |
| auth hdr | `Authorization: Bearer {t}` (Gitea also `token {t}`) | `PRIVATE-TOKEN: {t}` |
| CR path | `pull` | `merge_requests` |

---

## Task 1: extract `Provider`, refactor GitHub behind it, host selection + registry

**Files:** Create `internal/repo/provider.go`, `internal/repo/provider_github.go`, `internal/repo/forges.go`; Modify `internal/repo/repo.go` (RepoIdent), `internal/repo/reviews.go`/`pr.go` (move into the github provider), `internal/mission/engine.go` (RepoOps sig), `internal/mission/store.go` (ParsePRNumber), `cmd/corral/main.go` (registry + adapter).

**Interfaces produced:**
- `type Provider interface { OpenPR(ctx, owner, repo, head, base, title, body string)(string,error); ListReviews(ctx, owner, repo string, cr int, etag string)([]Review,string,bool,error); ListReviewComments(ctx, owner, repo string, cr int)([]ReviewComment,error); GetPR(ctx, owner, repo string, cr int)(string,bool,error); PostComment(ctx, owner, repo string, cr int, body string) error; AuthLogin(ctx)(string,error); ChangeRequestPath() string; PushCredURL(repoURL, token string) string }`
- `func RepoIdent(repoURL string) (host, owner, repo string, err error)` (host-aware)
- `type ForgeConfig struct{ Type, APIBase, Token string }` + `func ForgesFromEnv() map[string]ForgeConfig` (host → config; defaults + `CORRALAI_FORGES`)
- `func (e *Engine) providerFor(host string) (Provider, error)`

**The seam change (minimal):** the `mission.RepoOps` methods that take `(owner, repo string)` also need the host to select the provider. Add the repo URL: change the `RepoOps` methods to take the mission's `repoURL string` (the engine already has `mission.Repo`); inside the adapter/engine, `RepoIdent(repoURL)` yields `(host, owner, repo)` → `providerFor(host)` → call. Thread `repoURL` through the ~handful of engine call sites (Tick/ReviewPoll). The engine's review *logic* doesn't change — only what it passes.

- [ ] **Step 1: Write failing tests**
  - `TestRepoIdentMultiHost`: parses `https://github.com/o/r(.git)`, `https://gitlab.com/o/r`, `git@github.com:o/r.git`, `https://gitea.example.com/o/r` → correct `(host, owner, repo)`.
  - `TestForgesFromEnv`: defaults give github.com→github(api.github.com), gitlab.com→gitlab(gitlab.com/api/v4); `CORRALAI_FORGES="gitea.example.com=gitea,https://gitea.example.com/api/v1,TOK"` adds that host; `CORRALAI_GIT_TOKEN`/`CORRALAI_GITHUB_API` back-compat sets the github token/base.
  - `TestGitHubProviderAgainstMock` (httptest server replaying the GitHub shapes): OpenPR→html_url; ListReviews→states + ETag/304; GetPR→state/merged; PostComment→201; AuthLogin→login. (Port/keep the existing GitHub tests, now via the provider.)
  - `TestParsePRNumberByForge`: `/pull/7`→7 (github/gitea), `/merge_requests/7`→7 (gitlab).

- [ ] **Step 2: Run red.**

- [ ] **Step 3: Implement**
  - `provider.go`: the `Provider` interface + the shared `Review`/`ReviewComment` types (reuse existing) + a shared `restClient` helper (base URL + auth-header func + Accept) so github/gitea share the REST core.
  - `provider_github.go`: move `OpenPR`/`findOpenPR`/`ListReviews`/`ListReviewComments`/`GetPR`/`PostComment`(was PostIssueComment)/`AuthLogin` here as a `githubProvider{restClient}`; `ChangeRequestPath()="pull"`; `PushCredURL(url,tok)` = `x-access-token:` form. Keep `ghGet` semantics as the shared `restClient.get`.
  - `repo.go` `RepoIdent`: host-aware parse (strip scheme, handle `git@host:` SSH form, split host / owner / repo, trim `.git`).
  - `forges.go`: `ForgeConfig` + `ForgesFromEnv` (defaults + `CORRALAI_FORGES` parse + back-compat env). `Engine.providerFor(host)`: look up the host config → construct the matching provider (github/gitea/gitlab) with its apiBase+token; unknown host → error.
  - `repo.Engine`: hold the forge registry; its `OpenPR`/`ListReviews`/... take `repoURL`, do `RepoIdent`→`providerFor(host)`→delegate. `mission.RepoOps` + `repoAdapter` + the engine call sites updated to pass `repoURL`.
  - `store.go` `ParsePRNumber`: accept `/pull/` and `/merge_requests/` (or take the provider's `ChangeRequestPath()`).
  - `cmd/corral`: build the registry via `repo.ForgesFromEnv()`; scrub all per-forge token env vars after load (extend `scrubSecrets`).

- [ ] **Step 4: Run green + build** — `go test ./internal/repo/ ./internal/mission/ ./...`; `go build ./...`. **The existing GitHub #15/#18 tests must pass unchanged (regression gate).**
- [ ] **Step 5: Commit** — `git commit -m "refactor(repo): extract Provider interface + GitHub provider + host-based forge registry (no engine logic change)"`

---

## Task 2: Gitea provider (GitHub-API-compatible thin variant)

**Files:** Create `internal/repo/provider_gitea.go`; test alongside.

- [ ] **Step 1: Failing test** — `TestGiteaProviderAgainstMock` (httptest replaying Gitea's shapes, which mirror GitHub): OpenPR (`/repos/{o}/{r}/pulls`), ListReviews (`/pulls/{n}/reviews`, states like GitHub), GetPR (`{state,merged}`), PostComment (`/issues/{n}/comments`), AuthLogin (`/user`.login). Assert the Gitea AUTH header (`Authorization: token {t}` or Bearer — verify against Gitea docs; support the one Gitea accepts) and `ChangeRequestPath()=="pull"`.

- [ ] **Step 2: Run red / Step 3: Implement** — `giteaProvider` reusing the shared `restClient` from T1 with Gitea's apiBase + auth header + Accept (Gitea doesn't require `vnd.github+json`). Since the endpoints/JSON match GitHub, most logic is the shared core; only the auth header + base differ. `providerFor` constructs it for `type==gitea`. `PushCredURL` = token-in-URL (Gitea accepts `{user}:{tok}@` or `x-access-token:`; pick one, document).

- [ ] **Step 4: Run green + build + commit** — `git commit -m "feat(repo): Gitea provider (GitHub-API-compatible)"`

---

## Task 3: GitLab provider (merge requests + notes + unresolved-discussions→CHANGES_REQUESTED)

**Files:** Create `internal/repo/provider_gitlab.go`; test alongside. This is the substantial task.

- [ ] **Step 1: Failing tests** (httptest replaying GitLab v4 shapes):
  - `OpenPR` → `POST /projects/{o%2Fr}/merge_requests` (project = URL-encoded `owner/repo`), body `{source_branch, target_branch, title, description}`, response `{web_url}`; on "MR exists" → find via `GET .../merge_requests?source_branch={h}&state=opened`.
  - `GetPR` → `GET .../merge_requests/{iid}` → `state`; return `(state, merged := state=="merged")`.
  - **`ListReviews` mapping (load-bearing test):** `GET .../merge_requests/{iid}/discussions` (paged). For each discussion that is **resolvable && not resolved && authored by a non-bot** (author.username != the bot's, from AuthLogin), synthesize `Review{State:"CHANGES_REQUESTED", SubmittedAt: latest note created_at (RFC3339), User: author.username, Body: note body}`. Tests: (a) an unresolved non-bot discussion → one CHANGES_REQUESTED review; (b) all discussions resolved → zero reviews; (c) only the bot's discussion → zero; (d) pagination across two pages of discussions → all considered.
  - `PostComment` → `POST .../merge_requests/{iid}/notes` body `{body}`.
  - `AuthLogin` → `GET /user` → `username`.
  - Auth header `PRIVATE-TOKEN: {token}` on every call; `ChangeRequestPath()=="merge_requests"`; `PushCredURL` = `https://oauth2:{tok}@host/...`.

- [ ] **Step 2: Run red / Step 3: Implement** — `gitlabProvider` (its own REST shapes; project path = `url.PathEscape(owner+"/"+repo)`; iid as the CR number). The discussions→reviews synthesis is the core: page `/discussions`, filter resolvable+unresolved+non-bot, emit CHANGES_REQUESTED reviews normalized to the engine's model. ETag likely unsupported on discussions → return `("", false)` (engine polls each time — fine). `providerFor` constructs it for `type==gitlab`.

- [ ] **Step 4: Run green + build** — `go test ./internal/repo/ ./...`; `go build ./...`.
- [ ] **Step 5: Commit** — `git commit -m "feat(repo): GitLab provider — merge requests + notes; unresolved discussions -> CHANGES_REQUESTED"`

---

## Final verification
- [ ] `go build ./...` clean; `go test ./...` all PASS.
- [ ] **GitHub regression:** the existing #15/#18 repo + review tests pass unchanged against the refactored GitHub provider.
- [ ] All three providers pass their mock-server tests; forge selection by host (github.com/gitlab.com/self-hosted via CORRALAI_FORGES) routes correctly; unknown host → clear error.
- [ ] **The GitLab review mapping** (the load-bearing behavior): an unresolved non-bot MR discussion yields a CHANGES_REQUESTED review that drives the #18 rework loop; resolved/bot-only → none.
- [ ] `ParsePRNumber` handles `/pull/` and `/merge_requests/`; `RepoIdent` parses all forge URL forms.
- [ ] Credential boundary per forge: correct auth header + push-cred format; tokens scrubbed from env, never logged, never in `.git/config`.
- [ ] The mission engine's review LOGIC is unchanged (only `RepoOps` plumbing carries the repo URL).
