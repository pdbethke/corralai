// SPDX-License-Identifier: Elastic-2.0

package mission

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/queue"
)

// ---- fakeRepo review methods (extend the struct defined in engine_test.go) ----

func (f *fakeRepo) ListReviews(_ context.Context, _ string, _ int, _ string) ([]ReviewInfo, string, bool, error) {
	return f.reviews, "etag-1", f.notModified, nil
}

func (f *fakeRepo) ListReviewComments(_ context.Context, _ string, _ int) ([]ReviewCommentInfo, error) {
	f.listCommentCalls++
	return f.reviewComments, nil
}

func (f *fakeRepo) GetPR(_ context.Context, _ string, _ int) (string, bool, error) {
	st := f.prState
	if st == "" {
		st = "open"
	}
	return st, f.prMerged, nil
}

func (f *fakeRepo) PostComment(_ context.Context, _ string, _ int, _ string) error {
	f.commentCalls++
	return nil
}

func (f *fakeRepo) AuthLogin(_ context.Context, repoURL string) (string, error) {
	f.authLoginRepoURLs = append(f.authLoginRepoURLs, repoURL)
	return f.loginName, nil
}

func reviewSetup(t *testing.T) (*Engine, *queue.Store, *Store) {
	t.Helper()
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })
	m, err := Open(filepath.Join(dir, "m.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return NewEngine(m, q), q, m
}

// A trivial single-task plan keeps the lifecycle test focused on the gate.
func oneTask() []PhaseSpec {
	return []PhaseSpec{{Name: "build", Role: "builder", Instruction: "build it"}}
}

func TestReviewGateAwaitsAcceptance(t *testing.T) {
	e, q, m := reviewSetup(t)
	mid, err := CreateMission(m, q, "thing", oneTask(), true) // requires review
	if err != nil {
		t.Fatal(err)
	}
	// Drain the work, then tick: a review mission parks at awaiting_review, NOT done.
	e.Tick()
	b, _ := q.ClaimNext("Bee", nil, 300)
	q.Complete(b.ID, "Bee", "done")
	e.Tick()
	if mv, _ := m.Mission(mid); mv.Status != "awaiting_review" {
		t.Fatalf("review mission should park at awaiting_review, got %q", mv.Status)
	}
}

func TestNonReviewMissionAutoCompletes(t *testing.T) {
	e, q, m := reviewSetup(t)
	mid, _ := CreateMission(m, q, "thing", oneTask(), false) // no review
	e.Tick()
	b, _ := q.ClaimNext("Bee", nil, 300)
	q.Complete(b.ID, "Bee", "done")
	e.Tick()
	if mv, _ := m.Mission(mid); mv.Status != "done" {
		t.Fatalf("non-review mission should auto-complete, got %q (no regression)", mv.Status)
	}
}

func TestReviewFeedbackOpensNextSprint(t *testing.T) {
	e, q, m := reviewSetup(t)
	mid, _ := CreateMission(m, q, "thing", oneTask(), true)
	e.Tick()
	b, _ := q.ClaimNext("Bee", nil, 300)
	q.Complete(b.ID, "Bee", "done")
	e.Tick() // -> awaiting_review

	// Client requests changes: open a change-request, bump sprint, back to running
	// (this is what the review_mission tool does).
	cr, err := q.AddFinding(queue.Finding{MissionID: mid, Reporter: "client", Type: "change-request", Severity: "high", Evidence: "needs dark mode"})
	if err != nil {
		t.Fatal(err)
	}
	if sp, _ := m.BumpSprint(mid); sp != 2 {
		t.Fatalf("sprint should be 2 after feedback, got %d", sp)
	}
	m.SetMissionStatus(mid, "running")

	// The engine must NOT re-gate to awaiting_review while the change-request is
	// open (the lead hasn't turned it into rework yet).
	e.Tick()
	if mv, _ := m.Mission(mid); mv.Status != "running" {
		t.Fatalf("mission must stay running while client feedback is unaddressed, got %q", mv.Status)
	}

	// The lead addresses the feedback (resolves it + enqueues rework, then it's done).
	q.SetFindingStatus(cr, queue.FindingAddressed)
	q.Enqueue(mid, []queue.TaskSpec{{Key: "rework#1", Role: "builder", Title: "rework", Instruction: "dark mode"}})
	e.Tick()
	rw, _ := q.ClaimNext("Bee", nil, 300)
	q.Complete(rw.ID, "Bee", "done")
	e.Tick() // queue drained, no open change-request -> awaiting_review again
	if mv, _ := m.Mission(mid); mv.Status != "awaiting_review" {
		t.Fatalf("after the sprint's rework, mission should await review again, got %q", mv.Status)
	}
}

// TestResolveNeedsReview: the human-gate resolution path for a mission the
// findings gate parked at needs-review. It must refuse to certify while a
// blocking finding is still open, and converge to done once the human has
// cleared (dismissed/addressed) every blocker.
func TestResolveNeedsReview(t *testing.T) {
	_, q, m := reviewSetup(t)
	mid, _ := CreateMission(m, q, "thing", oneTask(), false)
	fid, err := q.AddFinding(queue.Finding{
		MissionID: mid, Reporter: "reviewer", Type: "design-flaw", Severity: "critical",
		Target: "arch", Evidence: "unsound",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetMissionStatus(mid, "needs-review"); err != nil {
		t.Fatal(err)
	}

	// Refused while the blocker stands — and the mission stays parked.
	if _, err := ResolveNeedsReview(m, q, mid, "high"); err == nil {
		t.Fatal("ResolveNeedsReview must refuse while a blocking finding is open")
	}
	if mv, _ := m.Mission(mid); mv == nil || mv.Status != "needs-review" {
		t.Fatalf("mission must stay needs-review while a blocker is open")
	}

	// Human dismisses the finding → the mission may now certify done.
	if _, err := q.SetFindingStatus(fid, queue.FindingDismissed); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveNeedsReview(m, q, mid, "high"); err != nil {
		t.Fatalf("ResolveNeedsReview after clearing blockers: %v", err)
	}
	if mv, _ := m.Mission(mid); mv == nil || mv.Status != "done" {
		t.Fatalf("mission should converge to done once blockers are cleared, got %v", mv)
	}
}

// ResolveNeedsReview only applies to a parked mission; calling it on a mission
// in any other state is a stale assumption and must be refused (mirrors the
// Pause/Resume/Cancel state guards).
func TestResolveNeedsReviewRefusesWrongState(t *testing.T) {
	_, q, m := reviewSetup(t)
	mid, _ := CreateMission(m, q, "thing", oneTask(), false)
	// still "running"
	if _, err := ResolveNeedsReview(m, q, mid, "high"); err == nil {
		t.Fatal("ResolveNeedsReview on a running mission must be refused")
	}
}

// seedDonePRMission creates a single-task mission, drives it to done, and sets
// up the repo+PRURL fields — leaving it in the state MissionsWithOpenPR returns.
func seedDonePRMission(t *testing.T, m *Store, q *queue.Store) int64 {
	t.Helper()
	mid, err := CreateMission(m, q, "seed directive", oneTask(), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetMissionStatus(mid, "done"); err != nil {
		t.Fatal(err)
	}
	if err := m.SetRepo(mid, "https://github.com/o/r", "main", "corralai/m1"); err != nil {
		t.Fatal(err)
	}
	if err := m.SetPRURL(mid, "https://github.com/o/r/pull/7"); err != nil {
		t.Fatal(err)
	}
	return mid
}

// TestReviewPoll is the six-case contract for Engine.ReviewPoll.
func TestReviewPoll(t *testing.T) {
	const (
		ts0 = "2026-07-01T09:00:00Z" // before the review
		ts1 = "2026-07-01T10:00:00Z" // the review timestamp
		bot = "corralai-bot"
	)

	t.Run("case1_changes_requested_reopens_mission", func(t *testing.T) {
		eng, q, m := reviewSetup(t)
		fake := &fakeRepo{loginName: bot, reviews: []ReviewInfo{
			{ID: 1, State: "CHANGES_REQUESTED", User: "alice", SubmittedAt: ts1, Body: "please fix foo"},
		}}
		eng.Repo = fake
		mid := seedDonePRMission(t, m, q)

		if err := eng.ReviewPoll(); err != nil {
			t.Fatalf("ReviewPoll: %v", err)
		}

		mi, err := m.Mission(mid)
		if err != nil || mi == nil {
			t.Fatalf("mission lookup: %v", err)
		}
		if mi.Status != "running" {
			t.Errorf("case1: want status=running, got %q", mi.Status)
		}
		if mi.ReviewRounds != 1 {
			t.Errorf("case1: want ReviewRounds=1, got %d", mi.ReviewRounds)
		}
		if mi.ReviewWatermark != ts1 {
			t.Errorf("case1: want watermark=%s, got %q", ts1, mi.ReviewWatermark)
		}
		if fake.commentCalls != 1 {
			t.Errorf("case1: want 1 PostIssueComment, got %d", fake.commentCalls)
		}
		// The bot login must be resolved per-forge: AuthLogin is called with the
		// mission's repoURL so the self-filter uses the correct forge identity.
		if len(fake.authLoginRepoURLs) == 0 || fake.authLoginRepoURLs[0] != "https://github.com/o/r" {
			t.Errorf("case1: AuthLogin must be called with the mission repoURL, got %v", fake.authLoginRepoURLs)
		}
	})

	t.Run("case2_no_newer_review_no_reopen", func(t *testing.T) {
		eng, q, m := reviewSetup(t)
		fake := &fakeRepo{loginName: bot, reviews: []ReviewInfo{
			{ID: 1, State: "CHANGES_REQUESTED", User: "alice", SubmittedAt: ts1},
		}}
		eng.Repo = fake
		mid := seedDonePRMission(t, m, q)
		// Pre-advance the watermark to ts1 so the review is NOT newer than the watermark.
		if err := m.SetReviewState(mid, 0, ts1, false); err != nil {
			t.Fatal(err)
		}

		if err := eng.ReviewPoll(); err != nil {
			t.Fatalf("ReviewPoll: %v", err)
		}

		mi, _ := m.Mission(mid)
		if mi.Status != "done" {
			t.Errorf("case2: mission should stay done, got %q", mi.Status)
		}
		if fake.commentCalls != 0 {
			t.Errorf("case2: want 0 comments, got %d", fake.commentCalls)
		}
	})

	t.Run("case3_self_authored_review_ignored", func(t *testing.T) {
		// Security guard: a review by the bot's own login must never trigger a
		// reopen — this prevents an infinite self-loop.
		eng, q, m := reviewSetup(t)
		fake := &fakeRepo{loginName: bot, reviews: []ReviewInfo{
			{ID: 1, State: "CHANGES_REQUESTED", User: bot, SubmittedAt: ts1},
		}}
		eng.Repo = fake
		mid := seedDonePRMission(t, m, q)

		if err := eng.ReviewPoll(); err != nil {
			t.Fatalf("ReviewPoll: %v", err)
		}

		mi, _ := m.Mission(mid)
		if mi.Status != "done" {
			t.Errorf("case3 (self-guard): mission must stay done, got %q", mi.Status)
		}
		if fake.commentCalls != 0 {
			t.Errorf("case3 (self-guard): must not post a comment, got %d", fake.commentCalls)
		}
	})

	t.Run("case4_rounds_capped_parks_mission", func(t *testing.T) {
		eng, q, m := reviewSetup(t)
		fake := &fakeRepo{loginName: bot, reviews: []ReviewInfo{
			{ID: 1, State: "CHANGES_REQUESTED", User: "alice", SubmittedAt: ts1},
		}}
		eng.Repo = fake
		mid := seedDonePRMission(t, m, q)
		// Already at the max-rounds cap; watermark before the new review.
		if err := m.SetReviewState(mid, maxReviewRounds, ts0, false); err != nil {
			t.Fatal(err)
		}

		if err := eng.ReviewPoll(); err != nil {
			t.Fatalf("ReviewPoll: %v", err)
		}

		mi, _ := m.Mission(mid)
		if mi.Status != "done" {
			t.Errorf("case4: mission should stay done (parked, not reopened), got %q", mi.Status)
		}
		if !mi.ReviewParked {
			t.Errorf("case4: mission should be parked after cap exceeded")
		}
		if fake.commentCalls != 1 {
			t.Errorf("case4: want 1 limit-comment, got %d", fake.commentCalls)
		}
	})

	t.Run("case5_closed_pr_parks_mission", func(t *testing.T) {
		eng, q, m := reviewSetup(t)
		fake := &fakeRepo{
			loginName: bot,
			prState:   "closed",
			reviews: []ReviewInfo{
				{ID: 1, State: "CHANGES_REQUESTED", User: "alice", SubmittedAt: ts1},
			},
		}
		eng.Repo = fake
		mid := seedDonePRMission(t, m, q)

		if err := eng.ReviewPoll(); err != nil {
			t.Fatalf("ReviewPoll: %v", err)
		}

		mi, _ := m.Mission(mid)
		if !mi.ReviewParked {
			t.Errorf("case5: closed PR should park the mission")
		}
		if mi.Status != "done" {
			t.Errorf("case5: status should remain done (parked), got %q", mi.Status)
		}
		if fake.commentCalls != 0 {
			t.Errorf("case5: closed PR must not post a comment, got %d", fake.commentCalls)
		}
	})

	t.Run("case6_304_not_modified_skips_comments", func(t *testing.T) {
		// DoS/quota guard: a 304 Not-Modified must short-circuit BEFORE
		// ListReviewComments is called — the entire review-processing path is skipped.
		eng, q, m := reviewSetup(t)
		fake := &fakeRepo{
			loginName:   bot,
			notModified: true,
			reviews: []ReviewInfo{
				{ID: 1, State: "CHANGES_REQUESTED", User: "alice", SubmittedAt: ts1},
			},
		}
		eng.Repo = fake
		mid := seedDonePRMission(t, m, q)

		if err := eng.ReviewPoll(); err != nil {
			t.Fatalf("ReviewPoll: %v", err)
		}

		mi, _ := m.Mission(mid)
		if mi.Status != "done" {
			t.Errorf("case6 (304 guard): mission should stay done, got %q", mi.Status)
		}
		if fake.listCommentCalls != 0 {
			t.Errorf("case6 (304 guard): ListReviewComments must NOT be called on 304, got %d calls", fake.listCommentCalls)
		}
		if fake.commentCalls != 0 {
			t.Errorf("case6 (304 guard): PostIssueComment must NOT be called on 304, got %d calls", fake.commentCalls)
		}
	})
}

// TestReviewLoopClosure is the end-to-end proof of sub-project #18: a done repo
// mission with an open PR, on a CHANGES_REQUESTED review, is reopened by
// ReviewPoll AND re-drives through Tick to a SECOND push on the SAME PR (no
// duplicate PR). The other TestReviewPoll cases only cover the ReviewPoll half;
// this closes the loop by trace.
func TestReviewLoopClosure(t *testing.T) {
	eng, q, m := reviewSetup(t)
	fake := &fakeRepo{loginName: "corralai-bot"}
	eng.Repo = fake
	eng.Workspace = t.TempDir()

	// A non-review repo mission so the engine's fast path push/PRs on done.
	mid, err := CreateMission(m, q, "loop-closure directive", oneTask(), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetRepo(mid, "https://github.com/o/r", "main", "corralai/m1"); err != nil {
		t.Fatal(err)
	}

	// --- First convergence: drive the build phase to done → first push + PR. ---
	if err := eng.Tick(); err != nil { // promote build
		t.Fatalf("tick 1: %v", err)
	}
	drain(t, q)
	if err := eng.Tick(); err != nil { // commit + done + push/PR
		t.Fatalf("tick 2: %v", err)
	}
	if fake.pushCalls != 1 || fake.prCalls != 1 {
		t.Fatalf("first convergence: want pushCalls=1 prCalls=1, got push=%d pr=%d", fake.pushCalls, fake.prCalls)
	}
	mi, _ := m.Mission(mid)
	if mi.Status != "done" || mi.PRURL == "" {
		t.Fatalf("first convergence: want done+PRURL, got status=%q PRURL=%q", mi.Status, mi.PRURL)
	}
	firstPRURL := mi.PRURL // the fake resolves this deterministically from owner/repo

	// --- A reviewer requests changes. ---
	fake.reviews = []ReviewInfo{
		{ID: 1, State: "CHANGES_REQUESTED", User: "alice", SubmittedAt: "2026-07-01T10:00:00Z", Body: "please fix foo"},
	}
	if err := eng.ReviewPoll(); err != nil {
		t.Fatalf("ReviewPoll: %v", err)
	}
	mi, _ = m.Mission(mid)
	if mi.Status != "running" {
		t.Fatalf("after ReviewPoll: mission should be reopened (running), got %q", mi.Status)
	}
	if mi.ReviewRounds != 1 {
		t.Fatalf("after ReviewPoll: want ReviewRounds=1, got %d", mi.ReviewRounds)
	}

	// --- Second convergence: drive the review phase to done → SECOND push + PR. ---
	if err := eng.Tick(); err != nil { // promote the review phase task
		t.Fatalf("tick 3: %v", err)
	}
	drain(t, q)
	if err := eng.Tick(); err != nil { // commit review phase + done + push/PR
		t.Fatalf("tick 4: %v", err)
	}

	// The loop closed: a second push fired on the SAME PR (no duplicate PR).
	if fake.pushCalls != 2 {
		t.Errorf("loop-closure: want a SECOND push (pushCalls=2), got %d", fake.pushCalls)
	}
	if fake.prCalls != 2 {
		t.Errorf("loop-closure: want OpenPR re-resolved (prCalls=2), got %d", fake.prCalls)
	}
	mi, _ = m.Mission(mid)
	if mi.PRURL != firstPRURL {
		t.Errorf("loop-closure: PR URL must be unchanged (no duplicate PR): first=%q now=%q", firstPRURL, mi.PRURL)
	}
	if mi.Status != "done" {
		t.Errorf("loop-closure: mission should be done again after the review round, got %q", mi.Status)
	}
}
