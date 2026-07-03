# PR-Review-Response Loop — Design

**Status:** design · **Date:** 2026-07-01 · **Sub-project:** #18

## Where this fits

Repo-work mode (#15) makes the swarm open a pull request; a human then reviews it.
This sub-project closes the human-in-the-loop cycle: when a reviewer submits a
**`CHANGES_REQUESTED`** review on a swarm-opened PR, the swarm addresses the feedback
and pushes follow-up commits to the same branch — up to **3 rounds** — then parks and
hands back to the human. It is **poll-based** (no public webhook endpoint, which suits
the tunnel'd brain) and treats **resource governance as a first-class concern**, not a
bolt-on: an unbounded or naive poller would be an outbound DoS on the GitHub API, and
the response loop itself must never react to its own activity.

It builds directly on #15 (the mission → phase → commit → push → PR pipeline) and reuses
that entire pipeline; GitHub's review API is the only genuinely new surface.

## First principle: the brain polls; the credential/GitHub surface stays brain-only

Consistent with the whole system's spine, **the brain — not the bees — does the
polling and every GitHub call.** `ReviewPoll()` runs in the brain daemon (`cmd/corral`
ticker); a bee never contacts GitHub, never polls, and never sees the token. The brain
is the single trusted egress for anything privileged (token, git, GitHub API); bees are
confined workers that speak only MCP to the brain (plus their own model backend, and
local net-off build/test). This feature adds one more privileged-egress job — review
polling + replies — to the brain, and changes nothing about the bee boundary.

## Second principle: a response round reuses the existing pipeline; only GitHub is new

A response round is **not** new build machinery. The original mission's working copy at
`<workspace>/m<id>` (with its branch checked out) survives mission completion, and the
whole phase → verify-gate → per-phase-commit → push → PR pipeline already exists. A
review round therefore just **reopens the mission** with new phases derived from the
review, and the existing engine does the rest: on re-completion `finishRepoMission`
pushes (the PR auto-updates) and `findOpenPR` resolves the SAME PR. The only new code is
(a) reading reviews/comments from GitHub, (b) turning a review into phases, (c) posting a
reply, and (d) the bounded, governed poll that drives it.

## Architecture

```
mission done → PR opened (PRURL stored; review_rounds=0, review_watermark='', review_parked=false)

[cmd/corral, slow ticker ~60s]  Engine.ReviewPoll():
  for each mission in MissionsWithOpenPR()  (done AND repo!='' AND pr_url!='' AND !parked — bounded set):
    conditional GET /pulls/{n}          → merged or closed? → park, continue
    conditional GET /pulls/{n}/reviews  → new CHANGES_REQUESTED since watermark?   (ETag 304 ⇒ ~0 quota)
       none                     → continue
       yes & review_rounds < 3  → GET /pulls/{n}/comments → build phases from review body + inline comments
                                  ReopenForReview: status→running, append phases + enqueue tasks,
                                                   review_rounds++, watermark = review.submitted_at
                                  POST issue comment: "🐝 addressing your review (round N/3)…"
       yes & review_rounds ≥ 3  → POST issue comment: "reached the 3-round limit; needs a human";
                                  review_parked = true; watermark = review.submitted_at

[engine Tick, ~3s]  the reopened mission is 'running' again → phases → per-phase commit → done
                    → finishRepoMission pushes (PR auto-updates) → findOpenPR resolves the SAME PR
```

## Components

### 1. `internal/repo` — GitHub review reads + reply (extends the PR engine)

Bearer-auth calls mirroring the existing `OpenPR`/`findOpenPR` shape (token redacted from
every error). The PR **number** is parsed from the stored `PRURL` (`…/pull/7` → `7`).

- `ListReviews(ctx, owner, repo, prNumber int) ([]Review, error)` — GET
  `/repos/{o}/{r}/pulls/{n}/reviews`. `Review{ID int64; State, Body, SubmittedAt, User string}`.
- `ListReviewComments(ctx, owner, repo, prNumber int) ([]ReviewComment, error)` — GET
  `/repos/{o}/{r}/pulls/{n}/comments`. `ReviewComment{Path string; Line int; Body, User string}`.
- `GetPR(ctx, owner, repo, prNumber int) (state string, merged bool, err error)` — GET
  `/repos/{o}/{r}/pulls/{n}` (to detect merged/closed → park).
- `PostIssueComment(ctx, owner, repo, prNumber int, body string) error` — POST
  `/repos/{o}/{r}/issues/{n}/comments` (the swarm's reply; an **issue** comment, not a
  review, so it can never re-trigger the loop).
- `AuthLogin(ctx) (string, error)` — GET `/user` (the token's own login, fetched once and
  cached) so reviews authored by the swarm's own account are ignored.
- **Conditional requests:** the review-list GET accepts an ETag and returns a sentinel
  "not modified" on `304` so an unchanged PR costs ~0 quota. ETags are held in an
  in-memory map (rebuilt after a restart — a cache miss is at most one full fetch).

`internal/repo` still shells `git` and talks HTTP; the token stays a field, never logged.

### 2. `internal/mission` — review state + reopen

- `Mission` gains persisted columns (idempotent `ALTER`): `review_rounds INTEGER`,
  `review_watermark VARCHAR`, `review_parked BOOLEAN` (all defaulted), plus struct fields.
- `SetReviewState(id, rounds int, watermark string, parked bool) error`.
- `MissionsWithOpenPR() ([]Mission, error)` — `WHERE status='done' AND repo!='' AND
  pr_url!='' AND review_parked=false` (the bounded poll set).
- `ReopenForReview(id int64, phases []PhaseSpec) error` — in one transaction: set
  `status='running'`, append the new phase rows, enqueue their tasks (reusing the same
  plan→tasks path `CreateMission` uses), and `review_rounds++`. After this the engine's
  `View()`-computed status resumes the mission on the next `Tick`.

### 3. `internal/mission/engine.go` — `ReviewPoll()`

The orchestration in the Architecture diagram. It:
1. lists `MissionsWithOpenPR()`; for each, parses the PR number from `PRURL` and resolves
   `owner/repo` via `RepoIdent`.
2. `GetPR` → if merged/closed, `SetReviewState(parked=true)`, continue.
3. conditional `ListReviews` (ETag) → the newest `CHANGES_REQUESTED` review whose
   `SubmittedAt` > `review_watermark` and whose `User` ≠ the swarm's own login. None → continue.
4. if `review_rounds < 3`: `ListReviewComments`, build `[]PhaseSpec` (one phase per file
   touched by inline comments, plus a phase for the review body if non-empty; each phase's
   task instruction embeds the file:line + comment text and says "address this review
   feedback, then let the verify gate confirm the build"), `ReopenForReview`, advance the
   watermark, `PostIssueComment` the round-N acknowledgement.
5. else (`≥ 3`): `PostIssueComment` the round-limit message, `SetReviewState(parked=true)`,
   advance the watermark.

`RepoOps` (the engine's local interface) gains the new read/reply methods so the engine
stays decoupled from the `repo` package. Errors are logged and never crash the poll.

### 4. `cmd/corral` — the second ticker

A goroutine ticking every `CORRALAI_REVIEW_POLL_SEC` (default 60) calling
`engine.ReviewPoll()`, started only when the repo engine is enabled. Separate from the
~3s mission `Tick` so review polling is deliberately slow.

## Resource governance (the DoS pillar)

- **Slow cadence** (~60s), never per-`Tick`.
- **Bounded set:** only done-missions with an open, unparked PR — a small, naturally
  bounded list.
- **Conditional GETs (ETag):** an unchanged PR's review list returns `304` → ~0 quota.
- **Backoff:** on `403` / secondary-rate-limit, honor `Retry-After` and skip the cycle;
  a rate-limited poll never advances a watermark (so nothing is lost).
- **Watermark:** each review is acted on exactly once; re-polls of the same review are no-ops.
- **3-round cap → park:** bounds swarm compute per PR; a parked mission leaves the poll set.
- **Never react to self:** only `CHANGES_REQUESTED` reviews by a login ≠ the token's own
  account trigger a round; the swarm's replies are issue comments and its output is
  commits — neither is a review, so the loop cannot amplify itself.

Inbound, the trigger requires a human with repo review access and is capped at 3 rounds/PR,
so it is a trusted-collaborator surface, not a public one. Outbound, the governance above
keeps GitHub-API usage negligible for idle PRs.

**Enabled hardening (ops-facing, out of scope to implement here).** Because the brain is
the *sole* authenticated egress to GitHub (see the first principle), this feature adds no
new credentialed surface and instead reinforces one that can be pinned end to end: a GitHub
org/Enterprise **IP allow list** scoped to the brain's egress IP + a **fine-grained PAT or
GitHub App** scoped to the target repo(s) + a **host egress firewall** limiting the brain to
`api.github.com`/`github.com` (and the model/embed endpoints). The result is that the entire
GitHub/token blast radius collapses to one pinnable IP and one repo scope, regardless of
fleet size — a free consequence of the single-egress design, noted here so the deployment
takes advantage of it.

## Error handling / edge cases

- **API/network/5xx** → logged, skip this cycle, retry next (watermark unadvanced → idempotent).
- **Rate limit (403/secondary)** → backoff per `Retry-After`, skip cycle.
- **PR merged or closed** → `review_parked=true` (stop polling it).
- **Review body only, no inline comments** → a single phase built from the body.
- **`ReopenForReview` failure** → logged, watermark NOT advanced (retried next poll).
- **Malformed/absent PR number in `PRURL`** → logged, mission skipped (can't happen for a
  PR the engine itself opened, but guarded).
- **No token / repo disabled** → `ReviewPoll` is a no-op (same gating as the repo engine).
- **Self-authored review** (token account) → ignored, watermark advanced (so it doesn't block).

## Testing

- **`internal/repo`:** `ListReviews`/`ListReviewComments`/`GetPR`/`PostIssueComment` against
  a stub server (assert URL, method, Bearer header, parsed fields); the `304`/ETag
  "not-modified" path; `GetPR` merged/closed; token redaction on a non-2xx error.
- **`internal/mission`:** the three columns round-trip; `MissionsWithOpenPR` filters
  correctly (excludes parked / no-PR / running); `ReopenForReview` flips status to running,
  appends phases, enqueues tasks, bumps `review_rounds`, in one transaction; migration
  idempotent on re-open.
- **engine `ReviewPoll` (fake `RepoOps` spy):**
  - a new `CHANGES_REQUESTED` review (rounds 0) → mission reopened (running, rounds=1,
    phases appended, ack comment posted), watermark advanced;
  - a second poll with no newer review → no-op (no reopen, no comment);
  - `review_rounds==3` + a new request → limit comment posted, `parked=true`, no reopen;
  - merged PR → parked, no reopen;
  - **a review authored by the swarm's own login → ignored** (the self-trigger guard);
  - a `304`/not-modified review list → no-op with no `ListReviewComments` call (quota guard).
  The self-trigger guard and the no-op-on-unchanged tests are the correctness/DoS-critical ones.

## Out of scope (follow-ups)

- **Webhook trigger** (push-based) instead of polling.
- **Inline per-comment replies** and **resolving review threads** (GitHub GraphQL) rather
  than one summary issue comment.
- **Non-GitHub providers** (GitLab/Gitea).
- **Reviewer-intent classification** (deciding a comment is a question vs. a change request
  beyond the `CHANGES_REQUESTED` signal).
