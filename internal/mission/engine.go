// SPDX-License-Identifier: Elastic-2.0

package mission

import (
	"context"
	"fmt"
	"log"
	"strings"
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

	// Repo, when non-nil, enables per-phase commits and push+PR on mission done.
	// Workspace is the root directory under which per-mission working copies live
	// (MissionDir(Workspace, id) = <Workspace>/m<id>).
	Repo      RepoOps
	Workspace string

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
	// mission to "done". The caller wires it to telemetry so an auto-completed
	// mission speaks mission_completed the same way a reviewed one does at its
	// own call site — mission.Store never imports telemetry, so this is the
	// engine's half of that split.
	OnMissionCompleted func(missionID int64, status string, reviewRounds int)

	// committed tracks which phase names have already been committed per mission
	// so a re-tick after a transient error never produces duplicate commits.
	committed map[int64]map[string]bool

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

	// Staffing coordinates dynamic role allocations
	Staffing *StaffingManager
	staffed  map[int64]bool
}

// maxPRAttempts bounds how many times the reconcile loop retries push/PR for a
// single mission before giving up. Keeps a permanent failure from spinning
// forever (~every 3s) and hammering the GitHub API.
const maxPRAttempts = 5

func NewEngine(m *Store, q *queue.Store) *Engine {
	return &Engine{
		m:                 m,
		q:                 q,
		ReflexMinSeverity: "high",
		ReflexMaxTasks:    50,
		committed:         map[int64]map[string]bool{},
		prAttempts:        map[int64]int{},
		prGaveUp:          map[int64]bool{},
		egressBlocked:     map[int64]bool{},
		etags:             map[int64]string{},
		botLogins:         map[string]string{},
		staffed:           map[int64]bool{},
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
		// Dynamic role staffing: Sense ➔ Judge ➔ Clamp
		if e.Staffing != nil && !e.staffed[mi.ID] && e.Staffing.LLM != nil && e.Staffing.LLM.Available() {
			log.Printf("mission %d: starting dynamic staffing...", mi.ID)
			resources := e.Staffing.Sense()
			stats := e.Staffing.Perf.GetRoleModelStats()

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			assignments, loadOrder, err := e.Staffing.Judge(ctx, mi.Directive, resources, stats, 3, 3)
			cancel()

			if err != nil {
				log.Printf("mission %d: dynamic staffing judge failed: %v (using default policy)", mi.ID, err)
			} else {
				clamped := e.Staffing.Clamp(assignments, resources)
				log.Printf("mission %d: dynamic staffing complete. Clamped Assignments: %+v, Load Order: %v", mi.ID, clamped, loadOrder)

				for role, model := range clamped {
					backend := "ollama"
					if isCloudModel(model) {
						if strings.Contains(strings.ToLower(model), "claude") {
							backend = "anthropic"
						} else if strings.Contains(strings.ToLower(model), "gpt") {
							backend = "openai"
						} else if strings.Contains(strings.ToLower(model), "gemini") {
							backend = "openai"
						}
					}
					if colonIdx := strings.Index(model, ":"); colonIdx >= 0 {
						backend = model[:colonIdx]
						model = model[colonIdx+1:]
					}
					e.Staffing.RoleModels[role] = rolemodel.ModelRef{
						Backend: backend,
						Model:   model,
					}
				}
				e.staffed[mi.ID] = true
			}
		}

		// Reflex re-planning first: open findings spawn remediation tasks before
		// we promote/complete, so a finding on the last task revives the mission.
		if err := e.replan(mi.ID); err != nil {
			log.Printf("mission %d: replan: %v", mi.ID, err)
		}
		if _, err := e.q.PromoteReady(mi.ID); err != nil {
			log.Printf("mission %d: promote: %v", mi.ID, err)
			continue
		}
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
// not just the last commit) right before push+PR — the last brain-side
// checkpoint before the herd's output leaves for the forge. Every finding is
// filed as a queue.Finding so it is visible the same way any other finding is
// (UI feed, learn sweep, resolve_finding). It returns true iff a BLOCKING
// finding (a detected secret) was seen, in which case the mission is parked in
// egressBlocked and push/PR is withheld — loud (logged) and blocking, per the
// egress-scan gate contract. Advisory findings (dep vulns, license issues) are
// filed but never withhold the push.
func (e *Engine) runEgressGate(m *Mission) bool {
	files, err := e.Repo.ChangedFilesRange(context.Background(), e.workdir(m), m.Base)
	if err != nil {
		log.Printf("mission %d: egress: changed-files: %v (scan skipped, not blocked)", m.ID, err)
		return false
	}
	if len(files) == 0 {
		return false
	}
	findings := e.Egress.Scan(context.Background(), e.workdir(m), files)
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
