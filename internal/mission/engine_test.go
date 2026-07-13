// SPDX-License-Identifier: Elastic-2.0

package mission

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/queue"
)

// fakeRepo is a RepoOps spy used in tests. Zero value = always succeed; the
// optional fields inject failures for the retry/transient-error tests.
type fakeRepo struct {
	commits []string
	pushed  bool

	prCalls   int  // number of OpenPR invocations
	pushCalls int  // number of Push invocations
	prFail    bool // when true, OpenPR returns an error every call

	// commitFail[substr] > 0 makes Commit return an error (decrementing the count)
	// when the message contains substr — so a phase can fail once then succeed.
	commitFail map[string]int

	// review polling fields (used by TestReviewPoll in review_test.go)
	reviews           []ReviewInfo        // returned by ListReviews
	reviewComments    []ReviewCommentInfo // returned by ListReviewComments
	loginName         string              // returned by AuthLogin
	commentCalls      int                 // counts PostIssueComment calls
	prState           string              // returned by GetPR; empty → "open"
	prMerged          bool                // returned by GetPR
	notModified       bool                // ListReviews returns 304 when true
	listCommentCalls  int                 // counts ListReviewComments calls
	authLoginRepoURLs []string            // records each repoURL passed to AuthLogin

	// egress-gate fields
	rangeFiles []string // returned by ChangedFilesRange; nil = fall back to ["calc.go"]
	rangeCalls []string // records each base passed to ChangedFilesRange
	rangeErr   error    // when set, ChangedFilesRange returns this error instead
}

func (f *fakeRepo) Commit(_ context.Context, _, msg string) (bool, error) {
	for k, n := range f.commitFail {
		if n > 0 && strings.Contains(msg, k) {
			f.commitFail[k] = n - 1
			return false, fmt.Errorf("transient commit error for %q", k)
		}
	}
	f.commits = append(f.commits, msg)
	return true, nil
}
func (f *fakeRepo) Push(_ context.Context, _, _ string) error {
	f.pushCalls++
	f.pushed = true
	return nil
}
func (f *fakeRepo) OpenPR(_ context.Context, repoURL, _, _, _, _ string) (string, error) {
	f.prCalls++
	if f.prFail {
		return "", fmt.Errorf("permanent PR failure")
	}
	// Deterministic URL: append /pull/1 to the repoURL so tests can verify
	// the same URL is returned on multiple calls (no duplicate PR).
	return repoURL + "/pull/1", nil
}
func (f *fakeRepo) ChangedFiles(_ context.Context, _ string) ([]string, error) {
	return []string{"calc.go"}, nil
}

// rangeFiles, when non-nil, is returned by ChangedFilesRange (the egress
// gate's changed-file source); nil falls back to the same fixed list as
// ChangedFiles, so existing tests that never set it are unaffected.
func (f *fakeRepo) ChangedFilesRange(_ context.Context, _, base string) ([]string, error) {
	f.rangeCalls = append(f.rangeCalls, base)
	if f.rangeErr != nil {
		return nil, f.rangeErr
	}
	if f.rangeFiles != nil {
		return f.rangeFiles, nil
	}
	return []string{"calc.go"}, nil
}

// fakeIndexer is an Indexer spy used in tests.
type fakeIndexer struct {
	indexed map[int64][]string
	dropped map[int64]bool
}

func newFakeIndexer() *fakeIndexer {
	return &fakeIndexer{indexed: map[int64][]string{}, dropped: map[int64]bool{}}
}
func (f *fakeIndexer) IndexPaths(missionID int64, _ string, paths []string) error {
	f.indexed[missionID] = append(f.indexed[missionID], paths...)
	return nil
}
func (f *fakeIndexer) DropMission(missionID int64) error {
	f.dropped[missionID] = true
	return nil
}

func status(t *testing.T, m *Store, q *queue.Store, mid int64, name string) string {
	t.Helper()
	mv, err := m.View(mid, q)
	if err != nil || mv == nil {
		t.Fatalf("view: %v", err)
	}
	for _, p := range mv.Phases {
		if p.Name == name {
			return p.Status
		}
	}
	t.Fatalf("no phase %q", name)
	return ""
}

// drain claims and completes every currently-ready task (as a generic bee), then
// returns how many it cleared — one "layer" of the DAG per call.
func drain(t *testing.T, q *queue.Store) int {
	t.Helper()
	n := 0
	for {
		task, err := q.ClaimNext("Bee", nil, 300)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if task == nil {
			return n
		}
		if ok, err := q.Complete(task.ID, "Bee", "done"); err != nil || !ok {
			t.Fatalf("complete %d: ok=%v err=%v", task.ID, ok, err)
		}
		n++
	}
}

func TestMissionPipelinePull(t *testing.T) {
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	m, err := Open(filepath.Join(dir, "m.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	mid, err := CreateMission(m, q, "add a wishlist feature", nil, false) // default pipeline
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(m, q)

	// research has no deps and gates everything else.
	_ = e.Tick()
	if s := status(t, m, q, mid, "research"); s != "running" {
		t.Fatalf("research should be first (ready), got %q", s)
	}
	for _, n := range []string{"design", "build", "test", "secops", "perf", "integrate", "docs", "retro"} {
		if s := status(t, m, q, mid, n); s != "pending" {
			t.Fatalf("%s should be pending behind research, got %q", n, s)
		}
	}

	// Drain the DAG layer by layer (generic bee clears each ready set, then tick
	// promotes the next layer) until the mission converges.
	done := false
	for i := 0; i < 60; i++ {
		drain(t, q)
		_ = e.Tick()
		if mv, _ := m.Mission(mid); mv != nil && mv.Status == "done" {
			done = true
			break
		}
	}
	if !done {
		t.Fatal("mission did not converge through the full pipeline")
	}
	// retro is the terminal phase — done only after the whole chain.
	if s := status(t, m, q, mid, "retro"); s != "done" {
		t.Fatalf("retro should be done at convergence, got %q", s)
	}
}

// TestEngineFiresOnMissionCompleted verifies that the engine's auto-complete
// path (no review required) fires OnMissionCompleted exactly once, with the
// mission's id, "done" status, and its ReviewRounds count.
func TestEngineFiresOnMissionCompleted(t *testing.T) {
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	m, err := Open(filepath.Join(dir, "m.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	mid, err := CreateMission(m, q, "add a wishlist feature", nil, false) // default pipeline, no review
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(m, q)
	var calls int
	var gotID int64
	var gotStatus string
	var gotRounds int
	e.OnMissionCompleted = func(missionID int64, status string, reviewRounds int) {
		calls++
		gotID, gotStatus, gotRounds = missionID, status, reviewRounds
	}

	done := false
	for i := 0; i < 60 && !done; i++ {
		drain(t, q)
		_ = e.Tick()
		if mv, _ := m.Mission(mid); mv != nil && mv.Status == "done" {
			done = true
		}
	}
	if !done {
		t.Fatal("mission did not converge")
	}
	if calls != 1 {
		t.Fatalf("OnMissionCompleted should fire exactly once, got %d calls", calls)
	}
	if gotID != mid || gotStatus != "done" {
		t.Fatalf("got id=%d status=%q, want id=%d status=done", gotID, gotStatus, mid)
	}
	if gotRounds != 0 {
		t.Fatalf("non-review mission should report review_rounds=0, got %d", gotRounds)
	}
}

// TestEngineHoldsBackWhenFinalStateVerifyFails is the #42 guarantee: once the
// queue has drained, a mission must NOT converge to "done" if the brain's re-run
// of the mission's verify commands against the FINAL working copy fails — no
// shipping a broken tree on the strength of an earlier per-task pass.
func TestEngineHoldsBackWhenFinalStateVerifyFails(t *testing.T) {
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	m, err := Open(filepath.Join(dir, "m.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	mid, err := CreateMission(m, q, "add a wishlist feature", nil, false)
	if err != nil {
		t.Fatal(err)
	}

	e := NewEngine(m, q)
	// A materialized working copy so the final-state verify can actually run.
	e.Workspace = t.TempDir()
	if err := os.MkdirAll(e.workdir(&Mission{ID: mid}), 0o755); err != nil {
		t.Fatal(err)
	}
	// The final tree is "broken": every re-run of a verify command fails.
	e.Verify = func(_ context.Context, _, _ string) (bool, string) { return false, "final tree does not build" }

	for i := 0; i < 60; i++ {
		drain(t, q)
		if err := e.Tick(); err != nil {
			t.Fatalf("tick: %v", err)
		}
		if mv, _ := m.Mission(mid); mv != nil && mv.Status == "done" {
			t.Fatal("mission converged to done despite a failing final-state verify (#42)")
		}
	}
	// And it filed a loud final-state regression finding for the reflex loop.
	fs, _ := q.Findings(mid, queue.FindingOpen)
	found := false
	for _, f := range fs {
		if f.Reporter == "verify-gate" && f.Type == "regression" && strings.HasPrefix(f.Target, "final-state") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected a verify-gate final-state regression finding, got %+v", fs)
	}
}

// TestEngineRoutesToNeedsReviewOnOpenCriticalFinding: a drained queue is not a
// clean bill of health. If an open finding at/above `high` remains — e.g. a
// critical `design-flaw`, which reflexRules deems non-actionable so it never
// becomes a task and never blocks MissionDone — the brain must NOT certify the
// mission "done". It routes to the human-gate `needs-review` terminal state
// instead: a judge may not certify a result it knows still holds a critical
// defect.
func TestEngineRoutesToNeedsReviewOnOpenCriticalFinding(t *testing.T) {
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	m, err := Open(filepath.Join(dir, "m.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	mid, err := CreateMission(m, q, "add a wishlist feature", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	// A non-actionable but critical finding: reflexRules returns actionable=false
	// for design-flaw, so replan never turns it into a task and never marks it
	// addressed — it stays open right through convergence.
	if _, err := q.AddFinding(queue.Finding{
		MissionID: mid, Reporter: "reviewer", Type: "design-flaw", Severity: "critical",
		Target: "architecture", Evidence: "the auth model is fundamentally unsound",
	}); err != nil {
		t.Fatal(err)
	}

	e := NewEngine(m, q)
	var gotStatus string
	var completedCalls int
	e.OnMissionCompleted = func(_ int64, status string, _ int) {
		completedCalls++
		gotStatus = status
	}

	settled := false
	for i := 0; i < 60 && !settled; i++ {
		drain(t, q)
		_ = e.Tick()
		if mv, _ := m.Mission(mid); mv != nil && mv.Status != "running" {
			settled = true
		}
	}
	mv, _ := m.Mission(mid)
	if mv == nil {
		t.Fatal("mission missing")
	}
	if mv.Status == "done" {
		t.Fatal("mission certified done despite an open critical finding — the gate must withhold certification")
	}
	if mv.Status != "needs-review" {
		t.Fatalf("mission status = %q, want needs-review", mv.Status)
	}
	if gotStatus != "needs-review" || completedCalls != 1 {
		t.Fatalf("OnMissionCompleted got status=%q calls=%d, want needs-review/1", gotStatus, completedCalls)
	}
}

// TestEngineConvergesDoneWhenOnlyLowFindingsOpen: the needs-review gate must not
// over-fire — an open finding BELOW the blocking threshold (e.g. a low-severity
// note) does not withhold certification; the mission still converges to done.
func TestEngineConvergesDoneWhenOnlyLowFindingsOpen(t *testing.T) {
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	m, err := Open(filepath.Join(dir, "m.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	mid, err := CreateMission(m, q, "add a wishlist feature", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.AddFinding(queue.Finding{
		MissionID: mid, Reporter: "reviewer", Type: "note", Severity: "low",
		Target: "style", Evidence: "consider a shorter variable name",
	}); err != nil {
		t.Fatal(err)
	}

	e := NewEngine(m, q)
	done := false
	for i := 0; i < 60 && !done; i++ {
		drain(t, q)
		_ = e.Tick()
		if mv, _ := m.Mission(mid); mv != nil && mv.Status == "done" {
			done = true
		}
	}
	if !done {
		mv, _ := m.Mission(mid)
		got := "<nil>"
		if mv != nil {
			got = mv.Status
		}
		t.Fatalf("a low-severity finding must not block convergence; status = %q", got)
	}
}

// TestEngineSweepsBlockedDependencies is the graceful-degradation guarantee for a
// DAG deadlock: a pending task whose dependency can never be satisfied (cancelled/
// superseded/missing) is invisibly stuck forever (PromoteReady never promotes it,
// MissionDone never converges, DetectRoleStalls can't see non-ready tasks). The
// engine must sweep it — cancel it and file a loud finding — so the hang becomes a
// visible failure.
func TestEngineSweepsBlockedDependencies(t *testing.T) {
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	m, err := Open(filepath.Join(dir, "m.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	mid, err := CreateMission(m, q, "build a thing", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(m, q)

	// Find a pending task that depends on another, and cancel that dependency so it
	// can never become done — orphaning the dependent.
	tasks, err := q.List(mid)
	if err != nil {
		t.Fatal(err)
	}
	var dependent queue.Task
	for _, tk := range tasks {
		if len(tk.DependsOn) > 0 {
			dependent = tk
			break
		}
	}
	if dependent.ID == 0 {
		t.Fatal("expected the default plan to contain a task with a dependency")
	}
	depKey := dependent.DependsOn[0]
	var depID int64
	for _, tk := range tasks {
		if tk.Key == depKey {
			depID = tk.ID
		}
	}
	if depID == 0 {
		t.Fatalf("dependency task %q not found", depKey)
	}
	if _, err := q.CancelTask(depID); err != nil {
		t.Fatal(err)
	}

	// One tick: the sweep must cancel the now-orphaned dependent and record it.
	if err := e.Tick(); err != nil {
		t.Fatalf("tick: %v", err)
	}

	tk, err := q.TaskByID(dependent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tk.Status != queue.StatusCancelled {
		t.Fatalf("orphaned task %q should be swept to cancelled, got %q", dependent.Key, tk.Status)
	}
	fs, _ := q.Findings(mid, queue.FindingOpen)
	found := false
	for _, f := range fs {
		if f.Reporter == "dep-sweep" && strings.HasPrefix(f.Target, "blocked-dep") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected a dep-sweep blocked-dependency finding, got %+v", fs)
	}
}

// TestBlockedDepChainRoutesToNeedsReviewNotPR guards against a false convergence:
// a dep-sweep blocker (cancelled dependency) files a high-severity finding that
// replan must NOT auto-remediate/address, so blockingFindingOpen keeps holding the
// mission at needs-review instead of falling through to done + OpenPR.
func TestBlockedDepChainRoutesToNeedsReviewNotPR(t *testing.T) {
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	m, err := Open(filepath.Join(dir, "m.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	mid, err := CreateMission(m, q, "build a thing", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetRepo(mid, "https://github.com/o/r", "main", "corralai/m1"); err != nil {
		t.Fatal(err)
	}

	repo := &fakeRepo{}
	e := NewEngine(m, q)
	e.Repo = repo
	e.Workspace = t.TempDir()

	// Find a pending task that depends on another, and cancel that dependency so it
	// can never become done — orphaning the dependent (same setup as
	// TestEngineSweepsBlockedDependencies).
	tasks, err := q.List(mid)
	if err != nil {
		t.Fatal(err)
	}
	var dependent queue.Task
	for _, tk := range tasks {
		if len(tk.DependsOn) > 0 {
			dependent = tk
			break
		}
	}
	if dependent.ID == 0 {
		t.Fatal("expected the default plan to contain a task with a dependency")
	}
	depKey := dependent.DependsOn[0]
	var depID int64
	for _, tk := range tasks {
		if tk.Key == depKey {
			depID = tk.ID
		}
	}
	if depID == 0 {
		t.Fatalf("dependency task %q not found", depKey)
	}
	if _, err := q.CancelTask(depID); err != nil {
		t.Fatal(err)
	}

	// Drive to convergence: sweep cancels the orphan and files the dep-sweep
	// finding, then subsequent ticks/replans must not auto-address it away.
	var mi *Mission
	for i := 0; i < 60; i++ {
		if err := e.Tick(); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
		drain(t, q)
		mi, err = m.Mission(mid)
		if err != nil {
			t.Fatal(err)
		}
		if mi.Status == "done" || mi.Status == "needs-review" || mi.Status == "failed" {
			break
		}
	}
	if mi == nil {
		t.Fatal("mission never converged")
	}
	if mi.Status == "done" {
		t.Fatalf("mission converged to done despite a cancelled dependency chain")
	}
	if repo.prCalls != 0 {
		t.Fatalf("opened %d PR(s) despite a blocked dep chain; want 0 (should be needs-review)", repo.prCalls)
	}
	if mi.Status != "needs-review" {
		t.Fatalf("status = %q, want needs-review", mi.Status)
	}
}

// TestEngineFailsMissionWithNoProgress is the universal give-up backstop: a
// running mission that makes no forward progress for NoProgressTicks consecutive
// ticks while nothing is claimed (no agent actively holding work) must reach the
// terminal `failed` state — not hang in "running" forever — and say so.
func TestEngineFailsMissionWithNoProgress(t *testing.T) {
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	m, err := Open(filepath.Join(dir, "m.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	mid, err := CreateMission(m, q, "build a thing", nil, false)
	if err != nil {
		t.Fatal(err)
	}

	e := NewEngine(m, q)
	e.NoProgressTicks = 3
	var terminalStatus string
	e.OnMissionCompleted = func(_ int64, status string, _ int) {
		if status != "done" {
			terminalStatus = status
		}
	}

	// No agents ever claim anything; nothing progresses. The backstop must give up.
	for i := 0; i < 6; i++ {
		if err := e.Tick(); err != nil {
			t.Fatalf("tick: %v", err)
		}
	}

	mv, err := m.Mission(mid)
	if err != nil {
		t.Fatal(err)
	}
	if mv.Status != "failed" {
		t.Fatalf("a mission with no progress and nothing claimable must reach the terminal failed state, got %q", mv.Status)
	}
	if terminalStatus != "failed" {
		t.Fatalf("a failed mission must fire OnMissionCompleted with 'failed', got %q", terminalStatus)
	}
}

// TestEnginePhaseCommitAndPRForRepoMission verifies that:
//   - every done phase produces one commit (message contains phase name)
//   - on mission done, push fires and PRURL is stored in the mission
//   - a plain (no-repo) mission never touches the RepoOps fake
func TestEnginePhaseCommitAndPRForRepoMission(t *testing.T) {
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	ms, err := Open(filepath.Join(dir, "m.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer ms.Close()

	// Single-phase repo mission so the test stays fast and deterministic.
	plan := []PhaseSpec{{Name: "build", Instruction: "build it", Count: 1}}
	mid, err := CreateMission(ms, q, "add a wishlist feature", plan, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := ms.SetRepo(mid, "https://github.com/o/r", "main", "corralai/m1"); err != nil {
		t.Fatal(err)
	}

	fake := &fakeRepo{}
	idx := newFakeIndexer()
	e := NewEngine(ms, q)
	e.Repo = fake
	e.Index = idx
	e.Workspace = t.TempDir()

	// Tick 1: promotes the "build" task from pending→ready.
	// Drain: claims and completes it.
	// Tick 2: sees MissionDone, commits the done phase, sets status="done", push+PR.
	if err := e.Tick(); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	drain(t, q)
	if err := e.Tick(); err != nil {
		t.Fatalf("tick 2: %v", err)
	}

	// Phase commit: message must mention the phase name.
	if len(fake.commits) == 0 {
		t.Fatal("expected at least one commit for the repo mission")
	}
	if !strings.Contains(fake.commits[0], "build") {
		t.Fatalf("expected commit message to contain %q, got %q", "build", fake.commits[0])
	}
	// Push must have fired.
	if !fake.pushed {
		t.Fatal("expected Push to be called on mission done")
	}
	// PRURL must be persisted in the store.
	mi, err := ms.Mission(mid)
	if err != nil || mi == nil {
		t.Fatalf("mission lookup: %v", err)
	}
	if mi.PRURL == "" {
		t.Fatal("expected PRURL to be stored after mission done")
	}
	// Indexer must have recorded the changed file for the mission.
	idxPaths := idx.indexed[mid]
	found := false
	for _, p := range idxPaths {
		if p == "calc.go" {
			found = true
		}
	}
	if !found {
		t.Fatalf("indexer did not record calc.go for mission %d: got %v", mid, idxPaths)
	}
	// Indexer must have dropped the mission index on completion.
	if !idx.dropped[mid] {
		t.Fatalf("indexer did not drop mission %d on completion", mid)
	}

	// A plain (no-repo) mission must NOT call RepoOps at all.
	fake2 := &fakeRepo{}
	e.Repo = fake2
	plainPlan := []PhaseSpec{{Name: "research", Instruction: "research it", Count: 1}}
	mid2, err := CreateMission(ms, q, "no-repo directive", plainPlan, false)
	if err != nil {
		t.Fatal(err)
	}
	// mid2 has no SetRepo call — Repo field is "".
	if err := e.Tick(); err != nil { // promote to ready
		t.Fatalf("tick (no-repo promote): %v", err)
	}
	drain(t, q)
	if err := e.Tick(); err != nil {
		t.Fatalf("tick (no-repo done): %v", err)
	}
	if mv, _ := ms.Mission(mid2); mv == nil || mv.Status != "done" {
		t.Fatal("no-repo mission should reach done")
	}
	if len(fake2.commits) != 0 {
		t.Fatalf("no-repo mission must not call fake: got commits %v", fake2.commits)
	}
}

// TestEngineReconcilePushPRForReviewAcceptedRepoMission verifies the Tick
// reconcile pass: a review-gated repo mission completes via SubmitReview (which
// sets status="done" with no Engine reference), and the NEXT Tick's reconcile
// pass picks it up and runs push+PR.
func TestEngineReconcilePushPRForReviewAcceptedRepoMission(t *testing.T) {
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	ms, err := Open(filepath.Join(dir, "m.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer ms.Close()

	plan := []PhaseSpec{{Name: "build", Instruction: "build it", Count: 1}}
	mid, err := CreateMission(ms, q, "review-gated repo work", plan, true) // requiresReview=true
	if err != nil {
		t.Fatal(err)
	}
	if err := ms.SetRepo(mid, "https://github.com/o/r", "main", "corralai/m1"); err != nil {
		t.Fatal(err)
	}

	fake := &fakeRepo{}
	e := NewEngine(ms, q)
	e.Repo = fake
	e.Workspace = t.TempDir()

	// Drive all phases done. Review-gated => engine gates to awaiting_review, no
	// push/PR yet (human must accept first).
	if err := e.Tick(); err != nil { // promote
		t.Fatalf("tick 1: %v", err)
	}
	drain(t, q)
	if err := e.Tick(); err != nil { // gate to awaiting_review
		t.Fatalf("tick 2: %v", err)
	}
	if mv, _ := ms.Mission(mid); mv == nil || mv.Status != "awaiting_review" {
		t.Fatalf("expected awaiting_review, got %v", mv)
	}
	if fake.pushed {
		t.Fatal("review-gated mission must NOT push before acceptance")
	}

	// Client accepts — store sets status=done with no Engine reference.
	if _, err := SubmitReview(ms, q, mid, true, "", "client"); err != nil {
		t.Fatalf("submit review: %v", err)
	}
	if fake.pushed {
		t.Fatal("SubmitReview itself must not push (no Engine reference)")
	}

	// Next Tick's reconcile pass must catch the done-but-no-PR mission.
	if err := e.Tick(); err != nil {
		t.Fatalf("reconcile tick: %v", err)
	}
	if !fake.pushed {
		t.Fatal("reconcile pass should have pushed the review-accepted mission")
	}
	mi, err := ms.Mission(mid)
	if err != nil || mi == nil {
		t.Fatalf("mission lookup: %v", err)
	}
	if mi.PRURL == "" {
		t.Fatal("reconcile pass should have stored PRURL")
	}
}

// TestEnginePRRetryCappedOnPermanentFailure verifies that a persistently-failing
// OpenPR is not retried forever: after maxPRAttempts the engine parks the mission
// and stops calling push/PR, so the GitHub API isn't hammered every tick.
func TestEnginePRRetryCappedOnPermanentFailure(t *testing.T) {
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	ms, err := Open(filepath.Join(dir, "m.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer ms.Close()

	plan := []PhaseSpec{{Name: "build", Instruction: "build it", Count: 1}}
	mid, err := CreateMission(ms, q, "perma-fail PR", plan, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := ms.SetRepo(mid, "https://github.com/o/r", "main", "corralai/m1"); err != nil {
		t.Fatal(err)
	}

	fake := &fakeRepo{prFail: true} // OpenPR errors on every call
	e := NewEngine(ms, q)
	e.Repo = fake
	e.Workspace = t.TempDir()

	// Drive the mission to done.
	if err := e.Tick(); err != nil { // promote build
		t.Fatalf("tick 1: %v", err)
	}
	drain(t, q)

	// Many subsequent ticks: the reconcile loop keeps seeing a done-but-no-PR
	// mission, but retries must be capped at maxPRAttempts (not unbounded).
	for i := 0; i < 20; i++ {
		if err := e.Tick(); err != nil {
			t.Fatalf("tick %d: %v", i+2, err)
		}
	}

	if fake.prCalls != maxPRAttempts {
		t.Fatalf("OpenPR should be capped at %d attempts, got %d (unbounded retry)", maxPRAttempts, fake.prCalls)
	}
	if fake.pushCalls != maxPRAttempts {
		t.Fatalf("Push should be capped at %d attempts, got %d", maxPRAttempts, fake.pushCalls)
	}
	// PRURL never set; mission stays done with the branch preserved.
	if mi, _ := ms.Mission(mid); mi == nil || mi.PRURL != "" {
		t.Fatalf("PRURL must remain empty after giving up, got %v", mi)
	}
}

// TestEnginePRGiveUpDropsIndex verifies Fix 2: when a mission permanently fails
// push/PR (prGaveUp), DropMission must be called so orphaned chunks do not
// persist in the DB for the lifetime of the process.
func TestEnginePRGiveUpDropsIndex(t *testing.T) {
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	ms, err := Open(filepath.Join(dir, "m.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer ms.Close()

	plan := []PhaseSpec{{Name: "build", Instruction: "build it", Count: 1}}
	mid, err := CreateMission(ms, q, "perma-fail PR with index", plan, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := ms.SetRepo(mid, "https://github.com/o/r", "main", "corralai/m1"); err != nil {
		t.Fatal(err)
	}

	fake := &fakeRepo{prFail: true} // OpenPR errors on every call
	idx := newFakeIndexer()
	e := NewEngine(ms, q)
	e.Repo = fake
	e.Index = idx
	e.Workspace = t.TempDir()

	// Drive the mission to done.
	if err := e.Tick(); err != nil {
		t.Fatalf("tick 1 (promote): %v", err)
	}
	drain(t, q)

	// Many subsequent ticks: the reconcile loop keeps seeing a done-but-no-PR
	// mission and retries until capped at maxPRAttempts.
	for i := 0; i < 20; i++ {
		if err := e.Tick(); err != nil {
			t.Fatalf("tick %d: %v", i+2, err)
		}
	}

	// The indexer's DropMission must have been called on give-up.
	if !idx.dropped[mid] {
		t.Fatal("DropMission must be called when the engine permanently gives up on push/PR")
	}
	// Sanity: PR call count is still capped.
	if fake.prCalls != maxPRAttempts {
		t.Fatalf("OpenPR should be capped at %d, got %d", maxPRAttempts, fake.prCalls)
	}
}

// TestEngineCommitRetriedAfterTransientError verifies Fix 2: a phase whose first
// commit errors transiently is NOT marked done, so a later commit pass retries it
// and the phase's work still lands (Push never ships a branch missing that phase).
func TestEngineCommitRetriedAfterTransientError(t *testing.T) {
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	ms, err := Open(filepath.Join(dir, "m.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer ms.Close()

	plan := []PhaseSpec{{Name: "build", Instruction: "build it", Count: 1}}
	mid, err := CreateMission(ms, q, "transient commit", plan, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := ms.SetRepo(mid, "https://github.com/o/r", "main", "corralai/m1"); err != nil {
		t.Fatal(err)
	}

	// The first Commit call for the "build" phase errors; the retry succeeds.
	fake := &fakeRepo{commitFail: map[string]int{"build": 1}}
	e := NewEngine(ms, q)
	e.Repo = fake
	e.Workspace = t.TempDir()

	if err := e.Tick(); err != nil { // promote build
		t.Fatalf("tick 1: %v", err)
	}
	drain(t, q)
	if err := e.Tick(); err != nil { // commit (1st fails) → done → finish re-commits (succeeds) → push+PR
		t.Fatalf("tick 2: %v", err)
	}

	// Despite the transient first-commit error, the phase must eventually commit.
	if len(fake.commits) == 0 {
		t.Fatal("build phase was skipped after a transient commit error (lost work!)")
	}
	if !strings.Contains(fake.commits[0], "build") {
		t.Fatalf("expected the build phase to be committed, got %v", fake.commits)
	}
	// And the failure injection was actually exercised (count drained to 0).
	if fake.commitFail["build"] != 0 {
		t.Fatalf("expected the first commit to fail once; remaining=%d", fake.commitFail["build"])
	}
	if !fake.pushed {
		t.Fatal("mission should still push after the phase committed on retry")
	}
}
