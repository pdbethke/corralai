// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

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
	ParseMutants(raw, original string) ([]adequacy.Mutant, error) // = testgen.ParseMutantsOutput (applies each SEARCH/REPLACE hunk to `original`)
	ParseTest(raw string) string                                  // = testgen.ParseTestOutput (strip fences/prose from a worker's raw test)
}

// Verdict status values. Never auto-certified: a blocking finding or a
// below-threshold DevKillRate always routes to needs-review.
const (
	StatusCertified   = "certified"
	StatusNeedsReview = "needs-review"
)

// Leaderboard outcome values fed by the driver after a terminal, signed
// verdict — never derived from a worker's self-report (soundness #5).
const (
	OutcomePass = "pass"
	OutcomeFail = "fail"
)

// Signer wraps the certify chain (certify.BuildLedger/BuildAttestation/
// SignDSSE + buildstore — the real impl is Task 5.1): it signs a terminal
// Verdict as a tamper-evident record, subject = repo@commit, byproducts =
// the Verdict fields (incl ModelsByRole). Here it is an interface so the
// driver stays pure/unit-testable with a fake; the resulting record
// verifies with `corral certify verify`.
type Signer interface {
	SignVerdict(ctx context.Context, v Verdict) (recordID int64, head string, err error)
}

// LeaderboardSink is the gate-earned fitness feed: one (model, role, outcome)
// observation per role, fed ONLY after a terminal Verdict has been scored by
// the deterministic gate (Scorer, never a worker's self-report) AND signed
// (soundness #5 — "a judge may not certify herself" extends to the fitness
// signal too: a model cannot earn leaderboard credit from its own claim).
type LeaderboardSink interface {
	Record(model, role, outcome string)
}

// BugCatchObservation is one seat's execution-proven contribution from a
// single converged run (see internal/bugcatch). Catches come ONLY from
// ProvenMissed — no claim/self-report path may ever populate it.
type BugCatchObservation struct {
	Model, Role                                  string
	Catches, Opportunities                       int
	SoundTests, AuthoredTests                    int
	CriticFlags, MutantsPlanted, MutantsSurvived int
	// Per-shard generator dimensions (zero for the single-seat roles).
	Shard            int
	Region           string
	RegionComplexity int
	RegionLines      int
	TestComplexity   int
	ParseRetries     int
	Dropped          bool
	Shadow           bool // set by Task 6; a shadow seat NEVER gates
}

// BugCatchSink is the optional per-run bug-catching feed (nil ⇒ no-op),
// mirroring LeaderboardSink but fed on EVERY converged run (certified AND
// needs-review) — a catch or a miss is meaningful regardless of the overall
// verdict, unlike leaderboard fitness which is gated on certification.
type BugCatchSink interface {
	Record(recordID int64, recordHead string, obs []BugCatchObservation)
}

// EventSink receives the pool's reasoning milestones as replay/telemetry
// events. Optional (nil ⇒ no-op), like Signer/Leaderboard — the pure Driver
// takes no telemetry dependency; the brain wires this to its telemetry store
// keyed on the run's missionID. Kinds: pool_subject, pool_dev_adequacy,
// pool_verdict. detail carries the real values/evidence, never a summary.
type EventSink interface {
	Emit(missionID int64, kind, subject string, detail map[string]any)
}

// Verdict is one run's final, gated outcome.
type Verdict struct {
	Repo, Commit    string
	Lang            string          // the run's resolved language plugin name (e.g. "go", "python")
	DevKillRate     float64         // the headline: the DEV suite's kill-rate, from Scorer — never a self-report
	MutantsTotal    int             // total mutants the mutant-generator produced
	Survivors       int             // mutants the dev's own tests did NOT kill
	ProvenMissed    int             // survivors the pool's authored test then killed — real, catchable gaps
	RegionsTotal    int             // mutant-generator seats the run dispatched
	RegionsProbed   int             // seats that returned usable mutants
	DroppedRegions  []string        // seats abandoned after MaxShardRetries — the coverage shortfall
	VacuousFindings []queue.Finding // test-critic's designed-to-pass/vacuous flags
	ModelsByRole    map[string]string
	Status          string // certified | needs-review
	RecordID        int64  // the signed build-record id (0 if signing skipped/failed)
	RecordHead      string // the record's ledger head
}

// RunState is the observable status of one run: Converged is true once the run
// has a terminal Verdict, and Verdict is non-nil exactly when Converged is true.
type RunState struct {
	Converged bool
	Verdict   *Verdict
	// AuthoredTest is the pool's compiling killing test, when one was authored
	// (the test-writer ran because the dev suite left survivors). Empty when a
	// perfect dev suite made the test-writer moot. NOT part of the signed
	// Verdict — evidence handed back to the dev, not certified state.
	AuthoredTest string
}

// CheckDecorrelation rejects an assignment where test-critic and test-writer
// share a model. A test-critic judging tests written by its own model (or a
// copy of it) is not an independent check — it is the same failure mode
// grading its own homework. Enforced at driver construction (soundness #4).
func CheckDecorrelation(assign RoleAssignment) error {
	critic, writer := assign[RoleTestCritic], assign[RoleTestWriter]
	if critic != "" && critic == writer {
		return fmt.Errorf("advpool: decorrelation guard: test-critic and test-writer must not share model %q", critic)
	}
	return nil
}

// ShadowProviderFailedResult is the sentinel a shadow (challenger) seat is
// completed with when the LLM call itself failed — a network error, a 429, a
// typo'd --shadow-model — rather than the model responding with output that
// merely failed to parse (see cmd/corral's runOneTask). The two cases MUST be
// kept distinguishable: without this sentinel, both a never-asked model and a
// model that answered with garbage would leave the same empty Result, and
// ParseMutants("") fails identically for both — the parse failure alone gives
// runShadowPass no way to tell "the model was never asked" apart from "the
// model answered with garbage". That ambiguity used to route the never-asked
// case straight into the parse-failure branch and record a MEASURED,
// DROPPED, zero-yield row for a model that never ran.
// That is data fabrication: it attributes a bad result to a model that was
// never asked the question, and it would have landed in the shared scorecard
// store that feeds model routing — exactly the corruption the `measured`
// flag exists to prevent. Recording nothing is strictly better than
// recording a fabricated zero: an absent row is honestly "we don't know",
// while a fabricated row is confidently wrong.
const ShadowProviderFailedResult = "\x00shadow-provider-call-failed\x00"

// shardStat is one generator seat's recorded outcome. Region complexity is the
// DIFFICULTY CONTROL: raw yield cannot distinguish a weak model from an easy
// region, so effectiveness is read CONDITIONED on complexity, never pooled
// across it.
type shardStat struct {
	region       string
	complexity   int
	lines        int
	mutants      int
	parseRetries int
	dropped      bool
	// survived is set only on a shadowStats entry (the challenger's scored
	// outcome for this region) — the primary's survivor count is NOT tracked
	// per shard here; see the survivorIdx placement note in
	// bugCatchObservations for why the primary's is recorded differently.
	survived int
	// measured is set only on a shadowStats entry, and only once the
	// challenger seat actually PRODUCED an observation for this region —
	// either a scored mutant set or a real parse failure. It stays false when
	// the seat never finished, when its scoring errored, or when the shadow
	// budget guard skipped it. An unmeasured seat emits NO bugcatch row: a
	// zero-mutant row for a seat that never ran would be recorded as the
	// challenger producing nothing, which is a fabricated comparison, not a
	// measurement. The telemetry event is still emitted for it, carrying
	// measured=false, so the skip is visible rather than silent.
	measured bool
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

	// shardRetries counts parse failures per mutant-generator task KEY (never
	// its id). Keying by key is deliberate: a lease-expiry re-claim and a
	// parse-failure reopen must draw on the SAME budget, or a shard could
	// retry forever by alternating failure modes.
	shardRetries map[string]int
	// droppedKeys is the set of mutant-generator task keys already recorded in
	// droppedRegions. tickDevAdequacy re-runs its whole scan on EVERY tick
	// until the run is devScored (Tick re-calls it unconditionally), and two
	// paths return an error AFTER a drop is recorded (the all-regions-failed
	// guard, and a transient Scorer error) — both are ordinary, expected
	// re-entry, not a fresh drop. Without this set, re-entry would re-append
	// the same region to droppedRegions on every subsequent tick, corrupting
	// the signed counts (unbounded slice growth, a shortfall message whose
	// count inflates forever). Keyed by task key, same as shardRetries.
	droppedKeys map[string]bool
	// droppedRegions names the shards abandoned after exhausting their retry
	// budget — the coverage shortfall, carried into the signed verdict so a
	// partial audit is provably partial rather than silently partial. Each
	// entry is recorded exactly once, guarded by droppedKeys.
	droppedRegions []string
	regionsTotal   int
	// regionsProbed counts the regions that actually contributed at least one
	// mutant to the union scored against the dev suite — NOT regionsTotal
	// minus len(droppedRegions), which would over-report a shard that parsed
	// cleanly but produced zero mutants as "probed" when it never contributed
	// anything to the exam. Recomputed fresh on every tickDevAdequacy pass
	// (deterministic over the same task results), so re-entry is safe.
	regionsProbed int
	// shardSymbols maps a mutant-generator task key to the qualified symbols
	// that shard was aimed at (Shard.Symbols), captured once at StartRun. Used
	// so a dropped region is recorded in droppedRegions by the SYMBOLS it left
	// unprobed (e.g. "A, B") rather than the task-UI title string ("Generate
	// mutants for A, B") — a signed verdict is evidence, not a task list, and
	// should read like the former. Empty/absent for an unsharded run's single
	// bare-keyed task, which falls back to its Title.
	shardSymbols map[string][]string

	// shardStats is per-shard generation outcome, keyed by shard index — the
	// metrics substrate. Recorded per shard and NEVER summed: summing collapses
	// N seats into one row and makes an underperforming seat invisible by
	// construction.
	shardStats map[int]shardStat

	// shadowStats mirrors shardStats but for the CHALLENGER seats (Task 6):
	// keyed by the SAME shard index, seeded with the SAME region/complexity/
	// lines in StartRun — the whole point of a shadow run is that both models
	// are graded on identical regions, not a second independent partition.
	// Populated by the shadow pass in tickDevAdequacy; empty when
	// rs.ShadowModel == "" (no change to any pre-existing run's behavior).
	shadowStats map[int]shardStat

	// testComplexity is the dev suite's complexity — the SECOND conditioning
	// axis (a model that only wins against naive suites is a different
	// proposition from one that wins against rigorous ones).
	//
	// FILE-granular by necessity: attributing a specific test to a specific
	// region requires knowing which tests exercise which code, which is exactly
	// what the slice-5 tests-x-mutants matrix establishes by execution. Any
	// per-region test-complexity claim would be unproven until then.
	testComplexity int

	// authoredTest is the pool's compiling killing test (the test-writer's
	// cleaned source), surfaced via RunState so `corral certify --adversarial`
	// can hand it back to the dev ("add this test; it catches the gap your suite
	// missed"). Evidence, deliberately NOT folded into the signed Verdict digest
	// — kept as run status, per the reasoning-trace design's non-goals.
	authoredTest string

	// testWriterMoot is set when a perfect dev suite (0 survivors) skipped the
	// test-writer entirely: the assigned model never ran, so it must NOT be fed
	// to the leaderboard as a failure for a task it never attempted.
	testWriterMoot bool

	verdict *Verdict

	// startedAt is the run's start time (from Driver.Now, set in StartRun),
	// used by the RunDeadline backstop below. It is advanced by exactly the
	// wall-clock time runShadowPass spends, so the deadline measures PRIMARY
	// elapsed time only — shadow measurement can never push a would-be
	// certified run into a needs-review timeout. Only the tick goroutine
	// touches it.
	startedAt time.Time
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

	// Signer and Leaderboard are the terminal-verdict effects (Task 4.3):
	// optional (nil = skipped) so every pre-existing Driver test keeps
	// working unwired. Phase 5 wires the real certify-chain Signer and
	// leaderboard PerformanceTracker sink. When set, Signer.SignVerdict is
	// called on every terminal verdict (certified or needs-review — a
	// needs-review run may still get a signed needs-review record, just
	// never a "certified"-status one past the gate); Leaderboard.Record is
	// only ever called AFTER SignVerdict returns successfully, never before
	// the deterministic score + sign (soundness #5).
	Signer      Signer
	Leaderboard LeaderboardSink

	// BugCatch is the optional per-run bug-catching feed (nil = no-op),
	// fed AFTER Signer (once RecordID/RecordHead are set) on every terminal
	// verdict — certified AND needs-review, unlike Leaderboard which only
	// fires on certified. See bugCatchObservations.
	BugCatch BugCatchSink

	// Events is the optional reasoning-event sink (nil = no-op), mirroring
	// Signer/Leaderboard: every pre-existing Driver test keeps working
	// unwired. When set, the driver emits pool_subject/pool_dev_adequacy/
	// pool_verdict at the three milestones below via the d.emit helper.
	Events EventSink

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

	// Now returns the current time; injected so the deadline logic below is
	// pure/unit-testable with a fake clock (the driver/store convention
	// forbids a bare time.Now() call in the pure logic). Defaulted to
	// time.Now in NewDriver when left nil.
	Now func() time.Time

	// RunDeadline is the wall-clock backstop checkNoProgress can't be: it
	// explicitly stands down while any task is StatusClaimed ("slow is not
	// stuck"), so a claimed-but-wedged task would otherwise stall a run
	// forever. 0 disables it. When a run's wall-clock age (Now() minus
	// startedAt) exceeds RunDeadline before convergence, Tick converges the
	// run to a signed needs-review verdict noting the timeout — honest and
	// terminal, so the CLI gets an answer and the single active slot frees.
	// A sane non-zero default is set in the brain wiring (StartAdversarialPool).
	RunDeadline time.Duration

	// mu guards the runs map lookups and each run's verdict pointer against
	// concurrent RunStatus callers (the get_adversarial_run MCP handler runs
	// on a different goroutine than the tick loop). It is NEVER held across
	// slow work (Q.Enqueue/Q.List/Scorer.Score/tick helpers) — only around a
	// map op or the verdict read/write. noProgress/lastFingerprint are not
	// guarded: only the single tick goroutine touches them.
	mu sync.Mutex

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
	if threshold <= 0 {
		return nil, fmt.Errorf("advpool: threshold must be > 0 (got %v) — a non-positive threshold would auto-certify any dev suite, defeating the human gate", threshold)
	}
	d := &Driver{
		Q: q, Scorer: scorer, Validator: validator, Assign: assign,
		Threshold:       threshold,
		BlockSeverity:   "high",
		NoProgressTicks: 240,
		runs:            map[int64]*runState{},
		noProgress:      map[int64]int{},
		lastFingerprint: map[int64]string{},
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return d, nil
}

// emit forwards a reasoning-milestone event to d.Events, no-op when it is
// nil (the default — every pre-existing Driver test keeps working unwired).
func (d *Driver) emit(missionID int64, kind, subject string, detail map[string]any) {
	if d.Events != nil {
		d.Events.Emit(missionID, kind, subject, detail)
	}
}

// survivorIDs returns the Mutant.ID of each survivor, for the
// pool_dev_adequacy event — NOT the mutant source, which is recoverable from
// the mutant-generator task's Result if ever needed.
func survivorIDs(survivors []adequacy.Mutant) []string {
	ids := make([]string, len(survivors))
	for i, m := range survivors {
		ids[i] = m.ID
	}
	return ids
}

// StartRun enqueues a run's DAG (BuildDAG(rs, d.Assign, sigs)) under missionID
// and begins tracking its progress. missionID is caller-supplied (Phase 5
// wires it to a real mission.Store id); the driver has no mission package of
// its own, mirroring the RepoOps/Indexer decoupling pattern elsewhere in the
// codebase.
func (d *Driver) StartRun(missionID int64, rs RunSpec, sigs []repoindex.Signature) error {
	d.mu.Lock()
	_, exists := d.runs[missionID]
	d.mu.Unlock()
	if exists {
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
	// Capture the shard→symbols map once, from the SAME ShardSymbols call
	// BuildDAG makes internally, so a dropped shard's coverage-shortfall entry
	// can name the functions it left unprobed (M-2) instead of the task-UI
	// title string.
	shards := ShardSymbols(sigs, rs.MaxShards)
	shardSymbols := make(map[string][]string, len(shards))
	// shardStats seeds the metrics substrate with each shard's difficulty
	// control (region/complexity/lines), computed once here from the SAME
	// ShardSymbols call BuildDAG used — never a second source of truth for
	// what a region contains.
	stats := make(map[int]shardStat, len(shards))
	// shadowStats is seeded with the SAME region/complexity/lines as stats —
	// the challenger is graded on IDENTICAL regions, not a second partition,
	// which is the entire point of a shadow run (see RoleMutantGeneratorShadow).
	// Left nil (never seeded) when no shadow model is configured, so an
	// ordinary sharded run's bugCatchObservations emits zero shadow rows —
	// exactly its pre-Task-6 behavior.
	var shadowStats map[int]shardStat
	if strings.TrimSpace(rs.ShadowModel) != "" {
		shadowStats = make(map[int]shardStat, len(shards))
	}
	for _, sh := range shards {
		shardSymbols[ShardTaskKey(sh.Index)] = sh.Symbols
		seed := shardStat{
			region: strings.Join(sh.Symbols, ", "), complexity: sh.Complexity, lines: sh.Lines,
		}
		stats[sh.Index] = seed
		if shadowStats != nil {
			shadowStats[sh.Index] = seed
		}
	}

	// testComplexity is the dev suite's own complexity — see the runState
	// field comment. A parse failure here (an unsupported/unparseable dev
	// test) is not fatal to the run: the conditioning axis is best-effort
	// telemetry, not a gate, so it is simply left at its zero value.
	testComplexity := 0
	if testSigs, terr := repoindex.ExtractSignatures(rs.DevTestCode, rs.Lang); terr == nil {
		for _, s := range testSigs {
			testComplexity += s.Complexity
		}
	}

	d.mu.Lock()
	d.runs[missionID] = &runState{
		rs: rs, sigs: sigs, testWriterTaskID: twID, startedAt: d.Now(),
		shardRetries:   map[string]int{},
		droppedKeys:    map[string]bool{},
		shardSymbols:   shardSymbols,
		shardStats:     stats,
		shadowStats:    shadowStats,
		testComplexity: testComplexity,
	}
	d.mu.Unlock()
	d.emit(missionID, "pool_subject", rs.CodePath, map[string]any{
		"goal": rs.Goal, "code": rs.Code, "dev_test_code": rs.DevTestCode,
		"code_path": rs.CodePath, "dev_test_path": rs.DevTestPath,
	})
	return nil
}

// RunStatus reports whether missionID's run has converged, and its Verdict if
// so. found is false when the driver has no such run. A run is retained in
// d.runs after convergence (never deleted), so a converged verdict stays
// queryable after the runtime frees the active slot — which is exactly when a
// caller polls for it. Safe to call concurrently with Tick (guarded by d.mu).
func (d *Driver) RunStatus(missionID int64) (RunState, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	run, ok := d.runs[missionID]
	if !ok {
		return RunState{}, false
	}
	return RunState{Converged: run.verdict != nil, Verdict: run.verdict, AuthoredTest: run.authoredTest}, true
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
	d.mu.Lock()
	run, ok := d.runs[missionID]
	var existing *Verdict
	if ok {
		existing = run.verdict
	}
	d.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("advpool: unknown run for mission %d (call StartRun first)", missionID)
	}
	if existing != nil {
		return existing, nil
	}

	// Wall-clock deadline: the backstop checkNoProgress can't be, since it
	// stands down whenever any task is claimed ("slow is not stuck"). On
	// exceed, converge to a signed needs-review verdict now — honest and
	// terminal — so the CLI gets an answer and the slot frees.
	if d.RunDeadline > 0 && d.Now().Sub(run.startedAt) > d.RunDeadline {
		v := d.timeoutVerdict(run)
		if d.Signer != nil {
			recordID, head, serr := d.Signer.SignVerdict(ctx, v)
			if serr != nil {
				return nil, fmt.Errorf("advpool: sign timeout verdict: %w", serr)
			}
			v.RecordID, v.RecordHead = recordID, head
		}
		d.emit(missionID, "pool_verdict", v.Commit, map[string]any{
			"status": v.Status, "dev_kill_rate": v.DevKillRate, "mutants_total": v.MutantsTotal,
			"survivors": v.Survivors, "proven_missed": v.ProvenMissed, "models_by_role": v.ModelsByRole,
			"record_id": v.RecordID, "record_head": v.RecordHead,
		})
		d.mu.Lock()
		run.verdict = &v
		d.mu.Unlock()
		return &v, nil
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
		v, err := d.tickAggregate(ctx, missionID, run)
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
	mgs, err := d.tasksByRole(missionID, RoleMutantGenerator)
	if err != nil {
		return err
	}
	if len(mgs) == 0 {
		return nil
	}
	// EVERY shard must be terminal before the dev's tests are scored. Scoring a
	// partial mutant set would grade the suite against a smaller exam than the
	// run claims to have set — the kill-rate would be real but would not mean
	// what the verdict says it means.
	for i := range mgs {
		if mgs[i].Status != queue.StatusDone {
			return nil
		}
	}

	// Union every shard's mutants. IDs are prefixed with the shard index so two
	// shards returning "m1" cannot collide, and so each survivor names the
	// region it came from (including in the test-writer's prompt). An UNSHARDED
	// run keeps its original, unprefixed IDs.
	//
	// This whole scan re-runs on EVERY tick until run.devScored is true (Tick
	// re-calls tickDevAdequacy unconditionally each pass), and two paths below
	// return an error AFTER a region has already been dropped and recorded:
	// the all-regions-failed guard just past this loop, and a transient
	// Scorer.Score error further down (the scorer runs a suite in a sandbox —
	// a transient failure there is exactly the condition dropping exists to
	// survive). Both are ordinary re-entry, not a fresh event, so every
	// mutation to run state in this loop must be idempotent per shard key:
	// already-dropped regions are skipped via droppedKeys rather than
	// re-counted or re-appended, and regionsProbed is recomputed fresh from
	// this pass's results rather than accumulated.
	var mutants []adequacy.Mutant
	probed := 0
	for i := range mgs {
		key := mgs[i].Key
		if run.droppedKeys[key] {
			// Already exhausted its retry budget and recorded as dropped on a
			// prior pass: this task's Result hasn't changed (it was never
			// reopened past the budget), so re-parsing it would only rediscover
			// the same failure. Skip it silently — the drop is already honestly
			// recorded, and re-running the drop bookkeeping here is what
			// corrupts the signed counts.
			continue
		}
		shardIdx, sharded := ShardIndexFromKey(key)
		parsed, perr := d.Validator.ParseMutants(mgs[i].Result, run.rs.Code)
		if perr != nil {
			run.shardRetries[key]++
			if run.shardRetries[key] <= MaxShardRetries {
				if _, rerr := d.Q.ReopenTask(mgs[i].ID); rerr != nil {
					return fmt.Errorf("advpool: reopen %s after parse failure: %w", key, rerr)
				}
				return fmt.Errorf("advpool: %s result unparseable, reissued for retry (%d/%d): %w",
					key, run.shardRetries[key], MaxShardRetries, perr)
			}
			// Budget exhausted: DROP this region and proceed on the shards that
			// parsed. Recorded exactly once (droppedKeys), never swallowed.
			log.Printf("advpool: run %d: dropping region %s after %d unparseable results — its functions go unprobed",
				missionID, key, run.shardRetries[key])
			label := mgs[i].Title
			if symbols := run.shardSymbols[key]; len(symbols) > 0 {
				label = strings.Join(symbols, ", ")
			}
			run.droppedKeys[key] = true
			run.droppedRegions = append(run.droppedRegions, label)
			// Guarded by `sharded`: an unsharded run's shardStats map starts
			// (and must stay) empty — bugCatchObservations reads its length
			// to decide whether to emit the single-seat row or the per-shard
			// rows, so writing a key here for the bare, unsharded task would
			// silently flip that decision.
			if sharded {
				st := run.shardStats[shardIdx]
				st.parseRetries = run.shardRetries[key]
				st.dropped = true
				run.shardStats[shardIdx] = st
			}
			continue
		}
		if len(parsed) > 0 {
			// "Probed" means this region actually contributed to the exam the
			// dev suite is graded against — a shard that parsed cleanly but
			// produced zero mutants contributed nothing, and must not count as
			// probed just because it wasn't dropped.
			probed++
		}
		for _, m := range parsed {
			if sharded {
				m.ID = fmt.Sprintf("s%d/%s", shardIdx, m.ID)
			}
			mutants = append(mutants, m)
		}
		if sharded {
			st := run.shardStats[shardIdx]
			st.mutants = len(parsed)
			st.parseRetries = run.shardRetries[mgs[i].Key]
			run.shardStats[shardIdx] = st
		}
	}
	run.regionsTotal = len(mgs)
	run.regionsProbed = probed

	if len(mutants) == 0 {
		// Unconditional on len(mutants): a run where every shard parsed
		// cleanly but each produced zero mutants would otherwise sail past
		// this guard (nothing was DROPPED), score against an empty exam, and
		// still claim full coverage. Zero mutants to grade against is fatal
		// regardless of why.
		return fmt.Errorf("advpool: no usable mutants from any of %d mutant-generator region(s) (%d dropped) — nothing to grade the dev suite against",
			run.regionsTotal, len(run.droppedRegions))
	}

	killRate, survivors, serr := d.Scorer.Score(ctx, run.rs.CodePath, run.rs.Code, run.rs.DevTestCode, mutants, run.rs.TestCmd)
	if serr != nil {
		return fmt.Errorf("advpool: score dev tests: %w", serr)
	}
	run.devScored = true
	run.devKillRate = killRate
	run.mutantsTotal = len(mutants)
	run.devSurvivors = survivors

	// The challenger pass: score the shadow seats' mutants against the SAME dev
	// suite so the comparison measures POTENCY (mutants that survive a good
	// suite), not merely output volume. Results are recorded and never
	// aggregated into the verdict — the exam stays the primary model's.
	//
	// A shadow failure is NEVER fatal: it is measurement, not the gate. Errors
	// are logged and the seat is skipped.
	if strings.TrimSpace(run.rs.ShadowModel) != "" {
		d.runShadowPass(ctx, missionID, run)
	}

	// Emit one telemetry event per shard now that run.shardStats is final
	// (this whole function only reaches here once, guarded by devScored, so
	// this cannot double-emit on a re-entrant tick) — the --record tape, the
	// cockpit, and telemetry all get it from this one write.
	for _, i := range sortedShardIndexes(run.shardStats) {
		st := run.shardStats[i]
		d.emit(missionID, "pool_shard", st.region, map[string]any{
			"shard": i, "region": st.region,
			"region_complexity": st.complexity, "region_lines": st.lines,
			"mutants": st.mutants, "parse_retries": st.parseRetries, "dropped": st.dropped,
		})
	}
	// The challenger's paired telemetry: the SAME pool_shard kind (so the tape
	// and cockpit render it with the existing per-shard handling) marked
	// shadow=true, plus measured — a seat the budget guard skipped, or one
	// whose scoring errored, emits measured=false rather than a silent absence
	// or a fabricated zero. Without this the --record tape carried only the
	// primary's rows and the comparison was invisible in every replay.
	for _, i := range sortedShardIndexes(run.shadowStats) {
		st := run.shadowStats[i]
		d.emit(missionID, "pool_shard", st.region, map[string]any{
			"shard": i, "region": st.region,
			"region_complexity": st.complexity, "region_lines": st.lines,
			"mutants": st.mutants, "parse_retries": st.parseRetries, "dropped": st.dropped,
			"survived": st.survived, "shadow": true, "measured": st.measured,
			"model": run.rs.ShadowModel,
		})
	}
	// Log the headline the moment it's computed — the dev suite's grade — so it
	// is visible even if the downstream test-writer/aggregate steps stall.
	log.Printf("advpool: run %d dev-adequacy: the dev's OWN tests scored %.0f%% (killed %d of %d mutants, %d survived — bugs the dev's tests miss)",
		missionID, killRate*100, len(mutants)-len(survivors), len(mutants), len(survivors))
	d.emit(missionID, "pool_dev_adequacy", "", map[string]any{
		"dev_kill_rate": run.devKillRate, "mutants_total": run.mutantsTotal,
		"survivors": len(run.devSurvivors), "survivor_ids": survivorIDs(run.devSurvivors),
	})

	if len(survivors) == 0 {
		// A perfect dev suite: it killed every mutant. There are no survivors
		// for the test-writer to expose, so skip it and the pool-adequacy step
		// entirely and go straight to aggregate — the run certifies on its 100%
		// kill-rate. Without this the test-writer would be promoted to "write a
		// test targeting the survivors" of which there are none (a degenerate
		// prompt) and the run could NEVER converge — i.e. the pool could grade a
		// bad suite but never certify a perfect one.
		run.poolScored = true
		run.provenMissed = 0
		run.testWriterMoot = true // it never ran — keep it off the leaderboard
		if tw, terr := d.Q.TaskByID(run.testWriterTaskID); terr == nil && tw != nil && tw.Status == queue.StatusPending {
			cancelled, cerr := d.Q.CancelTask(tw.ID)
			if cerr != nil {
				return fmt.Errorf("advpool: cancel moot test-writer (perfect suite): %w", cerr)
			}
			if !cancelled {
				log.Printf("advpool: run %d: moot test-writer task %d was not pending at cancel time (benign race)", missionID, tw.ID)
			}
		}
		return nil
	}

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

// ShadowTimeBudget is the hard wall-clock cap on ALL of a run's shadow
// measurement work, derived from the run's deadline. Shadow scoring runs real
// jail executions of the dev suite — a second full Scorer.Score per shard — so
// it must be bounded twice over: this cap bounds how long it may take, and
// runShadowPass credits whatever it does spend back to the run's deadline
// clock so it cannot consume the PRIMARY run's budget.
//
// A zero deadline (the pure-unit-test / no-backstop case) means there is no
// budget to protect, so shadow work is unbounded there.
func ShadowTimeBudget(deadline time.Duration) time.Duration {
	if deadline <= 0 {
		return 0
	}
	return deadline / 4
}

// runShadowPass scores the challenger seats' mutants against the SAME dev
// suite as the primary, so the head-to-head measures POTENCY (mutants that
// survive a good suite) rather than mere output volume. It is MEASUREMENT, and
// is held to two invariants that the role key alone cannot enforce:
//
//  1. A shadow failure is NEVER fatal. Every error path here logs and leaves
//     the seat unmeasured; nothing returns an error to Tick.
//
//  2. Shadow work can never change the run's Status. RunDeadline is a
//     wall-clock budget from run start, and exceeding it forces a
//     needs-review TIMEOUT verdict — so absent a guard, enabling shadow could
//     flip a would-be-certified run to needs-review purely by making it
//     slower. That would breach "shadow never gates" through a channel the
//     role key cannot close. Two mechanisms close it together:
//     (a) every shadow Score is bounded by ShadowTimeBudget, and the pass
//     stops as soon as that budget is spent (remaining seats are recorded
//     as UNMEASURED, never as a challenger that produced nothing); and
//     (b) the wall-clock time this pass consumes is credited back to the
//     run's deadline clock by advancing startedAt — but ONLY up to
//     ShadowTimeBudget (min(elapsed, budget); see the clamp below), so the
//     credit itself cannot exceed what (a) is supposed to bound. That cap
//     bounds shadow's charge against the deadline to at most the shadow
//     budget: any overspend beyond the budget IS charged against the primary
//     deadline. The guarantee is therefore not purely structural — it still
//     depends on Scorer.Score honoring the context this pass hands it (sctx
//     below), so (a) can actually cut a call off rather than merely being
//     ignored. The production jail Scorer does honor its context, keeping
//     the behavioral risk low, but a Scorer that ignores sctx and runs long
//     can still consume up to ShadowTimeBudget of the primary run's margin.
//     (a) also exists so the caller's own outer context (which must allow
//     deadline + this budget; see cmd/corral's certify --local) stays
//     bounded.
func (d *Driver) runShadowPass(ctx context.Context, missionID int64, run *runState) {
	shadows, serr := d.tasksByRole(missionID, RoleMutantGeneratorShadow)
	if serr != nil {
		log.Printf("advpool: run %d: shadow seats unavailable (measurement only): %v", missionID, serr)
		return
	}

	budget := ShadowTimeBudget(d.RunDeadline)
	started := d.Now()
	if budget > 0 {
		// Credit the pass's wall-clock spend back to the run's deadline clock
		// on EVERY exit path — see invariant (2b) above. Capped at `budget`:
		// crediting the raw elapsed time would let a Scorer that ignored its
		// own context (sctx below) extend the deadline arbitrarily by simply
		// running long, which would blow past the CALLER's own outer bound
		// (see cmd/corral's outerBound) — failing worse than the timeout this
		// exists to avoid (an ungraceful exit 1/no verdict, instead of the
		// honest signed needs-review the deadline is supposed to produce).
		defer func() {
			elapsed := d.Now().Sub(started)
			if elapsed > budget {
				elapsed = budget
			}
			run.startedAt = run.startedAt.Add(elapsed)
		}()
	}

	for i := range shadows {
		if shadows[i].Status != queue.StatusDone {
			// Never finished (still pending/claimed, or superseded): there is
			// nothing to measure, and — critically — this must NOT hold up the
			// primary run, which has already scored above.
			continue
		}
		if shadows[i].Result == ShadowProviderFailedResult {
			// The challenger's LLM call itself failed (see
			// ShadowProviderFailedResult) — there is no output to attempt to
			// parse, and running it through ParseMutants would fabricate an
			// observed parse failure for a model that was never asked the
			// question. Leave the seat unmeasured, exactly like the
			// still-claimed/skipped-by-budget cases below.
			continue
		}
		idx, ok := ShadowShardIndexFromKey(shadows[i].Key)
		if !ok {
			// A key this function cannot parse would otherwise silently become
			// index 0 and collapse this seat onto shard 0's row, mis-attributing
			// one region's difficulty control to another. Skip it, loudly —
			// matching the log-and-degrade pattern the rest of the shadow path
			// uses.
			log.Printf("advpool: run %d: shadow seat key %q does not parse to a shard index — skipping (measurement only)", missionID, shadows[i].Key)
			continue
		}

		sctx := ctx
		var cancel context.CancelFunc
		if budget > 0 {
			left := budget - d.Now().Sub(started)
			if left <= 0 {
				log.Printf("advpool: run %d: shadow budget (%s) spent — skipping the remaining challenger seats; they are recorded as UNMEASURED, not as zero yield", missionID, budget)
				return
			}
			sctx, cancel = context.WithTimeout(ctx, left)
		}

		parsed, perr := d.Validator.ParseMutants(shadows[i].Result, run.rs.Code)
		if perr != nil {
			// A real, observed challenger failure: it produced output the
			// validator could not use. That IS a measurement, so mark it so.
			st := run.shadowStats[idx]
			st.parseRetries++
			st.dropped = true
			st.measured = true
			run.shadowStats[idx] = st
			if cancel != nil {
				cancel()
			}
			continue
		}
		_, shadowSurvivors, sserr := d.Scorer.Score(sctx, run.rs.CodePath, run.rs.Code, run.rs.DevTestCode, parsed, run.rs.TestCmd)
		// Release sctx's timeout right after the call it bounded, rather than
		// deferring to the end of the pass — correctness must not depend on
		// reasoning about how many shards (and therefore deferred cancels) may
		// accumulate before this function returns.
		if cancel != nil {
			cancel()
		}
		if sserr != nil {
			// Infrastructure, not a challenger verdict — leave it unmeasured
			// rather than recording a zero the scorecard would read as yield.
			log.Printf("advpool: run %d: shadow shard %d scoring failed (measurement only): %v", missionID, idx, sserr)
			continue
		}
		st := run.shadowStats[idx]
		st.mutants = len(parsed)
		st.survived = len(shadowSurvivors)
		st.measured = true
		run.shadowStats[idx] = st
	}
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

	// The worker hands back the model's RAW output (structured fast path); a
	// model commonly wraps a test in ```go fences / prose. Clean it to the bare
	// source before compiling or scoring — symmetric with ParseMutants on the
	// mutant-generator side.
	writerTest := d.Validator.ParseTest(tw.Result)

	if cerr := d.Validator.CompileTest(ctx, run.rs.CodePath, run.rs.Code, writerTest); cerr != nil {
		if _, rerr := d.Q.ReopenTask(tw.ID); rerr != nil {
			return fmt.Errorf("advpool: reopen test-writer after compile failure: %w", rerr)
		}
		return fmt.Errorf("advpool: test-writer result does not compile, reissued for retry: %w", cerr)
	}

	// Capture the compiling killing test for hand-back (read by RunStatus under
	// d.mu, so store it under the same lock).
	d.mu.Lock()
	run.authoredTest = writerTest
	d.mu.Unlock()

	_, poolSurvivors, serr := d.Scorer.Score(ctx, run.rs.CodePath, run.rs.Code, writerTest, run.devSurvivors, run.rs.TestCmd)
	if serr != nil {
		return fmt.Errorf("advpool: score pool test: %w", serr)
	}
	run.poolScored = true
	run.provenMissed = len(run.devSurvivors) - len(poolSurvivors)
	return nil
}

// tickAggregate is step 4: once test-critic is done AND pool-adequacy is
// scored, aggregate the Verdict, apply the human gate, sign it (Signer, if
// wired), and — only after that sign succeeds — feed the gate-earned
// leaderboard (Leaderboard, if wired). run.verdict is set (and the run
// considered terminal) only once this whole sequence has succeeded: if
// signing fails, the aggregate is left unset so a later Tick simply
// recomputes and retries — aggregate/sign are both deterministic/idempotent
// over the same scored inputs.
func (d *Driver) tickAggregate(ctx context.Context, missionID int64, run *runState) (*Verdict, error) {
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
	criticFindings := filterCriticFindings(findings, tc.ID)

	// The critic's findings are a second model's UNVERIFIED review — carried on
	// the verdict as advisory (VacuousFindings) but NOT gating the signed record
	// (pass false, not d.blockingFindingOpen(findings)): certification is an
	// execution-proven judgment (kill-rate + proven_missed), never an LLM's
	// opinion, which can hallucinate. blockingFindingOpen remains for a future
	// execution-verified finding path.
	v := aggregate(run.rs, d.Assign, run.devKillRate, run.mutantsTotal, len(run.devSurvivors), run.provenMissed,
		criticFindings, d.Threshold, false)
	v.RegionsTotal = run.regionsTotal
	v.RegionsProbed = run.regionsProbed
	v.DroppedRegions = run.droppedRegions

	if d.Signer != nil {
		recordID, head, serr := d.Signer.SignVerdict(ctx, v)
		if serr != nil {
			return nil, fmt.Errorf("advpool: sign verdict: %w", serr)
		}
		v.RecordID = recordID
		v.RecordHead = head
		// Gate-earned fitness (soundness #6): the leaderboard is fed ONLY from a
		// CERTIFIED verdict — a run parked for human review has not earned fitness
		// for anyone yet. A needs-review record is still signed (evidence), but no
		// model gets leaderboard credit until the gate actually certified the run.
		if d.Leaderboard != nil && v.Status == StatusCertified {
			d.feedLeaderboard(v, run.testWriterMoot)
		}
	}

	// BugCatch is fed regardless of Status (certified AND needs-review) — a
	// proven catch or a proven miss is meaningful evidence either way, unlike
	// Leaderboard fitness which is gated on certification. Guarded on a real
	// v.RecordID (nonzero): the BugCatch field doc asserts it is fed AFTER
	// Signer, "once RecordID/RecordHead are set" — a Driver wired with
	// BugCatch but no Signer (or one whose sign attempt failed and returned
	// early above) leaves v.RecordID at its documented zero value, and every
	// row this sink writes would carry that same unlinkable record_id=0. Since
	// Cell.Runs is COUNT(DISTINCT record_id), those rows would all collapse
	// into a single "run", pinning every cell below provisionalBelow forever.
	if d.BugCatch != nil && v.RecordID != 0 {
		d.BugCatch.Record(v.RecordID, v.RecordHead, bugCatchObservations(run, v))
	}

	d.emit(missionID, "pool_verdict", v.Commit, map[string]any{
		"status": v.Status, "dev_kill_rate": v.DevKillRate, "mutants_total": v.MutantsTotal,
		"survivors": v.Survivors, "proven_missed": v.ProvenMissed, "models_by_role": v.ModelsByRole,
		"record_id": v.RecordID, "record_head": v.RecordHead,
	})

	d.mu.Lock()
	run.verdict = &v
	d.mu.Unlock()
	return &v, nil
}

// timeoutVerdict builds the signed needs-review verdict for a run that did
// not converge within RunDeadline. It uses whatever partial data was scored
// (dev kill-rate if the dev-adequacy step ran, else zero) and is forced to
// StatusNeedsReview — a timed-out run is NEVER certified, and (mirroring
// tickAggregate's leaderboard gate, which only fires on StatusCertified) it
// earns no leaderboard fitness for any model: a stalled run proved nothing.
func (d *Driver) timeoutVerdict(run *runState) Verdict {
	return Verdict{
		Repo:         run.rs.Repo,
		Commit:       run.rs.Commit,
		Lang:         run.rs.Lang,
		DevKillRate:  run.devKillRate,
		MutantsTotal: run.mutantsTotal,
		Survivors:    len(run.devSurvivors),
		ProvenMissed: run.provenMissed,
		// Coverage fields (I-5): a run that dispatched N regions and dropped
		// some before hitting RunDeadline must carry that shortfall on the
		// timeout verdict too, or the CLI's RegionsTotal > 0 guard silently
		// suppresses PARTIAL AUDIT for exactly the run most likely to have one
		// (a stall is often the dropped regions' downstream symptom).
		RegionsTotal:   run.regionsTotal,
		RegionsProbed:  run.regionsProbed,
		DroppedRegions: run.droppedRegions,
		ModelsByRole:   map[string]string(d.Assign),
		Status:         StatusNeedsReview,
	}
}

// feedLeaderboard is the gate-earned fitness feed: one (model, role,
// outcome) call per role, derived from the CERTIFIED (Scorer-scored, gated,
// signed) result only — never from a worker's self-report.
func (d *Driver) feedLeaderboard(v Verdict, testWriterMoot bool) {
	outcome := func(ok bool) string {
		if ok {
			return OutcomePass
		}
		return OutcomeFail
	}
	// test-writer: did its authored test kill the survivors it was targeted at?
	// Skipped entirely when it never ran (a perfect dev suite left no survivors
	// to target) — a model must never be recorded as failing a task it didn't
	// attempt, or a strong suite would systematically penalize a good writer.
	if !testWriterMoot {
		d.Leaderboard.Record(v.ModelsByRole[RoleTestWriter], RoleTestWriter, outcome(v.ProvenMissed > 0))
	}
	// mutant-generator: it did its job if it produced usable (compiling) mutants
	// at all — whether those mutants then SURVIVE is the dev suite's business,
	// not the generator's, so a perfect suite killing them is not a generator
	// failure.
	d.Leaderboard.Record(v.ModelsByRole[RoleMutantGenerator], RoleMutantGenerator, outcome(v.MutantsTotal > 0))
	// test-critic: did its findings hold (it actually flagged something)?
	d.Leaderboard.Record(v.ModelsByRole[RoleTestCritic], RoleTestCritic, outcome(len(v.VacuousFindings) > 0))
}

// bugCatchObservations derives each seat's execution-proven contribution
// from the run state + the signed verdict. Catches = ProvenMissed only — no
// claim/self-report path may reach it.
func bugCatchObservations(run *runState, v Verdict) []BugCatchObservation {
	var out []BugCatchObservation
	// test-writer: the execution-proven catcher.
	authored, sound := 0, 0
	if !run.testWriterMoot {
		authored = 1
		if run.poolScored && run.authoredTest != "" { // compiled + scored ⇒ a valid discriminating test
			sound = 1
		}
	}
	out = append(out, BugCatchObservation{
		Model: v.ModelsByRole[RoleTestWriter], Role: RoleTestWriter,
		Catches: v.ProvenMissed, Opportunities: v.Survivors,
		AuthoredTests: authored, SoundTests: sound,
	})
	// test-critic: theater-detection (judgement, lower-confidence).
	out = append(out, BugCatchObservation{
		Model: v.ModelsByRole[RoleTestCritic], Role: RoleTestCritic,
		CriticFlags: len(v.VacuousFindings),
	})
	// mutant-generator: one row PER SHARD. Never summed — see shardStat.
	if len(run.shardStats) == 0 {
		out = append(out, BugCatchObservation{
			Model: v.ModelsByRole[RoleMutantGenerator], Role: RoleMutantGenerator,
			MutantsPlanted: v.MutantsTotal, MutantsSurvived: v.Survivors,
			TestComplexity: run.testComplexity,
		})
	} else {
		// MutantsSurvived is measured against the MERGED mutant set (Scorer.Score
		// runs once over the union of every shard's mutants — see
		// tickDevAdequacy) — there is no sound way to attribute which shard's
		// mutants specifically survived, so it CANNOT be split per shard without
		// inventing a false per-shard attribution. Record v.Survivors on exactly
		// ONE row — the lowest NON-DROPPED shard index, never just the lowest
		// index — so the run-level aggregate (SUM(mutants_survived) for this
		// role) stays exact; every other shard row carries 0. A dropped seat
		// never ran (it exhausted its retry budget before contributing any
		// mutants), so parking the run's survivor count there would produce an
		// internally incoherent row (planted=0, survived>0) AND make the
		// natural analytical filter "exclude shards that never ran" silently
		// zero the run's adversary-potency aggregate. This is always safe: a
		// run where every shard dropped produces zero mutants and errors out
		// (see the len(mutants)==0 guard in tickDevAdequacy) before ever
		// reaching a verdict, so there is always at least one non-dropped shard
		// here. Do NOT "fix" this into an even/proportional split across
		// shards — that would be a fabricated number, not a measured one.
		survivorIdx := -1
		for _, i := range sortedShardIndexes(run.shardStats) {
			if !run.shardStats[i].dropped {
				survivorIdx = i
				break
			}
		}
		for _, i := range sortedShardIndexes(run.shardStats) {
			st := run.shardStats[i]
			obs := BugCatchObservation{
				Model: v.ModelsByRole[RoleMutantGenerator], Role: RoleMutantGenerator,
				MutantsPlanted:   st.mutants,
				Shard:            i,
				Region:           st.region,
				RegionComplexity: st.complexity,
				RegionLines:      st.lines,
				TestComplexity:   run.testComplexity,
				ParseRetries:     st.parseRetries,
				Dropped:          st.dropped,
			}
			if i == survivorIdx {
				obs.MutantsSurvived = v.Survivors
			}
			out = append(out, obs)
		}
	}
	// The challenger's paired rows (Task 6): one row per shard, SAME region as
	// its primary counterpart, flagged Shadow so the scorecard can tell them
	// apart. Empty (no-op) when no shadow run was configured — shadowStats is
	// only ever seeded alongside shardStats.
	for _, i := range sortedShardIndexes(run.shadowStats) {
		st := run.shadowStats[i]
		if !st.measured {
			// The seat never produced an observation (unfinished, scoring
			// failed, or skipped by the shadow budget guard). Recording it
			// would enter mutants_planted=0 for a model that was never asked
			// the question — a fabricated comparison. See shardStat.measured.
			continue
		}
		out = append(out, BugCatchObservation{
			Model: run.rs.ShadowModel, Role: RoleMutantGeneratorShadow,
			MutantsPlanted: st.mutants, MutantsSurvived: st.survived,
			Shard: i, Region: st.region,
			RegionComplexity: st.complexity, RegionLines: st.lines,
			TestComplexity: run.testComplexity,
			ParseRetries:   st.parseRetries, Dropped: st.dropped,
			Shadow: true,
		})
	}
	return out
}

// sortedShardIndexes returns the shard indexes in ascending order, so emitted
// events and recorded rows are deterministic.
func sortedShardIndexes(m map[int]shardStat) []int {
	out := make([]int, 0, len(m))
	for i := range m {
		out = append(out, i)
	}
	sort.Ints(out)
	return out
}

// isOperationalFinding reports whether f is an operational event (e.g. a
// model-unreachable notice filed by a worker), not an audit finding. These are
// visible to operators but never count as a critic's judgment nor block
// certification — an infrastructure hiccup is not a defect in the change.
func isOperationalFinding(f queue.Finding) bool { return f.Type == "ops" }

// filterCriticFindings returns the test-critic task's AUDIT findings
// (excluding operational events), used to populate Verdict.VacuousFindings.
func filterCriticFindings(findings []queue.Finding, criticTaskID int64) []queue.Finding {
	var out []queue.Finding
	for _, f := range findings {
		if f.TaskID == criticTaskID && !isOperationalFinding(f) {
			out = append(out, f)
		}
	}
	return out
}

// blockingFindingOpen mirrors mission.Engine.blockingFindingOpen: any OPEN
// finding at or above BlockSeverity withholds certification. "" disables it.
// Operational findings (model-unreachable, etc.) are excluded — an infra
// hiccup is never certification-blocking.
func (d *Driver) blockingFindingOpen(findings []queue.Finding) bool {
	if d.BlockSeverity == "" {
		return false
	}
	minRank := queue.SeverityRank(d.BlockSeverity)
	for _, f := range findings {
		if isOperationalFinding(f) {
			continue
		}
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

// shardIndexOfAnyKey parses a shard index out of EITHER a primary
// mutant-generator key or a challenger (shadow) one. tasksByRole is called for
// both roles, and the two key formats differ by prefix — so parsing only the
// primary form dropped every shadow seat into the lexicographic fallback,
// ordering them /0, /1, /10, /2 past ten shards and silently voiding the
// numeric-order guarantee tasksByRole's doc comment spends ten lines making.
func shardIndexOfAnyKey(key string) (int, bool) {
	if i, ok := ShardIndexFromKey(key); ok {
		return i, true
	}
	return ShadowShardIndexFromKey(key)
}

// tasksByRole returns every task for a role, sorted by parsed shard index
// (the bare unsharded key first) rather than by key string — a lexicographic
// sort on Key would order ten-plus shards as /0, /1, /10, /11, /2, ... once
// --max-shards (operator-settable, unbounded) crosses ten. Nothing downstream
// derives shard index from slice position today (ShardIndexFromKey always
// re-parses it from the key), but shard index is itself a recorded metrics
// key, and per-shard metrics are about to fold over exactly this slice — so
// the order must be numeric and deterministic, not an inherited positional
// assumption. Used for the mutant-generator, which fans out into one task per
// symbol shard; taskByKey remains correct for the single-task roles.
func (d *Driver) tasksByRole(missionID int64, role string) ([]queue.Task, error) {
	tasks, err := d.Q.List(missionID)
	if err != nil {
		return nil, err
	}
	var out []queue.Task
	for i := range tasks {
		if tasks[i].Role == role {
			out = append(out, tasks[i])
		}
	}
	sort.Slice(out, func(i, j int) bool {
		ii, iSharded := shardIndexOfAnyKey(out[i].Key)
		ji, jSharded := shardIndexOfAnyKey(out[j].Key)
		if iSharded != jSharded {
			// Unsharded key sorts first (it stands in for shard 0 in an
			// unsharded run).
			return jSharded
		}
		if iSharded {
			return ii < ji
		}
		// Both unsharded: identical role means identical key, but keep the
		// comparator total.
		return out[i].Key < out[j].Key
	})
	return out, nil
}
