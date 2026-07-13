// SPDX-License-Identifier: Elastic-2.0

package mission

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/pdbethke/corralai/internal/queue"
)

// ReviewInfo is the mission-package mirror of repo.Review, declared here so the
// mission package never imports internal/repo (decoupling is structural).
type ReviewInfo struct {
	ID          int64
	State       string // "APPROVED" | "CHANGES_REQUESTED" | "COMMENTED" | "DISMISSED"
	Body        string
	SubmittedAt string // RFC3339; sorts lexically
	User        string
}

// ReviewCommentInfo is the mission-package mirror of repo.ReviewComment.
type ReviewCommentInfo struct {
	Path string
	Line int
	Body string
	User string
}

// RepoOps is the interface the mission engine uses to interact with a git
// repository. *repo.Engine satisfies it via the repoAdapter in cmd/corral;
// tests use a fakeRepo spy.
//
// REST methods (OpenPR, ListReviews, etc.) take a repoURL so the engine can
// select the correct forge Provider by host without the mission package
// importing internal/repo (decoupling is structural). The engine's git ops
// (Commit, Push) are forge-neutral and take directory paths as before.
type RepoOps interface {
	Commit(ctx context.Context, dir, msg string) (bool, error)
	Push(ctx context.Context, dir, branch string) error
	// OpenPR creates a change request for the given repoURL.
	OpenPR(ctx context.Context, repoURL, head, base, title, body string) (string, error)
	ChangedFiles(ctx context.Context, dir string) ([]string, error)
	// ChangedFilesRange returns the mission's cumulative changed-file set — the
	// diff between base and HEAD, not just the most recent commit. The egress
	// scan gate uses this (not ChangedFiles) so a secret committed in an
	// earlier phase is still caught at push time, not just the last phase's diff.
	ChangedFilesRange(ctx context.Context, dir, base string) ([]string, error)
	// DiffAddedLines returns the raw `git log -p base..HEAD` patch text — every
	// commit's added lines across the whole branch history. The egress gate
	// scans this so a secret committed in an earlier phase and then DELETED
	// (clean final tree, absent from the net base...HEAD diff and from a squash)
	// is still caught, because the push ships the full history.
	DiffAddedLines(ctx context.Context, dir, base string) (string, error)
	// ListReviews returns reviews for the given repoURL+PR number.
	ListReviews(ctx context.Context, repoURL string, pr int, etag string) ([]ReviewInfo, string, bool, error)
	// ListReviewComments returns inline comments for the given repoURL+PR number.
	ListReviewComments(ctx context.Context, repoURL string, pr int) ([]ReviewCommentInfo, error)
	// GetPR returns state and merged flag for the given repoURL+PR number.
	GetPR(ctx context.Context, repoURL string, pr int) (state string, merged bool, err error)
	// PostComment posts a comment on the given repoURL+PR number.
	PostComment(ctx context.Context, repoURL string, pr int, body string) error
	// AuthLogin returns the bot's login on the forge that hosts repoURL, so the
	// review self-filter uses the correct per-forge identity.
	AuthLogin(ctx context.Context, repoURL string) (string, error)
}

// EgressFinding mirrors egress.Finding so the mission package never imports
// internal/egress directly (same structural decoupling as ReviewInfo mirroring
// repo.Review). Severity is "block" (a detected secret — the auto-PR must be
// withheld) or "advisory" (surfaced as a finding but does not stop the push).
type EgressFinding struct {
	Path     string
	Line     int
	Rule     string
	Sample   string
	Severity string // "block" | "advisory"
}

// EgressScanner vets a mission's changed files right before they leave the
// brain (push + PR open) — the forge-agnostic egress gate. It runs regardless
// of which forge the mission targets. A nil Engine.Egress disables scanning
// entirely, leaving the push/PR flow for clean output unchanged.
type EgressScanner interface {
	Scan(ctx context.Context, dir string, files []string) []EgressFinding
	// ScanText scans the added lines of a `git log -p` patch (branch history)
	// for secrets — the history-aware companion to Scan's working-tree read, so
	// a commit-then-delete secret can't evade the gate. Findings are block-only.
	ScanText(text string) []EgressFinding
}

// Indexer is the interface the mission engine uses to index files changed by
// each gate-passed commit and drop the index when a mission completes.
// *repoindex.Store satisfies it; the mission package does not import repoindex
// directly (same decoupling pattern as RepoOps / repo.Engine).
type Indexer interface {
	IndexPaths(missionID int64, dir string, paths []string) error
	DropMission(missionID int64) error
}

// Engine drives missions over the task queue (the pull model): a mission's tasks
// are enqueued at creation; each tick the engine promotes tasks whose
// dependencies are now done (making them claimable by the hive), and marks a
// mission done once its queue is drained. It is deterministic and idempotent —
// the bees do the work; the engine only gates dependencies and detects
// completion.
type Engine struct {
	m *Store
	q *queue.Store

	// ConvergeBlockSeverity is the lowest OPEN-finding severity that withholds
	// mission certification at convergence (low|medium|high|critical). A drained
	// queue is not a clean bill of health: some finding types (design-flaw, note)
	// never become tasks and never block MissionDone. Rather than certify a
	// result it knows still holds such a defect, the engine routes the mission to
	// the human-gate `needs-review` terminal state. "" disables the gate.
	// Default: "high".
	ConvergeBlockSeverity string

	// NoProgressTicks is the deterministic give-up backstop: a running mission that
	// makes no forward progress (no task reaches a terminal state, no task is
	// added, no finding changes) for this many consecutive ticks WHILE nothing is
	// claimed (no agent is actively holding work) is transitioned to the terminal
	// `failed` state. 0 disables it. The specific stall detectors act sooner; this
	// is the catch-all that guarantees no mission hangs forever.
	NoProgressTicks int

	// Repo, when non-nil, enables per-phase commits and push+PR on mission done.
	// Workspace is the root directory under which per-mission working copies live
	// (MissionDir(Workspace, id) = <Workspace>/m<id>).
	Repo      RepoOps
	Workspace string

	// Verify is the sandboxed command-runner (see NewSandboxVerify/execBackend in
	// cmd/corral/main.go). The build-side final-state re-check that used to be its
	// only caller here is gone (retire-the-builder #4); the runner itself is kept
	// wired for slice 2 (control-gate style verification).
	Verify func(ctx context.Context, dir, command string) (ok bool, detail string)

	// Egress, when non-nil, scans a mission's cumulative changed-file set for
	// secrets (blocking) and advisory issues (Go dep vulns, incompatible
	// license files) right before push+PR — the last brain-side checkpoint
	// before the herd's output leaves for the forge. Nil disables the gate
	// entirely; a clean change set then proceeds exactly as before.
	Egress EgressScanner

	// Index, when non-nil, indexes the files changed by each gate-passed commit
	// and drops the per-mission index when the mission completes. Nil means skip
	// indexing (search is an aid, not a gate — never blocks Tick).
	Index Indexer

	// OnMissionCompleted, when non-nil, fires whenever the ENGINE (not the
	// human-gate resolve path — see mission.ResolveNeedsReview's caller)
	// transitions a mission to a terminal state ("done", or "failed" via the
	// give-up backstop). The caller wires it to telemetry so an auto-completed
	// mission speaks mission_completed the same way a resolved one does at its
	// own call site — mission.Store never imports telemetry, so this is the
	// engine's half of that split.
	OnMissionCompleted func(missionID int64, status string, reviewRounds int)

	// noProgress / lastFingerprint back the NoProgressTicks backstop: per mission,
	// the consecutive count of no-progress ticks and the last progress fingerprint.
	noProgress      map[int64]int
	lastFingerprint map[int64]string
}

func NewEngine(m *Store, q *queue.Store) *Engine {
	return &Engine{
		m:                     m,
		q:                     q,
		ConvergeBlockSeverity: "high",
		NoProgressTicks:       240, // generous backstop; the "nothing claimed" guard keeps it off slow-but-healthy work
		noProgress:            map[int64]int{},
		lastFingerprint:       map[int64]string{},
	}
}

// blockingFindingOpen reports whether the mission has an open finding at or above
// ConvergeBlockSeverity — the gate that keeps the engine from certifying a
// mission "done" while a known critical/high defect stands. Returns false when
// the gate is disabled (empty threshold) or the findings query fails, so a
// transient DB error degrades to the prior (permissive) convergence behaviour
// rather than wedging every mission at needs-review.
func (e *Engine) blockingFindingOpen(missionID int64) bool {
	if e.ConvergeBlockSeverity == "" {
		return false
	}
	minRank := queue.SeverityRank(e.ConvergeBlockSeverity)
	fs, err := e.q.Findings(missionID, queue.FindingOpen)
	if err != nil {
		log.Printf("mission %d: converge findings-gate query: %v", missionID, err)
		return false
	}
	for _, f := range fs {
		if queue.SeverityRank(f.Severity) >= minRank {
			return true
		}
	}
	return false
}

// Run ticks the engine until ctx is cancelled.
func (e *Engine) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := e.Tick(); err != nil {
				log.Printf("mission: tick: %v", err)
			}
		}
	}
}

// Tick advances every running mission one step: promote dependency-ready tasks,
// then complete the mission if every task is done. Exported so it can be driven
// deterministically in tests.
func (e *Engine) Tick() error {
	missions, err := e.m.RunningMissions()
	if err != nil {
		return err
	}
	for _, mi := range missions {
		if _, err := e.q.PromoteReady(mi.ID); err != nil {
			log.Printf("mission %d: promote: %v", mi.ID, err)
			continue
		}
		// Sweep tasks whose dependencies can never be satisfied (a cancelled/
		// superseded/missing dep) — converting an otherwise-invisible DAG deadlock
		// into a visible, cancelled task + a loud finding before the done-check.
		e.sweepBlockedDeps(mi.ID)
		done, err := e.q.MissionDone(mi.ID)
		if err != nil {
			log.Printf("mission %d: done-check: %v", mi.ID, err)
			continue
		}
		if !done {
			// Not converged. The give-up backstop watches for a mission that makes
			// no progress with nothing claimable and transitions it to `failed`.
			e.checkNoProgress(&mi)
			continue
		}
		// Queue drained. A drained queue can still hold an open finding at/above
		// the blocking severity that never became a task (some finding types,
		// e.g. design-flaw/note, are never auto-actioned). Rather than certify a
		// result it knows still holds that defect, route to the human-gate
		// `needs-review` terminal state — and do NOT push/PR. The human dismisses
		// the finding (then
		// ResolveNeedsReview converges it) or reworks. #findings-gate.
		if e.blockingFindingOpen(mi.ID) {
			_ = e.m.SetMissionStatus(mi.ID, "needs-review")
			if e.OnMissionCompleted != nil {
				e.OnMissionCompleted(mi.ID, "needs-review", mi.ReviewRounds)
			}
			continue
		}
		_ = e.m.SetMissionStatus(mi.ID, "done")
		if e.OnMissionCompleted != nil {
			e.OnMissionCompleted(mi.ID, "done", mi.ReviewRounds)
		}
	}
	return nil
}

// checkNoProgress is the give-up backstop. It fingerprints a running mission's
// progress (terminal-task count / total tasks / open findings). While the
// fingerprint keeps changing, or any task is claimed (an agent is actively
// holding work — slow is not stuck), the mission is fine. Only when the
// fingerprint has been unchanged AND nothing is claimed for NoProgressTicks
// consecutive ticks does it transition the mission to the terminal `failed`
// state — the catch-all guaranteeing no mission hangs in "running" forever.
func (e *Engine) checkNoProgress(m *Mission) {
	if e.NoProgressTicks <= 0 {
		return // backstop disabled
	}
	fp, claimed, err := e.progressFingerprint(m.ID)
	if err != nil {
		log.Printf("mission %d: progress check: %v", m.ID, err)
		return
	}
	if fp != e.lastFingerprint[m.ID] {
		e.lastFingerprint[m.ID] = fp
		e.noProgress[m.ID] = 0
		return
	}
	if claimed > 0 {
		return // an agent is actively holding work — slow, not stuck
	}
	e.noProgress[m.ID]++
	if e.noProgress[m.ID] >= e.NoProgressTicks {
		e.failMission(m, "no forward progress and nothing claimable")
	}
}

// progressFingerprint returns a string that changes whenever a mission makes
// forward progress (a task reaches a terminal state, a task is added, or a
// finding is filed/resolved), plus the number of currently-claimed tasks.
func (e *Engine) progressFingerprint(missionID int64) (string, int, error) {
	tasks, err := e.q.List(missionID)
	if err != nil {
		return "", 0, err
	}
	terminal, claimed := 0, 0
	for _, t := range tasks {
		switch t.Status {
		case queue.StatusDone, queue.StatusCancelled, queue.StatusSuperseded:
			terminal++
		case queue.StatusClaimed:
			claimed++
		}
	}
	open, err := e.q.Findings(missionID, queue.FindingOpen)
	if err != nil {
		return "", 0, err
	}
	return fmt.Sprintf("%d/%d/%d", terminal, len(tasks), len(open)), claimed, nil
}

// failMission transitions a mission to the terminal `failed` state: halt the
// queue so nothing keeps claiming, announce it loudly, fire the completion hook
// (so the failure lands in the same telemetry ledger as a "done"), and drop the
// backstop bookkeeping. RunningMissions excludes it, so it is never ticked again.
func (e *Engine) failMission(m *Mission, reason string) {
	if err := e.m.SetMissionStatus(m.ID, "failed"); err != nil {
		log.Printf("mission %d: set failed: %v", m.ID, err)
		return
	}
	if err := e.q.HaltMission(m.ID, "failed: "+reason); err != nil {
		log.Printf("mission %d: halt on fail: %v", m.ID, err)
	}
	log.Printf("mission %d: FAILED — %s", m.ID, reason)
	if e.OnMissionCompleted != nil {
		e.OnMissionCompleted(m.ID, "failed", m.ReviewRounds)
	}
	delete(e.noProgress, m.ID)
	delete(e.lastFingerprint, m.ID)
}

// sweepBlockedDeps cancels pending tasks whose dependencies can never be
// satisfied — a dep that is cancelled/superseded, or was never created — filing a
// loud, deduplicated finding for each. PromoteReady only promotes a task once
// every dep is `done`, so such a task would otherwise sit `pending` forever
// (MissionDone never converges; DetectRoleStalls only sees `ready` tasks). This
// turns that invisible hang into a visible failure the mission can move past.
func (e *Engine) sweepBlockedDeps(missionID int64) {
	tasks, err := e.q.List(missionID)
	if err != nil {
		log.Printf("mission %d: dep-sweep list: %v", missionID, err)
		return
	}
	status := make(map[string]string, len(tasks))
	for _, t := range tasks {
		status[t.Key] = t.Status
	}
	for _, t := range tasks {
		if t.Status != queue.StatusPending {
			continue
		}
		for _, dep := range t.DependsOn {
			st, exists := status[dep]
			// Unsatisfiable = the dep doesn't exist, or is terminal-but-not-done
			// (cancelled/superseded). A dep still pending/ready/claimed may yet
			// complete, so it is not swept here; if it later becomes orphaned
			// itself, a subsequent tick catches this task in turn.
			if !exists || st == queue.StatusCancelled || st == queue.StatusSuperseded {
				e.fileBlockedDepFinding(missionID, t.Key, dep, exists)
				if _, err := e.q.CancelTask(t.ID); err != nil {
					log.Printf("mission %d: dep-sweep cancel %s: %v", missionID, t.Key, err)
				}
				break
			}
		}
	}
}

// fileBlockedDepFinding records a blocked-dependency finding once per stuck task
// (deduped against open findings so a mission doesn't spam the ledger every tick).
func (e *Engine) fileBlockedDepFinding(missionID int64, taskKey, dep string, depExists bool) {
	target := "blocked-dep:" + taskKey
	if open, err := e.q.Findings(missionID, queue.FindingOpen); err == nil {
		for _, f := range open {
			if f.Reporter == "dep-sweep" && f.Target == target {
				return
			}
		}
	}
	why := "was cancelled or superseded"
	if !depExists {
		why = "does not exist in the mission"
	}
	if _, err := e.q.AddFinding(queue.Finding{
		MissionID: missionID, Reporter: "dep-sweep", Type: "missing-req", Severity: "high",
		Target:          target,
		Evidence:        "task '" + taskKey + "' depends on '" + dep + "', which " + why + " — it can never run, so it was cancelled",
		SuggestedAction: "re-plan without the dead dependency, or restore/replace '" + dep + "'",
	}); err != nil {
		log.Printf("mission %d: blocked-dep finding: %v", missionID, err)
		return
	}
	log.Printf("mission %d: dep-sweep cancelled '%s' — dependency '%s' can never be satisfied", missionID, taskKey, dep)
}
