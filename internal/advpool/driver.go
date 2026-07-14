// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"fmt"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/repoindex"
)

// Scorer wraps adequacy.Score in the jail: the brain-side, sandboxed judge
// that actually RUNS a candidate test against the compliant code plus a set
// of mutants and reports the kill rate. The driver NEVER derives
// DevKillRate/ProvenMissed from a worker's self-report — only from this
// (soundness #1: "a judge may not certify herself").
type Scorer interface {
	Score(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (killRate float64, survivors []adequacy.Mutant, err error)
}

// Validator is brain-side artifact validation of a worker's structured
// result, run before the driver trusts it enough to score or promote on it.
type Validator interface {
	CompileTest(ctx context.Context, codePath, code, test string) error
	ParseMutants(raw string) ([]adequacy.Mutant, error) // = testgen.ParseMutantsOutput
}

// Verdict status values. Never auto-certified: a blocking finding or a
// below-threshold DevKillRate always routes to needs-review.
const (
	StatusCertified   = "certified"
	StatusNeedsReview = "needs-review"
)

// Verdict is one run's final, gated outcome.
type Verdict struct {
	Repo, Commit    string
	DevKillRate     float64         // the headline: the DEV suite's kill-rate, from Scorer — never a self-report
	MutantsTotal    int             // total mutants the mutant-generator produced
	Survivors       int             // mutants the dev's own tests did NOT kill
	ProvenMissed    int             // survivors the pool's authored test then killed — real, catchable gaps
	VacuousFindings []queue.Finding // test-critic's designed-to-pass/vacuous flags
	ModelsByRole    map[string]string
	Status          string // certified | needs-review
}

// CheckDecorrelation rejects an assignment where test-critic and test-writer
// share a model. A test-critic judging tests written by its own model (or a
// copy of it) is not an independent check — it is the same failure mode
// grading its own homework. Enforced at driver construction (soundness #2).
func CheckDecorrelation(assign RoleAssignment) error {
	critic, writer := assign[RoleTestCritic], assign[RoleTestWriter]
	if critic != "" && critic == writer {
		return fmt.Errorf("advpool: decorrelation guard: test-critic and test-writer must not share model %q", critic)
	}
	return nil
}

// runState is one run's mutable progress, tracked across ticks. The
// test-writer task's id (not its key) is tracked explicitly because
// SupersedeTask auto-uniquifies the replacement's key when it reuses the
// original — see the comment in Tick.
type runState struct {
	rs   RunSpec
	sigs []repoindex.Signature

	devScored    bool
	devKillRate  float64
	mutantsTotal int
	devSurvivors []adequacy.Mutant

	testWriterTaskID int64

	poolScored   bool
	provenMissed int

	verdict *Verdict
}

// Driver runs one or more adversarial-pool runs' tick state machines over
// injected effects: a real *queue.Store (cheap local SQLite — the same
// substrate the mission engine drives directly, see internal/mission.Engine)
// plus a Scorer and Validator. It has no jail/brain/LLM wiring of its own —
// that's Task 4.3 (real Scorer/Validator) and 5.1 (brain integration); this
// driver is pure and fully unit-testable with fakes.
type Driver struct {
	Q         *queue.Store
	Scorer    Scorer
	Validator Validator
	Assign    RoleAssignment

	// Threshold is the minimum DevKillRate a run may be auto-certified at;
	// below it (or with any blocking finding open) the run is routed to
	// needs-review — the human gate never auto-certifies a weak dev suite.
	Threshold float64

	// BlockSeverity is the lowest open-finding severity that forces
	// needs-review at aggregate time (mirrors mission.Engine's
	// ConvergeBlockSeverity). "" disables the findings gate. Default "high".
	BlockSeverity string

	// NoProgressTicks is the give-up backstop: Tick returns an error once a
	// run has shown no forward progress for this many consecutive ticks
	// while nothing is claimed. 0 disables it. Default 240 (mirrors
	// mission.Engine.NoProgressTicks).
	NoProgressTicks int

	runs            map[int64]*runState
	noProgress      map[int64]int
	lastFingerprint map[int64]string
}

// NewDriver constructs a Driver for the given assignment, rejecting a
// decorrelated (test-critic == test-writer model) assignment up front so no
// run can ever be started under it.
func NewDriver(q *queue.Store, scorer Scorer, validator Validator, assign RoleAssignment, threshold float64) (*Driver, error) {
	if err := CheckDecorrelation(assign); err != nil {
		return nil, err
	}
	return &Driver{
		Q: q, Scorer: scorer, Validator: validator, Assign: assign,
		Threshold:       threshold,
		BlockSeverity:   "high",
		NoProgressTicks: 240,
		runs:            map[int64]*runState{},
		noProgress:      map[int64]int{},
		lastFingerprint: map[int64]string{},
	}, nil
}

// StartRun enqueues a run's DAG (BuildDAG(rs, d.Assign, sigs)) under missionID
// and begins tracking its progress. missionID is caller-supplied (Phase 5
// wires it to a real mission.Store id); the driver has no mission package of
// its own, mirroring the RepoOps/Indexer decoupling pattern elsewhere in the
// codebase.
func (d *Driver) StartRun(missionID int64, rs RunSpec, sigs []repoindex.Signature) error {
	if _, exists := d.runs[missionID]; exists {
		return fmt.Errorf("advpool: run already started for mission %d", missionID)
	}
	specs := BuildDAG(rs, d.Assign, sigs)
	if err := d.Q.Enqueue(missionID, specs); err != nil {
		return fmt.Errorf("advpool: enqueue: %w", err)
	}
	tasks, err := d.Q.List(missionID)
	if err != nil {
		return fmt.Errorf("advpool: list after enqueue: %w", err)
	}
	var twID int64
	for _, t := range tasks {
		if t.Key == RoleTestWriter {
			twID = t.ID
		}
	}
	if twID == 0 {
		return fmt.Errorf("advpool: test-writer task not found after enqueue")
	}
	d.runs[missionID] = &runState{rs: rs, sigs: sigs, testWriterTaskID: twID}
	return nil
}

// Tick advances one run given the current task states. It returns a non-nil
// Verdict once the run has converged (test-critic done AND pool-adequacy
// scored); otherwise it returns (nil, nil) and progress is left for the next
// tick. It is pure over the injected Scorer/Validator/queue.Store — no
// hidden clock, no goroutines.
//
// The tick logic mirrors the mission-engine promote/gate pattern
// (internal/mission.Engine.Tick), re-pointed at the pool's three-role DAG:
//  1. PromoteReady.
//  2. mutant-generator done -> parse + score the DEV's own tests -> promote
//     test-writer re-rendered with the real survivors.
//  3. test-writer done -> validate (compiles) + score the pool's test against
//     the survivors -> ProvenMissed.
//  4. test-critic done AND pool-adequacy done -> aggregate -> Verdict, gated
//     by the human gate (blocking finding or below-threshold DevKillRate).
//  5. No-progress backstop: a stalled run fails.
func (d *Driver) Tick(ctx context.Context, missionID int64) (*Verdict, error) {
	run, ok := d.runs[missionID]
	if !ok {
		return nil, fmt.Errorf("advpool: unknown run for mission %d (call StartRun first)", missionID)
	}
	if run.verdict != nil {
		return run.verdict, nil
	}

	if _, err := d.Q.PromoteReady(missionID); err != nil {
		return nil, fmt.Errorf("advpool: promote: %w", err)
	}

	if !run.devScored {
		if err := d.tickDevAdequacy(ctx, missionID, run); err != nil {
			return nil, err
		}
	}

	if run.devScored && !run.poolScored {
		if err := d.tickPoolAdequacy(ctx, run); err != nil {
			return nil, err
		}
	}

	if run.poolScored {
		v, err := d.tickAggregate(missionID, run)
		if err != nil {
			return nil, err
		}
		if v != nil {
			return v, nil
		}
	}

	if err := d.checkNoProgress(missionID); err != nil {
		return nil, err
	}
	return nil, nil
}

// tickDevAdequacy is step 2: once mutant-generator is done, parse its
// mutants, score the dev's own tests against them (brain-side, via Scorer —
// never the worker's self-report), and promote test-writer re-rendered with
// the real survivors.
func (d *Driver) tickDevAdequacy(ctx context.Context, missionID int64, run *runState) error {
	mg, err := d.taskByKey(missionID, RoleMutantGenerator)
	if err != nil {
		return err
	}
	if mg == nil || mg.Status != queue.StatusDone {
		return nil
	}

	mutants, perr := d.Validator.ParseMutants(mg.Result)
	if perr != nil {
		// Malformed artifact: refuse it. The pure driver has no live hook into
		// the completion call (that already happened, unlike brain's
		// complete_task verify-gate) — reopen the task so a bee can retry, and
		// surface the failure to the caller to handle/log.
		if _, rerr := d.Q.ReopenTask(mg.ID); rerr != nil {
			return fmt.Errorf("advpool: reopen mutant-generator after parse failure: %w", rerr)
		}
		return fmt.Errorf("advpool: mutant-generator result unparseable, reissued for retry: %w", perr)
	}

	killRate, survivors, serr := d.Scorer.Score(ctx, run.rs.CodePath, run.rs.Code, run.rs.DevTestCode, mutants, run.rs.TestCmd)
	if serr != nil {
		return fmt.Errorf("advpool: score dev tests: %w", serr)
	}
	run.devScored = true
	run.devKillRate = killRate
	run.mutantsTotal = len(mutants)
	run.devSurvivors = survivors

	tw, terr := d.Q.TaskByID(run.testWriterTaskID)
	if terr != nil {
		return fmt.Errorf("advpool: load test-writer task: %w", terr)
	}
	if tw == nil || tw.Status != queue.StatusPending {
		// Already promoted/superseded/claimed by something else — nothing to do.
		return nil
	}

	// test-writer's original DependsOn=[DevAdequacyKey] can never be satisfied
	// by a normal worker-completed task: dev-adequacy is driver-internal
	// bookkeeping, never its own claimable task. Break the otherwise-permanent
	// deadlock the same way the brain's re-planning tools do: SupersedeTask
	// with a dep-free replacement now that the survivors are known.
	//
	// NOTE: SupersedeTask auto-uniquifies a replacement that reuses the old
	// key (the old row's key isn't freed until the same transaction that
	// inserts the new row), so the live test-writer task must be tracked by
	// id (run.testWriterTaskID), never re-looked-up by RoleTestWriter's key.
	newID, serr2 := d.Q.SupersedeTask(tw.ID, queue.TaskSpec{
		Key:         RoleTestWriter,
		Role:        RoleTestWriter,
		Title:       tw.Title,
		Instruction: renderTestWriter(run.rs, run.sigs, survivors),
		Model:       tw.Model,
	})
	if serr2 != nil {
		return fmt.Errorf("advpool: promote test-writer with survivors: %w", serr2)
	}
	run.testWriterTaskID = newID
	if _, err := d.Q.PromoteReady(missionID); err != nil {
		return fmt.Errorf("advpool: promote after test-writer supersede: %w", err)
	}
	return nil
}

// tickPoolAdequacy is step 3: once test-writer is done, validate that its
// test compiles, then score it (via Scorer, brain-side) against the
// survivors the dev's tests missed. ProvenMissed is how many of those
// survivors the pool's test then killed — real, catchable gaps.
func (d *Driver) tickPoolAdequacy(ctx context.Context, run *runState) error {
	tw, err := d.Q.TaskByID(run.testWriterTaskID)
	if err != nil {
		return fmt.Errorf("advpool: load test-writer task: %w", err)
	}
	if tw == nil || tw.Status != queue.StatusDone {
		return nil
	}

	if cerr := d.Validator.CompileTest(ctx, run.rs.CodePath, run.rs.Code, tw.Result); cerr != nil {
		if _, rerr := d.Q.ReopenTask(tw.ID); rerr != nil {
			return fmt.Errorf("advpool: reopen test-writer after compile failure: %w", rerr)
		}
		return fmt.Errorf("advpool: test-writer result does not compile, reissued for retry: %w", cerr)
	}

	_, poolSurvivors, serr := d.Scorer.Score(ctx, run.rs.CodePath, run.rs.Code, tw.Result, run.devSurvivors, run.rs.TestCmd)
	if serr != nil {
		return fmt.Errorf("advpool: score pool test: %w", serr)
	}
	run.poolScored = true
	run.provenMissed = len(run.devSurvivors) - len(poolSurvivors)
	return nil
}

// tickAggregate is step 4: once test-critic is done AND pool-adequacy is
// scored, aggregate the Verdict and apply the human gate.
func (d *Driver) tickAggregate(missionID int64, run *runState) (*Verdict, error) {
	tc, err := d.taskByKey(missionID, RoleTestCritic)
	if err != nil {
		return nil, err
	}
	if tc == nil || tc.Status != queue.StatusDone {
		return nil, nil
	}

	findings, ferr := d.Q.Findings(missionID, "")
	if ferr != nil {
		return nil, fmt.Errorf("advpool: load findings: %w", ferr)
	}
	var criticFindings []queue.Finding
	for _, f := range findings {
		if f.TaskID == tc.ID {
			criticFindings = append(criticFindings, f)
		}
	}

	v := aggregate(run.rs, d.Assign, run.devKillRate, run.mutantsTotal, len(run.devSurvivors), run.provenMissed,
		criticFindings, d.Threshold, d.blockingFindingOpen(findings))
	run.verdict = &v
	return run.verdict, nil
}

// blockingFindingOpen mirrors mission.Engine.blockingFindingOpen: any OPEN
// finding at or above BlockSeverity withholds certification. "" disables it.
func (d *Driver) blockingFindingOpen(findings []queue.Finding) bool {
	if d.BlockSeverity == "" {
		return false
	}
	minRank := queue.SeverityRank(d.BlockSeverity)
	for _, f := range findings {
		if f.Status == queue.FindingOpen && queue.SeverityRank(f.Severity) >= minRank {
			return true
		}
	}
	return false
}

// checkNoProgress is the give-up backstop, mirroring
// mission.Engine.checkNoProgress: while the run's progress fingerprint keeps
// changing, or any task is claimed (a bee is actively holding work — slow is
// not stuck), it is fine. Only when the fingerprint is unchanged AND nothing
// is claimed for NoProgressTicks consecutive ticks does the run fail.
func (d *Driver) checkNoProgress(missionID int64) error {
	if d.NoProgressTicks <= 0 {
		return nil
	}
	fp, claimed, err := d.progressFingerprint(missionID)
	if err != nil {
		return fmt.Errorf("advpool: progress check: %w", err)
	}
	if fp != d.lastFingerprint[missionID] {
		d.lastFingerprint[missionID] = fp
		d.noProgress[missionID] = 0
		return nil
	}
	if claimed > 0 {
		return nil
	}
	d.noProgress[missionID]++
	if d.noProgress[missionID] >= d.NoProgressTicks {
		return fmt.Errorf("advpool: run %d stalled — no forward progress and nothing claimable for %d ticks", missionID, d.NoProgressTicks)
	}
	return nil
}

// progressFingerprint mirrors mission.Engine.progressFingerprint: a string
// that changes whenever the run makes forward progress (a task reaches a
// terminal state or a finding is filed/resolved), plus the claimed count.
func (d *Driver) progressFingerprint(missionID int64) (string, int, error) {
	tasks, err := d.Q.List(missionID)
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
	open, err := d.Q.Findings(missionID, queue.FindingOpen)
	if err != nil {
		return "", 0, err
	}
	return fmt.Sprintf("%d/%d/%d", terminal, len(tasks), len(open)), claimed, nil
}

// taskByKey looks up a mission task by its (still-stable) key. Safe for
// mutant-generator and test-critic, which are only ever Reopened (status
// changes, key never does) — never Superseded (that's test-writer's path;
// see the note in tickDevAdequacy for why that one must be tracked by id).
func (d *Driver) taskByKey(missionID int64, key string) (*queue.Task, error) {
	tasks, err := d.Q.List(missionID)
	if err != nil {
		return nil, err
	}
	for i := range tasks {
		if tasks[i].Key == key {
			return &tasks[i], nil
		}
	}
	return nil, nil
}
