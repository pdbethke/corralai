// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/buildstore"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/repoindex"
)

func newTestQueue(t *testing.T) *queue.Store {
	t.Helper()
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = q.Close() })
	return q
}

// scoreCall records one Scorer.Score invocation for assertions.
type scoreCall struct {
	code, test string
	mutants    []adequacy.Mutant
}

// fakeScorer never runs a real jail: the first SUCCESSFUL call is assumed to
// be the dev score (mutant-generator's mutants against the dev's own tests),
// the second the pool score (survivors against the pool's authored test) —
// matching the order Tick's state machine actually calls Score in.
type fakeScorer struct {
	calls []scoreCall
	err   error
	// failFirstN, when > 0 (together with err), makes Score fail with err on
	// exactly the first failFirstN call ATTEMPTS (successful or not — this
	// counts total attempts), then succeed on every attempt after —
	// simulating a transient scorer failure (the sandboxed jail run this
	// interface wraps can fail transiently) so a re-entrancy test can drive a
	// Tick that errors once and then recovers, without failing forever. 0
	// (the default) preserves the original always-fail-if-err-set behavior.
	failFirstN int

	devKillRate  float64
	devSurvivors []adequacy.Mutant

	poolSurvivors []adequacy.Mutant

	// successes counts calls that did NOT return f.err — used (rather than
	// len(calls)) to decide dev-vs-pool branch, so a failed attempt (which
	// the driver treats as "nothing happened yet" and simply retries) does
	// not consume the dev-score slot a subsequent successful retry needs.
	successes int
}

func (f *fakeScorer) Score(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (float64, []adequacy.Mutant, error) {
	f.calls = append(f.calls, scoreCall{code, test, mutants})
	if f.err != nil && (f.failFirstN == 0 || len(f.calls) <= f.failFirstN) {
		return 0, nil, f.err
	}
	f.successes++
	if f.successes == 1 {
		return f.devKillRate, f.devSurvivors, nil
	}
	return 1.0, f.poolSurvivors, nil
}

type fakeValidator struct {
	mutants    []adequacy.Mutant
	parseErr   error
	compileErr error
	compileGot string // the test string CompileTest last received (post-ParseTest)
}

func (f *fakeValidator) ParseMutants(raw, _ string) ([]adequacy.Mutant, error) {
	return f.mutants, f.parseErr
}

// ParseTest strips a "RAW:" sentinel so a test can prove the driver cleans the
// worker's raw output (fences/prose) BEFORE compiling/scoring it.
func (f *fakeValidator) ParseTest(raw string) string {
	return strings.TrimPrefix(raw, "RAW:")
}

func (f *fakeValidator) CompileTest(ctx context.Context, codePath, code, test string) error {
	f.compileGot = test
	return f.compileErr
}

func decorrelatedAssign() RoleAssignment {
	return RoleAssignment{
		RoleMutantGenerator: "model-gen",
		RoleTestWriter:      "model-writer",
		RoleTestCritic:      "model-critic",
	}
}

// claimAllReady claims (as a generic bee) every currently-ready task and
// returns them indexed by key. Claiming one at a time via ClaimNext and
// checking for a specific key doesn't compose — a mismatched claim can't be
// released, so it would strand an unrelated task — so tests instead drain
// the whole ready set in one deterministic sweep and index by key.
func claimAllReady(t *testing.T, q *queue.Store) map[string]*queue.Task {
	t.Helper()
	out := map[string]*queue.Task{}
	for {
		task, err := q.ClaimNext("bee", nil, 300)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if task == nil {
			return out
		}
		out[task.Key] = task
	}
}

// claimByKey drains the ready set and returns the task with the given key,
// failing the test if it isn't among them.
func claimByKey(t *testing.T, q *queue.Store, key string) *queue.Task {
	t.Helper()
	ready := claimAllReady(t, q)
	task, ok := ready[key]
	if !ok {
		t.Fatalf("no claimable task found for key %q (ready were: %v)", key, keysOf(ready))
	}
	return task
}

func keysOf(m map[string]*queue.Task) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// claimTaskByID claims tasks until it wins the one identified by id — used
// for test-writer, whose live key drifts across SupersedeTask calls
// ("test-writer" -> "test-writer-r2" -> ...; see the note in
// tickDevAdequacy), so claiming by key is unreliable for it.
func claimTaskByID(t *testing.T, q *queue.Store, id int64) *queue.Task {
	t.Helper()
	for i := 0; i < 5; i++ {
		task, err := q.ClaimNext("bee", nil, 300)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if task == nil {
			t.Fatalf("no claimable task found for id %d", id)
		}
		if task.ID == id {
			return task
		}
	}
	t.Fatalf("could not claim task %d within attempts", id)
	return nil
}

func mustComplete(t *testing.T, q *queue.Store, id int64, result string) {
	t.Helper()
	ok, err := q.Complete(id, "bee", result)
	if err != nil || !ok {
		t.Fatalf("complete %d: ok=%v err=%v", id, ok, err)
	}
}

// newTestDriver wires a Driver over a fresh queue + the given fakes/threshold
// and starts a run for missionID, returning the driver and its RunSpec.
func newTestDriver(t *testing.T, missionID int64, scorer *fakeScorer, validator *fakeValidator, threshold float64) (*Driver, RunSpec) {
	t.Helper()
	q := newTestQueue(t)
	d, err := NewDriver(q, scorer, validator, decorrelatedAssign(), threshold)
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}
	rs := testRunSpec()
	if err := d.StartRun(missionID, rs, nil); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	// Enqueue leaves every task pending; flip the no-deps roles
	// (mutant-generator, test-critic) to ready before a test claims them.
	if _, err := q.PromoteReady(missionID); err != nil {
		t.Fatalf("PromoteReady: %v", err)
	}
	return d, rs
}

// (a) mutant-generator done -> Tick scores the dev's own tests and promotes
// test-writer, re-rendered with the real survivors.
func TestTick_DevAdequacy_PromotesTestWriterWithSurvivors(t *testing.T) {
	survivor := adequacy.Mutant{ID: "m1", Code: "SURVIVOR-MARKER-m1"}
	scorer := &fakeScorer{devKillRate: 0.9, devSurvivors: []adequacy.Mutant{survivor}}
	validator := &fakeValidator{mutants: []adequacy.Mutant{survivor, {ID: "m2", Code: "killed-m2"}}}
	d, _ := newTestDriver(t, 1, scorer, validator, 0.5)

	mg := claimByKey(t, d.Q, RoleMutantGenerator)
	mustComplete(t, d.Q, mg.ID, "raw mutant-generator output")

	v, err := d.Tick(context.Background(), 1)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if v != nil {
		t.Fatalf("expected no verdict yet (test-critic/test-writer not done), got %+v", v)
	}
	if len(scorer.calls) != 1 {
		t.Fatalf("expected Scorer.Score called once (dev tests), got %d calls", len(scorer.calls))
	}
	if scorer.calls[0].test != testRunSpec().DevTestCode {
		t.Fatalf("dev score call used test %q, want the dev's own test code", scorer.calls[0].test)
	}

	live, err := d.Q.TaskByID(d.runs[1].testWriterTaskID)
	if err != nil {
		t.Fatal(err)
	}
	if live == nil {
		t.Fatal("no live test-writer task after dev-adequacy scoring")
	}
	if live.Status != queue.StatusReady {
		t.Fatalf("test-writer status = %s, want ready (promoted)", live.Status)
	}
	if !strings.Contains(live.Instruction, "SURVIVOR-MARKER-m1") {
		t.Fatalf("test-writer instruction does not reference the real survivor:\n%s", live.Instruction)
	}
}

// (b) test-writer done -> Tick validates it compiles and scores it against
// the survivors, producing ProvenMissed.
func TestTick_PoolAdequacy_ScoresProvenMissed(t *testing.T) {
	survivors := []adequacy.Mutant{{ID: "m1", Code: "c1"}, {ID: "m2", Code: "c2"}}
	scorer := &fakeScorer{
		devKillRate:   0.5,
		devSurvivors:  survivors,
		poolSurvivors: []adequacy.Mutant{{ID: "m2", Code: "c2"}}, // pool test killed m1 but not m2
	}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m0", Code: "c0"}, survivors[0], survivors[1]}}
	d, _ := newTestDriver(t, 2, scorer, validator, 0.1)

	mg := claimByKey(t, d.Q, RoleMutantGenerator)
	mustComplete(t, d.Q, mg.ID, "raw mutants")
	if _, err := d.Tick(context.Background(), 2); err != nil {
		t.Fatalf("Tick (dev-adequacy): %v", err)
	}

	tw := claimTaskByID(t, d.Q, d.runs[2].testWriterTaskID)
	mustComplete(t, d.Q, tw.ID, "pool test source")

	v, err := d.Tick(context.Background(), 2)
	if err != nil {
		t.Fatalf("Tick (pool-adequacy): %v", err)
	}
	if v != nil {
		t.Fatalf("expected no verdict yet (test-critic not done), got %+v", v)
	}
	if len(scorer.calls) != 2 {
		t.Fatalf("expected 2 Scorer.Score calls (dev, pool), got %d", len(scorer.calls))
	}
	if scorer.calls[1].test != "pool test source" {
		t.Fatalf("pool score call used test %q, want the test-writer's result", scorer.calls[1].test)
	}
	if len(scorer.calls[1].mutants) != len(survivors) {
		t.Fatalf("pool score call used %d mutants, want the %d dev survivors", len(scorer.calls[1].mutants), len(survivors))
	}
	for i, m := range scorer.calls[1].mutants {
		if m.ID != survivors[i].ID {
			t.Fatalf("pool score call mutant[%d] = %q, want survivor %q (Score must be scored against the survivor set, not the full mutant list)", i, m.ID, survivors[i].ID)
		}
	}
	run := d.runs[2]
	if !run.poolScored {
		t.Fatal("expected poolScored=true")
	}
	if run.provenMissed != 1 { // 2 dev-survivors, pool killed 1 (only m2 still survives)
		t.Fatalf("ProvenMissed = %d, want 1", run.provenMissed)
	}
	// The compiling killing test is captured for hand-back and surfaced on
	// RunState (not the signed Verdict) so `corral certify --adversarial` can
	// return it to the dev.
	if run.authoredTest == "" {
		t.Fatal("expected authoredTest captured after pool-adequacy scoring")
	}
	if rs, ok := d.RunStatus(2); !ok || rs.AuthoredTest != run.authoredTest {
		t.Fatalf("RunState.AuthoredTest = %q, want %q (surfaced via RunStatus)", rs.AuthoredTest, run.authoredTest)
	}
}

// completeFullRun drives test-critic, mutant-generator, and test-writer to
// done and returns the final Verdict. mutant-generator and test-critic are
// both ready from the start, so they're claimed in one sweep; test-critic is
// completed first so the aggregate step's second precondition is already met
// by the time test-writer's completion satisfies the first.
func completeFullRun(t *testing.T, d *Driver, missionID int64, criticResult string) *Verdict {
	t.Helper()
	ctx := context.Background()

	ready := claimAllReady(t, d.Q)
	tc, mg := ready[RoleTestCritic], ready[RoleMutantGenerator]
	if tc == nil || mg == nil {
		t.Fatalf("expected test-critic and mutant-generator both ready, got: %v", keysOf(ready))
	}
	mustComplete(t, d.Q, tc.ID, criticResult)
	mustComplete(t, d.Q, mg.ID, "raw mutants")
	if _, err := d.Tick(ctx, missionID); err != nil {
		t.Fatalf("Tick (dev-adequacy): %v", err)
	}

	tw := claimTaskByID(t, d.Q, d.runs[missionID].testWriterTaskID)
	mustComplete(t, d.Q, tw.ID, "pool test source")
	v, err := d.Tick(ctx, missionID)
	if err != nil {
		t.Fatalf("Tick (pool-adequacy + aggregate): %v", err)
	}
	if v == nil {
		t.Fatal("expected a verdict once test-critic + pool-adequacy are both done")
	}
	return v
}

// (c) test-critic + pool-adequacy done, no blocking finding, DevKillRate
// above threshold -> certified.
func TestTick_Aggregate_Certified(t *testing.T) {
	survivors := []adequacy.Mutant{{ID: "m1", Code: "c1"}}
	scorer := &fakeScorer{devKillRate: 0.9, devSurvivors: survivors, poolSurvivors: nil}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m0", Code: "c0"}, survivors[0]}}
	d, _ := newTestDriver(t, 3, scorer, validator, 0.5)

	v := completeFullRun(t, d, 3, "no vacuous tests found")

	if v.Status != StatusCertified {
		t.Fatalf("Status = %q, want %q", v.Status, StatusCertified)
	}
	if v.DevKillRate != 0.9 {
		t.Fatalf("DevKillRate = %v, want 0.9", v.DevKillRate)
	}
	if v.ProvenMissed != 1 { // pool killed the one dev-survivor
		t.Fatalf("ProvenMissed = %d, want 1", v.ProvenMissed)
	}
	if v.Lang != "go" {
		t.Fatalf("Verdict.Lang = %q, want %q (from the run's RunSpec.Lang)", v.Lang, "go")
	}
}

// (d) a blocking finding open -> needs-review, even with a high DevKillRate.
// A test-critic finding is a SECOND MODEL'S UNVERIFIED opinion: it is carried on
// the verdict as advisory review but must NOT gate the signed record. With a
// kill-rate (0.95) at/above threshold (0.5), the run CERTIFIES despite the
// critic's flag — execution proves adequacy; an LLM opinion does not un-prove it.
func TestTick_CriticFindingIsAdvisory_CertifiesOnKillRate(t *testing.T) {
	survivors := []adequacy.Mutant{{ID: "m1", Code: "c1"}}
	scorer := &fakeScorer{devKillRate: 0.95, devSurvivors: survivors, poolSurvivors: nil}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m0", Code: "c0"}, survivors[0]}}
	d, _ := newTestDriver(t, 4, scorer, validator, 0.5)

	ctx := context.Background()

	// mutant-generator and test-critic are both ready from the start; claim
	// both in one sweep (claiming one at a time by key can't compose here —
	// see claimAllReady), file the blocking finding against test-critic, then
	// complete both.
	ready := claimAllReady(t, d.Q)
	tc, mg := ready[RoleTestCritic], ready[RoleMutantGenerator]
	if tc == nil || mg == nil {
		t.Fatalf("expected test-critic and mutant-generator both ready, got: %v", keysOf(ready))
	}
	if _, err := d.Q.AddFinding(queue.Finding{
		MissionID: 4, TaskID: tc.ID, Reporter: "test-critic", Type: "bug",
		Severity: "high", Target: "TestAlwaysPasses", Evidence: "vacuous — no assertions",
	}); err != nil {
		t.Fatalf("AddFinding: %v", err)
	}
	mustComplete(t, d.Q, tc.ID, "flagged TestAlwaysPasses as vacuous")
	mustComplete(t, d.Q, mg.ID, "raw mutants")
	if _, err := d.Tick(ctx, 4); err != nil {
		t.Fatalf("Tick (dev-adequacy): %v", err)
	}
	tw := claimTaskByID(t, d.Q, d.runs[4].testWriterTaskID)
	mustComplete(t, d.Q, tw.ID, "pool test source")

	v, err := d.Tick(ctx, 4)
	if err != nil {
		t.Fatalf("Tick (pool-adequacy + aggregate): %v", err)
	}
	if v == nil {
		t.Fatal("expected a verdict")
	}
	if v.Status != StatusCertified {
		t.Fatalf("Status = %q, want %q — a critic finding is UNVERIFIED review and must not gate; kill-rate 0.95 >= threshold 0.5 certifies", v.Status, StatusCertified)
	}
	if len(v.VacuousFindings) != 1 {
		t.Fatalf("VacuousFindings = %d, want 1 (the critic's review is still CARRIED as advisory, just not gating)", len(v.VacuousFindings))
	}
}

// (e) DevKillRate below threshold -> needs-review, even with no findings.
func TestTick_Aggregate_BelowThreshold_NeedsReview(t *testing.T) {
	survivors := []adequacy.Mutant{{ID: "m1", Code: "c1"}, {ID: "m2", Code: "c2"}}
	scorer := &fakeScorer{devKillRate: 0.2, devSurvivors: survivors, poolSurvivors: nil}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m0", Code: "c0"}, survivors[0], survivors[1]}}
	d, _ := newTestDriver(t, 5, scorer, validator, 0.8)

	v := completeFullRun(t, d, 5, "no vacuous tests found")

	if v.Status != StatusNeedsReview {
		t.Fatalf("Status = %q, want %q (DevKillRate 0.2 < threshold 0.8)", v.Status, StatusNeedsReview)
	}
}

// A needs-review verdict signs an evidence record but feeds NO leaderboard
// fitness (soundness #6: fitness only from CERTIFIED outcomes — a run parked
// for human review has not earned credit for any model yet).
func TestTick_Aggregate_NeedsReview_SignsButFeedsNoLeaderboard(t *testing.T) {
	survivors := []adequacy.Mutant{{ID: "m1", Code: "c1"}}
	scorer := &fakeScorer{devKillRate: 0.2, devSurvivors: survivors, poolSurvivors: nil}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m0", Code: "c0"}, survivors[0]}}
	d, _ := newTestDriver(t, 9, scorer, validator, 0.8)
	signer := &fakeSigner{}
	leaderboard := &fakeLeaderboard{}
	d.Signer = signer
	d.Leaderboard = leaderboard

	v := completeFullRun(t, d, 9, "no vacuous tests found")

	if v.Status != StatusNeedsReview {
		t.Fatalf("Status = %q, want %q", v.Status, StatusNeedsReview)
	}
	// The evidence record IS still signed (with needs-review status)...
	if len(signer.calls) != 1 || signer.calls[0].verdict.Status != StatusNeedsReview {
		t.Fatalf("expected one signed needs-review record, got %+v", signer.calls)
	}
	// ...but NO model earns fitness for a run the human still has to review.
	if len(leaderboard.calls) != 0 {
		t.Fatalf("needs-review run must not feed the leaderboard, got %d calls: %+v", len(leaderboard.calls), leaderboard.calls)
	}
}

// (f) decorrelation: an assignment where test-critic and test-writer share a
// model must be rejected at driver construction.
func TestNewDriver_RejectsDecorrelatedAssignment(t *testing.T) {
	q := newTestQueue(t)
	scorer := &fakeScorer{}
	validator := &fakeValidator{}
	assign := RoleAssignment{
		RoleMutantGenerator: "model-gen",
		RoleTestWriter:      "same-model",
		RoleTestCritic:      "same-model",
	}
	d, err := NewDriver(q, scorer, validator, assign, 0.5)
	if err == nil {
		t.Fatal("expected an error for test-critic == test-writer model, got nil")
	}
	if d != nil {
		t.Fatal("expected a nil Driver on decorrelation rejection")
	}
}

// (g) NewDriver must reject a non-positive threshold: DevKillRate < 0 (or
// < a threshold <=0) can never be true, so a caller passing threshold<=0
// would silently auto-certify every run regardless of suite strength.
func TestNewDriver_RejectsNonPositiveThreshold(t *testing.T) {
	q := newTestQueue(t)
	scorer := &fakeScorer{}
	validator := &fakeValidator{}

	for _, threshold := range []float64{0, -0.1, -1} {
		d, err := NewDriver(q, scorer, validator, decorrelatedAssign(), threshold)
		if err == nil {
			t.Fatalf("threshold=%v: expected an error, got nil", threshold)
		}
		if d != nil {
			t.Fatalf("threshold=%v: expected a nil Driver on rejection", threshold)
		}
	}
}

// (h) malformed mutant-generator output: ParseMutants failing must refuse
// the artifact — reopen mutant-generator for retry and never let it reach
// Scorer.Score (soundness #1: no malformed artifact flows into scoring).
func TestTick_DevAdequacy_ParseError_ReopensAndSkipsScore(t *testing.T) {
	scorer := &fakeScorer{devKillRate: 0.9}
	validator := &fakeValidator{parseErr: fmt.Errorf("malformed mutant output")}
	d, _ := newTestDriver(t, 6, scorer, validator, 0.5)

	mg := claimByKey(t, d.Q, RoleMutantGenerator)
	mustComplete(t, d.Q, mg.ID, "garbage, not valid mutant output")

	v, err := d.Tick(context.Background(), 6)
	if err == nil {
		t.Fatal("expected Tick to return an error on unparseable mutant output")
	}
	if v != nil {
		t.Fatalf("expected no verdict on a refused artifact, got %+v", v)
	}
	if len(scorer.calls) != 0 {
		t.Fatalf("expected Scorer.Score NOT called on a malformed artifact, got %d calls", len(scorer.calls))
	}

	reopened, terr := d.Q.TaskByID(mg.ID)
	if terr != nil {
		t.Fatal(terr)
	}
	if reopened == nil {
		t.Fatal("mutant-generator task vanished after reopen")
	}
	if reopened.Status != queue.StatusReady {
		t.Fatalf("mutant-generator status = %s, want ready (reopened for retry)", reopened.Status)
	}
}

// (i) non-compiling test-writer output: CompileTest failing must refuse the
// artifact — reopen test-writer for retry and never let it reach the pool
// Scorer.Score call.
func TestTick_PoolAdequacy_CompileError_ReopensAndSkipsScore(t *testing.T) {
	survivors := []adequacy.Mutant{{ID: "m1", Code: "c1"}, {ID: "m2", Code: "c2"}}
	scorer := &fakeScorer{devKillRate: 0.5, devSurvivors: survivors}
	validator := &fakeValidator{
		mutants:    []adequacy.Mutant{{ID: "m0", Code: "c0"}, survivors[0], survivors[1]},
		compileErr: fmt.Errorf("syntax error in generated test"),
	}
	d, _ := newTestDriver(t, 7, scorer, validator, 0.1)

	mg := claimByKey(t, d.Q, RoleMutantGenerator)
	mustComplete(t, d.Q, mg.ID, "raw mutants")
	if _, err := d.Tick(context.Background(), 7); err != nil {
		t.Fatalf("Tick (dev-adequacy): %v", err)
	}
	if len(scorer.calls) != 1 {
		t.Fatalf("expected 1 Scorer.Score call after dev-adequacy, got %d", len(scorer.calls))
	}

	tw := claimTaskByID(t, d.Q, d.runs[7].testWriterTaskID)
	mustComplete(t, d.Q, tw.ID, "test that does not compile")

	v, err := d.Tick(context.Background(), 7)
	if err == nil {
		t.Fatal("expected Tick to return an error on a non-compiling test")
	}
	if v != nil {
		t.Fatalf("expected no verdict on a refused artifact, got %+v", v)
	}
	if len(scorer.calls) != 1 {
		t.Fatalf("expected pool Scorer.Score NOT called on a non-compiling artifact, still want 1 call, got %d", len(scorer.calls))
	}

	reopened, terr := d.Q.TaskByID(tw.ID)
	if terr != nil {
		t.Fatal(terr)
	}
	if reopened == nil {
		t.Fatal("test-writer task vanished after reopen")
	}
	if reopened.Status != queue.StatusReady {
		t.Fatalf("test-writer status = %s, want ready (reopened for retry)", reopened.Status)
	}
}

// signCall records one Signer.SignVerdict invocation for assertions.
type signCall struct {
	verdict Verdict
}

// fakeSigner never touches the real certify chain: it just records what it
// was asked to sign and returns a fixed canned (recordID, head), or an error.
type fakeSigner struct {
	calls []signCall
	err   error
}

func (f *fakeSigner) SignVerdict(ctx context.Context, v Verdict) (int64, string, error) {
	f.calls = append(f.calls, signCall{verdict: v})
	if f.err != nil {
		return 0, "", f.err
	}
	return 41, "head41", nil
}

// leaderboardCall records one LeaderboardSink.Record invocation.
type leaderboardCall struct {
	model, role, outcome string
}

type fakeLeaderboard struct {
	calls []leaderboardCall
}

func (f *fakeLeaderboard) Record(model, role, outcome string) {
	f.calls = append(f.calls, leaderboardCall{model, role, outcome})
}

// (j) a certified verdict: SignVerdict is called with ModelsByRole
// populated, and (only after that succeeds) the leaderboard is fed
// (model, role, outcome) for all three roles — soundness #5: the fitness
// feed never runs before the deterministic gate has scored and signed.
func TestTick_Aggregate_Certified_SignsAndFeedsLeaderboard(t *testing.T) {
	survivors := []adequacy.Mutant{{ID: "m1", Code: "c1"}}
	scorer := &fakeScorer{devKillRate: 0.9, devSurvivors: survivors, poolSurvivors: nil}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m0", Code: "c0"}, survivors[0]}}
	d, _ := newTestDriver(t, 8, scorer, validator, 0.5)
	signer := &fakeSigner{}
	leaderboard := &fakeLeaderboard{}
	d.Signer = signer
	d.Leaderboard = leaderboard

	v := completeFullRun(t, d, 8, "no vacuous tests found")

	if v.Status != StatusCertified {
		t.Fatalf("Status = %q, want %q", v.Status, StatusCertified)
	}

	if len(signer.calls) != 1 {
		t.Fatalf("expected SignVerdict called once, got %d calls", len(signer.calls))
	}
	signed := signer.calls[0].verdict
	if signed.Status != StatusCertified {
		t.Fatalf("signed verdict Status = %q, want %q", signed.Status, StatusCertified)
	}
	if len(signed.ModelsByRole) == 0 {
		t.Fatal("expected SignVerdict to be called with ModelsByRole populated")
	}
	for _, role := range []string{RoleMutantGenerator, RoleTestWriter, RoleTestCritic} {
		if signed.ModelsByRole[role] == "" {
			t.Fatalf("signed verdict ModelsByRole[%q] is empty", role)
		}
	}

	if len(leaderboard.calls) != 3 {
		t.Fatalf("expected 3 leaderboard.Record calls (one per role), got %d: %+v", len(leaderboard.calls), leaderboard.calls)
	}
	seenRoles := map[string]bool{}
	for _, c := range leaderboard.calls {
		seenRoles[c.role] = true
		if c.model == "" {
			t.Fatalf("leaderboard.Record called with empty model for role %q", c.role)
		}
		if c.outcome != OutcomePass && c.outcome != OutcomeFail {
			t.Fatalf("leaderboard.Record called with unexpected outcome %q for role %q", c.outcome, c.role)
		}
	}
	for _, role := range []string{RoleMutantGenerator, RoleTestWriter, RoleTestCritic} {
		if !seenRoles[role] {
			t.Fatalf("leaderboard was never fed for role %q", role)
		}
	}
	// test-writer's authored test killed the one dev-survivor (ProvenMissed=1
	// per TestTick_Aggregate_Certified) -> its outcome must be a pass.
	for _, c := range leaderboard.calls {
		if c.role == RoleTestWriter && c.outcome != OutcomePass {
			t.Fatalf("test-writer outcome = %q, want %q (ProvenMissed=1)", c.outcome, OutcomePass)
		}
	}
}

// The signed record id/head (from Signer.SignVerdict) must land on the
// stored Verdict — Task 2's RunStatus and Task 4's advVerdict both read
// these fields off the converged verdict.
func TestVerdictCarriesSignedRecordID(t *testing.T) {
	survivors := []adequacy.Mutant{{ID: "m1", Code: "c1"}}
	scorer := &fakeScorer{devKillRate: 0.9, devSurvivors: survivors, poolSurvivors: nil}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m0", Code: "c0"}, survivors[0]}}
	d, _ := newTestDriver(t, 11, scorer, validator, 0.5)
	d.Signer = &fakeSigner{}

	v := completeFullRun(t, d, 11, "no vacuous tests found")

	if v.RecordID != 41 {
		t.Fatalf("RecordID = %d, want 41 (from the fake Signer)", v.RecordID)
	}
	if v.RecordHead != "head41" {
		t.Fatalf("RecordHead = %q, want head41", v.RecordHead)
	}
}

// (k) a needs-review verdict (below-threshold DevKillRate, no blocking
// finding): the driver must not auto-sign a "certified" record — it may
// still sign a needs-review one, but the signed verdict's Status must never
// read "certified" on a run that didn't clear the human gate.
func TestTick_Aggregate_NeedsReview_NeverSignsCertified(t *testing.T) {
	survivors := []adequacy.Mutant{{ID: "m1", Code: "c1"}, {ID: "m2", Code: "c2"}}
	scorer := &fakeScorer{devKillRate: 0.2, devSurvivors: survivors, poolSurvivors: nil}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m0", Code: "c0"}, survivors[0], survivors[1]}}
	d, _ := newTestDriver(t, 9, scorer, validator, 0.8)
	signer := &fakeSigner{}
	leaderboard := &fakeLeaderboard{}
	d.Signer = signer
	d.Leaderboard = leaderboard

	v := completeFullRun(t, d, 9, "no vacuous tests found")

	if v.Status != StatusNeedsReview {
		t.Fatalf("Status = %q, want %q (DevKillRate 0.2 < threshold 0.8)", v.Status, StatusNeedsReview)
	}
	for _, c := range signer.calls {
		if c.verdict.Status == StatusCertified {
			t.Fatalf("a needs-review run must never sign a %q-status record, got one: %+v", StatusCertified, c.verdict)
		}
	}
	// The needs-review record itself may still be signed...
	if len(signer.calls) != 1 {
		t.Fatalf("expected SignVerdict called once (a needs-review record), got %d calls", len(signer.calls))
	}
	if signer.calls[0].verdict.Status != StatusNeedsReview {
		t.Fatalf("signed verdict Status = %q, want %q", signer.calls[0].verdict.Status, StatusNeedsReview)
	}
}

// (l) SignVerdict failing: the driver must not feed the leaderboard from an
// unsigned verdict (soundness #5), and must leave the run non-terminal so a
// later Tick can retry the (idempotent) aggregate+sign sequence.
func TestTick_Aggregate_SignFails_SkipsLeaderboardAndRetries(t *testing.T) {
	survivors := []adequacy.Mutant{{ID: "m1", Code: "c1"}}
	scorer := &fakeScorer{devKillRate: 0.9, devSurvivors: survivors, poolSurvivors: nil}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m0", Code: "c0"}, survivors[0]}}
	d, _ := newTestDriver(t, 10, scorer, validator, 0.5)
	signer := &fakeSigner{err: fmt.Errorf("certify chain unavailable")}
	leaderboard := &fakeLeaderboard{}
	d.Signer = signer
	d.Leaderboard = leaderboard

	ctx := context.Background()
	ready := claimAllReady(t, d.Q)
	tc, mg := ready[RoleTestCritic], ready[RoleMutantGenerator]
	mustComplete(t, d.Q, tc.ID, "no vacuous tests found")
	mustComplete(t, d.Q, mg.ID, "raw mutants")
	if _, err := d.Tick(ctx, 10); err != nil {
		t.Fatalf("Tick (dev-adequacy): %v", err)
	}
	tw := claimTaskByID(t, d.Q, d.runs[10].testWriterTaskID)
	mustComplete(t, d.Q, tw.ID, "pool test source")

	v, err := d.Tick(ctx, 10)
	if err == nil {
		t.Fatal("expected Tick to return an error when SignVerdict fails")
	}
	if v != nil {
		t.Fatalf("expected no verdict returned on a sign failure, got %+v", v)
	}
	if len(leaderboard.calls) != 0 {
		t.Fatalf("expected the leaderboard NOT fed when SignVerdict fails, got %d calls", len(leaderboard.calls))
	}
	if d.runs[10].verdict != nil {
		t.Fatal("expected run.verdict left unset after a sign failure, so a later Tick retries")
	}
}

// eventCall records one EventSink.Emit invocation for assertions.
type eventCall struct {
	mid     int64
	kind    string
	subject string
	detail  map[string]any
}

// fakeEventSink never touches real telemetry: it just captures what it was
// asked to emit, in order, for assertions.
type fakeEventSink struct {
	events []eventCall
}

func (f *fakeEventSink) Emit(missionID int64, kind, subject string, detail map[string]any) {
	f.events = append(f.events, eventCall{missionID, kind, subject, detail})
}

// TestDriverEmitsReasoningEvents drives a full run to convergence with an
// EventSink wired and asserts the three reasoning-milestone events fire with
// the expected detail keys.
func TestDriverEmitsReasoningEvents(t *testing.T) {
	const mission int64 = 88
	survivors := []adequacy.Mutant{{ID: "m1", Code: "c1"}}
	scorer := &fakeScorer{devKillRate: 0.9, devSurvivors: survivors, poolSurvivors: nil}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m0", Code: "c0"}, survivors[0]}}
	q := newTestQueue(t)
	d, err := NewDriver(q, scorer, validator, decorrelatedAssign(), 0.5)
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}
	sink := &fakeEventSink{}
	d.Events = sink
	d.Signer = &fakeSigner{}
	if err := d.StartRun(mission, testRunSpec(), nil); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if _, err := q.PromoteReady(mission); err != nil {
		t.Fatalf("PromoteReady: %v", err)
	}

	completeFullRun(t, d, mission, "no vacuous tests found")

	kinds := map[string]map[string]any{}
	for _, e := range sink.events {
		if e.mid != mission {
			t.Fatalf("event %q emitted with missionID %d, want %d", e.kind, e.mid, mission)
		}
		kinds[e.kind] = e.detail
	}
	for _, want := range []string{"pool_subject", "pool_dev_adequacy", "pool_verdict"} {
		if _, ok := kinds[want]; !ok {
			t.Fatalf("missing emit %q; got %v", want, keysOf2(kinds))
		}
	}
	if kinds["pool_subject"]["dev_test_code"] == nil {
		t.Fatal("pool_subject must carry the dev tests (the subject)")
	}
	if kinds["pool_subject"]["goal"] == nil || kinds["pool_subject"]["code"] == nil ||
		kinds["pool_subject"]["code_path"] == nil || kinds["pool_subject"]["dev_test_path"] == nil {
		t.Fatalf("pool_subject detail incomplete: %v", kinds["pool_subject"])
	}
	da := kinds["pool_dev_adequacy"]
	if da["dev_kill_rate"] == nil || da["mutants_total"] == nil || da["survivors"] == nil || da["survivor_ids"] == nil {
		t.Fatalf("pool_dev_adequacy detail incomplete: %v", da)
	}
	ids, ok := da["survivor_ids"].([]string)
	if !ok || len(ids) != 1 || ids[0] != "m1" {
		t.Fatalf("pool_dev_adequacy survivor_ids = %v, want [\"m1\"]", da["survivor_ids"])
	}
	verdict := kinds["pool_verdict"]
	if verdict["status"] == nil || verdict["dev_kill_rate"] == nil || verdict["mutants_total"] == nil ||
		verdict["survivors"] == nil || verdict["proven_missed"] == nil || verdict["models_by_role"] == nil ||
		verdict["record_id"] == nil || verdict["record_head"] == nil {
		t.Fatalf("pool_verdict detail incomplete: %v", verdict)
	}
	if verdict["status"] != StatusCertified {
		t.Fatalf("pool_verdict status = %v, want %q", verdict["status"], StatusCertified)
	}
}

// keysOf2 returns the keys of a map[string]map[string]any — a second helper
// distinct from the file's keysOf(map[string]*queue.Task) since Go generics
// aren't used here.
func keysOf2(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestDriverNilEventSinkNoop proves a Driver with Events left nil (the
// default — every pre-existing test) drives a full run to convergence
// without panicking: the emit helper must no-op cleanly.
func TestDriverNilEventSinkNoop(t *testing.T) {
	const mission int64 = 89
	survivors := []adequacy.Mutant{{ID: "m1", Code: "c1"}}
	scorer := &fakeScorer{devKillRate: 0.9, devSurvivors: survivors, poolSurvivors: nil}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m0", Code: "c0"}, survivors[0]}}
	d, _ := newTestDriver(t, mission, scorer, validator, 0.5)
	// d.Events left nil deliberately.

	v := completeFullRun(t, d, mission, "no vacuous tests found")
	if v.Status != StatusCertified {
		t.Fatalf("Status = %q, want %q", v.Status, StatusCertified)
	}
}

func TestCheckDecorrelation(t *testing.T) {
	if err := CheckDecorrelation(decorrelatedAssign()); err != nil {
		t.Fatalf("expected a properly decorrelated assignment to pass, got %v", err)
	}
	same := RoleAssignment{RoleTestWriter: "x", RoleTestCritic: "x"}
	if err := CheckDecorrelation(same); err == nil {
		t.Fatal("expected an error when test-writer and test-critic share a model")
	}
	// Both empty (no assignment yet) must not false-positive.
	if err := CheckDecorrelation(RoleAssignment{}); err != nil {
		t.Fatalf("expected an empty assignment to pass, got %v", err)
	}
}

// The driver must clean the test-writer's RAW output (fences/prose) via
// ParseTest BEFORE compiling/scoring it — otherwise a ```go-fenced test never
// compiles (the bug the live e2e exercise surfaced).
func TestTick_PoolAdequacy_StripsRawTestBeforeCompile(t *testing.T) {
	const mission int64 = 33
	survivors := []adequacy.Mutant{{ID: "m1", Code: "c1"}}
	scorer := &fakeScorer{devKillRate: 0.9, devSurvivors: survivors, poolSurvivors: nil}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m0", Code: "c0"}, survivors[0]}}
	d, _ := newTestDriver(t, mission, scorer, validator, 0.5)

	ctx := context.Background()
	ready := claimAllReady(t, d.Q)
	mustComplete(t, d.Q, ready[RoleTestCritic].ID, "no vacuous tests found")
	mustComplete(t, d.Q, ready[RoleMutantGenerator].ID, "raw mutants")
	if _, err := d.Tick(ctx, mission); err != nil {
		t.Fatalf("Tick (dev-adequacy): %v", err)
	}
	// Complete the test-writer with a RAW (fenced-style) artifact.
	tw := claimTaskByID(t, d.Q, d.runs[mission].testWriterTaskID)
	mustComplete(t, d.Q, tw.ID, "RAW:func TestX(t *testing.T){}")
	if _, err := d.Tick(ctx, mission); err != nil {
		t.Fatalf("Tick (pool-adequacy): %v", err)
	}
	if validator.compileGot != "func TestX(t *testing.T){}" {
		t.Errorf("CompileTest received %q, want the ParseTest-cleaned source (RAW: prefix stripped)", validator.compileGot)
	}
}

// A PERFECT dev suite (0 survivors) must skip the test-writer + pool-adequacy
// entirely and certify on its 100% kill-rate — without this the pool could
// grade a bad suite but never certify a good one (the real-repo run's finding).
func TestTick_PerfectSuite_SkipsTestWriterAndCertifies(t *testing.T) {
	const mission int64 = 77
	scorer := &fakeScorer{devKillRate: 1.0, devSurvivors: nil} // killed every mutant, 0 survivors
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m0", Code: "c0"}, {ID: "m1", Code: "c1"}}}
	d, _ := newTestDriver(t, mission, scorer, validator, 0.8)

	ctx := context.Background()
	ready := claimAllReady(t, d.Q)
	mustComplete(t, d.Q, ready[RoleTestCritic].ID, "no vacuous tests found")
	mustComplete(t, d.Q, ready[RoleMutantGenerator].ID, "raw mutants")

	// One tick: dev-adequacy sees 0 survivors -> skips test-writer, poolScored,
	// then aggregate (test-critic already done) -> certified.
	v, err := d.Tick(ctx, mission)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if v == nil {
		t.Fatal("a perfect suite must converge without a test-writer completion, got no verdict")
	}
	if v.Status != StatusCertified {
		t.Errorf("Status = %q, want certified (100%% kill-rate)", v.Status)
	}
	if v.Survivors != 0 || v.ProvenMissed != 0 {
		t.Errorf("Survivors=%d ProvenMissed=%d, want 0/0", v.Survivors, v.ProvenMissed)
	}
	// The moot test-writer task must be cancelled, not left pending or promoted.
	tw, _ := d.Q.TaskByID(d.runs[mission].testWriterTaskID)
	if tw != nil && tw.Status != queue.StatusCancelled {
		t.Errorf("test-writer status = %q, want cancelled (no survivors to prove)", tw.Status)
	}
	// Scorer.Score called exactly once (dev tests) — never a second pool score.
	if len(scorer.calls) != 1 {
		t.Errorf("Scorer.Score called %d times, want 1 (no pool-adequacy for a perfect suite)", len(scorer.calls))
	}
}

// A perfect suite (0 survivors) skips the test-writer — so its assigned model
// must NOT be recorded on the leaderboard (it never ran); recording it a
// failure for a task it never attempted would penalize a good model on exactly
// the strong-suite runs. The mutant-generator IS fed, as a pass (it produced
// usable mutants; the suite killing them is not its failure).
func TestTick_PerfectSuite_DoesNotPenalizeMootTestWriter(t *testing.T) {
	const mission int64 = 78
	scorer := &fakeScorer{devKillRate: 1.0, devSurvivors: nil}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m0", Code: "c0"}, {ID: "m1", Code: "c1"}}}
	d, _ := newTestDriver(t, mission, scorer, validator, 0.8)
	d.Signer = &fakeSigner{}
	lb := &fakeLeaderboard{}
	d.Leaderboard = lb

	ctx := context.Background()
	ready := claimAllReady(t, d.Q)
	mustComplete(t, d.Q, ready[RoleTestCritic].ID, "no vacuous tests found")
	mustComplete(t, d.Q, ready[RoleMutantGenerator].ID, "raw mutants")

	v, err := d.Tick(ctx, mission)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if v == nil || v.Status != StatusCertified {
		t.Fatalf("want certified, got %+v", v)
	}
	var mgOutcome string
	for _, c := range lb.calls {
		if c.role == RoleTestWriter {
			t.Errorf("test-writer was fed to the leaderboard (%q) despite never running on a perfect suite", c.outcome)
		}
		if c.role == RoleMutantGenerator {
			mgOutcome = c.outcome
		}
	}
	if mgOutcome != OutcomePass {
		t.Errorf("mutant-generator outcome = %q, want pass (it produced usable mutants; a good suite killing them is not its failure)", mgOutcome)
	}
}

// The human gate still applies at a PERFECT (100%) kill-rate: a blocking finding
// from the test-critic routes even a 0-survivor run to needs-review.
// The more-itertools case, distilled: a perfect suite (100% kill-rate) with a
// critic flag CERTIFIES — the flag is a second model's unverified opinion (the
// one that once hallucinated islice raising nothing on a negative index), and a
// tamper-evident record asserts only what execution proves.
func TestTick_PerfectSuite_CriticFlagIsAdvisory_Certifies(t *testing.T) {
	const mission int64 = 79
	scorer := &fakeScorer{devKillRate: 1.0, devSurvivors: nil}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m0", Code: "c0"}, {ID: "m1", Code: "c1"}}}
	d, _ := newTestDriver(t, mission, scorer, validator, 0.8)

	ctx := context.Background()
	ready := claimAllReady(t, d.Q)
	tc := ready[RoleTestCritic]
	if _, err := d.Q.AddFinding(queue.Finding{
		MissionID: mission, TaskID: tc.ID, Reporter: "test-critic", Type: "bug",
		Severity: "high", Target: "SomeTest", Evidence: "vacuous — no assertions",
	}); err != nil {
		t.Fatalf("AddFinding: %v", err)
	}
	mustComplete(t, d.Q, tc.ID, "flagged a vacuous test")
	mustComplete(t, d.Q, ready[RoleMutantGenerator].ID, "raw mutants")

	v, err := d.Tick(ctx, mission)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if v == nil || v.Status != StatusCertified {
		t.Fatalf("Status = %v, want certified — a 100%% kill-rate certifies; a critic's UNVERIFIED flag is advisory, not a gate (the islice-hallucination lesson)", v)
	}
	if len(v.VacuousFindings) != 1 {
		t.Fatalf("VacuousFindings = %d, want 1 (advisory review carried, not gating)", len(v.VacuousFindings))
	}
}

// RunStatus must report unknown/mid-run/converged correctly: not-found for an
// id that was never started, found-but-not-converged mid-run, and
// found-and-converged (with the real Verdict) once completeFullRun finishes.
func TestRunStatusUnknownRunningConverged(t *testing.T) {
	const mission int64 = 7
	survivors := []adequacy.Mutant{{ID: "m1", Code: "c1"}}
	scorer := &fakeScorer{devKillRate: 0.9, devSurvivors: survivors, poolSurvivors: nil}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m0", Code: "c0"}, survivors[0]}}
	d, _ := newTestDriver(t, mission, scorer, validator, 0.5)

	if st, found := d.RunStatus(999); found || st.Converged {
		t.Fatalf("unknown id: found=%v converged=%v, want false/false", found, st.Converged)
	}
	if st, found := d.RunStatus(mission); !found || st.Converged || st.Verdict != nil {
		t.Fatalf("mid-run: found=%v converged=%v verdict=%v, want true/false/nil", found, st.Converged, st.Verdict)
	}

	v := completeFullRun(t, d, mission, "no vacuous tests found")

	st, found := d.RunStatus(mission)
	if !found || !st.Converged || st.Verdict == nil {
		t.Fatalf("converged: found=%v converged=%v verdict=%v, want true/true/non-nil", found, st.Converged, st.Verdict)
	}
	if st.Verdict.Status != v.Status {
		t.Fatalf("Verdict.Status = %q, want %q", st.Verdict.Status, v.Status)
	}
}

// TestRunStatusRaceWithTick proves RunStatus is safe to call concurrently with
// Tick — run the package with -race to catch an unsynchronized run.verdict or
// map access. Poll RunStatus in a goroutine while the main goroutine drives the
// run's ticks to convergence.
func TestRunStatusRaceWithTick(t *testing.T) {
	const mission int64 = 7
	survivors := []adequacy.Mutant{{ID: "m1", Code: "c1"}}
	scorer := &fakeScorer{devKillRate: 0.9, devSurvivors: survivors, poolSurvivors: nil}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m0", Code: "c0"}, survivors[0]}}
	d, _ := newTestDriver(t, mission, scorer, validator, 0.5)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			_, _ = d.RunStatus(mission)
		}
	}()

	completeFullRun(t, d, mission, "no vacuous tests found")

	<-done
}

// fakeClock is an injected, manually-advanced clock: the deadline logic must
// never call time.Now() directly (the driver/store convention), so tests can
// simulate a stalled run's wall-clock without sleeping.
type fakeClock struct {
	t time.Time
}

func (c *fakeClock) Now() time.Time { return c.t }

func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// TestRunDeadlineProducesNeedsReviewVerdict proves the wall-clock deadline is
// the backstop checkNoProgress can't be: checkNoProgress explicitly stands
// down while any task is claimed ("slow is not stuck"), so a claimed-but-
// wedged task would otherwise stall a run forever. Here the run is started
// but never completes any task (simulating a stall); once the injected clock
// crosses RunDeadline, Tick must converge to a SIGNED needs-review verdict —
// never certified, honest about the timeout — and a second Tick must return
// the same stored verdict (idempotent, no double-sign).
func TestRunDeadlineProducesNeedsReviewVerdict(t *testing.T) {
	const mission int64 = 42
	scorer := &fakeScorer{devKillRate: 0.9}
	validator := &fakeValidator{}
	q := newTestQueue(t)
	clk := &fakeClock{t: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)}
	d, err := NewDriver(q, scorer, validator, decorrelatedAssign(), 0.5)
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}
	d.Now = clk.Now
	d.RunDeadline = 10 * time.Minute
	signer := &fakeSigner{}
	d.Signer = signer
	leaderboard := &fakeLeaderboard{}
	d.Leaderboard = leaderboard
	sink := &fakeEventSink{}
	d.Events = sink

	if err := d.StartRun(mission, testRunSpec(), nil); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	// Simulate a stall: do NOT complete any task. Tick before the deadline must
	// not converge.
	if v, err := d.Tick(context.Background(), mission); err != nil || v != nil {
		t.Fatalf("pre-deadline Tick: v=%+v err=%v, want nil/nil", v, err)
	}

	clk.advance(d.RunDeadline + time.Second)

	v, err := d.Tick(context.Background(), mission)
	if err != nil {
		t.Fatalf("deadline Tick should not error: %v", err)
	}
	if v == nil || v.Status != StatusNeedsReview {
		t.Fatalf("want a needs-review verdict, got %+v", v)
	}
	if len(signer.calls) != 1 || signer.calls[0].verdict.Status != StatusNeedsReview {
		t.Fatalf("expected one signed needs-review record, got %+v", signer.calls)
	}
	if len(leaderboard.calls) != 0 {
		t.Fatalf("a timed-out run must never feed the leaderboard, got %d calls", len(leaderboard.calls))
	}

	// The replay trace must not lose the verdict beat just because the run
	// timed out instead of aggregating normally: pool_verdict must still fire.
	var found *eventCall
	for i := range sink.events {
		if sink.events[i].kind == "pool_verdict" {
			found = &sink.events[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected a pool_verdict event on the timeout path, got events: %+v", sink.events)
	}
	if found.detail["status"] != StatusNeedsReview {
		t.Fatalf("pool_verdict status = %v, want %q", found.detail["status"], StatusNeedsReview)
	}

	// A second Tick must be idempotent: same stored verdict pointer, no re-sign.
	v2, err := d.Tick(context.Background(), mission)
	if err != nil {
		t.Fatalf("second Tick: %v", err)
	}
	if v2 != v {
		t.Fatalf("second Tick returned a different verdict: %+v vs %+v", v, v2)
	}
	if len(signer.calls) != 1 {
		t.Fatalf("second Tick must not re-sign, got %d signer calls", len(signer.calls))
	}
}

// TestAggregateExcludesOpsFindings covers Task 3: an operational
// (model-unreachable) finding filed against the critic's task must never
// count as the critic's judgment and must never block certification — only
// a real audit finding does either.
func TestAggregateExcludesOpsFindings(t *testing.T) {
	ops := queue.Finding{TaskID: 3, Type: "ops", Severity: "high", Status: queue.FindingOpen}
	real := queue.Finding{TaskID: 3, Type: "note", Severity: "high", Status: queue.FindingOpen, Target: "TestX", Evidence: "vacuous"}

	d := &Driver{BlockSeverity: "high"}
	if d.blockingFindingOpen([]queue.Finding{ops}) {
		t.Fatal("an operational ops finding must NOT block certification")
	}
	if !d.blockingFindingOpen([]queue.Finding{real}) {
		t.Fatal("a real high finding must still block")
	}

	got := filterCriticFindings([]queue.Finding{ops, real}, 3)
	if len(got) != 1 || got[0].Type != "note" {
		t.Fatalf("ops finding must be excluded from critic findings, got %+v", got)
	}
}

// --- BugCatchSink (Task 2) ---

// fakeBugCatch records the (recordID, obs) it was fed.
type fakeBugCatch struct {
	recordID int64
	obs      []BugCatchObservation
}

func (f *fakeBugCatch) Record(recordID int64, _ string, obs []BugCatchObservation) {
	f.recordID = recordID
	f.obs = append(f.obs, obs...)
}

func obsFor(obs []BugCatchObservation, role string) (BugCatchObservation, bool) {
	for _, o := range obs {
		if o.Role == role {
			return o, true
		}
	}
	return BugCatchObservation{}, false
}

// scoredRun is the shape of a run's scored outcome, used by newScoredRun to
// build a driver whose dev-adequacy/pool-adequacy scoring lands on exactly
// these numbers once driven to convergence.
type scoredRun struct {
	survivors, provenMissed, mutantsTotal, vacuous int
	writerModel, criticModel                       string
}

// scoredRunVacuous stashes each newScoredRun's vacuous-finding count, keyed
// by missionID, so drivePoolToConvergence (which has no other way to learn
// it — the Driver/runState carry no such field) can file that many findings
// against the test-critic task before completing it.
var scoredRunVacuous = map[int64]int{}

// newScoredRun wires a Driver (fakeScorer/fakeValidator, matching the
// existing convergence tests' harness) whose dev-adequacy/pool-adequacy
// scoring will produce cfg's survivors/provenMissed/mutantsTotal once driven
// through drivePoolToConvergence, with the given per-role models assigned.
var scoredRunNextMissionID int64 = 1000

func newScoredRun(t *testing.T, cfg scoredRun) (*Driver, int64) {
	t.Helper()
	missionID := scoredRunNextMissionID
	scoredRunNextMissionID++
	scoredRunVacuous[missionID] = cfg.vacuous

	devSurvivors := make([]adequacy.Mutant, cfg.survivors)
	for i := range devSurvivors {
		devSurvivors[i] = adequacy.Mutant{ID: fmt.Sprintf("survivor%d", i+1), Code: fmt.Sprintf("s%d", i+1)}
	}
	poolSurvivorCount := cfg.survivors - cfg.provenMissed
	if poolSurvivorCount < 0 {
		t.Fatalf("scoredRun: provenMissed %d must not exceed survivors %d", cfg.provenMissed, cfg.survivors)
	}
	poolSurvivors := append([]adequacy.Mutant(nil), devSurvivors[:poolSurvivorCount]...)

	scorer := &fakeScorer{devKillRate: 0.5, devSurvivors: devSurvivors, poolSurvivors: poolSurvivors}

	mutants := make([]adequacy.Mutant, cfg.mutantsTotal)
	copy(mutants, devSurvivors)
	for i := len(devSurvivors); i < cfg.mutantsTotal; i++ {
		mutants[i] = adequacy.Mutant{ID: fmt.Sprintf("filler%d", i), Code: fmt.Sprintf("f%d", i)}
	}
	validator := &fakeValidator{mutants: mutants}

	assign := RoleAssignment{
		RoleMutantGenerator: "model-gen",
		RoleTestWriter:      cfg.writerModel,
		RoleTestCritic:      cfg.criticModel,
	}
	q := newTestQueue(t)
	d, err := NewDriver(q, scorer, validator, assign, 0.9)
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}
	rs := testRunSpec()
	if err := d.StartRun(missionID, rs, nil); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if _, err := q.PromoteReady(missionID); err != nil {
		t.Fatalf("PromoteReady: %v", err)
	}
	d.Signer = &fakeSigner{}
	return d, missionID
}

// drivePoolToConvergence ticks a Driver built by newScoredRun through
// dev-adequacy, pool-adequacy, and aggregate — filing the run's configured
// vacuous-finding count against test-critic first — returning the signed
// Verdict.
func drivePoolToConvergence(t *testing.T, d *Driver, missionID int64) *Verdict {
	t.Helper()
	ctx := context.Background()

	ready := claimAllReady(t, d.Q)
	tc, mg := ready[RoleTestCritic], ready[RoleMutantGenerator]
	if tc == nil || mg == nil {
		t.Fatalf("expected test-critic and mutant-generator both ready, got: %v", keysOf(ready))
	}
	for i := 0; i < scoredRunVacuous[missionID]; i++ {
		if _, err := d.Q.AddFinding(queue.Finding{
			MissionID: missionID, TaskID: tc.ID, Reporter: "test-critic", Type: "bug",
			Severity: "high", Target: fmt.Sprintf("VacuousTest%d", i+1), Evidence: "vacuous — no assertions",
		}); err != nil {
			t.Fatalf("AddFinding: %v", err)
		}
	}
	mustComplete(t, d.Q, tc.ID, "critic findings filed")
	mustComplete(t, d.Q, mg.ID, "raw mutants")
	if _, err := d.Tick(ctx, missionID); err != nil {
		t.Fatalf("Tick (dev-adequacy): %v", err)
	}

	tw := claimTaskByID(t, d.Q, d.runs[missionID].testWriterTaskID)
	mustComplete(t, d.Q, tw.ID, "pool test source")
	v, err := d.Tick(ctx, missionID)
	if err != nil {
		t.Fatalf("Tick (pool-adequacy + aggregate): %v", err)
	}
	if v == nil {
		t.Fatal("expected a verdict once test-critic + pool-adequacy are both done")
	}
	return v
}

func TestBugCatchSinkFedOnConvergence(t *testing.T) {
	// A needs-review run with 2 survivors, 1 proven-missed → the test-writer seat
	// records catches=1, opportunities=2; the mutant-generator seat records
	// potency; the critic seat records its vacuous-flag count.
	bc := &fakeBugCatch{}
	d, missionID := newScoredRun(t, scoredRun{
		survivors: 2, provenMissed: 1, mutantsTotal: 4, vacuous: 1,
		writerModel: "claude-sonnet-5", criticModel: "gemini-3.5-flash",
	})
	d.BugCatch = bc
	drivePoolToConvergence(t, d, missionID) // ticks through sign + aggregate

	if bc.recordID == 0 {
		t.Fatal("BugCatch sink was not fed after signing")
	}
	tw, ok := obsFor(bc.obs, "test-writer")
	if !ok || tw.Catches != 1 || tw.Opportunities != 2 {
		t.Fatalf("test-writer obs = %+v, want catches=1 opportunities=2", tw)
	}
	if tw.AuthoredTests != 1 || tw.SoundTests != 1 {
		t.Fatalf("test-writer authored/sound = %d/%d, want 1/1", tw.AuthoredTests, tw.SoundTests)
	}
	crit, ok := obsFor(bc.obs, "test-critic")
	if !ok || crit.CriticFlags != 1 || crit.Model != "gemini-3.5-flash" {
		t.Fatalf("critic obs = %+v, want flags=1 model=gemini-3.5-flash", crit)
	}
	mg, ok := obsFor(bc.obs, "mutant-generator")
	if !ok || mg.MutantsPlanted != 4 || mg.MutantsSurvived != 2 {
		t.Fatalf("mutant-generator obs = %+v, want planted=4 survived=2", mg)
	}
	// Execution-proven invariant: catches never exceeds proven_missed.
	if tw.Catches > 1 {
		t.Fatal("catches exceeded proven_missed — a claim leaked into the headline")
	}
}

// shardedRunSpec is the fixture every sharded test starts from: three symbols,
// so a maxShards of 2 or 3 produces a real fan-out.
func shardedRunSpec(maxShards int) (RunSpec, []repoindex.Signature) {
	rs := RunSpec{
		Repo: "r", Commit: "c", Goal: "g",
		CodePath: "a.go", Code: "package p\nfunc A() {}\nfunc B() {}\nfunc C() {}\n",
		DevTestPath: "a_test.go", DevTestCode: "package p\n",
		NMutants: 1, Lang: "go", MaxShards: maxShards,
	}
	sigs := []repoindex.Signature{
		{Name: "A", Complexity: 5, Lines: 10},
		{Name: "B", Complexity: 3, Lines: 6},
		{Name: "C", Complexity: 1, Lines: 2},
	}
	return rs, sigs
}

// newShardedRun mirrors newTestDriver but starts the run WITH signatures and a
// MaxShards budget, so BuildDAG actually fans out. maxShards 0 yields the
// unsharded single-seat run. validator is an interface param so a test can
// supply a shard-aware fake (see shardValidator in Task 4).
func newShardedRun(t *testing.T, missionID int64, maxShards int, scorer *fakeScorer, validator Validator) *Driver {
	t.Helper()
	q := newTestQueue(t)
	d, err := NewDriver(q, scorer, validator, decorrelatedAssign(), 0.5)
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}
	rs, sigs := shardedRunSpec(maxShards)
	if err := d.StartRun(missionID, rs, sigs); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if _, err := q.PromoteReady(missionID); err != nil {
		t.Fatalf("PromoteReady: %v", err)
	}
	return d
}

// completeAllReady claims every currently-ready task and completes it with
// result — the sharded analogue of the file's claimByKey/mustComplete pair.
func completeAllReady(t *testing.T, d *Driver, result string) int {
	t.Helper()
	ready := claimAllReady(t, d.Q)
	for _, task := range ready {
		mustComplete(t, d.Q, task.ID, result)
	}
	return len(ready)
}

// driveShardedToVerdict ticks to convergence, completing whatever becomes
// claimable, and returns the terminal verdict.
func driveShardedToVerdict(t *testing.T, d *Driver, missionID int64, result string) Verdict {
	t.Helper()
	for i := 0; i < 50; i++ {
		v, err := d.Tick(context.Background(), missionID)
		if err != nil {
			t.Fatalf("Tick: %v", err)
		}
		if v != nil {
			return *v
		}
		completeAllReady(t, d, result)
	}
	t.Fatal("run did not converge in 50 ticks")
	return Verdict{}
}

// TestTickDevAdequacyWaitsForEveryShard proves the gate never scores a PARTIAL
// mutant set: with 3 shards and only 2 done, dev-adequacy must not run.
func TestTickDevAdequacyWaitsForEveryShard(t *testing.T) {
	const missionID = int64(200)
	scorer := &fakeScorer{devKillRate: 0.9}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m1", Code: "c1"}}}
	d := newShardedRun(t, missionID, 3, scorer, validator)

	mgs, err := d.tasksByRole(missionID, RoleMutantGenerator)
	if err != nil {
		t.Fatalf("tasksByRole: %v", err)
	}
	if len(mgs) != 3 {
		t.Fatalf("want 3 shard tasks, got %d", len(mgs))
	}

	// Claim every ready task in one sweep (mirrors a real dispatch round), then
	// complete only two of the three shards. The leftover shard stays CLAIMED
	// (not done) with a live lease — it must be finished directly below rather
	// than re-claimed, since an unnamed-instance claim only self-heals once its
	// lease expires (see ClaimNextAs), which a fast unit test never waits out.
	ready := claimAllReady(t, d.Q)
	// Iterate keys in sorted order rather than ranging the map directly — map
	// iteration order is randomized per-run, so which two of the three shards
	// get completed (and which one is left CLAIMED below) would otherwise
	// differ from run to run, making a failure here unreproducible.
	keys := keysOf(ready)
	sort.Strings(keys)
	var pending []*queue.Task
	done := 0
	for _, key := range keys {
		task := ready[key]
		if _, sharded := ShardIndexFromKey(key); sharded {
			if done < 2 {
				mustComplete(t, d.Q, task.ID, "raw")
				done++
			} else {
				pending = append(pending, task)
			}
		}
	}
	if _, err := d.Tick(context.Background(), missionID); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if d.runs[missionID].devScored {
		t.Fatal("dev-adequacy scored a PARTIAL mutant set — the gate must wait for every shard")
	}
	if len(scorer.calls) != 0 {
		t.Fatalf("Scorer ran on a partial mutant set (%d calls)", len(scorer.calls))
	}

	// Complete the last shard (already claimed above); now dev-adequacy may run.
	for _, task := range pending {
		mustComplete(t, d.Q, task.ID, "raw")
	}
	if _, err := d.Tick(context.Background(), missionID); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if !d.runs[missionID].devScored {
		t.Fatal("dev-adequacy did not run once every shard was done")
	}
}

// TestShardedMutantIDsArePrefixed proves mutants from different shards cannot
// collide. Every shard's fake returns a mutant named "m1", so ONLY prefixing
// keeps them distinct. Asserted on the mutants handed TO the Scorer, because
// devSurvivors comes from the fake scorer's canned list, not from the union.
func TestShardedMutantIDsArePrefixed(t *testing.T) {
	const missionID = int64(201)
	scorer := &fakeScorer{devKillRate: 1.0}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m1", Code: "c1"}}}
	d := newShardedRun(t, missionID, 2, scorer, validator)

	completeAllReady(t, d, "raw")
	if _, err := d.Tick(context.Background(), missionID); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(scorer.calls) == 0 {
		t.Fatal("Scorer never ran")
	}
	got := scorer.calls[0].mutants
	if len(got) != 2 {
		t.Fatalf("want 2 unioned mutants, got %d — identical shard IDs collided", len(got))
	}
	seen := map[string]bool{}
	for _, m := range got {
		if seen[m.ID] {
			t.Errorf("duplicate mutant ID %q across shards", m.ID)
		}
		seen[m.ID] = true
		if !strings.HasPrefix(m.ID, "s") || !strings.Contains(m.ID, "/") {
			t.Errorf("sharded mutant ID %q must carry its shard prefix (s<idx>/…)", m.ID)
		}
	}
}

// TestUnshardedMutantIDsUnchanged pins the back-compat guarantee.
func TestUnshardedMutantIDsUnchanged(t *testing.T) {
	const missionID = int64(202)
	scorer := &fakeScorer{devKillRate: 1.0}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m1", Code: "c1"}}}
	d := newShardedRun(t, missionID, 0, scorer, validator) // MaxShards 0 => unsharded

	mgs, _ := d.tasksByRole(missionID, RoleMutantGenerator)
	if len(mgs) != 1 {
		t.Fatalf("want 1 unsharded task, got %d", len(mgs))
	}
	if mgs[0].Key != RoleMutantGenerator {
		t.Errorf("unsharded key: want %q, got %q", RoleMutantGenerator, mgs[0].Key)
	}
	completeAllReady(t, d, "raw")
	if _, err := d.Tick(context.Background(), missionID); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	for _, m := range scorer.calls[0].mutants {
		if strings.Contains(m.ID, "/") {
			t.Errorf("unsharded mutant ID %q must not be prefixed", m.ID)
		}
	}
}

// TestTasksByRole_NumericShardOrder proves tasksByRole orders shard tasks by
// parsed shard index, not by lexicographic key. Ten-plus shards is the case
// where the two orders diverge: a string sort puts "mutant-generator/10"
// before "mutant-generator/2". --max-shards is operator-settable and
// unbounded, so this is reachable in a real run, and per-shard metrics
// (about to land) fold over exactly this slice.
func TestTasksByRole_NumericShardOrder(t *testing.T) {
	tests := []struct {
		name    string
		keys    []string // enqueued out of numeric order
		wantIdx []int    // expected shard index sequence after tasksByRole
	}{
		{
			name:    "single digit stays sorted",
			keys:    []string{ShardTaskKey(2), ShardTaskKey(0), ShardTaskKey(1)},
			wantIdx: []int{0, 1, 2},
		},
		{
			name: "ten-plus shards sort numerically, not lexicographically",
			keys: []string{
				ShardTaskKey(11), ShardTaskKey(2), ShardTaskKey(0), ShardTaskKey(10),
				ShardTaskKey(1), ShardTaskKey(9), ShardTaskKey(3), ShardTaskKey(4),
				ShardTaskKey(5), ShardTaskKey(6), ShardTaskKey(7), ShardTaskKey(8),
			},
			wantIdx: []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11},
		},
		{
			name:    "bare unsharded key sorts first",
			keys:    []string{ShardTaskKey(1), RoleMutantGenerator, ShardTaskKey(0)},
			wantIdx: []int{0, 0, 1}, // unsharded key reports (0,false); real shards follow numerically
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			const missionID = int64(300)
			q := newTestQueue(t)
			specs := make([]queue.TaskSpec, len(tc.keys))
			for i, k := range tc.keys {
				specs[i] = queue.TaskSpec{Key: k, Role: RoleMutantGenerator, Title: k, Instruction: "x"}
			}
			if err := q.Enqueue(missionID, specs); err != nil {
				t.Fatalf("Enqueue: %v", err)
			}
			d := &Driver{Q: q}
			out, err := d.tasksByRole(missionID, RoleMutantGenerator)
			if err != nil {
				t.Fatalf("tasksByRole: %v", err)
			}
			if len(out) != len(tc.wantIdx) {
				t.Fatalf("want %d tasks, got %d", len(tc.wantIdx), len(out))
			}
			for i, task := range out {
				idx, _ := ShardIndexFromKey(task.Key)
				if idx != tc.wantIdx[i] {
					t.Errorf("position %d: key %q parsed shard index %d, want %d (full order: %v)", i, task.Key, idx, tc.wantIdx[i], keysInOrder(out))
				}
			}
		})
	}
}

func keysInOrder(tasks []queue.Task) []string {
	out := make([]string, len(tasks))
	for i, t := range tasks {
		out[i] = t.Key
	}
	return out
}

// shardValidator fails to parse any result equal to failRaw, parses any
// result equal to emptyRaw to a CLEAN empty mutant set (no error, zero
// mutants — distinct from failRaw's unparseable case), and returns canned
// mutants otherwise — so a test can make ONE shard persistently bad, or one
// shard persistently vacuous, while its siblings succeed. (fakeValidator's
// single parseErr applies to every call and cannot express either.)
type shardValidator struct {
	failRaw  string
	emptyRaw string
	mutants  []adequacy.Mutant
}

func (v *shardValidator) ParseMutants(raw, _ string) ([]adequacy.Mutant, error) {
	if raw == v.failRaw {
		return nil, fmt.Errorf("shardValidator: unparseable")
	}
	if v.emptyRaw != "" && raw == v.emptyRaw {
		return nil, nil
	}
	return v.mutants, nil
}

func (v *shardValidator) ParseTest(raw string) string { return raw }

func (v *shardValidator) CompileTest(_ context.Context, _, _, _ string) error { return nil }

// TestShardDroppedAfterRetriesAndRecorded proves a persistently unparseable
// shard is dropped (not retried forever, not an aborted run) and that the
// shortfall lands in the verdict.
func TestShardDroppedAfterRetriesAndRecorded(t *testing.T) {
	const missionID = int64(210)
	const bad = "UNPARSEABLE"
	scorer := &fakeScorer{devKillRate: 1.0}
	validator := &shardValidator{failRaw: bad, mutants: []adequacy.Mutant{{ID: "m1", Code: "c1"}}}
	d := newShardedRun(t, missionID, 3, scorer, validator)

	// One chosen shard always returns junk; the others return parseable output.
	badKey := ShardTaskKey(0)
	completeShards := func() {
		for key, task := range claimAllReady(t, d.Q) {
			result := "raw"
			if key == badKey {
				result = bad
			}
			mustComplete(t, d.Q, task.ID, result)
		}
	}
	completeShards()

	// Each Tick reopens the bad shard and errors, up to the retry budget.
	for i := 0; i < MaxShardRetries; i++ {
		if _, err := d.Tick(context.Background(), missionID); err == nil {
			t.Fatalf("Tick %d: want a retry error while the shard has budget left", i)
		}
		completeShards()
	}

	// Budget exhausted: the shard DROPS and the run proceeds.
	if _, err := d.Tick(context.Background(), missionID); err != nil {
		t.Fatalf("Tick after drop: %v", err)
	}
	run := d.runs[missionID]
	if !run.devScored {
		t.Fatal("run did not proceed after dropping a persistently bad shard")
	}
	if got := len(run.droppedRegions); got != 1 {
		t.Fatalf("want 1 dropped region, got %d: %v", got, run.droppedRegions)
	}
	if run.regionsTotal != 3 {
		t.Errorf("regionsTotal: want 3, got %d", run.regionsTotal)
	}
}

// TestVerdictCarriesRegionCoverage proves a clean run reports full coverage —
// the counterpart to the drop case, and what makes PARTIAL meaningful.
func TestVerdictCarriesRegionCoverage(t *testing.T) {
	const missionID = int64(211)
	scorer := &fakeScorer{devKillRate: 1.0}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m1", Code: "c1"}}}
	d := newShardedRun(t, missionID, 3, scorer, validator)

	v := driveShardedToVerdict(t, d, missionID, "raw")
	if v.RegionsTotal != 3 {
		t.Errorf("RegionsTotal: want 3, got %d", v.RegionsTotal)
	}
	if v.RegionsProbed != 3 {
		t.Errorf("RegionsProbed: want 3, got %d", v.RegionsProbed)
	}
	if len(v.DroppedRegions) != 0 {
		t.Errorf("DroppedRegions: want none, got %v", v.DroppedRegions)
	}
}

// completeShardsWith is the general form of TestShardDroppedAfterRetriesAndRecorded's
// local completeShards closure, lifted out so more than one test can drive a
// run where a specific shard key gets a different completion result than its
// siblings (claimAllReady's map-by-key return makes this natural; the file's
// other completion helper, completeAllReady, can only send ONE result to
// every ready task).
func completeShardsWith(t *testing.T, d *Driver, byKey map[string]string, otherwise string) {
	t.Helper()
	for key, task := range claimAllReady(t, d.Q) {
		result, ok := byKey[key]
		if !ok {
			result = otherwise
		}
		mustComplete(t, d.Q, task.ID, result)
	}
}

// TestShardDropReachesSignedVerdict drives a run with one persistently
// unparseable shard all the way to a terminal Verdict (not just internal run
// state, which TestShardDroppedAfterRetriesAndRecorded already covers) and
// asserts the coverage fields the SIGNED record actually carries — this is
// the gap that let C-2's append-on-reentry bug and I-1's over-counted
// RegionsProbed both ship: neither was ever checked against a real Verdict.
func TestShardDropReachesSignedVerdict(t *testing.T) {
	const missionID = int64(220)
	const bad = "UNPARSEABLE"
	scorer := &fakeScorer{devKillRate: 1.0}
	validator := &shardValidator{failRaw: bad, mutants: []adequacy.Mutant{{ID: "m1", Code: "c1"}}}
	d := newShardedRun(t, missionID, 3, scorer, validator)

	badKey := ShardTaskKey(0) // packs to the single heaviest symbol, "A" — see shard.go's greedy packer
	completeShards := func() { completeShardsWith(t, d, map[string]string{badKey: bad}, "raw") }
	completeShards()
	for i := 0; i < MaxShardRetries; i++ {
		if _, err := d.Tick(context.Background(), missionID); err == nil {
			t.Fatalf("tick %d: want a retry error while the shard has budget left", i)
		}
		completeShards()
	}
	// Budget exhausted this tick: shard 0 drops and dev-adequacy scores.
	if _, err := d.Tick(context.Background(), missionID); err != nil {
		t.Fatalf("tick after drop: %v", err)
	}

	// Drive the rest of the run (test-writer, test-critic, aggregate) to a
	// terminal Verdict.
	var v Verdict
	converged := false
	for i := 0; i < 50; i++ {
		got, err := d.Tick(context.Background(), missionID)
		if err != nil {
			t.Fatalf("Tick: %v", err)
		}
		if got != nil {
			v = *got
			converged = true
			break
		}
		completeAllReady(t, d, "raw")
	}
	if !converged {
		t.Fatal("run did not converge in 50 ticks")
	}

	if v.RegionsTotal != 3 {
		t.Errorf("Verdict.RegionsTotal: want 3, got %d", v.RegionsTotal)
	}
	if v.RegionsProbed != 2 {
		t.Errorf("Verdict.RegionsProbed: want 2 (3 total minus the 1 dropped), got %d", v.RegionsProbed)
	}
	if len(v.DroppedRegions) != 1 {
		t.Fatalf("Verdict.DroppedRegions: want 1 entry, got %d: %v", len(v.DroppedRegions), v.DroppedRegions)
	}
	// M-2: a dropped region is recorded by the SYMBOLS it left unprobed, not
	// the task-UI title string ("Generate mutants for A").
	if v.DroppedRegions[0] != "A" {
		t.Errorf("DroppedRegions[0]: want bare symbol %q, got %q", "A", v.DroppedRegions[0])
	}
	if strings.Contains(v.DroppedRegions[0], "Generate mutants for") {
		t.Errorf("DroppedRegions carries task-UI phrasing: %q", v.DroppedRegions[0])
	}
}

// TestAllRegionsDroppedRefusesToGrade is I-2's regression test: every shard
// exhausting its retry budget must trip the all-regions-failed guard, even
// though every individual shard PARSED without error along the way to
// exhaustion (the bug was the guard's second conjunct, len(droppedRegions) >
// 0, which this run also satisfies — see
// TestZeroMutantShardIsNotCountedAsProbed for the case that conjunct
// actually gated wrong).
func TestAllRegionsDroppedRefusesToGrade(t *testing.T) {
	const missionID = int64(221)
	const bad = "BAD"
	scorer := &fakeScorer{devKillRate: 1.0}
	validator := &shardValidator{failRaw: bad}
	d := newShardedRun(t, missionID, 3, scorer, validator)

	var lastErr error
	for i := 0; i < 30; i++ {
		completeAllReady(t, d, bad)
		v, err := d.Tick(context.Background(), missionID)
		if v != nil {
			t.Fatalf("run converged to a verdict despite every region being dropped: %+v", v)
		}
		if err != nil {
			lastErr = err
			if strings.Contains(err.Error(), "no usable mutants") {
				break
			}
		}
	}
	if lastErr == nil || !strings.Contains(lastErr.Error(), "no usable mutants") {
		t.Fatalf("want the all-regions-failed guard to fire with \"no usable mutants\", last error: %v", lastErr)
	}
	run := d.runs[missionID]
	if run.devScored {
		t.Fatal("guard fired but devScored was still left true")
	}
	if got := len(run.droppedRegions); got != 3 {
		t.Fatalf("want all 3 regions dropped, got %d: %v", got, run.droppedRegions)
	}
}

// TestZeroMutantShardIsNotCountedAsProbed is I-1's regression test: a shard
// that PARSES cleanly to zero mutants must not inflate RegionsProbed — it
// contributed nothing to the exam the dev suite is graded against, so
// counting it as probed would over-report coverage even though nothing was
// ever dropped (RegionsTotal - len(droppedRegions) would wrongly say 3/3).
func TestZeroMutantShardIsNotCountedAsProbed(t *testing.T) {
	const missionID = int64(222)
	const empty = "EMPTY"
	scorer := &fakeScorer{devKillRate: 1.0}
	validator := &shardValidator{emptyRaw: empty, mutants: []adequacy.Mutant{{ID: "m1", Code: "c1"}}}
	d := newShardedRun(t, missionID, 3, scorer, validator)

	emptyKey := ShardTaskKey(2) // packs to the lightest symbol, "C"
	completeShardsWith(t, d, map[string]string{emptyKey: empty}, "raw")
	if _, err := d.Tick(context.Background(), missionID); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	run := d.runs[missionID]
	if !run.devScored {
		t.Fatal("dev-adequacy did not run")
	}
	if len(run.droppedRegions) != 0 {
		t.Fatalf("nothing should be DROPPED here (the shard parsed fine, just to zero mutants), got %v", run.droppedRegions)
	}
	if run.regionsTotal != 3 {
		t.Errorf("regionsTotal: want 3, got %d", run.regionsTotal)
	}
	if run.regionsProbed != 2 {
		t.Errorf("regionsProbed: want 2 (the zero-mutant shard must not count), got %d", run.regionsProbed)
	}
}

// TestDropAndTransientScorerFailureIsIdempotent is C-2's regression test:
// tickDevAdequacy re-runs its whole scan on every Tick while the run has not
// yet devScored, and BOTH re-entry paths this test exercises (the shard was
// already dropped on a prior pass; the Scorer then fails transiently on the
// pass that would otherwise have devScored the run) must leave droppedRegions
// and regionsProbed exactly as if they had happened once — not appended or
// decremented again on each re-entry.
func TestDropAndTransientScorerFailureIsIdempotent(t *testing.T) {
	const missionID = int64(223)
	const bad = "UNPARSEABLE"
	scorer := &fakeScorer{devKillRate: 1.0, err: fmt.Errorf("transient jail failure"), failFirstN: 1}
	validator := &shardValidator{failRaw: bad, mutants: []adequacy.Mutant{{ID: "m1", Code: "c1"}}}
	d := newShardedRun(t, missionID, 3, scorer, validator)

	badKey := ShardTaskKey(0)
	completeShards := func() { completeShardsWith(t, d, map[string]string{badKey: bad}, "raw") }
	completeShards()
	for i := 0; i < MaxShardRetries; i++ {
		if _, err := d.Tick(context.Background(), missionID); err == nil {
			t.Fatalf("tick %d: want a retry error while the shard has budget left", i)
		}
		completeShards()
	}

	// This tick: shard 0 drops, shards 1/2 parse fine, but Scorer.Score fails
	// transiently (failFirstN=1) — the drop must already be recorded even
	// though the tick as a whole errors, since re-entry must find it recorded.
	if _, err := d.Tick(context.Background(), missionID); err == nil {
		t.Fatal("want the transient scorer error to surface on the first attempt")
	}
	run := d.runs[missionID]
	if got := len(run.droppedRegions); got != 1 {
		t.Fatalf("after the first (failed) scorer attempt: want 1 dropped region, got %d: %v", got, run.droppedRegions)
	}
	if run.regionsProbed < 0 {
		t.Fatalf("regionsProbed went negative after the first attempt: %d", run.regionsProbed)
	}

	// Re-enter several more times, mirroring Tick's real re-call pattern
	// (nothing is completed between calls — the task states are unchanged).
	// The second attempt should succeed (the scorer recovers) and devScore
	// the run; further re-entries are no-ops since devScored gates them off.
	for i := 0; i < 3; i++ {
		_, _ = d.Tick(context.Background(), missionID)
	}
	if !run.devScored {
		t.Fatal("run never recovered from the transient scorer failure")
	}
	if got := len(run.droppedRegions); got != 1 {
		t.Fatalf("after re-entry: want droppedRegions to stay at 1 (no duplicates), got %d: %v", got, run.droppedRegions)
	}
	seen := map[string]bool{}
	for _, r := range run.droppedRegions {
		if seen[r] {
			t.Fatalf("duplicate entry in droppedRegions after re-entry: %v", run.droppedRegions)
		}
		seen[r] = true
	}
	if run.regionsProbed < 0 {
		t.Fatalf("regionsProbed went negative after re-entry: %d", run.regionsProbed)
	}
	if run.regionsProbed != 2 {
		t.Errorf("regionsProbed: want 2, got %d", run.regionsProbed)
	}
}

// TestCertSigner_SignVerdict_CarriesCoverageInDetail proves the coverage
// fields reach the SIGNED statement, not just the in-memory Verdict struct:
// it asserts on what CertSigner actually writes into the "execution" step's
// Detail map (regions_total/regions_probed/dropped_regions), reading the
// record back out of a real buildstore the way `certify verify` would — so a
// future refactor of gate.go's Detail map literal cannot silently drop these
// fields without a test noticing.
func TestCertSigner_SignVerdict_CarriesCoverageInDetail(t *testing.T) {
	dir := t.TempDir()
	bs, err := buildstore.Open(filepath.Join(dir, "build.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bs.Close() })

	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	s := CertSigner{Key: priv, Store: bs}

	v := Verdict{
		Repo: "example/repo", Commit: "deadbeef01",
		Status:         StatusNeedsReview,
		DevKillRate:    0.5,
		RegionsTotal:   3,
		RegionsProbed:  2,
		DroppedRegions: []string{"A"},
		ModelsByRole: RoleAssignment{
			RoleMutantGenerator: "model-gen",
			RoleTestWriter:      "model-writer",
			RoleTestCritic:      "model-critic",
		},
	}

	id, _, err := s.SignVerdict(context.Background(), v)
	if err != nil {
		t.Fatalf("SignVerdict: %v", err)
	}

	rec, found, err := bs.Get(id)
	if err != nil || !found {
		t.Fatalf("bs.Get(%d): found=%v err=%v", id, found, err)
	}
	steps, ok := rec["steps"].([]any)
	if !ok {
		t.Fatalf("rec[\"steps\"] is not a []any: %T", rec["steps"])
	}
	var execDetail map[string]any
	for _, raw := range steps {
		step, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if step["kind"] == "execution" {
			execDetail, _ = step["detail"].(map[string]any)
		}
	}
	if execDetail == nil {
		t.Fatal("no \"execution\" step with a detail map found in the signed record's steps")
	}

	if got, want := asFloat(t, execDetail["regions_total"]), float64(3); got != want {
		t.Errorf("signed detail regions_total: want %v, got %v", want, got)
	}
	if got, want := asFloat(t, execDetail["regions_probed"]), float64(2); got != want {
		t.Errorf("signed detail regions_probed: want %v, got %v", want, got)
	}
	dropped, ok := execDetail["dropped_regions"].([]any)
	if !ok || len(dropped) != 1 || dropped[0] != "A" {
		t.Fatalf("signed detail dropped_regions: want [\"A\"], got %v (ok=%v)", execDetail["dropped_regions"], ok)
	}
}

// asFloat unwraps a JSON-decoded number (always float64 via encoding/json's
// map[string]any path) for a clean comparison, failing loudly on any other
// type rather than silently comparing zero values.
func asFloat(t *testing.T, v any) float64 {
	t.Helper()
	f, ok := v.(float64)
	if !ok {
		t.Fatalf("value is not a float64 (JSON number): %T %v", v, v)
	}
	return f
}

// TestTimeoutVerdictCarriesRegionCoverage proves timeoutVerdict — the verdict
// path for a run that hits its wall-clock RunDeadline instead of converging
// normally — carries the same three coverage fields
// (RegionsTotal/RegionsProbed/DroppedRegions) as an ordinary converged
// verdict. This is the run MOST likely to actually carry a shortfall: a
// persistently bad shard is exactly the kind of thing that can also wedge a
// run past its deadline. A prior change deleted these three assignments from
// timeoutVerdict and the whole repo suite stayed green.
func TestTimeoutVerdictCarriesRegionCoverage(t *testing.T) {
	const missionID = int64(212)
	const bad = "UNPARSEABLE"
	// devKillRate < 1.0 with a real survivor leaves pool-adequacy work
	// (test-writer) outstanding after the drop, so the run does NOT converge
	// to a normal verdict on its own — it's still pending when the deadline
	// check runs, exactly like the stall TestRunDeadlineProducesNeedsReviewVerdict
	// simulates. A perfect dev score would certify immediately after the drop
	// (no pool stage needed) and never reach the timeout path at all.
	scorer := &fakeScorer{devKillRate: 0.5, devSurvivors: []adequacy.Mutant{{ID: "m1", Code: "c1"}}}
	validator := &shardValidator{failRaw: bad, mutants: []adequacy.Mutant{{ID: "m1", Code: "c1"}}}
	d := newShardedRun(t, missionID, 3, scorer, validator)
	d.Signer = &fakeSigner{}

	// Install the fake clock AFTER StartRun so run.startedAt (set from the
	// driver's real clock during newShardedRun) is effectively "now" —
	// leaving headroom before the deadline for the retry-and-drop sequence
	// below.
	clk := &fakeClock{t: time.Now()}
	d.Now = clk.Now
	d.RunDeadline = 10 * time.Minute

	// Drive one shard to a persistent parse failure, exactly as in
	// TestShardDroppedAfterRetriesAndRecorded, so the region actually drops
	// and regionsTotal/regionsProbed/droppedRegions get recorded on the run
	// — all before the deadline fires.
	badKey := ShardTaskKey(0)
	completeShards := func() {
		for key, task := range claimAllReady(t, d.Q) {
			result := "raw"
			if key == badKey {
				result = bad
			}
			mustComplete(t, d.Q, task.ID, result)
		}
	}
	completeShards()
	for i := 0; i < MaxShardRetries; i++ {
		if _, err := d.Tick(context.Background(), missionID); err == nil {
			t.Fatalf("Tick %d: want a retry error while the shard has budget left", i)
		}
		completeShards()
	}
	if _, err := d.Tick(context.Background(), missionID); err != nil {
		t.Fatalf("Tick after drop: %v", err)
	}
	run := d.runs[missionID]
	if len(run.droppedRegions) != 1 || run.regionsTotal != 3 {
		t.Fatalf("setup: want 1 dropped region of 3 total, got dropped=%v total=%d", run.droppedRegions, run.regionsTotal)
	}

	// Now exceed the deadline. Tick checks the deadline FIRST (before any
	// normal-progress logic), so this takes the timeoutVerdict path — and
	// that verdict must still carry the coverage numbers already recorded.
	clk.advance(d.RunDeadline + time.Minute)
	v, err := d.Tick(context.Background(), missionID)
	if err != nil {
		t.Fatalf("deadline Tick: %v", err)
	}
	if v == nil || v.Status != StatusNeedsReview {
		t.Fatalf("want a needs-review timeout verdict, got %+v", v)
	}
	if v.RegionsTotal != 3 {
		t.Errorf("RegionsTotal = %d, want 3", v.RegionsTotal)
	}
	if v.RegionsProbed != 2 {
		t.Errorf("RegionsProbed = %d, want 2", v.RegionsProbed)
	}
	if len(v.DroppedRegions) != 1 {
		t.Errorf("DroppedRegions = %v, want 1 entry", v.DroppedRegions)
	}
}
