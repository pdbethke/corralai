# PR-Review-Response Loop Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** On a `CHANGES_REQUESTED` review of a swarm-opened PR, the brain reopens the mission with review-derived phases and pushes follow-up commits to the same branch — up to 3 rounds, then parks — polling GitHub on a slow, governed cadence.

**Architecture:** Extend `internal/repo` with GitHub review reads + a reply (mirroring `OpenPR`/`findOpenPR`). Add review-state columns + `ReopenForReview` to `internal/mission`. A new `Engine.ReviewPoll()` orchestrates: list done-missions with an open PR, detect a new `CHANGES_REQUESTED` review (ETag-conditional, watermark-gated, never self-authored), reopen the mission with phases built from the review, reply, and let the existing Tick pipeline commit → push → update the PR. A slow ticker in `cmd/corral` drives it.

**Tech Stack:** Go 1.26; GitHub REST over `net/http`; the #15 repo/mission engine (already on this branch's base `main`).

## Global Constraints

- **The brain polls; bees never touch GitHub or the token.** `ReviewPoll` runs in the brain daemon; the token stays a `repo.Engine` field, never logged (redact via `e.redact`).
- **Trigger only on `CHANGES_REQUESTED`** reviews, authored by a login ≠ the token's own account (self-trigger guard).
- **3-round cap → park.** After 3 reopen cycles on one PR, post a limit comment and stop.
- **Reuse the existing pipeline:** a response round reopens the mission (status → running, append phases, bump rounds) and relies on the existing per-phase commit → `finishRepoMission` push/PR (`OpenPR` already resolves an existing PR via `findOpenPR`).
- **Resource governance is first-class:** slow cadence (~60s), bounded set (done + open-PR + !parked), ETag conditional GETs (304 ⇒ ~0 quota), backoff on 403, per-PR watermark (each review acted on once, idempotent).
- **Errors log and never crash the poll.** `go build ./...` and `go test ./...` stay green each task.

---

## File Structure

- `internal/repo/reviews.go` (create) — `ListReviews`/`ListReviewComments`/`GetPR`/`PostIssueComment`/`AuthLogin` + ETag; types `Review`/`ReviewComment`.
- `internal/repo/reviews_test.go` (create) — stub-server tests incl. the 304 path.
- `internal/mission/store.go` (modify) — review columns + `SetReviewState`/`MissionsWithOpenPR`/`ReopenForReview` + `ParsePRNumber`.
- `internal/mission/store_test.go` (modify) — column round-trip, filter, reopen.
- `internal/mission/engine.go` (modify) — extend `RepoOps`; add `Engine.ReviewPoll` + review→phases + self-guard/watermark/cap.
- `internal/mission/review_test.go` (create) — `ReviewPoll` with a fake `RepoOps`.
- `cmd/corral/main.go` (modify) — the review-poll ticker.

---

## Task 1: `internal/repo` — GitHub review reads + reply

**Files:** Create `internal/repo/reviews.go`, `internal/repo/reviews_test.go`

**Interfaces:**
- Consumes (existing): `Engine`, `e.token`, `e.apiBase`, `e.redact`.
- Produces:
  - `type Review struct{ ID int64; State, Body, SubmittedAt, User string }`
  - `type ReviewComment struct{ Path string; Line int; Body, User string }`
  - `func (e *Engine) ListReviews(ctx context.Context, owner, repo string, prNumber int, etag string) (reviews []Review, newETag string, notModified bool, err error)`
  - `func (e *Engine) ListReviewComments(ctx context.Context, owner, repo string, prNumber int) ([]ReviewComment, error)`
  - `func (e *Engine) GetPR(ctx context.Context, owner, repo string, prNumber int) (state string, merged bool, err error)`
  - `func (e *Engine) PostIssueComment(ctx context.Context, owner, repo string, prNumber int, body string) error`
  - `func (e *Engine) AuthLogin(ctx context.Context) (string, error)`

- [ ] **Step 1: Write the failing test**

```go
// internal/repo/reviews_test.go
package repo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListReviewsAndConditional(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/repos/o/r/pulls/7/reviews" {
			if r.Header.Get("If-None-Match") == `"etag123"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"etag123"`)
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": 11, "state": "CHANGES_REQUESTED", "body": "fix the naming", "submitted_at": "2026-07-01T10:00:00Z", "user": map[string]any{"login": "alice"}},
			})
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	e := New("tok", srv.URL)
	revs, etag, nm, err := e.ListReviews(context.Background(), "o", "r", 7, "")
	if err != nil || nm || len(revs) != 1 {
		t.Fatalf("first list: revs=%v nm=%v err=%v", revs, nm, err)
	}
	if revs[0].State != "CHANGES_REQUESTED" || revs[0].User != "alice" || revs[0].ID != 11 {
		t.Fatalf("parsed wrong: %+v", revs[0])
	}
	if etag != `"etag123"` {
		t.Fatalf("etag not captured: %q", etag)
	}
	// second call WITH the etag → 304, no body
	_, _, nm2, err := e.ListReviews(context.Background(), "o", "r", 7, etag)
	if err != nil || !nm2 {
		t.Fatalf("conditional should be not-modified: nm=%v err=%v", nm2, err)
	}
}

func TestGetPRAndPostComment(t *testing.T) {
	var posted string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/o/r/pulls/7":
			json.NewEncoder(w).Encode(map[string]any{"state": "open", "merged": false})
		case r.Method == "POST" && r.URL.Path == "/repos/o/r/issues/7/comments":
			var in map[string]any
			json.NewDecoder(r.Body).Decode(&in)
			posted, _ = in["body"].(string)
			w.WriteHeader(201)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	e := New("tok", srv.URL)
	state, merged, err := e.GetPR(context.Background(), "o", "r", 7)
	if err != nil || state != "open" || merged {
		t.Fatalf("GetPR: %s %v %v", state, merged, err)
	}
	if err := e.PostIssueComment(context.Background(), "o", "r", 7, "hello"); err != nil {
		t.Fatal(err)
	}
	if posted != "hello" {
		t.Fatalf("comment body = %q", posted)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/repo/ -run 'Reviews|GetPR'`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement `internal/repo/reviews.go`**

```go
package repo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type Review struct {
	ID          int64
	State       string
	Body        string
	SubmittedAt string
	User        string
}
type ReviewComment struct {
	Path string
	Line int
	Body string
	User string
}

// get performs a Bearer GET, returning the body bytes and the response (for headers).
// The token is redacted from any error.
func (e *Engine) ghGet(ctx context.Context, url, etag string) (body []byte, resp *http.Response, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if e.token != "" {
		req.Header.Set("Authorization", "Bearer "+e.token)
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode == http.StatusNotModified {
		resp.Body.Close()
		return nil, resp, nil
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, resp, fmt.Errorf("GET %s: %s: %s", url, resp.Status, e.redact(string(b)))
	}
	return b, resp, nil
}

func (e *Engine) ListReviews(ctx context.Context, owner, repo string, prNumber int, etag string) ([]Review, string, bool, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/reviews", e.apiBase, owner, repo, prNumber)
	b, resp, err := e.ghGet(ctx, url, etag)
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

func (e *Engine) ListReviewComments(ctx context.Context, owner, repo string, prNumber int) ([]ReviewComment, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/comments", e.apiBase, owner, repo, prNumber)
	b, _, err := e.ghGet(ctx, url, "")
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

func (e *Engine) GetPR(ctx context.Context, owner, repo string, prNumber int) (string, bool, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", e.apiBase, owner, repo, prNumber)
	b, _, err := e.ghGet(ctx, url, "")
	if err != nil {
		return "", false, err
	}
	var out struct {
		State  string `json:"state"`
		Merged bool   `json:"merged"`
	}
	json.Unmarshal(b, &out)
	return out.State, out.Merged, nil
}

func (e *Engine) AuthLogin(ctx context.Context) (string, error) {
	b, _, err := e.ghGet(ctx, e.apiBase+"/user", "")
	if err != nil {
		return "", err
	}
	var out struct {
		Login string `json:"login"`
	}
	json.Unmarshal(b, &out)
	return out.Login, nil
}

func (e *Engine) PostIssueComment(ctx context.Context, owner, repo string, prNumber int, body string) error {
	payload, _ := json.Marshal(map[string]any{"body": body})
	url := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments", e.apiBase, owner, repo, prNumber)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.github+json")
	if e.token != "" {
		req.Header.Set("Authorization", "Bearer "+e.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("post comment: %s: %s", resp.Status, e.redact(string(b)))
	}
	return nil
}
```

- [ ] **Step 4: Run tests + commit**

Run: `go test ./internal/repo/ && go build ./...`
Expected: PASS; build OK.

```bash
git add internal/repo/reviews.go internal/repo/reviews_test.go
git commit -m "feat(repo): GitHub review reads (ETag-conditional) + issue-comment reply"
```

---

## Task 2: mission review columns + queries + PR-number parse

**Files:** Modify `internal/mission/store.go`; Test `internal/mission/store_test.go`

**Interfaces:**
- Produces: `Mission` gains `ReviewRounds int`, `ReviewWatermark string`, `ReviewParked bool`;
  `func (s *Store) SetReviewState(id int64, rounds int, watermark string, parked bool) error`;
  `func (s *Store) MissionsWithOpenPR() ([]Mission, error)`;
  `func ParsePRNumber(prURL string) (int, error)`.

- [ ] **Step 1: Write the failing test**

```go
// internal/mission/store_test.go — add (reuse the existing store+queue helpers)
func TestReviewStateAndOpenPRFilter(t *testing.T) {
	dir := t.TempDir()
	s := openMissionStore(t, dir) // use whatever the existing tests use
	q := openQueue(t, dir)
	id, _ := CreateMission(s, q, "build calc", DefaultPlan("build calc"), false)
	s.SetMissionStatus(id, "done")
	s.SetRepo(id, "https://github.com/o/r", "main", "corralai/m1")
	s.SetPRURL(id, "https://github.com/o/r/pull/7")

	// not in the poll set until it has an open PR — it does now; parked excludes it
	got, err := s.MissionsWithOpenPR()
	if err != nil || len(got) != 1 || got[0].ID != id {
		t.Fatalf("open-PR set: %v err=%v", got, err)
	}
	if err := s.SetReviewState(id, 2, "2026-07-01T10:00:00Z", false); err != nil {
		t.Fatal(err)
	}
	m, _ := s.Mission(id)
	if m.ReviewRounds != 2 || m.ReviewWatermark != "2026-07-01T10:00:00Z" || m.ReviewParked {
		t.Fatalf("review state not persisted: %+v", m)
	}
	s.SetReviewState(id, 3, "x", true) // parked → leaves the set
	if got, _ := s.MissionsWithOpenPR(); len(got) != 0 {
		t.Fatalf("parked mission must leave the open-PR set, got %v", got)
	}
	if n, err := ParsePRNumber("https://github.com/o/r/pull/7"); err != nil || n != 7 {
		t.Fatalf("ParsePRNumber = %d err=%v", n, err)
	}
}
```

> NOTE: use the same store/queue test helpers the existing `store_test.go` uses (see `TestMissionRepoFields`). `SetMissionStatus`/`SetRepo`/`SetPRURL` already exist.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/mission/ -run TestReviewState`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

In `internal/mission/store.go`:
(a) Migrations in `Open` (mirror the existing idempotent `ALTER` block):
```go
	s.db.Exec(`ALTER TABLE missions ADD COLUMN review_rounds INTEGER NOT NULL DEFAULT 0`)
	s.db.Exec(`ALTER TABLE missions ADD COLUMN review_watermark VARCHAR NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE missions ADD COLUMN review_parked BOOLEAN NOT NULL DEFAULT false`)
```
(b) `Mission` struct fields:
```go
	ReviewRounds   int    `json:"review_rounds,omitempty"`
	ReviewWatermark string `json:"review_watermark,omitempty"`
	ReviewParked   bool   `json:"review_parked,omitempty"`
```
(c) Append `review_rounds, review_watermark, review_parked` to the SELECT column list + scan in BOTH `Mission(id)` and `queryMissions` (the helper `RunningMissions`/`MissionsPendingPR` share). Follow exactly how the #15 repo columns were added.
(d) Methods:
```go
func (s *Store) SetReviewState(id int64, rounds int, watermark string, parked bool) error {
	_, err := s.db.Exec(`UPDATE missions SET review_rounds=?, review_watermark=?, review_parked=?, updated_ts=? WHERE id=?`,
		rounds, watermark, parked, now(), id)
	return err
}

func (s *Store) MissionsWithOpenPR() ([]Mission, error) {
	return s.queryMissions("WHERE status='done' AND repo!='' AND pr_url!='' AND review_parked=false ORDER BY id")
}
```
(e) In a suitable spot (e.g. near `MissionDir`):
```go
// ParsePRNumber extracts the numeric PR id from a GitHub PR html_url (…/pull/7).
func ParsePRNumber(prURL string) (int, error) {
	i := strings.LastIndex(prURL, "/pull/")
	if i < 0 {
		return 0, fmt.Errorf("no /pull/ in %q", prURL)
	}
	n, err := strconv.Atoi(strings.TrimRight(prURL[i+len("/pull/"):], "/"))
	if err != nil {
		return 0, fmt.Errorf("bad PR number in %q: %w", prURL, err)
	}
	return n, nil
}
```
(`strconv` may need importing.)

- [ ] **Step 4: Run tests + commit**

Run: `go test ./internal/mission/`
Expected: PASS.

```bash
git add internal/mission/store.go internal/mission/store_test.go
git commit -m "feat(mission): review-state columns + MissionsWithOpenPR + ParsePRNumber"
```

---

## Task 3: mission `ReopenForReview`

**Files:** Modify `internal/mission/store.go`; Test `internal/mission/store_test.go`

**Interfaces:**
- Consumes: `PhaseSpec`, `planToTasks`, `queue.Store.Enqueue`, `SetReviewState` (Task 2).
- Produces: `func (s *Store) ReopenForReview(q *queue.Store, id int64, phases []PhaseSpec, watermark string) error` — flips status → running, appends the phases (positions after the current max) with round-scoped names, enqueues their tasks, bumps `review_rounds`, advances `review_watermark`.

- [ ] **Step 1: Write the failing test**

```go
// internal/mission/store_test.go — add
func TestReopenForReview(t *testing.T) {
	dir := t.TempDir()
	s := openMissionStore(t, dir)
	q := openQueue(t, dir)
	id, _ := CreateMission(s, q, "build calc", DefaultPlan("build calc"), false)
	s.SetMissionStatus(id, "done")

	before, _ := s.Phases(id)
	phases := []PhaseSpec{{Name: "review-r1-fix", Instruction: "address: rename Foo", Count: 1, Verify: "go build ./..."}}
	if err := s.ReopenForReview(q, id, phases, "2026-07-01T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	m, _ := s.Mission(id)
	if m.Status != "running" || m.ReviewRounds != 1 || m.ReviewWatermark != "2026-07-01T10:00:00Z" {
		t.Fatalf("reopen state: %+v", m)
	}
	after, _ := s.Phases(id)
	if len(after) != len(before)+1 {
		t.Fatalf("expected one appended phase, before=%d after=%d", len(before), len(after))
	}
	// the appended phase's tasks are enqueued (a ready/pending task exists for it)
	if !queueHasTaskForPhase(t, q, id, "review-r1-fix") { // small test helper: scans q for a task whose title carries the phase
		t.Fatal("review phase tasks were not enqueued")
	}
}
```

> NOTE: `queueHasTaskForPhase` — write a tiny helper that lists the mission's tasks from `q` and checks one carries the phase name (task titles carry the phase name per `planToTasks`). Reuse whatever listing the queue package exposes in its tests.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/mission/ -run TestReopenForReview`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

```go
// ReopenForReview reopens a done mission for a review-response round: appends the
// given phases (after the current max position), enqueues their tasks, flips the
// mission back to running, bumps review_rounds and advances the watermark — in one
// transaction. Phase names must be round-unique (caller prefixes "review-r<N>-…")
// so planToTasks keys don't collide with the mission's existing tasks.
func (s *Store) ReopenForReview(q *queue.Store, id int64, phases []PhaseSpec, watermark string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var maxPos sql.NullInt64
	if err := tx.QueryRow(`SELECT max(position) FROM phases WHERE mission_id=?`, id).Scan(&maxPos); err != nil {
		return err
	}
	pos := int(maxPos.Int64) + 1
	for _, p := range phases {
		c := p.Count
		if c <= 0 {
			c = 1
		}
		if _, err := tx.Exec(`INSERT INTO phases(mission_id,name,role,program,instruction,depends_on,count,status,position)
			VALUES(?,?,?,?,?,?,?, 'pending', ?)`,
			id, p.Name, p.Role, p.Program, p.Instruction, strings.Join(p.DependsOn, ","), c, pos); err != nil {
			return err
		}
		pos++
	}
	var rounds int
	if err := tx.QueryRow(`SELECT review_rounds FROM missions WHERE id=?`, id).Scan(&rounds); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE missions SET status='running', review_rounds=?, review_watermark=?, updated_ts=? WHERE id=?`,
		rounds+1, watermark, now(), id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// enqueue outside the mission-store tx (queue is a separate store/DB)
	return q.Enqueue(id, planToTasks(phases))
}
```
> Implementer: confirm `queue.Store.Enqueue(missionID, []queue.TaskSpec)` **appends** (does not replace) — it does in `CreateMission`'s single call, but verify inserting a second batch for the same mission adds rows. If `Enqueue` needs the phase list de-duplicated against existing task keys, the round-scoped names already guarantee uniqueness.

- [ ] **Step 4: Run tests + commit**

Run: `go test ./internal/mission/`
Expected: PASS.

```bash
git add internal/mission/store.go internal/mission/store_test.go
git commit -m "feat(mission): ReopenForReview — append review phases + reopen + enqueue"
```

---

## Task 4: engine `ReviewPoll` + review→phases + governance guards

**Files:** Modify `internal/mission/engine.go`; Create `internal/mission/review_test.go`

**Interfaces:**
- Consumes: `RepoOps` (extended), `Store.{MissionsWithOpenPR,ReopenForReview,SetReviewState,Mission}`, `ParsePRNumber`, `repo.Review`/`repo.ReviewComment` (via the interface — see below).
- Produces: `RepoOps` gains the review methods; `func (e *Engine) ReviewPoll() error`; `Engine` gains `etags map[int64]string` + `botLogin string` (lazy) + `ReviewPollCtx context.Context` (optional).

- [ ] **Step 1: Write the failing test**

```go
// internal/mission/review_test.go
package mission
// Build the existing engine test harness (store+queue+fakeRepo). Extend fakeRepo with:
//   ListReviews(ctx, o, r, n, etag) ([]repo.Review, string, bool, error)
//   ListReviewComments(...) ([]repo.ReviewComment, error)
//   GetPR(...) (string, bool, error)  ; PostIssueComment(...) error ; AuthLogin(ctx) (string, error)
// Seed a done repo mission with PRURL=".../pull/7". Cases (each its own sub-test or fresh mission):
//  1) fakeRepo returns one CHANGES_REQUESTED review by "alice", rounds=0 →
//     after ReviewPoll: mission Status=="running", ReviewRounds==1, a phase appended,
//     PostIssueComment called once, watermark advanced.
//  2) second ReviewPoll with NO newer review (same submitted_at ≤ watermark) → no reopen, no comment.
//  3) review authored by the BOT's own login (AuthLogin) → ignored (no reopen).  [self-trigger guard]
//  4) rounds already 3 + a new CHANGES_REQUESTED → parked=true, limit comment posted, NO reopen.
//  5) GetPR returns state="closed" (or merged) → parked, no reopen.
//  6) ListReviews returns notModified=true → no ListReviewComments call, no reopen (quota guard).
//
// Implementer: the fake records calls (comment count, reopen via reading the store back).
// Use repo.Review/repo.ReviewComment as the interface's types (import internal/repo in the TEST;
// engine.go references them only through the RepoOps interface signatures — see the note in Step 3).
```

> Implementer: flesh out with the existing engine-test harness. The six cases above are the contract; cases 3 (self-guard) and 6 (not-modified quota) are the security/DoS-critical assertions.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/mission/ -run TestReviewPoll`
Expected: FAIL — `ReviewPoll` undefined.

- [ ] **Step 3: Implement**

`internal/mission/engine.go`:

(a) The `RepoOps` interface gains the review methods. Because they use `repo.Review`/`repo.ReviewComment`, and the mission package must stay decoupled from `repo`, define **local aliases** in the mission package that mirror those structs, and have `fakeRepo` + `*repo.Engine` use them. Simplest that avoids the import: declare the review methods on `RepoOps` returning mission-local types:
```go
type ReviewInfo struct{ ID int64; State, Body, SubmittedAt, User string }
type ReviewCommentInfo struct{ Path string; Line int; Body, User string }

type RepoOps interface {
	Commit(ctx context.Context, dir, msg string) (bool, error)
	Push(ctx context.Context, dir, branch string) error
	OpenPR(ctx context.Context, owner, repo, head, base, title, body string) (string, error)
	RepoIdent(repoURL string) (owner, repo string, err error)
	ChangedFiles(ctx context.Context, dir string) ([]string, error)
	ListReviews(ctx context.Context, owner, repo string, pr int, etag string) ([]ReviewInfo, string, bool, error)
	ListReviewComments(ctx context.Context, owner, repo string, pr int) ([]ReviewCommentInfo, error)
	GetPR(ctx context.Context, owner, repo string, pr int) (state string, merged bool, err error)
	PostIssueComment(ctx context.Context, owner, repo string, pr int, body string) error
	AuthLogin(ctx context.Context) (string, error)
}
```
Then in `cmd/corral` the `*repo.Engine` is wrapped by a thin adapter that converts `[]repo.Review` → `[]mission.ReviewInfo` (Task 5), OR — simpler and preferred — change `internal/repo`'s methods to return the mission-agnostic field set already (they do: `repo.Review` and `mission.ReviewInfo` have identical fields; write a 4-line adapter in cmd/corral). Pick the adapter approach so neither package imports the other.

(b) `Engine` fields: `etags map[int64]string` (init in `NewEngine`), `botLogin string`, `botOnce sync.Once`.

(c) `ReviewPoll`:
```go
const maxReviewRounds = 3

func (e *Engine) ReviewPoll() error {
	if e.Repo == nil {
		return nil
	}
	ctx := context.Background()
	missions, err := e.m.MissionsWithOpenPR()
	if err != nil {
		return err
	}
	// resolve the bot's own login once (self-trigger guard); tolerate failure (empty ⇒ no filter)
	e.botOnce.Do(func() { e.botLogin, _ = e.Repo.AuthLogin(ctx) })
	for _, mi := range missions {
		pr, perr := ParsePRNumber(mi.PRURL)
		if perr != nil {
			log.Printf("review: mission %d: %v", mi.ID, perr)
			continue
		}
		owner, rp, ierr := e.Repo.RepoIdent(mi.Repo)
		if ierr != nil {
			continue
		}
		// stop if the PR is gone
		if state, merged, gerr := e.Repo.GetPR(ctx, owner, rp, pr); gerr == nil && (merged || state == "closed") {
			_ = e.m.SetReviewState(mi.ID, mi.ReviewRounds, mi.ReviewWatermark, true)
			continue
		}
		revs, etag, notMod, lerr := e.Repo.ListReviews(ctx, owner, rp, pr, e.etags[mi.ID])
		if lerr != nil {
			log.Printf("review: mission %d list: %v", mi.ID, lerr)
			continue
		}
		e.etags[mi.ID] = etag
		if notMod {
			continue // 304 — nothing new, ~0 quota
		}
		rev, ok := newestActionable(revs, mi.ReviewWatermark, e.botLogin)
		if !ok {
			continue
		}
		if mi.ReviewRounds >= maxReviewRounds {
			_ = e.Repo.PostIssueComment(ctx, owner, rp, pr,
				fmt.Sprintf("🐝 This PR has reached the %d-round auto-response limit — handing back to a human.", maxReviewRounds))
			_ = e.m.SetReviewState(mi.ID, mi.ReviewRounds, rev.SubmittedAt, true)
			continue
		}
		comments, _ := e.Repo.ListReviewComments(ctx, owner, rp, pr)
		phases := reviewPhases(mi.ReviewRounds+1, rev, comments)
		if err := e.m.ReopenForReview(e.q, mi.ID, phases, rev.SubmittedAt); err != nil {
			log.Printf("review: mission %d reopen: %v", mi.ID, err)
			continue // watermark NOT advanced → retried next poll
		}
		_ = e.Repo.PostIssueComment(ctx, owner, rp, pr,
			fmt.Sprintf("🐝 Addressing your review (round %d/%d)…", mi.ReviewRounds+1, maxReviewRounds))
		log.Printf("review: mission %d reopened for round %d", mi.ID, mi.ReviewRounds+1)
	}
	return nil
}

// newestActionable returns the newest CHANGES_REQUESTED review submitted after the
// watermark and NOT authored by the bot; ok=false if none.
func newestActionable(revs []ReviewInfo, watermark, botLogin string) (ReviewInfo, bool) {
	var best ReviewInfo
	found := false
	for _, r := range revs {
		if r.State != "CHANGES_REQUESTED" {
			continue
		}
		if botLogin != "" && r.User == botLogin {
			continue // never react to our own reviews
		}
		if r.SubmittedAt <= watermark { // RFC3339 sorts lexically
			continue
		}
		if !found || r.SubmittedAt > best.SubmittedAt {
			best, found = r, true
		}
	}
	return best, found
}

// reviewPhases turns a review + its inline comments into round-scoped phases (one per
// touched file, plus one for the review body), each gated on `go build ./...` so the
// verify gate confirms the fix compiles before the phase's task can complete.
func reviewPhases(round int, rev ReviewInfo, comments []ReviewCommentInfo) []PhaseSpec {
	byFile := map[string][]string{}
	for _, c := range comments {
		byFile[c.Path] = append(byFile[c.Path], fmt.Sprintf("- %s:%d — %s", c.Path, c.Line, c.Body))
	}
	var phases []PhaseSpec
	for file, notes := range byFile {
		phases = append(phases, PhaseSpec{
			Name:        fmt.Sprintf("review-r%d-%s", round, sanitizePhase(file)),
			Instruction: "Address these review comments, then let the verify gate confirm the build:\n" + strings.Join(notes, "\n"),
			Count:       1,
			Verify:      "go build ./...",
		})
	}
	if strings.TrimSpace(rev.Body) != "" {
		phases = append(phases, PhaseSpec{
			Name:        fmt.Sprintf("review-r%d-summary", round),
			Instruction: "Address this review feedback, then let the verify gate confirm the build:\n" + rev.Body,
			Count:       1,
			Verify:      "go build ./...",
		})
	}
	if len(phases) == 0 { // a CHANGES_REQUESTED with no body/comments — make one generic phase
		phases = append(phases, PhaseSpec{
			Name:        fmt.Sprintf("review-r%d-address", round),
			Instruction: "A reviewer requested changes on this PR. Re-examine the diff, address the concerns, and let the verify gate confirm the build.",
			Count:       1, Verify: "go build ./...",
		})
	}
	return phases
}

func sanitizePhase(s string) string {
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, ".", "-")
	return s
}
```
Add imports `sync`, `fmt`, `strings` as needed. `e.q` is the queue store the engine already holds (confirm the field name; `NewEngine` receives it).

**Rate-limit handling:** every GitHub call error (including a `403`/secondary-rate-limit) is
logged and the mission is skipped for this cycle — the ≥60s ticker cadence (Task 5) is the
baseline backoff, so a rate-limited poll never hammers and never advances a watermark
(nothing lost). Honoring an explicit `Retry-After`/`X-RateLimit-Reset` for a longer,
precise backoff is a deliberate follow-up; the cadence bound is sufficient for the bounded
open-PR set.

- [ ] **Step 4: Run tests + commit**

Run: `go test ./internal/mission/ && go build ./...`
Expected: PASS; build OK.

```bash
git add internal/mission/engine.go internal/mission/review_test.go
git commit -m "feat(mission): ReviewPoll — CHANGES_REQUESTED → reopen, self-guard, watermark, 3-round cap"
```

---

## Task 5: cmd/corral — review-poll ticker + repo adapter

**Files:** Modify `cmd/corral/main.go`

**Interfaces:**
- Consumes: `Engine.ReviewPoll`; `*repo.Engine`'s new methods (via an adapter to `mission.RepoOps`).

- [ ] **Step 1: The adapter + wiring**

The engine's `Repo` field is `mission.RepoOps`; `*repo.Engine` already satisfies the pre-existing methods but its review methods return `repo.Review`/`repo.ReviewComment`, not `mission.ReviewInfo`. Add a tiny adapter in `cmd/corral` that wraps `*repo.Engine` and converts the two slice types (identical fields), and assign the adapter to `engine.Repo`:
```go
type repoAdapter struct{ *repo.Engine }

func (a repoAdapter) ListReviews(ctx context.Context, o, r string, pr int, etag string) ([]mission.ReviewInfo, string, bool, error) {
	revs, et, nm, err := a.Engine.ListReviews(ctx, o, r, pr, etag)
	out := make([]mission.ReviewInfo, len(revs))
	for i, v := range revs {
		out[i] = mission.ReviewInfo{ID: v.ID, State: v.State, Body: v.Body, SubmittedAt: v.SubmittedAt, User: v.User}
	}
	return out, et, nm, err
}
func (a repoAdapter) ListReviewComments(ctx context.Context, o, r string, pr int) ([]mission.ReviewCommentInfo, error) {
	cs, err := a.Engine.ListReviewComments(ctx, o, r, pr)
	out := make([]mission.ReviewCommentInfo, len(cs))
	for i, c := range cs {
		out[i] = mission.ReviewCommentInfo{Path: c.Path, Line: c.Line, Body: c.Body, User: c.User}
	}
	return out, err
}
```
Where `engine.Repo = repoEng` was set (#15), set `engine.Repo = repoAdapter{repoEng}` instead (the adapter forwards all the other methods via the embedded `*repo.Engine`).

- [ ] **Step 2: The ticker**

Near the mission `Tick` ticker (`main.go` ~413), add a slower review-poll ticker, only when the repo engine is enabled:
```go
	if repoEng != nil {
		reviewInterval := time.Duration(envInt("CORRALAI_REVIEW_POLL_SEC", 60)) * time.Second
		log.Printf("review: polling PRs for CHANGES_REQUESTED every %s", reviewInterval)
		go func() {
			t := time.NewTicker(reviewInterval)
			defer t.Stop()
			for range t.C {
				if err := engine.ReviewPoll(); err != nil {
					log.Printf("review poll: %v", err)
				}
			}
		}()
	}
```
(`envInt` — reuse the existing int-env helper if present; otherwise `strconv.Atoi(env(...))` with a default. Add the doc-comment env line `CORRALAI_REVIEW_POLL_SEC` near the others.)

- [ ] **Step 3: Build + commit**

Run: `go build ./... && go test ./...`
Expected: build OK; all tests PASS.

```bash
git add cmd/corral/main.go
git commit -m "feat(corral): review-poll ticker + repo adapter for mission.RepoOps"
```

---

## Final verification

- [ ] `go build ./...` — OK
- [ ] `go test ./...` — all PASS
- [ ] Governance: the poll is on a ≥60s ticker (not per-Tick); `ListReviews` sends `If-None-Match` and a 304 short-circuits before `ListReviewComments`; the self-trigger guard filters the bot's own login; the watermark advances only on a handled review; the 3-round cap parks.
- [ ] Boundary: the token is only in `repo.Engine`; `ReviewPoll` runs brain-side; no bee code path calls any review method.
