// SPDX-License-Identifier: Elastic-2.0

package mission

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/rolemodel"
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

	// ReflexMinSeverity is the lowest finding severity the reflex re-planner acts
	// on (low|medium|high|critical). ReflexMaxTasks bounds reflex-generated tasks
	// per mission so a non-converging verify→finding→fix cycle can't run away.
	ReflexMinSeverity string
	ReflexMaxTasks    int

	// ConvergeBlockSeverity is the lowest OPEN-finding severity that withholds
	// mission certification at convergence (low|medium|high|critical). A drained
	// queue is not a clean bill of health: reflexRules deems some finding types
	// (design-flaw, note) non-actionable, so a critical one of those never becomes
	// a task and never blocks MissionDone. Rather than certify a result it knows
	// still holds such a defect, the engine routes the mission to the human-gate
	// `needs-review` terminal state. "" disables the gate. Default: "high".
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

	// Verify, when non-nil, re-runs the mission's own verify commands against the
	// FINAL working copy before convergence (#42) — so a mission cannot reach
	// "done" on an earlier per-task pass while the final tree is actually broken.
	// It is the same runner the completion gate uses; nil disables the check.
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

	// OnFindingResolved, when non-nil, fires whenever the ENGINE transitions a
	// finding (the reflex re-planner auto-addressing one). The caller wires it
	// to telemetry so engine-side resolutions land in the same event log as the
	// resolve_finding MCP tool — without this, model_comparison counts every
	// reflex-addressed finding as still open.
	OnFindingResolved func(f queue.Finding, outcome string)

	// OnMissionCompleted, when non-nil, fires whenever the ENGINE (not the
	// review-accept path — see mission.SubmitReview's caller) transitions a
	// mission to a terminal state ("done", or "failed" via the give-up backstop).
	// The caller wires it to telemetry so an auto-completed
	// mission speaks mission_completed the same way a reviewed one does at its
	// own call site — mission.Store never imports telemetry, so this is the
	// engine's half of that split.
	OnMissionCompleted func(missionID int64, status string, reviewRounds int)

	// OnReflexCapExhausted, when non-nil, fires whenever the ENGINE hits the
	// task cap limit on auto-remediation, letting the caller record telemetry
	// and pause the mission to prevent infinite loops.
	OnReflexCapExhausted func(missionID int64, cap int, f queue.Finding)

	// committed tracks which phase names have already been committed per mission
	// so a re-tick after a transient error never produces duplicate commits.
	committed map[int64]map[string]bool

	// noProgress / lastFingerprint back the NoProgressTicks backstop: per mission,
	// the consecutive count of no-progress ticks and the last progress fingerprint.
	noProgress      map[int64]int
	lastFingerprint map[int64]string

	// prAttempts counts failed push/PR attempts per mission; prGaveUp records the
	// missions that hit maxPRAttempts so the reconcile loop stops hammering a
	// permanently-failing push/PR (bad auth, branch protection, etc.) every tick.
	prAttempts map[int64]int
	prGaveUp   map[int64]bool

	// egressBlocked records missions whose changed files tripped a blocking
	// egress finding (a detected secret). Permanent, unlike prGaveUp's retry
	// cap: a secret doesn't disappear on the next tick, so retrying push/PR
	// would just re-detect it forever. A human must intervene (fix the mission's
	// working copy / rotate the credential) — this map only prevents the
	// reconcile loop from hammering the same blocked mission every tick.
	egressBlocked map[int64]bool

	// etags caches the ETag returned by GitHub's ListReviews per mission, enabling
	// 304-Not-Modified short-circuits that cost ~0 API quota.
	etags map[int64]string

	// botLogins caches the authenticated bot login PER REPO URL (i.e. per forge
	// identity), resolved lazily via AuthLogin(repoURL) inside the review loop.
	// Keying by mi.Repo — not a single global login — means a mission on one forge
	// self-filters against THAT forge's bot identity, never another's (a Gitea
	// mission must not compare reviewers to the GitHub bot login). Cached once
	// non-empty; each poll retries a still-empty key, so a transient AuthLogin
	// failure only disables the guard for that one cycle.
	botLogins map[string]string

	// Staffing coordinates dynamic role allocations. The per-mission Judge call
	// is a 30s-bounded blocking LLM round-trip, so it runs OFF the tick goroutine
	// (see staffMission) — one mission's staffing must never head-of-line block
	// every other mission's promote/converge/PR work. Because a goroutine now
	// touches the staffing bookkeeping, all four maps below are guarded by staffMu.
	//
	//   staffed       — once-per-mission-on-success latch (staffing done, don't redo)
	//   staffInflight — a staffMission goroutine is currently running for this id
	//   staffAttempts — count of failed Judge probes per mission
	//   staffGaveUp   — hit maxStaffAttempts; stop probing, use the default policy
	//
	// staffWG lets tests deterministically await the async pass (waitStaffingIdle);
	// Tick Add()s before dispatch and the worker Done()s on exit.
	Staffing      *StaffingManager
	staffMu       sync.Mutex
	staffed       map[int64]bool
	staffInflight map[int64]bool
	staffAttempts map[int64]int
	staffGaveUp   map[int64]bool
	staffWG       sync.WaitGroup
}

// maxStaffAttempts bounds how many times a mission's dynamic-staffing Judge probe
// is retried before the engine gives up and falls back to the default role policy.
// Mirrors maxPRAttempts: a permanently-failing probe (bad LLM endpoint, malformed
// response) backs off instead of burning a 30s round-trip every tick.
const maxStaffAttempts = 3

// maxPRAttempts bounds how many times the reconcile loop retries push/PR for a
// single mission before giving up. Keeps a permanent failure from spinning
// forever (~every 3s) and hammering the GitHub API.
const maxPRAttempts = 5

func NewEngine(m *Store, q *queue.Store) *Engine {
	return &Engine{
		m:                     m,
		q:                     q,
		ReflexMinSeverity:     "high",
		ReflexMaxTasks:        50,
		ConvergeBlockSeverity: "high",
		NoProgressTicks:       240, // generous backstop; the "nothing claimed" guard keeps it off slow-but-healthy work
		committed:             map[int64]map[string]bool{},
		noProgress:            map[int64]int{},
		lastFingerprint:       map[int64]string{},
		prAttempts:            map[int64]int{},
		prGaveUp:              map[int64]bool{},
		egressBlocked:         map[int64]bool{},
		etags:                 map[int64]string{},
		botLogins:             map[string]string{},
		staffed:               map[int64]bool{},
		staffInflight:         map[int64]bool{},
		staffAttempts:         map[int64]int{},
		staffGaveUp:           map[int64]bool{},
	}
}

// hasOpenChangeRequests reports whether a mission has unaddressed client feedback
// (change-request findings) — which keeps the engine from gating to
// awaiting_review before the lead has turned that feedback into rework.
func (e *Engine) hasOpenChangeRequests(missionID int64) bool {
	fs, err := e.q.Findings(missionID, queue.FindingOpen)
	if err != nil {
		return false
	}
	for _, f := range fs {
		if f.Type == "change-request" {
			return true
		}
	}
	return false
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

// staffedModelRef derives the model reference for a clamped assignment. Clamped
// values are model IDENTIFIERS — an Ollama name:tag (qwen2.5-coder:7b) or a cloud
// model name — NEVER "backend:model", so we must not split on ':' (doing so
// corrupted every local tag). Cloud models map to their provider backend; every-
// thing else is an Ollama tag kept whole.
func staffedModelRef(model string) rolemodel.ModelRef {
	backend := "ollama"
	if isCloudModel(model) {
		lower := strings.ToLower(model)
		switch {
		case strings.Contains(lower, "claude"):
			backend = "anthropic"
		case strings.Contains(lower, "gpt"):
			backend = "openai"
		case strings.Contains(lower, "gemini"):
			backend = "openai" // NOTE: audit L-item — engine vs backend.go gemini mapping disagree; out of scope for this task, keep as-is
		}
	}
	return rolemodel.ModelRef{Backend: backend, Model: model}
}

// staffMission runs the Sense→Judge→Clamp staffing pass for one mission OFF the
// tick goroutine. On success it latches staffed[id] (once-per-mission-on-success);
// on failure it counts an attempt and, after maxStaffAttempts, latches staffGaveUp
// and falls back to the default role policy (never applying a nil assignment map),
// so a failing probe never re-runs the 30s Judge every tick or stalls the tick
// loop. All staffing-bookkeeping access is under staffMu.
func (e *Engine) staffMission(missionID int64, directive string) {
	defer e.staffWG.Done()
	defer func() {
		e.staffMu.Lock()
		delete(e.staffInflight, missionID)
		e.staffMu.Unlock()
	}()
	resources := e.Staffing.Sense()
	stats := e.Staffing.Perf.GetRoleModelStats()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	assignments, loadOrder, err := e.Staffing.Judge(ctx, directive, resources, stats, 3, 3)
	cancel()
	if err != nil {
		e.staffMu.Lock()
		e.staffAttempts[missionID]++
		attempts := e.staffAttempts[missionID]
		if attempts >= maxStaffAttempts {
			e.staffGaveUp[missionID] = true
		}
		e.staffMu.Unlock()
		if attempts >= maxStaffAttempts {
			log.Printf("mission %d: dynamic staffing gave up after %d attempts — using default policy", missionID, attempts)
		} else {
			log.Printf("mission %d: dynamic staffing judge failed (attempt %d): %v", missionID, attempts, err)
		}
		return
	}
	clamped := e.Staffing.Clamp(assignments, resources)
	log.Printf("mission %d: dynamic staffing complete. Clamped: %+v, Load Order: %v", missionID, clamped, loadOrder)
	for role, model := range clamped {
		e.Staffing.RoleModels.Set(role, staffedModelRef(model)) // threadsafe: the UI writes this same policy from the mission-create handler
	}
	e.staffMu.Lock()
	e.staffed[missionID] = true
	e.staffMu.Unlock()
}

// waitStaffingIdle blocks until no staffMission goroutine is in flight. Test-only
// sync hook: Tick dispatches staffing asynchronously, so a deterministic test must
// await the pass before asserting on it. Unexported by design.
func (e *Engine) waitStaffingIdle() { e.staffWG.Wait() }

// Tick advances every running mission one step: promote dependency-ready tasks,
// then complete the mission if every task is done. Exported so it can be driven
// deterministically in tests.
func (e *Engine) Tick() error {
	missions, err := e.m.RunningMissions()
	if err != nil {
		return err
	}
	for _, mi := range missions {
		// Dynamic role staffing: Sense ➔ Judge ➔ Clamp, run at most once per
		// mission and OFF the tick goroutine (the Judge call is a 30s-bounded LLM
		// round-trip). Dispatch is guarded by staffMu + a bounded attempt cap so a
		// failing Judge backs off and gives up instead of re-probing every tick or
		// head-of-line blocking every other mission behind a 30s stall.
		if e.Staffing != nil && e.Staffing.LLM != nil && e.Staffing.LLM.Available() {
			e.staffMu.Lock()
			skip := e.staffed[mi.ID] || e.staffInflight[mi.ID] || e.staffGaveUp[mi.ID]
			if !skip {
				e.staffInflight[mi.ID] = true
				e.staffWG.Add(1)
			}
			e.staffMu.Unlock()
			if !skip {
				go e.staffMission(mi.ID, mi.Directive)
			}
		}

		// Reflex re-planning first: open findings spawn remediation tasks before
		// we promote/complete, so a finding on the last task revives the mission.
		if err := e.replan(mi.ID); err != nil {
			log.Printf("mission %d: replan: %v", mi.ID, err)
		}
		// replan may have transitioned the mission to a terminal state (the reflex
		// cap failing it). Don't promote / converge / PR a mission that's no longer
		// running.
		if cur, err := e.m.Mission(mi.ID); err != nil || cur == nil || cur.Status != "running" {
			continue
		}
		if _, err := e.q.PromoteReady(mi.ID); err != nil {
			log.Printf("mission %d: promote: %v", mi.ID, err)
			continue
		}
		// Sweep tasks whose dependencies can never be satisfied (a cancelled/
		// superseded/missing dep) — converting an otherwise-invisible DAG deadlock
		// into a visible, cancelled task + a loud finding before the done-check.
		e.sweepBlockedDeps(mi.ID)
		// Commit any newly-done phases before checking for mission completion so
		// the final phase's commit lands even if MissionDone fires on this tick.
		if e.Repo != nil && mi.Repo != "" {
			e.commitDonePhases(&mi)
		}
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
		// Queue drained. A review-enabled mission waits for the client to ACCEPT
		// instead of auto-completing — but only once any open client feedback
		// (change-request findings) has been turned into rework by the lead, so we
		// don't re-gate before this sprint's feedback is acted on.
		if mi.RequiresReview {
			if !e.hasOpenChangeRequests(mi.ID) {
				_ = e.m.SetMissionStatus(mi.ID, "awaiting_review")
			}
		} else {
			// #42: a drained queue is not the same as a correct final state — a
			// later edit can break an earlier per-task pass. Before converging, the
			// brain re-runs the mission's verify commands against the FINAL working
			// copy. On failure, hold the mission back (a loud finding drives the
			// reflex re-planner) instead of shipping a broken tree.
			if e.Verify != nil && !e.finalStateOK(&mi) {
				continue
			}
			// A drained, verifying queue can still hold an open finding at/above the
			// blocking severity that never became a task (reflexRules leaves
			// design-flaw/note non-actionable). Rather than certify a result it knows
			// still holds that defect, route to the human-gate `needs-review` terminal
			// state — and do NOT push/PR. The human dismisses the finding (then
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
			if e.Repo != nil {
				e.finishRepoMission(mi.ID) // fast path
			}
		}
	}
	// Reconcile pass: push+PR any done repo-mission still missing a PR. This
	// catches review-accepted missions (which complete via SubmitReview, outside
	// this loop) and retries any mission whose earlier push/PR failed transiently.
	// The MissionsPendingPR filter excludes a mission once its PRURL is set, so
	// there's no double-PR and the non-review fast path above stays unchanged.
	if e.Repo != nil {
		pending, err := e.m.MissionsPendingPR()
		if err != nil {
			log.Printf("mission: pending-PR query: %v", err)
		} else {
			for _, mi := range pending {
				e.finishRepoMission(mi.ID)
			}
		}
	}
	return nil
}

// workdir returns the per-mission working-copy path.
func (e *Engine) workdir(m *Mission) string { return MissionDir(e.Workspace, m.ID) }

// finalStateOK re-runs each distinct verify command the mission's tasks declared
// against the mission's FINAL working copy (#42). It returns true when every
// command exits 0 — or when there is no working copy to run against (mirrors the
// completion gate's fallback) or there is nothing to verify. On the first failure
// it files a loud, deduplicated final-state regression finding and returns false,
// holding the mission back for the reflex re-planner.
func (e *Engine) finalStateOK(m *Mission) bool {
	dir := e.workdir(m)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return true // no brain-owned working copy — cannot independently verify
	}
	tasks, err := e.q.List(m.ID)
	if err != nil {
		log.Printf("mission %d: final-verify list: %v", m.ID, err)
		return true // never block convergence on an infrastructure error
	}
	seen := map[string]bool{}
	for _, t := range tasks {
		cmd := strings.TrimSpace(t.Verify)
		if cmd == "" || seen[cmd] {
			continue
		}
		seen[cmd] = true
		if ok, detail := e.Verify(context.Background(), dir, cmd); !ok {
			e.fileFinalStateFinding(m.ID, cmd, detail)
			return false
		}
	}
	return true
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

// fileFinalStateFinding records a final-state regression finding once per failing
// command (deduped against open findings so a held-back mission doesn't spam the
// ledger or over-count the reflex cap every tick).
func (e *Engine) fileFinalStateFinding(missionID int64, cmd, detail string) {
	target := "final-state:" + cmd
	if open, err := e.q.Findings(missionID, queue.FindingOpen); err == nil {
		for _, f := range open {
			if f.Reporter == "verify-gate" && f.Target == target {
				return
			}
		}
	}
	if len(detail) > 200 {
		detail = detail[:200]
	}
	if _, err := e.q.AddFinding(queue.Finding{
		MissionID: missionID, Reporter: "verify-gate", Type: "regression", Severity: "high",
		Target:          target,
		Evidence:        "final-state re-run of '" + cmd + "' failed: " + detail,
		SuggestedAction: "run '" + cmd + "' against the final tree and fix the failures, then it can converge",
	}); err != nil {
		log.Printf("mission %d: final-state finding: %v", missionID, err)
		return
	}
	log.Printf("mission %d: final-state verify FAILED (%s) — held back from done", missionID, cmd)
}

// commitDonePhases commits any phase that became done since the last tick.
// Phase status is derived from tasks via View (the phases table itself is not
// updated by the engine), so this is idempotent across ticks.
func (e *Engine) commitDonePhases(m *Mission) {
	mv, err := e.m.View(m.ID, e.q)
	if err != nil || mv == nil {
		return
	}
	seen := e.committed[m.ID]
	if seen == nil {
		seen = map[string]bool{}
		e.committed[m.ID] = seen
	}
	for _, p := range mv.Phases {
		if p.Status != "done" || seen[p.Name] {
			continue
		}
		// Mark the phase seen only AFTER a non-error commit. A transient git error
		// must leave the phase unmarked so a later tick (or finishRepoMission's
		// re-commit) retries it — otherwise Push could ship a branch missing that
		// phase's work. Commit is idempotent (no-op on an empty diff), so retrying
		// a phase that did land is safe.
		ok, err := e.Repo.Commit(context.Background(), e.workdir(m), p.Name+": "+m.Directive)
		if err != nil {
			log.Printf("mission %d: commit phase %s: %v", m.ID, p.Name, err)
			continue // leave unmarked → retried next tick
		}
		seen[p.Name] = true
		if ok {
			log.Printf("mission %d: committed phase %s", m.ID, p.Name)
			if e.Index != nil {
				changed, cerr := e.Repo.ChangedFiles(context.Background(), e.workdir(m))
				if cerr != nil {
					log.Printf("mission %d: changed files phase %s: %v", m.ID, p.Name, cerr)
				} else if len(changed) > 0 {
					if ierr := e.Index.IndexPaths(m.ID, e.workdir(m), changed); ierr != nil {
						log.Printf("mission %d: index phase %s: %v", m.ID, p.Name, ierr)
					}
				}
			}
		}
	}
}

// runEgressGate scans the mission's cumulative changed-file set (base...HEAD,
// not just the last commit) AND the full branch history's added lines
// (git log -p base..HEAD, so a secret committed then deleted still can't ship)
// right before push+PR — the last brain-side
// checkpoint before the herd's output leaves for the forge. Every finding is
// filed as a queue.Finding so it is visible the same way any other finding is
// (UI feed, learn sweep, resolve_finding). It returns true iff a BLOCKING
// finding (a detected secret) was seen, in which case the mission is parked in
// egressBlocked and push/PR is withheld — loud (logged) and blocking, per the
// egress-scan gate contract. Advisory findings (dep vulns, license issues) are
// filed but never withhold the push. If the changed-file diff itself cannot be
// computed, the gate fails closed (returns true / blocked) rather than letting
// an unscanned push through.
func (e *Engine) runEgressGate(m *Mission) bool {
	files, err := e.Repo.ChangedFilesRange(context.Background(), e.workdir(m), m.Base)
	if err != nil {
		log.Printf("mission %d: egress: changed-files failed: %v — BLOCKING push (fail-closed: cannot scan what we can't diff)", m.ID, err)
		e.egressBlocked[m.ID] = true
		return true
	}
	if len(files) == 0 {
		return false
	}
	dir := e.workdir(m)
	// Working-tree scan (line-addressable, on-disk-only content) PLUS a full
	// branch-history scan (git log -p base..HEAD): a secret committed in an
	// earlier phase and then deleted leaves a clean tree but still ships in the
	// push, so scanning the current tree alone misses it. Belt-and-suspenders.
	findings := e.Egress.Scan(context.Background(), dir, files)
	if diff, derr := e.Repo.DiffAddedLines(context.Background(), dir, m.Base); derr != nil {
		// A history diff we can't compute is a scan we can't complete — fail
		// closed rather than push an unscanned branch (same posture as the
		// changed-files failure above).
		log.Printf("mission %d: egress: history diff failed: %v — BLOCKING push (fail-closed: cannot scan branch history)", m.ID, derr)
		e.egressBlocked[m.ID] = true
		return true
	} else {
		findings = append(findings, e.Egress.ScanText(diff)...)
	}
	blocked := false
	for _, f := range findings {
		sev := "high"
		if f.Severity == "block" {
			sev = "critical"
			blocked = true
		}
		_, ferr := e.q.AddFinding(queue.Finding{
			MissionID: m.ID, Reporter: "egress-scan", Type: "vuln", Severity: sev,
			Target:          f.Path,
			Evidence:        fmt.Sprintf("line %d: %s: %s", f.Line, f.Rule, f.Sample),
			SuggestedAction: "remove the offending content (and rotate any exposed credential) before this mission can ship",
		})
		if ferr != nil {
			log.Printf("mission %d: egress: record finding: %v", m.ID, ferr)
		}
	}
	if blocked {
		e.egressBlocked[m.ID] = true
		log.Printf("mission %d: EGRESS BLOCKED — secret(s) detected in changed files; push/PR withheld (%d finding(s))", m.ID, len(findings))
	}
	return blocked
}

// finishRepoMission pushes the mission's branch and opens a PR. Errors are
// logged (never crash Tick) and the local branch is always preserved.
//
// Push/PR failures are retried by the reconcile loop, but only up to
// maxPRAttempts: a genuinely permanent failure (bad auth, branch protection)
// would otherwise spin every tick forever. After the cap the mission is parked
// in prGaveUp and skipped silently until the process restarts or the PRURL is
// set out of band.
func (e *Engine) finishRepoMission(id int64) {
	if e.prGaveUp[id] || e.egressBlocked[id] {
		return // already exhausted retries, or blocked on a detected secret — no work, no log
	}
	m, err := e.m.Mission(id)
	if err != nil || m == nil || m.Repo == "" {
		return
	}
	e.commitDonePhases(m) // catch any final phase not yet committed
	if e.Egress != nil && e.runEgressGate(m) {
		return // blocked: findings filed, push/PR withheld
	}
	if err := e.Repo.Push(context.Background(), e.workdir(m), m.Branch); err != nil {
		log.Printf("mission %d: push: %v (local branch preserved)", id, err)
		e.recordPRFailure(id)
		return
	}
	url, err := e.Repo.OpenPR(context.Background(), m.Repo, m.Branch, m.Base, "corralai: "+m.Directive, "Built by the corralai swarm.")
	if err != nil {
		log.Printf("mission %d: open PR: %v", id, err)
		e.recordPRFailure(id)
		return
	}
	_ = e.m.SetPRURL(id, url)
	if e.Index != nil {
		if ierr := e.Index.DropMission(id); ierr != nil {
			log.Printf("mission %d: drop index: %v", id, ierr)
		}
	}
	delete(e.committed, id) // mission is complete — release its per-phase commit set
	delete(e.prAttempts, id)
	delete(e.prGaveUp, id) // keep the retry maps bounded
	log.Printf("mission %d: PR opened: %s", id, url)
}

// recordPRFailure increments the push/PR attempt counter for a mission and, once
// it reaches maxPRAttempts, parks the mission in prGaveUp (logging exactly once)
// so the reconcile loop stops retrying a permanent failure every tick.
// When giving up, the per-mission code index is also dropped: the work is over
// either way, and orphaned chunks would persist for the life of the DB otherwise.
func (e *Engine) recordPRFailure(id int64) {
	e.prAttempts[id]++
	if e.prAttempts[id] >= maxPRAttempts {
		e.prGaveUp[id] = true
		log.Printf("mission %d: giving up on push/PR after %d attempts", id, e.prAttempts[id])
		if e.Index != nil {
			if ierr := e.Index.DropMission(id); ierr != nil {
				log.Printf("mission %d: drop index after PR give-up: %v", id, ierr)
			}
		}
	}
}

// maxReviewRounds is the maximum number of CHANGES_REQUESTED→rework cycles the
// engine will handle automatically. On hitting the cap the mission is parked and
// a human-handoff comment is posted.
const maxReviewRounds = 3

// ReviewPoll checks every done mission that has an open PR for new
// CHANGES_REQUESTED reviews and, when it finds one, reopens the mission with
// review-derived phases. It is safe to call concurrently with Tick (both
// operate on the same *Store but use separate DB connections via the store's
// mutex). Any GitHub API error is logged and the mission is skipped for that
// cycle — the polling cadence is the implicit backoff.
func (e *Engine) ReviewPoll() error {
	if e.Repo == nil {
		return nil
	}
	ctx := context.Background()
	missions, err := e.m.MissionsWithOpenPR()
	if err != nil {
		return err
	}
	for _, mi := range missions {
		pr, perr := ParsePRNumber(mi.PRURL)
		if perr != nil {
			log.Printf("review: mission %d: %v", mi.ID, perr)
			continue
		}
		// Resolve the bot's own login for the self-trigger guard on THIS mission's
		// forge (keyed by mi.Repo), retrying while still empty so a transient
		// AuthLogin failure doesn't permanently disable the guard (cached once
		// non-empty; empty → guard off for this cycle only). Routing by repoURL
		// means a mission on one forge never filters against another forge's bot.
		botLogin := e.botLogins[mi.Repo]
		if botLogin == "" {
			if login, _ := e.Repo.AuthLogin(ctx, mi.Repo); login != "" {
				botLogin = login
				e.botLogins[mi.Repo] = login
			}
		}
		// Park if the PR is already closed/merged — nothing to respond to. A GetPR
		// error is logged and the mission skipped for this cycle (spec invariant:
		// any forge error → log + skip; the ticker cadence is the backoff).
		state, merged, gerr := e.Repo.GetPR(ctx, mi.Repo, pr)
		if gerr != nil {
			log.Printf("review: mission %d GetPR: %v", mi.ID, gerr)
			continue
		}
		if merged || state == "closed" {
			_ = e.m.SetReviewState(mi.ID, mi.ReviewRounds, mi.ReviewWatermark, true)
			delete(e.etags, mi.ID) // parked — release its cached ETag
			continue
		}
		revs, etag, notMod, lerr := e.Repo.ListReviews(ctx, mi.Repo, pr, e.etags[mi.ID])
		if lerr != nil {
			log.Printf("review: mission %d list: %v", mi.ID, lerr)
			continue
		}
		e.etags[mi.ID] = etag
		if notMod {
			continue // 304 Not-Modified — no new reviews, ~0 quota consumed
		}
		// One round per poll, by design: act on the single NEWEST actionable review
		// and advance the watermark past any older ones. A superseded review's
		// summary body is intentionally not turned into a phase (its inline comments
		// are still fetched below via ListReviewComments, which returns all of a PR's
		// comments regardless of which review submitted them).
		rev, ok := newestActionable(revs, mi.ReviewWatermark, botLogin)
		if !ok {
			continue
		}
		if mi.ReviewRounds >= maxReviewRounds {
			_ = e.Repo.PostComment(ctx, mi.Repo, pr,
				fmt.Sprintf("🐝 This PR has reached the %d-round auto-response limit — handing back to a human.", maxReviewRounds))
			_ = e.m.SetReviewState(mi.ID, mi.ReviewRounds, rev.SubmittedAt, true)
			delete(e.etags, mi.ID) // parked — release its cached ETag
			continue
		}
		comments, _ := e.Repo.ListReviewComments(ctx, mi.Repo, pr)
		phases := reviewPhases(mi.ReviewRounds+1, rev, comments)
		if err := e.m.ReopenForReview(e.q, mi.ID, phases, rev.SubmittedAt); err != nil {
			log.Printf("review: mission %d reopen: %v", mi.ID, err)
			continue // watermark NOT advanced → retried next poll
		}
		_ = e.Repo.PostComment(ctx, mi.Repo, pr,
			fmt.Sprintf("🐝 Addressing your review (round %d/%d)…", mi.ReviewRounds+1, maxReviewRounds))
		log.Printf("review: mission %d reopened for round %d", mi.ID, mi.ReviewRounds+1)
	}
	return nil
}

// newestActionable returns the newest CHANGES_REQUESTED review submitted after
// the watermark and NOT authored by the bot. ok=false if no such review exists.
func newestActionable(revs []ReviewInfo, watermark, botLogin string) (ReviewInfo, bool) {
	var best ReviewInfo
	found := false
	for _, r := range revs {
		if r.State != "CHANGES_REQUESTED" {
			continue
		}
		if botLogin != "" && r.User == botLogin {
			continue // never react to our own reviews (self-trigger guard)
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

// reviewPhases turns a CHANGES_REQUESTED review and its inline comments into
// round-scoped phases: one per touched file plus one for the review body (if
// non-empty). Each phase carries a `go build ./...` verify gate so the fix
// must compile before the phase can complete.
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
	if len(phases) == 0 {
		// CHANGES_REQUESTED with no body and no inline comments — one generic phase.
		phases = append(phases, PhaseSpec{
			Name:        fmt.Sprintf("review-r%d-address", round),
			Instruction: "A reviewer requested changes on this PR. Re-examine the diff, address the concerns, and let the verify gate confirm the build.",
			Count:       1,
			Verify:      "go build ./...",
		})
	}
	return phases
}

// sanitizePhase converts a file path into a safe phase-name segment (slashes
// and dots → hyphens) so phase names remain valid task keys.
func sanitizePhase(s string) string {
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, ".", "-")
	return s
}
