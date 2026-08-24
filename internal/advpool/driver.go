// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	golang "github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/matrix"
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

	// ScoreReport is Score's richer sibling: it returns the full adequacy.Report
	// (CompliantPass + the Killed/Survived mutant IDs), so a caller can tell a
	// baseline that could not pass (CompliantPass=false) from a genuine zero-kill
	// (CompliantPass=true, len(Killed)==0). The matrix needs this distinction.
	ScoreReport(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (adequacy.Report, error)
}

// AuthoredScorer is an optional Scorer extension for scoring a POOL-authored
// test — the test-writer's output — as opposed to the dev suite's own.
// tickPoolAdequacy prefers this over ScoreReport when a Scorer implements it
// (the same optional-extension pattern verboseJail already uses in gate.go).
//
// The distinction matters only in repo-aware mode: JailScorer.ScoreReport's
// workspace-building deliberately does NOT overlay `test` there (correct for
// the dev test, which is already on disk — overlaying would shadow the real
// suite), but an AUTHORED test is a brand-new file the repo does not already
// contain, so silently dropping it means the pool re-scores the DEV suite
// against its own already-known survivors — every repo-aware run then
// computes ProvenMissed=0 unconditionally, regardless of what the pool's test
// actually proved. See JailScorer.ScoreAuthoredReport's doc for the full
// history. A Scorer that does not implement this (only test fakes, today —
// JailScorer, the only production implementation, always does) falls back to
// ScoreReport via scoreAuthored below, which is fine in single-file mode.
type AuthoredScorer interface {
	ScoreAuthoredReport(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (adequacy.Report, error)
}

// scoreAuthored scores a pool-authored test through AuthoredScorer when the
// wired Scorer implements it, else falls back to the plain ScoreReport —
// see AuthoredScorer's doc for why the two differ and when the fallback is
// safe.
func scoreAuthored(ctx context.Context, scorer Scorer, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (adequacy.Report, error) {
	if as, ok := scorer.(AuthoredScorer); ok {
		return as.ScoreAuthoredReport(ctx, codePath, code, test, mutants, testCmd)
	}
	return scorer.ScoreReport(ctx, codePath, code, test, mutants, testCmd)
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

// MutantAttempt is one SEAT's outcome for one mutant. Two attempts sharing a
// mutant id and differing in Model are the paired observation the correlation
// statistic needs.
type MutantAttempt struct {
	Path     string
	MutantID string
	Model    string
	Role     string // RoleTestWriter | RoleTestWriterShadow
	Shadow   bool
	Outcome  string // "killed" | "survived"
}

// MutantAttemptSink is the optional per-run feed of per-seat mutant outcomes
// (nil ⇒ no-op), mirroring BugCatchSink. advpool deliberately does NOT import
// internal/scanstore: the composition root adapts this to
// scanstore.RecordMutantAttempts, exactly as it does for the other sinks.
//
// This is a MEASUREMENT feed. Nothing written here may reach the Verdict, the
// aggregate, or the signed record.
type MutantAttemptSink interface {
	Record(recordID int64, recordHead string, attempts []MutantAttempt)
}

// CriticFindingObservation is one test-critic finding's execution-checked
// outcome from a single converged run: whether the flagged test, run ALONE
// against the run's own mutants, actually killed anything. Populated ONLY by
// the driver's conservative auto-refute (never a worker's self-report) —
// soundness #1 extends to critic findings too ("a judge may not certify
// herself" also means a claim about a test is not itself proof).
type CriticFindingObservation struct {
	QueueFindingID                     int64
	Model                              string
	TargetTest, TestFile, TestSelector string
	Scope                              string // normalized (NormalizeScope)
	Evidence, Severity                 string
	Adjudication                       string // auto verdict: refuted|unadjudicated
	Source                             string // "auto"
}

// CriticFindingSink is the optional per-run critic-finding feed (nil ⇒
// no-op), mirroring BugCatchSink: fed on every terminal verdict once
// RecordID/RecordHead are set, so every row carries a linkable record.
type CriticFindingSink interface {
	Record(recordID int64, recordHead string, obs []CriticFindingObservation)
}

// TestEnumerator runs a language plugin's list command in the run's jail
// workspace and returns its stdout — the seam tickMatrix needs to turn a
// suite file into individual selectors before scoring each one. Optional on
// the Driver (nil ⇒ the matrix phase is skipped even when RunSpec.Matrix is
// set — matrix is opt-in on TWO axes: the run must ask for it AND the driver
// must be wired with a way to enumerate).
type TestEnumerator interface {
	Enumerate(ctx context.Context, codePath, code, test string, listCmd []string) (stdout string, err error)
}

// MatrixObservation is one test's execution-proven adequacy from the
// tests×mutants matrix: how many of the run's mutants it killed ALONE, and
// whether that makes it a delete-candidate (scored, ran against a non-empty
// mutant set, killed nothing).
type MatrixObservation struct {
	TestSelector, TestFile string
	Kills, MutantsTotal    int
	DeleteCandidate        bool
}

// MatrixSink is the optional per-run tests×mutants matrix feed (nil ⇒
// no-op), mirroring CriticFindingSink/BugCatchSink: fed on every terminal
// verdict once RecordID/RecordHead are set, only when the matrix actually ran.
type MatrixSink interface {
	Record(recordID int64, recordHead string, obs []MatrixObservation)
}

// EventSink receives the pool's reasoning milestones as replay/telemetry
// events. Optional (nil ⇒ no-op), like Signer/Leaderboard — the pure Driver
// takes no telemetry dependency; the brain wires this to its telemetry store
// keyed on the run's missionID. Kinds: pool_subject, pool_dev_adequacy,
// pool_verdict. detail carries the real values/evidence, never a summary.
type EventSink interface {
	Emit(missionID int64, kind, subject string, detail map[string]any)
}

// MutantRef identifies a mutant without carrying its SOURCE.
//
// The ledger stores parent_sha256 and an id, never a patch: that is enough to
// group, count and compare, and it keeps a tenant's code out of the warehouse.
// This type exists so that choice is enforced by the TYPE rather than by every
// future caller remembering to strip a field — Verdict is marshalled whole
// (CertSigner.SignVerdict, and scan_files.verdict_json is documented as "stored
// whole"), so any field reachable from here reaches the warehouse eventually.
type MutantRef struct {
	ID           string
	ParentSHA256 string
}

// toMutantRefs strips MUTANT SOURCE down to the reference scan_mutants needs:
// id and parent hash. See MutantRef's own doc for why this must happen by
// TYPE, not by caller discipline — anything reachable from a Verdict field
// eventually reaches the warehouse.
func toMutantRefs(ms []adequacy.Mutant) []MutantRef {
	refs := make([]MutantRef, len(ms))
	for i, m := range ms {
		refs[i] = MutantRef{ID: m.ID, ParentSHA256: m.ParentSHA256}
	}
	return refs
}

// Verdict is one run's final, gated outcome.
type Verdict struct {
	Repo, Commit string
	Lang         string  // the run's resolved language plugin name (e.g. "go", "python")
	DevKillRate  float64 // the headline: the DEV suite's kill-rate, from Scorer — never a self-report
	// BaselineDuration is the dev suite's compliant (unmutated) wall-clock
	// runtime — the single input to the audit cost model (O(mutants x the
	// TARGET's suite runtime)). See adequacy.Report.BaselineDuration.
	BaselineDuration time.Duration
	MutantsTotal     int // total mutants the mutant-generator produced
	Survivors        int // mutants the dev's own tests did NOT kill
	ProvenMissed     int // survivors the pool's authored test then killed — real, catchable gaps
	// ProvenMutantIDs is the EVIDENCE behind ProvenMissed: which survivors the
	// authored test actually killed, derived from the same scoring report so
	// the two can never disagree. Empty on every path that did not grade
	// (TestWriterFailed, PoolTestUnsound) AND on a genuine "tried and missed" —
	// those are told apart by those flags, not by this being empty.
	ProvenMutantIDs []string
	// AuthoredTest is the pool's compiling authored test, retained as evidence.
	// A count is not evidence: on 2026-07-31 a paid audit produced a sound,
	// collected, genuinely-grading test that killed 0 of 10 survivors, and the
	// whole surviving record of the attempt was the integer 0 — `certify
	// --repo` has no tape flag, so diagnosing it meant paying to re-run. "" when
	// the writer never produced a compiling test.
	AuthoredTest    string
	RegionsTotal    int             // mutant-generator seats the run dispatched
	RegionsProbed   int             // seats that returned usable mutants
	DroppedRegions  []string        // seats abandoned after MaxShardRetries — the coverage shortfall
	VacuousFindings []queue.Finding // test-critic's designed-to-pass/vacuous flags
	ModelsByRole    map[string]string
	Status          string // certified | needs-review
	// TestWriterFailed is true when the pool exhausted MaxTestWriterAttempts
	// without producing a compiling killing test. HONESTY NOTE: when this is
	// true, ProvenMissed==0 does NOT mean "no real gaps" — it means "gaps
	// found (Survivors > 0), killing test not authored." A testWriterFailed
	// run is never certified (aggregate forces needs-review whenever
	// Survivors > 0 and ProvenMissed < Survivors — see aggregate).
	TestWriterFailed bool
	// PoolTestUnsound is true when the pool's authored test DID compile (so
	// TestWriterFailed is false) but, when actually run against the
	// survivors, its own report did not genuinely grade: it failed on the
	// unmutated compliant code (CompliantPass false — an ordinary outcome
	// for an LLM-written test, since the compile gate only checks syntax,
	// never "passes"), or the canary was not killed (CanaryKilled false —
	// the test never reads the file), or nothing was scored (Total == 0).
	// HONESTY NOTE: this is a DIFFERENT diagnosis from TestWriterFailed (a
	// compiling test WAS produced here), but the same rule applies —
	// ProvenMissed==0 when this is true does NOT mean "no real gaps," and
	// it must never be read as "the pool's test proved every survivor
	// either" (the inverse false claim: fabricating proof from a run that
	// graded nothing). A poolTestUnsound run is never certified — see
	// aggregate.
	PoolTestUnsound bool
	// AuthoredTestNotCollected narrows PoolTestUnsound: the run proved the
	// test command never reached the authored test's file at all. See
	// runState.authoredTestNotCollected.
	AuthoredTestNotCollected bool
	// BaselineFailed is true when the dev suite did not pass on the UNMUTATED
	// compliant code inside the jail — a build/environment failure, not a
	// test-quality verdict. When true, DevKillRate (0) and Survivors (0) are
	// meaningless: nothing was graded. The status is still needs-review
	// (fail-closed), but the readout says "could not grade" rather than
	// reporting a fabricated kill tally. See runState.baselineFailed.
	BaselineFailed bool
	// BaselineOutput is the failing baseline's OWN output, when BaselineFailed
	// is set. The refusal to grade is correct; silence about WHY is not — this
	// is what turns "a build/environment issue" into something an operator can
	// actually fix. Empty when the baseline passed, or when the runner produced
	// no output at all.
	BaselineOutput string
	// SuiteIgnoresFile is true when the dev suite passed on deliberately
	// invalid source — it provably never compiles or imports the file under
	// audit, so DevKillRate is meaningless. DISTINCT from BaselineFailed:
	// the suite is fine, the check command points somewhere else.
	SuiteIgnoresFile bool
	RecordID         int64  // the signed build-record id (0 if signing skipped/failed)
	RecordHead       string // the record's ledger head
	// TimedOut is true when this verdict came from RunDeadline's backstop
	// (see timeoutVerdict) rather than the pool actually converging — the
	// test-writer/shadow/critic/aggregate steps never finished. Status is
	// unconditionally StatusNeedsReview either way (a timed-out run is never
	// certified); this says WHY in a way that must ride into any report or
	// ledger row built from the verdict, so "measured, but the pool did not
	// finish" reads differently from a clean below-threshold audit — a claim
	// carries how it was earned.
	TimedOut bool
	// DevScored mirrors runState.devScored: true once the dev suite's OWN
	// kill-rate was actually measured against real mutants in the real jail.
	// On a TimedOut verdict this is the ONLY thing that makes
	// DevKillRate/Survivors/MutantsTotal real numbers rather than zero
	// values nothing ever computed. A caller must treat a TimedOut verdict
	// with DevScored==false as UNGRADABLE — never print or bank its
	// (fabricated) 0.00 kill rate as a measurement. See timeoutVerdict and
	// cmd/corral/certify_local_drive.go's bankableTimeoutVerdict.
	DevScored bool
	// DevKilledMutants and DevSurvivedMutants are the mutant-level EVIDENCE
	// behind DevKillRate and Survivors: which mutants the DEV suite's own
	// tests killed, and which it did not, each carrying ParentSHA256 — the
	// per-mutant grain scan_mutants records, that a per-file kill rate
	// averages away. "Which generator produces mutants a suite does not
	// catch" is a question about mutants, and cannot be asked of
	// DevKillRate/Survivors alone. Set on every scored run (mirrors
	// BaselineDuration), empty on the could-not-grade paths where no mutant
	// was ever run.
	DevKilledMutants   []MutantRef
	DevSurvivedMutants []MutantRef
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
	// Matrix is the tests×mutants matrix result (swarm slice 5), when the run
	// opted in (RunSpec.Matrix) and a Driver.Enumerator was wired. nil when
	// the matrix phase never ran (opted out, no Enumerator, or it hasn't
	// reached that tick yet) — a caller (the --local verdict summary, the
	// brain's matrix sink caller) must treat nil as "no matrix data",
	// never as "zero tests scored". Exported here (mirroring AuthoredTest)
	// so a caller outside the package can read the driver's internal
	// runState.matrix without a package-private accessor.
	Matrix *matrix.Result
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
	// devKilled is the mutant-level counterpart to devSurvivors: the mutants
	// the dev suite's OWN tests killed (rep.Killed), reduced to MutantRef (id
	// + ParentSHA256, no source) at the point it's built, since nothing else
	// downstream needs Code for a KILLED mutant the way renderTestWriter etc.
	// need it for survivors. Set alongside devSurvivors in applyDevScore,
	// carried onto Verdict.DevKilledMutants — see that field's doc for why.
	devKilled []MutantRef
	// baselineFailed is true when the dev suite did not pass on the UNMUTATED
	// compliant code inside the jail (adequacy.Report.CompliantPass=false) — a
	// build/environment failure (bad toolchain, missing dep, a shell-mangled
	// test command), NOT a test-quality verdict. When set, devKillRate is a
	// meaningless 0 and devSurvivors is empty because NOTHING was actually
	// graded, so the readout must say "could not grade", never fabricate a tally.
	baselineFailed bool
	// baselineOutput is the runner's OWN output from that failing baseline
	// (adequacy.Report.BaselineOutput). Without it, baselineFailed says only
	// THAT the suite did not pass, which is the least debuggable outcome an
	// audit can produce: the operator is told their environment is broken and
	// given nothing to act on. Carried on the verdict so the readout can print
	// the suite's own words — see renderAdvVerdict. Only meaningful when
	// baselineFailed is true.
	baselineOutput string
	// baselineDuration is how long the dev suite's compliant (unmutated) run
	// took (adequacy.Report.BaselineDuration) — set on every scored run,
	// including a failed baseline, since a suite that took 90s to fail is the
	// case an operator most needs the number for. See Verdict.BaselineDuration.
	baselineDuration time.Duration
	// suiteIgnoresFile is true when the dev suite PASSED its baseline but also
	// passed on deliberately invalid source (adequacy.Report.CanaryKilled=false):
	// the check command provably never compiles or imports the file under audit,
	// so devKillRate is a meaningless 0 and no mutant was ever graded. Kept
	// SEPARATE from baselineFailed on purpose — "your suite is broken or your
	// environment is wrong" and "your suite is fine but it is pointed somewhere
	// else" send an operator to completely different places.
	suiteIgnoresFile bool
	// mutants is the FULL merged mutant set (every shard, pre-scoring) that
	// tickDevAdequacy graded the dev suite against — retained (not just its
	// count/survivors) so tickAggregate's critic auto-refute step can
	// re-score a single flagged test against the SAME exam later.
	mutants []adequacy.Mutant

	testWriterTaskID int64

	poolScored   bool
	provenMissed int

	// matrix is the tests×mutants matrix result (nil until tickMatrix runs,
	// and forever nil when RunSpec.Matrix is false or no Enumerator is
	// wired). matrixDone guards tickMatrix to run AT MOST ONCE per run — the
	// matrix is O(tests × mutants) jail runs, the most expensive phase in the
	// pipeline, and Tick may be called many times before convergence.
	matrix     *matrix.Result
	matrixDone bool

	// shadowWriter* hold the CHALLENGER writer seat's outcome. They are
	// deliberately separate from devKilled/devSurvivors: nothing here may
	// reach aggregate(), the Verdict, or the signed record. Verdict is hashed
	// WHOLE by CertSigner.SignVerdict, so a single leaked outcome field would
	// change every previously signed record's digest — see
	// writer_shadow_test.go.
	//
	// shadowWriterKilled is the challenger writer's PROVEN-KILL set over
	// run.devSurvivors — the mutant-level counterpart of provenIDs, which is
	// the PRIMARY writer's vector over that same set (RULING P9). The two pair
	// one-for-one by MutantRef.ID: both are produced by provenMutantIDs, both
	// over run.devSurvivors, so the head-to-head compares the two WRITERS.
	// Deliberately NOT paired against devKilled, which is the DEV SUITE's
	// vector over every mutant and answers a different question.
	shadowWriterKilled   []MutantRef
	shadowWriterMeasured bool
	// shadowWriterAttempts is the challenger's OWN compile-retry budget.
	// Sharing testWriterAttempts would let a failing measurement seat exhaust
	// the graded seat's retries and change the verdict.
	shadowWriterAttempts int
	// shadowWriterTaskID is the live challenger writer task's id, tracked the
	// same way testWriterTaskID is and for the same reason: SupersedeTask
	// auto-uniquifies a replacement that reuses the old key, so the seat can
	// never be re-looked-up by RoleTestWriterShadow's key after a retry.
	// 0 when the challenger is off or was never enqueued.
	shadowWriterTaskID int64
	// shadowWriterSpent is the CUMULATIVE wall-clock this run has credited
	// back to the deadline clock for challenger-writer work. runShadowWriterPass
	// may be entered on several ticks (unlike runShadowPass, which runs once),
	// so the credit is capped in aggregate at ShadowTimeBudget rather than
	// per-call — otherwise repeated entries could extend the primary's
	// deadline without bound.
	shadowWriterSpent time.Duration

	// testWriterAttempts counts compile-failure reopens of the test-writer
	// task, guarded against MaxTestWriterAttempts in tickPoolAdequacy. Once
	// exhausted, testWriterFailed is set and the run converges instead of
	// reopening again.
	testWriterAttempts int
	// testWriterFailed is set when the test-writer exhausted
	// MaxTestWriterAttempts without producing a compiling test. The run still
	// converges (poolScored=true, provenMissed=0), but ProvenMissed=0 here does
	// NOT mean "no real gaps" — it means "gaps found (Survivors > 0), killing
	// test not authored." Carried onto the signed Verdict (TestWriterFailed) so
	// the CLI/cockpit can say so honestly instead of implying a clean suite.
	testWriterFailed bool
	// poolTestUnsound is set when the pool's authored test compiled (so
	// testWriterFailed is false) but its scoring report did not genuinely
	// grade (CompliantPass/CanaryKilled false, or Total 0) — see
	// Verdict.PoolTestUnsound's doc for the full honesty rule this carries
	// onto the signed verdict.
	poolTestUnsound bool
	// authoredTestNotCollected narrows poolTestUnsound to its most common
	// cause: the test command never collected the authored test's own file.
	// Carried separately because the two fixes are different -- "your test
	// asserts something untrue" vs "your command does not run that file" --
	// and the second is nearly always a command pinned to a single path while
	// the authored test is a NEW file beside the developer's.
	authoredTestNotCollected bool

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

	// provenIDs are the ids of the survivors the authored test actually
	// killed — the evidence behind provenMissed, carried onto the Verdict and
	// into the scan ledger so a later query can tell WHICH gaps were proven
	// (and, on a "tried and missed", that the attempt genuinely happened and
	// caught nothing). Empty on every path that did not grade.
	provenIDs []string
	// writerSalvaged is true when the primary writer's provenIDs came from a
	// DESELECTED re-score rather than a clean run. The challenger seat has no
	// equivalent rescue, so a salvaged run's head-to-head is confounded in the
	// primary's favour and must not be recorded as a comparison.
	writerSalvaged bool
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

	// CriticFindings is the optional per-run critic-finding auto-adjudication
	// feed (nil = no-op), mirroring BugCatch: fed AFTER Signer (once
	// RecordID/RecordHead are set) on every terminal verdict. See
	// tickAggregate's auto-refute step.
	CriticFindings CriticFindingSink

	// MutantAttempts is the optional per-run feed of BOTH writer seats'
	// per-mutant outcomes (nil = no-op), mirroring BugCatch/CriticFindings.
	// Fed pair-or-nothing by recordMutantAttempts: see its doc for the full
	// gating (challenger configured AND measured AND primary not salvaged).
	MutantAttempts MutantAttemptSink

	// Enumerator is the optional jail-backed test-list seam (nil = the matrix
	// phase is always skipped, regardless of any run's RunSpec.Matrix). When
	// set AND a run opts in, tickMatrix uses it to enumerate the dev suite's
	// individual tests before scoring each against the run's mutants.
	Enumerator TestEnumerator

	// Matrix is the optional per-run tests×mutants matrix feed (nil = no-op),
	// fed AFTER Signer (once RecordID/RecordHead are set), mirroring
	// CriticFindings/BugCatch — same RecordID!=0 guard, same reasoning.
	Matrix MatrixSink

	// MatrixWorkers bounds the matrix phase's concurrent jail scoring calls.
	// <= 0 defaults to matrixDefaultWorkers. Each ScoreReport/Enumerate call
	// runs in its OWN disposable os.MkdirTemp workspace with no shared
	// mutable state (confirmed against bwrapJail — see jail.go), so scoring
	// concurrently is safe the same way --swarm's concurrent workers are.
	MatrixWorkers int

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
	return RunState{Converged: run.verdict != nil, Verdict: run.verdict, AuthoredTest: run.authoredTest, Matrix: run.matrix}, true
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
		if err := d.tickPoolAdequacy(ctx, missionID, run); err != nil {
			return nil, err
		}
	}

	if run.poolScored && !run.matrixDone {
		d.tickMatrix(ctx, run)
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

// applyDevScore grades the dev suite against `mutants` and records the result
// on run. It reads ScoreReport (not Score) so the two could-not-grade cases
// arrive as their own flags instead of being inferred from the tally: the old
// inference (killRate==0 && len(survivors)==0) cannot tell a failed baseline
// from a suite that never reads the file, because BOTH return early with an
// empty report — and conflating them sends an operator to debug the wrong
// thing entirely.
func applyDevScore(ctx context.Context, run *runState, scorer Scorer, mutants []adequacy.Mutant) error {
	rep, serr := scorer.ScoreReport(ctx, run.rs.CodePath, run.rs.Code, run.rs.DevTestCode, mutants, run.rs.TestCmd)
	if serr != nil {
		return fmt.Errorf("advpool: score dev tests: %w", serr)
	}
	run.baselineFailed = !rep.CompliantPass
	run.baselineDuration = rep.BaselineDuration
	// Keep the runner's own words on a FAILING baseline. certify --repo has
	// printed these since the day two paid audits dead-ended here with nothing
	// to go on; certify --local computed the same string and discarded it, so
	// the single most common first-run failure was undiagnosable.
	if !rep.CompliantPass {
		run.baselineOutput = rep.BaselineOutput
	}
	// Gated on CompliantPass: when the baseline itself failed, adequacy.Score
	// returns BEFORE running the canary, so CanaryKilled is false for a reason
	// that is not "the suite ignores this file". Only a suite that PASSED its
	// baseline and then also passed on invalid source has proven it never
	// compiles or imports the file.
	run.suiteIgnoresFile = rep.CompliantPass && !rep.CanaryKilled
	run.devScored = true
	run.devKillRate = rep.KillRate()
	run.mutantsTotal = len(mutants)
	run.devSurvivors = survivorsFrom(rep, mutants)
	run.devKilled = toMutantRefs(killedFrom(rep, mutants))
	run.mutants = mutants
	return nil
}

// survivorsFrom maps a report's surviving mutant IDs back to the mutants they
// name, preserving the report's order. IDs with no matching mutant are
// dropped: a survivor the caller cannot produce the code for is not evidence.
func survivorsFrom(rep adequacy.Report, mutants []adequacy.Mutant) []adequacy.Mutant {
	byID := make(map[string]adequacy.Mutant, len(mutants))
	for _, m := range mutants {
		byID[m.ID] = m
	}
	survivors := make([]adequacy.Mutant, 0, len(rep.Survived))
	for _, id := range rep.Survived {
		if m, ok := byID[id]; ok {
			survivors = append(survivors, m)
		}
	}
	return survivors
}

// killedFrom is survivorsFrom's counterpart: it maps a report's KILLED mutant
// ids back to the mutants they name, preserving the report's order. Together
// with survivorsFrom this is the mutant-level evidence behind
// Verdict.DevKilledMutants/DevSurvivedMutants — a per-file kill rate averages
// this away; scan_mutants needs it at the mutant grain.
func killedFrom(rep adequacy.Report, mutants []adequacy.Mutant) []adequacy.Mutant {
	byID := make(map[string]adequacy.Mutant, len(mutants))
	for _, m := range mutants {
		byID[m.ID] = m
	}
	killed := make([]adequacy.Mutant, 0, len(rep.Killed))
	for _, id := range rep.Killed {
		if m, ok := byID[id]; ok {
			killed = append(killed, m)
		}
	}
	return killed
}

// salvageByDeselect tries to rescue a partially-broken authored test: read the
// runner's own failing-test selectors out of the clean-code failure, deselect
// exactly those, and re-score with what remains.
//
// Returns ok=false — and changes nothing — unless the remainder is genuinely
// sound (CompliantPass, CanaryKilled, Total > 0) AND proves at least one
// survivor. Both halves matter. Without the soundness gate this would be the
// fabrication path again; without the "proves something" gate it would consume
// a run's one chance at salvage to record a zero, displacing a retry that
// might have done better.
//
// Requires the language plugin to implement lang.FailureDeselector and the
// Scorer to be able to report the failure output. Anything missing means no
// salvage, never a guess: a wrong selector would deselect the wrong test and
// silently narrow the exam, making the run look healthier while proving less.
func (d *Driver) salvageByDeselect(ctx context.Context, run *runState, writerTest string, rep adequacy.Report) (proven int, ids []string, deselected int, ok bool) {
	fd, canDeselect := langFor(run.rs).(golang.FailureDeselector)
	if !canDeselect {
		return 0, nil, 0, false
	}
	failure := compliantFailureOutput(ctx, d.Scorer, run, writerTest)
	failing := fd.FailedTests(failure)
	if len(failing) == 0 {
		return 0, nil, 0, false
	}
	args := fd.DeselectArgs(failing)
	if len(args) == 0 {
		return 0, nil, 0, false
	}

	cmd := strings.TrimSpace(run.rs.TestCmd + " " + strings.Join(args, " "))
	salvaged, serr := scoreAuthored(ctx, d.Scorer, run.rs.CodePath, run.rs.Code, writerTest, run.devSurvivors, cmd)
	if serr != nil {
		// A failed salvage attempt is not a failed run: fall back to the
		// retry path rather than losing the dev-adequacy result already
		// computed.
		return 0, nil, 0, false
	}
	if !salvaged.CompliantPass || !salvaged.CanaryKilled || salvaged.Total == 0 {
		return 0, nil, 0, false
	}
	killed := provenMutantIDs(salvaged, run.devSurvivors)
	if len(killed) == 0 {
		return 0, nil, 0, false
	}
	return len(killed), killed, len(failing), true
}

// CompliantFailureExplainer is the optional Scorer extension that reports WHY
// an authored test failed against the unmutated code — the runner's own
// output, the thing that makes the retry corrective rather than blind. A
// Scorer that does not implement it simply yields no detail: the retry still
// happens, because "your test failed on the correct code" is itself
// information the writer never previously received, but it is far weaker than
// handing over the actual failing assertion.
type CompliantFailureExplainer interface {
	CompliantFailure(ctx context.Context, codePath, code, test, testCmd string) string
}

// compliantFailureOutput asks the Scorer for the clean-code failure detail,
// tolerating a Scorer that cannot supply it.
func compliantFailureOutput(ctx context.Context, s Scorer, run *runState, test string) string {
	ex, ok := s.(CompliantFailureExplainer)
	if !ok {
		return ""
	}
	return ex.CompliantFailure(ctx, run.rs.CodePath, run.rs.Code, test, run.rs.TestCmd)
}

// provenMutantIDs returns the ids of the survivors the POOL's authored test
// actually killed: everything in `mutants` that is NOT in rep.Survived, in the
// input's own order (deterministic, so a ledger row is stable across runs).
//
// This is the EVIDENCE behind ProvenMissed. The count on its own cannot
// distinguish "killed these three, missed those seven" from a bare 0, which is
// what made a real tried-and-missed on pallets/flask (2026-07-31) impossible
// to diagnose without paying for another run: `certify --repo` has no tape
// flag, so the integer was the entire surviving record of the attempt.
func provenMutantIDs(rep adequacy.Report, mutants []adequacy.Mutant) []string {
	stillAlive := make(map[string]struct{}, len(rep.Survived))
	for _, id := range rep.Survived {
		stillAlive[id] = struct{}{}
	}
	killed := make([]string, 0, len(mutants))
	for _, m := range mutants {
		if _, alive := stillAlive[m.ID]; !alive {
			killed = append(killed, m.ID)
		}
	}
	return killed
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

	if err := applyDevScore(ctx, run, d.Scorer, mutants); err != nil {
		return err
	}
	killRate, survivors := run.devKillRate, run.devSurvivors

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
	switch {
	case run.baselineFailed:
		// The dev suite did NOT pass on the unmutated code in the jail: nothing
		// was graded, so a "killed N of N" tally would be fabricated from an empty
		// survivor set. Say what actually happened instead.
		log.Printf("advpool: run %d dev-adequacy: COULD NOT GRADE — the dev suite did not pass on the UNMUTATED code in the jail (baseline build/test failed); this is a build/environment failure, not a test-quality verdict", missionID)
	case run.suiteIgnoresFile:
		// The suite passed on source that cannot compile: it provably never
		// compiles or imports the audited file, so no mutant of it could have
		// been graded. A different diagnosis from a failed baseline — the suite
		// is fine, the check command is pointed somewhere else.
		log.Printf("advpool: run %d dev-adequacy: COULD NOT GRADE — the dev suite PASSED on deliberately invalid source, so it never compiles or imports %s; the suite is fine, the check command does not exercise this file", missionID, run.rs.CodePath)
	default:
		log.Printf("advpool: run %d dev-adequacy: the dev's OWN tests scored %.0f%% (killed %d of %d mutants, %d survived — bugs the dev's tests miss)",
			missionID, killRate*100, len(mutants)-len(survivors), len(mutants), len(survivors))
	}
	d.emit(missionID, "pool_dev_adequacy", "", map[string]any{
		"dev_kill_rate": run.devKillRate, "mutants_total": run.mutantsTotal,
		"survivors": len(run.devSurvivors), "survivor_ids": survivorIDs(run.devSurvivors),
	})

	if len(survivors) == 0 {
		// THREE different runs reach here, and only one of them is good news:
		//   1. a perfect dev suite — it killed every mutant;
		//   2. a failed baseline (run.baselineFailed) — nothing was graded;
		//   3. a surviving canary (run.suiteIgnoresFile) — the check command
		//      never compiles or imports the file, so nothing was graded.
		// In all three there are no survivors for the test-writer to expose, so
		// skip it and the pool-adequacy step entirely and go straight to
		// aggregate. Without this the test-writer would be promoted to "write a
		// test targeting the survivors" of which there are none (a degenerate
		// prompt) and the run could NEVER converge — i.e. the pool could grade a
		// bad suite but never certify a perfect one. Cases 2 and 3 do NOT
		// certify on a 100% kill-rate: the could-not-grade flags ride the
		// verdict (see tickAggregate) so the readout says what happened
		// instead of reporting an empty survivor set as a perfect score.
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

	// The CHALLENGER writer seat, enqueued from the SAME rendered instruction
	// the primary was just superseded with — the same survivors, the same
	// target, under its OWN role key (RoleTestWriterShadow), so
	// tasksByRole(RoleTestWriter) structurally cannot return it.
	//
	// Enqueued HERE, beside the primary, rather than at the point it is read
	// (tickPoolAdequacy): a challenger asked only once the primary has already
	// finished would have to hold the run open to be answered, and shadow work
	// must never delay or gate the primary. Asked in parallel it costs the run
	// nothing.
	//
	// NEVER fatal: the seat is measurement, so an enqueue failure is logged
	// and the challenger is simply skipped.
	if strings.TrimSpace(run.rs.ShadowWriterModel) != "" {
		d.enqueueShadowWriter(missionID, run, tw.Title, renderTestWriter(run.rs, run.sigs, survivors))
	}

	if _, err := d.Q.PromoteReady(missionID); err != nil {
		return fmt.Errorf("advpool: promote after test-writer supersede: %w", err)
	}
	return nil
}

// tickPoolAdequacy is step 3: once test-writer is done, validate that its
// test compiles, then score it (via Scorer, brain-side) against the
// survivors the dev's tests missed. ProvenMissed is how many of those
// survivors the pool's test then killed — real, catchable gaps.
func (d *Driver) tickPoolAdequacy(ctx context.Context, missionID int64, run *runState) error {
	// The challenger writer pass, mirroring the challenger GENERATOR pass in
	// tickDevAdequacy: guarded on a trimmed non-empty model, errors logged, the
	// seat skipped. A shadow failure is NEVER fatal — it is measurement, not
	// the gate — so this returns nothing and is run BEFORE the primary's own
	// early return, giving the challenger every tick the primary's pipeline
	// happens to take without ever adding one of its own.
	if strings.TrimSpace(run.rs.ShadowWriterModel) != "" {
		d.runShadowWriterPass(ctx, missionID, run)
	}

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
		run.testWriterAttempts++
		if run.testWriterAttempts < MaxTestWriterAttempts {
			// CORRECTIVE retry: re-render the test-writer WITH the compiler error
			// and its own broken test so it fixes the actual problem, instead of
			// the old blind ReopenTask that re-ran the identical prompt and let
			// the model repeat the same mistake until exhaustion. Supersede (the
			// same mechanism the survivor-promote uses) is required to swap the
			// instruction — ReopenTask keeps the old one.
			var newInstr string
			if strings.TrimSpace(writerTest) == "" {
				// The model returned NO usable test (an empty result — seen on
				// hard targets). Feeding an empty "broken test" into the REPAIR
				// prompt ("here is your test: «», fix the compile error") just
				// begets more emptiness and burns the whole retry budget in a
				// degenerate loop, so a later good attempt never gets a slot.
				// Re-issue a FRESH prompt instead — a clean shot, not a repair.
				newInstr = renderTestWriter(run.rs, run.sigs, run.devSurvivors)
			} else {
				var ce *CompileError
				compileMsg := cerr.Error()
				if errors.As(cerr, &ce) && strings.TrimSpace(ce.Output) != "" {
					compileMsg = ce.Output
				}
				newInstr = renderTestWriterWithRepair(run.rs, run.sigs, run.devSurvivors, writerTest, compileMsg)
			}
			newID, serr := d.Q.SupersedeTask(tw.ID, queue.TaskSpec{
				Key:         RoleTestWriter,
				Role:        RoleTestWriter,
				Title:       tw.Title,
				Instruction: newInstr,
				Model:       tw.Model,
			})
			if serr != nil {
				return fmt.Errorf("advpool: reissue test-writer with compile feedback: %w", serr)
			}
			run.testWriterTaskID = newID
			if _, perr := d.Q.PromoteReady(missionID); perr != nil {
				return fmt.Errorf("advpool: promote test-writer after compile-feedback reissue: %w", perr)
			}
			return fmt.Errorf("advpool: test-writer result does not compile, reissued for retry (%d/%d): %w",
				run.testWriterAttempts, MaxTestWriterAttempts, cerr)
		}
		// Exhausted: STOP reopening. A hard survivor whose only authored tests
		// never compile must not spin the run to RunDeadline with no verdict —
		// converge now with the real, already-computed dev-adequacy result
		// (kill-rate, survivors, critic findings) rather than throwing it away.
		// cerr rides into the log line itself: the retry path above already
		// wraps it into the reissued instruction (renderTestWriterWithRepair),
		// but the repo path (certify_repo.go's localExecutor) sends progress
		// to io.Discard, so that instruction text never surfaces anywhere a
		// human reads it. Diagnosing the missing-import bug this constant
		// exists to prevent a repeat of took a code trace instead of a log
		// line — this is the fix for that: the LAST compile error is always
		// visible here, on the one path every caller's log actually reaches.
		log.Printf("advpool: %s: test-writer could not produce a compiling test after %d attempts — %d survivor(s) found but not proven-killed; converging without an authored test: %v",
			run.rs.CodePath, MaxTestWriterAttempts, len(run.devSurvivors), cerr)
		run.poolScored = true
		run.provenMissed = 0
		run.testWriterFailed = true
		return nil
	}

	// Capture the compiling killing test for hand-back (read by RunStatus under
	// d.mu, so store it under the same lock).
	d.mu.Lock()
	run.authoredTest = writerTest
	d.mu.Unlock()

	// ScoreReport (via scoreAuthored/AuthoredScorer), never the collapsed
	// Score: a caller MUST be able to tell "the authored test genuinely
	// killed N survivors" from "nothing was actually graded" — Score's bare
	// (killRate, survivors) tuple cannot express CompliantPass/CanaryKilled/
	// Total, so an ungraded run (e.g. the authored test fails on the
	// unmutated compliant code — an ordinary outcome for an LLM-written
	// test, since CompileTest only checks syntax, never "passes") would
	// return an EMPTY survivors slice with no error, and the arithmetic
	// below would silently compute provenMissed = len(devSurvivors) - 0,
	// reporting EVERY survivor as execution-proven from a run in which no
	// mutant ever executed — corral's strongest claim, fabricated.
	rep, serr := scoreAuthored(ctx, d.Scorer, run.rs.CodePath, run.rs.Code, writerTest, run.devSurvivors, run.rs.TestCmd)
	if serr != nil {
		return fmt.Errorf("advpool: score pool test: %w", serr)
	}
	// A test that COMPILED and then failed against the unmutated, correct code
	// is repairable, and until now was not repaired. The compile-failure path
	// above has fed the compiler's own error back since CompileError existed,
	// precisely because a bare "does not compile" taught the writer nothing —
	// but a clean-code failure, which is just as diagnosable (the runner says
	// exactly which assertion broke), got no retry at all: the run logged it,
	// marked poolTestUnsound and converged.
	//
	// Measured cost of that, on the first run whose authored test was ever
	// retained (gemini-3.6-flash on pallets/flask, 2026-07-31): the writer
	// produced 13 tests against real internals and TEN PASSED. Three carried
	// wrong API assumptions, and because the compliant check is all-or-nothing
	// per FILE, those three discarded all thirteen — Total=0, nothing scored,
	// the whole run zeroed including ten tests that might have killed
	// survivors. The writer was never told.
	//
	// Deliberately scoped to CompliantPass==false. The other two unsound
	// shapes are NOT the writer's fault and retrying would only burn budget:
	// !CanaryKilled means the project's own command never reads the file (a
	// discovery/config fact), and Total==0 with a passing baseline means there
	// was nothing to score.
	if !rep.CompliantPass {
		// SALVAGE FIRST, before spending another model call. The compliant
		// check is all-or-nothing per FILE, and the measured cost of that is
		// large: on the first authored test corral ever retained
		// (gemini-3.6-flash on pallets/flask, 2026-07-31) 13 tests were
		// written, TEN PASSED, and all 13 were discarded because 3 carried
		// wrong API assumptions.
		//
		// Deselecting the failures and re-scoring with the remainder does not
		// depend on the model being able to repair itself — it is arithmetic
		// over the runner's own output. Accepted ONLY if the remainder
		// actually proves something: a salvage that proves nothing is no
		// better than the retry it would displace, so in that case we fall
		// through and let the writer try again.
		if salvaged, ids, n, ok := d.salvageByDeselect(ctx, run, writerTest, rep); ok {
			run.poolScored = true
			run.writerSalvaged = true
			run.provenMissed = salvaged
			run.provenIDs = ids
			log.Printf("advpool: %s: the authored test failed on the unmutated code, but deselecting its %d failing test(s) left a sound remainder that PROVED %d of %d survivor(s)",
				run.rs.CodePath, n, salvaged, len(run.devSurvivors))
			return nil
		}
	}

	if !rep.CompliantPass && run.testWriterAttempts < MaxTestWriterAttempts-1 {
		run.testWriterAttempts++
		failure := compliantFailureOutput(ctx, d.Scorer, run, writerTest)
		newID, serr := d.Q.SupersedeTask(tw.ID, queue.TaskSpec{
			Key:         RoleTestWriter,
			Role:        RoleTestWriter,
			Title:       tw.Title,
			Instruction: renderTestWriterRepairing(run.rs, run.sigs, run.devSurvivors, writerTest, "", failure),
			Model:       tw.Model,
		})
		if serr != nil {
			return fmt.Errorf("advpool: reissue test-writer with clean-code failure: %w", serr)
		}
		run.testWriterTaskID = newID
		if _, perr := d.Q.PromoteReady(missionID); perr != nil {
			return fmt.Errorf("advpool: promote test-writer after clean-code-failure reissue: %w", perr)
		}
		log.Printf("advpool: %s: the authored test compiled but FAILED on the unmutated code — reissuing with the failure fed back (%d/%d)",
			run.rs.CodePath, run.testWriterAttempts, MaxTestWriterAttempts)
		return nil
	}

	run.poolScored = true
	if !rep.CompliantPass || !rep.CanaryKilled || rep.Total == 0 {
		// A DIAGNOSIS, not a score: the compiling authored test did not
		// genuinely grade against the survivors. provenMissed must not
		// become len(devSurvivors) (a fabricated maximum, the false-proof
		// inversion of the false-accusation class this codebase already
		// guards against elsewhere) NOR a bare 0 read as "tried and missed"
		// — poolTestUnsound carries the distinction onto the verdict so a
		// caller can print it honestly, the same way testWriterFailed
		// already is.
		log.Printf("advpool: %s: the pool's authored test compiled but did not genuinely grade against the survivors (CompliantPass=%v CanaryKilled=%v Total=%d) — converging with proven_missed=0, not a maximum",
			run.rs.CodePath, rep.CompliantPass, rep.CanaryKilled, rep.Total)
		run.provenMissed = 0
		run.poolTestUnsound = true
		run.authoredTestNotCollected = rep.AuthoredTestUnreached
		return nil
	}
	poolSurvivors := survivorsFrom(rep, run.devSurvivors)
	run.provenMissed = len(run.devSurvivors) - len(poolSurvivors)
	// The evidence behind that count — see provenMutantIDs. Derived from the
	// SAME report, so the list can never disagree with the number.
	run.provenIDs = provenMutantIDs(rep, run.devSurvivors)
	// The two failure paths above each log why they produced nothing. This one
	// — the path that actually grades — logged nothing at all, in either
	// direction. That asymmetry is why a real "tried and missed" (a sound,
	// collected, genuinely-grading authored test that killed NO survivor) was
	// undebuggable after the fact: `certify --repo` has no tape flag, so the
	// run's whole record of the attempt was a single `0 proven missed` in the
	// report. Log the outcome on the success path too, so the interesting case
	// is distinguishable from the boring one without re-running anything.
	if run.provenMissed > 0 {
		log.Printf("advpool: %s: the pool's authored test PROVED %d of %d survivor(s) catchable by execution",
			run.rs.CodePath, run.provenMissed, len(run.devSurvivors))
	} else {
		log.Printf("advpool: %s: the pool's authored test graded soundly (CompliantPass=true CanaryKilled=true Total=%d) but killed NONE of the %d survivor(s) — a real 'tried and missed', not an ungraded run",
			run.rs.CodePath, rep.Total, len(run.devSurvivors))
	}
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
	// A run with NO critic assigned seeds no critic task (see BuildDAG), so the
	// wait below could never be satisfied and the run would spin to its
	// --timeout and bank an unverified verdict — a silent hang, not an error.
	// Skip straight to aggregation with no advisory review, which is the honest
	// representation: no critic ran, so there are no findings, as distinct from
	// a critic that ran and found nothing.
	criticEnabled := strings.TrimSpace(d.Assign[RoleTestCritic]) != ""

	var criticFindings []queue.Finding
	if criticEnabled {
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
		criticFindings = filterCriticFindings(findings, tc.ID)
	}

	// The critic's findings are a second model's UNVERIFIED review — carried on
	// the verdict as advisory (VacuousFindings) but NOT gating the signed record
	// (pass false, not d.blockingFindingOpen(findings)): certification is an
	// execution-proven judgment (kill-rate + proven_missed), never an LLM's
	// opinion, which can hallucinate. blockingFindingOpen remains for a future
	// execution-verified finding path.
	v := aggregate(run.rs, d.Assign, run.devKillRate, run.mutantsTotal, len(run.devSurvivors), run.provenMissed,
		criticFindings, d.Threshold, false, run.testWriterFailed, run.poolTestUnsound)
	v.RegionsTotal = run.regionsTotal
	v.RegionsProbed = run.regionsProbed
	v.DroppedRegions = run.droppedRegions
	// The evidence behind ProvenMissed rides onto the verdict beside the count
	// itself — set here, with the other post-aggregate fields, rather than
	// widening aggregate()'s already-long signature.
	v.ProvenMutantIDs = run.provenIDs
	v.AuthoredTest = run.authoredTest
	// Narrows PoolTestUnsound to "your test command never collected the
	// authored test's file" -- set here rather than widening aggregate()'s
	// signature, alongside the other post-aggregate diagnosis fields.
	v.AuthoredTestNotCollected = run.authoredTestNotCollected
	// A baseline that couldn't pass is fail-closed to needs-review by aggregate
	// (devKillRate 0 < threshold); mark it so the readout says "could not grade"
	// instead of reporting the 0 as if the suite were graded and scored zero.
	v.BaselineFailed = run.baselineFailed
	v.BaselineOutput = run.baselineOutput
	// The other could-not-grade case, carried separately so the readout can
	// name the right one: the suite passed but never reads the audited file.
	v.SuiteIgnoresFile = run.suiteIgnoresFile
	// Carried alongside the other could-not-grade / baseline fields, set here
	// rather than widening aggregate()'s signature — see Verdict.BaselineDuration.
	v.BaselineDuration = run.baselineDuration
	// The mutant-level evidence behind DevKillRate/Survivors, carried the same
	// way — see Verdict.DevKilledMutants.
	v.DevKilledMutants = run.devKilled
	v.DevSurvivedMutants = toMutantRefs(run.devSurvivors)

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

	// Conservative auto-refute/confirm of the test-critic's findings — the full
	// matrix-vs-single-test policy lives on adjudicateCriticFindings' doc. Same
	// RecordID!=0 guard as BugCatch (see its doc comment): a record_id=0 row is
	// unlinkable.
	if d.CriticFindings != nil && v.RecordID != 0 {
		obs := d.adjudicateCriticFindings(ctx, missionID, run, criticFindings, v)
		if len(obs) > 0 {
			d.CriticFindings.Record(v.RecordID, v.RecordHead, obs)
		}
	}

	// Feed both writer seats' per-mutant outcomes, pair-or-nothing. Same
	// RecordID!=0 guard as BugCatch/CriticFindings above; see
	// recordMutantAttempts' own doc for the rest of the gating.
	d.recordMutantAttempts(run, v)

	// Feed the matrix sink with the SAME matrix result tickMatrix already
	// computed — only when the matrix actually ran (run.matrix != nil) and a
	// sink is wired. Same RecordID!=0 guard as CriticFindings/BugCatch above.
	if run.matrix != nil && d.Matrix != nil && v.RecordID != 0 {
		obs := make([]MatrixObservation, len(run.matrix.Rows))
		for i, row := range run.matrix.Rows {
			obs[i] = MatrixObservation{
				TestSelector:    row.Selector,
				TestFile:        row.TestFile,
				Kills:           row.Kills,
				MutantsTotal:    row.MutantsTotal,
				DeleteCandidate: row.DeleteCandidate,
			}
		}
		d.Matrix.Record(v.RecordID, v.RecordHead, obs)
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

// recordMutantAttempts feeds BOTH writer seats' per-mutant outcomes, or
// neither.
//
// The pair rule is the point: an unpaired vector cannot contribute to a
// within-run correlation, and a sink half-fed with unpairable rows would
// invite pooling correlation across runs — which is confounded, because each
// run has its own mutant set.
func (d *Driver) recordMutantAttempts(run *runState, v Verdict) {
	// The v.RecordID != 0 guard is the SAME one BugCatch and CriticFindings
	// use, for the same reason (driver.go:1630): a Driver wired without a
	// Signer leaves RecordID at zero, and rows carrying record_id=0 are
	// unlinkable to the audit that produced them.
	if d.MutantAttempts == nil || v.RecordID == 0 || run.rs.ShadowWriterModel == "" || !run.shadowWriterMeasured {
		return
	}
	// RULING P11 — a SALVAGED primary is not comparable.
	//
	// The two seats run under asymmetric leniency: the primary gets
	// salvageByDeselect (driver.go:1531 — a partially-broken suite has its
	// failing selectors deselected and is re-scored, and that salvaged
	// remainder becomes provenIDs), a clean-code repair round, and 3 attempts.
	// The challenger gets 2 compile retries and neither rescue.
	//
	// So when the primary salvaged, its vector came from a deselected
	// remainder and the challenger's did not, and the head-to-head quietly
	// favours the primary. That is CONFOUNDED — the same class of error as
	// scoring the two seats against different mutant sets — and this pool's
	// standing discipline is to record no comparison rather than a confounded
	// one.
	if run.writerSalvaged {
		return
	}
	// RULING P9: pair the two WRITERS over run.devSurvivors — the set both are
	// asked to kill. `run.provenIDs` is the PRIMARY writer's proven-kill vector
	// (driver.go:581, from provenMutantIDs(rep, run.devSurvivors)).
	// Do NOT use run.devKilled: that is the DEV SUITE's vector over every
	// mutant, so pairing it against a writer compares a writer to the
	// developer's own tests, not writer to writer.
	killedByPrimary := make(map[string]bool, len(run.provenIDs))
	for _, id := range run.provenIDs {
		killedByPrimary[id] = true
	}
	killedByShadow := make(map[string]bool, len(run.shadowWriterKilled))
	for _, m := range run.shadowWriterKilled {
		killedByShadow[m.ID] = true
	}
	outcome := func(killed bool) string {
		if killed {
			return "killed"
		}
		return "survived"
	}
	attempts := make([]MutantAttempt, 0, 2*len(run.devSurvivors))
	for _, m := range run.devSurvivors {
		attempts = append(attempts,
			MutantAttempt{
				Path: run.rs.CodePath, MutantID: m.ID,
				Model: d.Assign[RoleTestWriter], Role: RoleTestWriter,
				Shadow: false, Outcome: outcome(killedByPrimary[m.ID]),
			},
			MutantAttempt{
				Path: run.rs.CodePath, MutantID: m.ID,
				Model: run.rs.ShadowWriterModel, Role: RoleTestWriterShadow,
				Shadow: true, Outcome: outcome(killedByShadow[m.ID]),
			},
		)
	}
	d.MutantAttempts.Record(v.RecordID, v.RecordHead, attempts)
}

// adjudicateCriticFindings builds the execution-checked adjudication for each
// of the test-critic's findings on this run. When the tests×mutants matrix
// ran (run.matrix != nil), each whole-test finding's adjudication is driven
// off the matrix's OWN execution-proven per-test row instead of a fresh
// single-test re-score — see matrixAdjudication. When the matrix did NOT run
// (matrix off, no Enumerator wired, or it skipped/failed), this falls back to
// the ORIGINAL single-test path, unchanged: for each critic finding scoped
// whole-test with a runnable single-test selector, re-run the JAIL's Scorer
// with THAT test alone against the run's own mutant set. If it kills at least
// one mutant, execution has proven the "can never fail" claim false —
// AutoAdjudication downgrades it to refuted. Either path: dead-check findings
// and anything the language plugin can't target as a single test are left
// unadjudicated; neither path ever auto-fails the audit — a scoring/jail
// error is logged and simply leaves that one finding unadjudicated.
func (d *Driver) adjudicateCriticFindings(ctx context.Context, missionID int64, run *runState, criticFindings []queue.Finding, v Verdict) []CriticFindingObservation {
	p := langFor(run.rs)
	var obs []CriticFindingObservation
	for _, f := range criticFindings {
		scope := NormalizeScope(f.Scope)
		var adjudication string
		if run.matrix != nil {
			adjudication = AdjUnadjudicated
			if scope == ScopeWholeTest && f.TestSelector != "" {
				if row := matrixRowFor(run.matrix.Rows, f.TestSelector); row != nil {
					adjudication = matrixAdjudication(*row, run.matrix.Catchable)
				}
			}
		} else {
			ran, kills := false, 0
			if scope == ScopeWholeTest && f.TestSelector != "" {
				if cmd, ok := p.SingleTestCmd(f.TestFile, f.TestSelector); ok {
					if kr, survivors, serr := d.Scorer.Score(ctx, run.rs.CodePath, run.rs.Code, run.rs.DevTestCode, run.mutants, strings.Join(cmd, " ")); serr != nil {
						log.Printf("advpool: run %d: critic auto-refute score failed for %q: %v", missionID, f.TestSelector, serr)
					} else if kr > 0 {
						// kr == 0 covers BOTH the baseline-couldn't-pass case
						// (adequacy.Score returns CompliantPass:false, Total:0,
						// Survived:nil, err:nil — see JailScorer.Score) and the
						// genuine-zero-kills case; either way there is no
						// execution-proven kill, so leave ran=false, kills=0 ->
						// AutoAdjudication yields unadjudicated (inconclusive),
						// never a false "refuted".
						ran, kills = true, len(run.mutants)-len(survivors)
					}
				}
			}
			adjudication = AutoAdjudication(scope, ran, kills)
		}
		obs = append(obs, CriticFindingObservation{
			QueueFindingID: f.ID,
			Model:          v.ModelsByRole[RoleTestCritic],
			TargetTest:     f.Target,
			TestFile:       f.TestFile,
			TestSelector:   f.TestSelector,
			Scope:          scope,
			Evidence:       f.Evidence,
			Severity:       f.Severity,
			Adjudication:   adjudication,
			Source:         "auto",
		})
	}
	return obs
}

// timeoutVerdict builds the signed needs-review verdict for a run that did
// not converge within RunDeadline. It uses whatever partial data was scored
// (dev kill-rate if the dev-adequacy step ran, else zero) and is forced to
// StatusNeedsReview — a timed-out run is NEVER certified, and (mirroring
// tickAggregate's leaderboard gate, which only fires on StatusCertified) it
// earns no leaderboard fitness for any model: a stalled run proved nothing.
func (d *Driver) timeoutVerdict(run *runState) Verdict {
	return Verdict{
		Repo:             run.rs.Repo,
		Commit:           run.rs.Commit,
		Lang:             run.rs.Lang,
		DevKillRate:      run.devKillRate,
		BaselineDuration: run.baselineDuration,
		MutantsTotal:     run.mutantsTotal,
		Survivors:        len(run.devSurvivors),
		ProvenMissed:     run.provenMissed,
		// The mutant-level evidence must ride the TIMEOUT verdict too, for the
		// same reason BaselineDuration and the could-not-grade flags do below:
		// a run that scored dev adequacy and only then stalled has real
		// per-mutant data, not zero values nothing ever computed.
		DevKilledMutants:   run.devKilled,
		DevSurvivedMutants: toMutantRefs(run.devSurvivors),
		// The could-not-grade flags must ride the TIMEOUT verdict too. A run
		// that scored dev adequacy, could not grade it (failed baseline, or a
		// surviving canary), and only then stalled would otherwise be SIGNED
		// reading "DevKillRate 0, Survivors 0, MutantsTotal N" with no marker —
		// renderAdvVerdict falls straight through to `dev_kill_rate: 0.00`,
		// which is a fabricated measurement. Never fabricate a score.
		BaselineFailed:   run.baselineFailed,
		BaselineOutput:   run.baselineOutput,
		SuiteIgnoresFile: run.suiteIgnoresFile,
		// TestWriterFailed/PoolTestUnsound must ride the TIMEOUT verdict too:
		// a run that reached tickPoolAdequacy, converged its pool score (or
		// gave up on a non-compiling test) and only THEN stalled (test-critic
		// never finished) would otherwise sign a TimedOut verdict with a
		// ProvenMissed that looks like an ordinary graded value, dropping the
		// caveat that explains it.
		TestWriterFailed:         run.testWriterFailed,
		PoolTestUnsound:          run.poolTestUnsound,
		AuthoredTestNotCollected: run.authoredTestNotCollected,
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
		TimedOut:       true,
		DevScored:      run.devScored,
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
