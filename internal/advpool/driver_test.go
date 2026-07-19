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

// TestBugCatchSinkNotFedWithoutSigner proves a Driver wired with BugCatch but
// no Signer never calls Record. Verdict.RecordID is documented as "0 if
// signing skipped/failed"; the BugCatch field's own doc asserts it is fed
// AFTER Signer, "once RecordID/RecordHead are set" — a signer-less driver
// would otherwise write every row of every run with record_id=0, which
// Cell.Runs (COUNT(DISTINCT record_id)) then reads back as a single run
// forever, pinning every cell below the provisionalBelow trust threshold no
// matter how many runs actually happened.
func TestBugCatchSinkNotFedWithoutSigner(t *testing.T) {
	bc := &fakeBugCatch{}
	d, missionID := newScoredRun(t, scoredRun{
		survivors: 2, provenMissed: 1, mutantsTotal: 4, vacuous: 1,
		writerModel: "claude-sonnet-5", criticModel: "gemini-3.5-flash",
	})
	d.BugCatch = bc
	d.Signer = nil // the case under test: BugCatch wired, Signer not

	v := drivePoolToConvergence(t, d, missionID)
	if v.RecordID != 0 {
		t.Fatalf("RecordID = %d, want 0 (no Signer wired)", v.RecordID)
	}
	if bc.recordID != 0 || bc.obs != nil {
		t.Fatalf("BugCatch sink was fed despite no Signer: recordID=%d obs=%+v", bc.recordID, bc.obs)
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

// TestBugCatchRowsArePerShard proves the metrics keep every seat visible AND
// that each row's difficulty control (Region/RegionComplexity/RegionLines)
// belongs to THAT shard — not just that some non-empty/positive value showed
// up. A row whose difficulty control comes from the WRONG shard looks
// rigorous while being wrong, which is worse than no control at all, so this
// recomputes ShardSymbols itself (the same call StartRun makes) and checks
// each row keyed by its own Shard index. Summing shards back into one
// generator row would collapse N seats into 1 and make an underperforming
// seat invisible BY CONSTRUCTION.
func TestBugCatchRowsArePerShard(t *testing.T) {
	const missionID = int64(220)
	// devKillRate < 1.0 with a real devSurvivor gives Survivors == 1 — a
	// perfect (1.0) suite would leave Survivors == 0, which cannot
	// distinguish "correctly on exactly one row" from "correctly zero
	// everywhere by construction" for the MutantsSurvived assertion below.
	scorer := &fakeScorer{devKillRate: 0.5, devSurvivors: []adequacy.Mutant{{ID: "s0/m1", Code: "c1"}}}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m1", Code: "c1"}}}
	d := newShardedRun(t, missionID, 3, scorer, validator)
	sink := &fakeBugCatch{}
	d.BugCatch = sink
	d.Signer = &fakeSigner{} // BugCatch is fed on a terminal, signed verdict

	_, sigs := shardedRunSpec(3)
	wantShards := ShardSymbols(sigs, 3)
	wantByIndex := make(map[int]Shard, len(wantShards))
	for _, sh := range wantShards {
		wantByIndex[sh.Index] = sh
	}
	if len(wantByIndex) != 3 {
		t.Fatalf("test setup: want 3 distinct shards from the fixture, got %d", len(wantByIndex))
	}

	v := driveShardedToVerdict(t, d, missionID, "raw")

	var gen []BugCatchObservation
	for _, o := range sink.obs {
		if o.Role == RoleMutantGenerator {
			gen = append(gen, o)
		}
	}
	if len(gen) != 3 {
		t.Fatalf("want one generator row per shard (3), got %d — rows were summed", len(gen))
	}
	seenShards := map[int]bool{}
	for _, o := range gen {
		if seenShards[o.Shard] {
			t.Errorf("duplicate row for shard %d", o.Shard)
		}
		seenShards[o.Shard] = true

		want, ok := wantByIndex[o.Shard]
		if !ok {
			t.Fatalf("row names shard %d, which ShardSymbols never produced", o.Shard)
		}
		wantRegion := strings.Join(want.Symbols, ", ")
		if o.Region != wantRegion {
			t.Errorf("shard %d: Region = %q, want %q (a region belonging to a DIFFERENT shard looks rigorous while being wrong)", o.Shard, o.Region, wantRegion)
		}
		if o.RegionComplexity != want.Complexity {
			t.Errorf("shard %d: RegionComplexity = %d, want %d (that shard's actual complexity)", o.Shard, o.RegionComplexity, want.Complexity)
		}
		if o.RegionLines != want.Lines {
			t.Errorf("shard %d: RegionLines = %d, want %d", o.Shard, o.RegionLines, want.Lines)
		}
		// The fake validator hands back exactly one mutant per shard
		// regardless of which shard asked — st.mutants must record that, not
		// leave MutantsPlanted at its zero value.
		if o.MutantsPlanted != 1 {
			t.Errorf("shard %d: MutantsPlanted = %d, want 1 (st.mutants must be recorded)", o.Shard, o.MutantsPlanted)
		}
		if o.Dropped {
			t.Errorf("shard %d: Dropped = true, want false — no shard was dropped in this run", o.Shard)
		}
		if o.ParseRetries != 0 {
			t.Errorf("shard %d: ParseRetries = %d, want 0 — no shard retried in this run", o.Shard, o.ParseRetries)
		}
		if o.TestComplexity != 0 {
			// shardedRunSpec's dev test code is empty, so testComplexity is
			// genuinely 0 here; this just pins that every row carries the
			// SAME run-level value rather than something stray.
			t.Errorf("shard %d: TestComplexity = %d, want 0 (run.testComplexity for this fixture)", o.Shard, o.TestComplexity)
		}
	}
	var total int
	for _, o := range gen {
		total += o.MutantsSurvived
	}
	if total != v.Survivors {
		t.Fatalf("sum(MutantsSurvived) across shard rows = %d, want verdict.Survivors = %d", total, v.Survivors)
	}
	// MutantsSurvived is measured against the MERGED mutant set — it cannot be
	// attributed per shard — so it must land on exactly ONE row (the lowest
	// shard index is documented as carrying it, see
	// bugCatchObservations) — pin that too, not just "some row has it".
	lowest := gen[0].Shard
	for _, o := range gen {
		if o.Shard < lowest {
			lowest = o.Shard
		}
	}
	for _, o := range gen {
		if o.Shard == lowest && o.MutantsSurvived != v.Survivors {
			t.Errorf("lowest-index shard %d: MutantsSurvived = %d, want %d", lowest, o.MutantsSurvived, v.Survivors)
		}
		if o.Shard != lowest && o.MutantsSurvived != 0 {
			t.Errorf("non-lowest shard %d: MutantsSurvived = %d, want 0", o.Shard, o.MutantsSurvived)
		}
	}
}

// TestBugCatchRowsCarryDropAndParseRetries proves a dropped shard's
// BugCatchObservation row records BOTH Dropped=true and the ParseRetries
// count it actually exhausted — the two fields the drop path
// (tickDevAdequacy) sets on shardStat, which must survive unchanged into the
// per-shard rows bugCatchObservations builds from that same map.
func TestBugCatchRowsCarryDropAndParseRetries(t *testing.T) {
	const missionID = int64(221)
	const bad = "UNPARSEABLE"
	scorer := &fakeScorer{devKillRate: 1.0}
	validator := &shardValidator{failRaw: bad, mutants: []adequacy.Mutant{{ID: "m1", Code: "c1"}}}
	d := newShardedRun(t, missionID, 3, scorer, validator)
	sink := &fakeBugCatch{}
	d.BugCatch = sink
	d.Signer = &fakeSigner{}

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

	driveShardedToVerdict(t, d, missionID, "raw")

	var badRow *BugCatchObservation
	otherDropped, otherRetried := 0, 0
	for i, o := range sink.obs {
		if o.Role != RoleMutantGenerator {
			continue
		}
		if o.Shard == 0 {
			badRow = &sink.obs[i]
			continue
		}
		if o.Dropped {
			otherDropped++
		}
		if o.ParseRetries != 0 {
			otherRetried++
		}
	}
	if badRow == nil {
		t.Fatal("no row for the dropped shard 0")
	}
	if !badRow.Dropped {
		t.Error("dropped shard's row: Dropped = false, want true — the drop must be recorded on the row")
	}
	if badRow.ParseRetries != MaxShardRetries+1 {
		t.Errorf("dropped shard's row: ParseRetries = %d, want %d (MaxShardRetries+1, the exhausted attempt count)", badRow.ParseRetries, MaxShardRetries+1)
	}
	if otherDropped != 0 {
		t.Errorf("%d non-dropped shard rows falsely carry Dropped=true", otherDropped)
	}
	if otherRetried != 0 {
		t.Errorf("%d non-retried shard rows falsely carry a nonzero ParseRetries", otherRetried)
	}
}

// TestBugCatchRowsSurvivorsSkipDroppedShard proves v.Survivors is recorded on
// the lowest NON-DROPPED shard index, never just the lowest index. Shard 0 is
// dropped here (never contributed a mutant), so parking the run's survivor
// count there would produce an internally incoherent row (planted=0,
// survived>0) and make the natural analytical filter "exclude shards that
// never ran" silently zero the run's adversary-potency aggregate.
func TestBugCatchRowsSurvivorsSkipDroppedShard(t *testing.T) {
	const missionID = int64(223)
	const bad = "UNPARSEABLE"
	// Every shard's raw mutant output parses to the same bare "m1"; the
	// driver prefixes it with the shard index ("s1/m1", "s2/m1") once
	// unioned, so the surviving mutant must be named with the prefix it will
	// actually carry once it comes from shard 1.
	validator := &shardValidator{failRaw: bad, mutants: []adequacy.Mutant{{ID: "m1", Code: "c1"}}}
	scorer := &fakeScorer{devKillRate: 0.5, devSurvivors: []adequacy.Mutant{{ID: "s1/m1", Code: "c1"}}}
	d := newShardedRun(t, missionID, 3, scorer, validator)
	sink := &fakeBugCatch{}
	d.BugCatch = sink
	d.Signer = &fakeSigner{}

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

	v := driveShardedToVerdict(t, d, missionID, "raw")
	if v.Survivors != 1 {
		t.Fatalf("test setup: v.Survivors = %d, want 1", v.Survivors)
	}

	var gen []BugCatchObservation
	for _, o := range sink.obs {
		if o.Role == RoleMutantGenerator {
			gen = append(gen, o)
		}
	}
	if len(gen) != 3 {
		t.Fatalf("want one generator row per shard (3), got %d", len(gen))
	}
	for _, o := range gen {
		switch o.Shard {
		case 0:
			if !o.Dropped {
				t.Fatalf("test setup: shard 0 must be the dropped shard, row = %+v", o)
			}
			if o.MutantsSurvived != 0 {
				t.Errorf("dropped shard 0: MutantsSurvived = %d, want 0 — a shard that never ran must not carry the run's survivor count", o.MutantsSurvived)
			}
		case 1:
			if o.MutantsSurvived != v.Survivors {
				t.Errorf("lowest NON-dropped shard 1: MutantsSurvived = %d, want %d (v.Survivors)", o.MutantsSurvived, v.Survivors)
			}
		case 2:
			if o.MutantsSurvived != 0 {
				t.Errorf("shard 2: MutantsSurvived = %d, want 0 (only one row carries the run-level count)", o.MutantsSurvived)
			}
		}
	}
}

// TestBugCatchRowsCarryTestComplexity proves every shard row carries the
// run's real (nonzero) dev-suite complexity — not the zeroed default a
// deleted field-copy would leave behind. shardedRunSpec's fixture has an
// empty dev test file (testComplexity genuinely 0), which cannot distinguish
// "correctly zero" from "never set" — so this test supplies a real,
// non-trivial Go dev-test file instead.
func TestBugCatchRowsCarryTestComplexity(t *testing.T) {
	const missionID = int64(222)
	rs := RunSpec{
		Repo: "r", Commit: "c", Goal: "g",
		CodePath: "a.go", Code: "package p\nfunc A() {}\nfunc B() {}\nfunc C() {}\n",
		DevTestPath: "a_test.go",
		DevTestCode: "package p\nimport \"testing\"\nfunc TestA(t *testing.T) { if true { t.Log(1) } else { t.Log(2) } }\n",
		NMutants:    1, Lang: "go", MaxShards: 3,
	}
	sigs := []repoindex.Signature{
		{Name: "A", Complexity: 5, Lines: 10},
		{Name: "B", Complexity: 3, Lines: 6},
		{Name: "C", Complexity: 1, Lines: 2},
	}
	scorer := &fakeScorer{devKillRate: 1.0}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m1", Code: "c1"}}}
	q := newTestQueue(t)
	d, err := NewDriver(q, scorer, validator, decorrelatedAssign(), 0.5)
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}
	if err := d.StartRun(missionID, rs, sigs); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if _, err := q.PromoteReady(missionID); err != nil {
		t.Fatalf("PromoteReady: %v", err)
	}
	wantComplexity := d.runs[missionID].testComplexity
	if wantComplexity <= 0 {
		t.Fatalf("test setup: want a nonzero dev-suite complexity, got %d", wantComplexity)
	}
	sink := &fakeBugCatch{}
	d.BugCatch = sink
	d.Signer = &fakeSigner{}

	driveShardedToVerdict(t, d, missionID, "raw")

	got := 0
	for _, o := range sink.obs {
		if o.Role != RoleMutantGenerator {
			continue
		}
		got++
		if o.TestComplexity != wantComplexity {
			t.Errorf("shard %d: TestComplexity = %d, want %d", o.Shard, o.TestComplexity, wantComplexity)
		}
	}
	if got != 3 {
		t.Fatalf("want 3 generator rows, got %d", got)
	}
}

// TestPoolShardTelemetryEmitted proves tickDevAdequacy emits one "pool_shard"
// event per shard, carrying that shard's real region/complexity/lines/
// mutants/parse_retries/dropped — the telemetry the cockpit/replay/--record
// tape read. A prior change could delete this whole emit block and every
// other Driver test would stay green (it isn't wired to BugCatch/Scorecard),
// which is exactly why it needs its own direct assertion via fakeEventSink.
func TestPoolShardTelemetryEmitted(t *testing.T) {
	const missionID = int64(223)
	scorer := &fakeScorer{devKillRate: 1.0}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m1", Code: "c1"}}}
	d := newShardedRun(t, missionID, 3, scorer, validator)
	sink := &fakeEventSink{}
	d.Events = sink

	_, sigs := shardedRunSpec(3)
	wantShards := ShardSymbols(sigs, 3)
	wantByIndex := make(map[int]Shard, len(wantShards))
	for _, sh := range wantShards {
		wantByIndex[sh.Index] = sh
	}

	completeAllReady(t, d, "raw")
	if _, err := d.Tick(context.Background(), missionID); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	var shardEvents []eventCall
	for _, e := range sink.events {
		if e.kind == "pool_shard" {
			shardEvents = append(shardEvents, e)
		}
	}
	if len(shardEvents) != 3 {
		t.Fatalf("want 3 pool_shard events, got %d — the per-shard telemetry emit is missing", len(shardEvents))
	}
	seen := map[int]bool{}
	for _, e := range shardEvents {
		idx, ok := e.detail["shard"].(int)
		if !ok {
			t.Fatalf("pool_shard event detail[\"shard\"] not an int: %#v", e.detail["shard"])
		}
		if seen[idx] {
			t.Errorf("duplicate pool_shard event for shard %d", idx)
		}
		seen[idx] = true
		want, ok := wantByIndex[idx]
		if !ok {
			t.Fatalf("pool_shard event names shard %d, which ShardSymbols never produced", idx)
		}
		wantRegion := strings.Join(want.Symbols, ", ")
		if got := e.detail["region"]; got != wantRegion {
			t.Errorf("shard %d: detail[region] = %v, want %q", idx, got, wantRegion)
		}
		if got := e.detail["region_complexity"]; got != want.Complexity {
			t.Errorf("shard %d: detail[region_complexity] = %v, want %d", idx, got, want.Complexity)
		}
		if got := e.detail["region_lines"]; got != want.Lines {
			t.Errorf("shard %d: detail[region_lines] = %v, want %d", idx, got, want.Lines)
		}
		if got := e.detail["mutants"]; got != 1 {
			t.Errorf("shard %d: detail[mutants] = %v, want 1", idx, got)
		}
		if got := e.detail["dropped"]; got != false {
			t.Errorf("shard %d: detail[dropped] = %v, want false", idx, got)
		}
		if got := e.detail["parse_retries"]; got != 0 {
			t.Errorf("shard %d: detail[parse_retries] = %v, want 0", idx, got)
		}
	}
}

// TestShadowMutantsNeverReachTheGate is THE invariant test. A shadow seat's
// mutants must never influence DevKillRate, Survivors, MutantsTotal, or Status.
func TestShadowMutantsNeverReachTheGate(t *testing.T) {
	const missionID = int64(230)
	scorer := &fakeScorer{devKillRate: 1.0}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m1", Code: "c1"}}}
	d := newShadowedRun(t, missionID, 2, "challenger-model", scorer, validator)

	primary, err := d.tasksByRole(missionID, RoleMutantGenerator)
	if err != nil {
		t.Fatalf("tasksByRole: %v", err)
	}
	if len(primary) != 2 {
		t.Fatalf("want 2 primary seats, got %d", len(primary))
	}
	for _, mg := range primary {
		if strings.Contains(mg.Key, "shadow") {
			t.Fatalf("a shadow task leaked into the PRIMARY role lookup: %q", mg.Key)
		}
	}

	shadow, err := d.tasksByRole(missionID, RoleMutantGeneratorShadow)
	if err != nil {
		t.Fatalf("tasksByRole(shadow): %v", err)
	}
	if len(shadow) != 2 {
		t.Fatalf("want 2 shadow seats, got %d", len(shadow))
	}
	for _, sh := range shadow {
		if sh.Model != "challenger-model" {
			t.Errorf("shadow seat model: want %q, got %q", "challenger-model", sh.Model)
		}
	}

	// Every seat — primary and shadow — returns the SAME single canned mutant
	// via fakeValidator. There are 2 primary seats and 2 shadow seats, so if
	// shadow mutants reached the gate MutantsTotal would be 4, not 2.
	driveShardedToVerdict(t, d, missionID, "raw")
	st, ok := d.RunStatus(missionID)
	if !ok || st.Verdict == nil {
		t.Fatal("run did not converge to a verdict")
	}
	v := *st.Verdict
	if v.MutantsTotal != 2 {
		t.Fatalf("MutantsTotal: want 2 (primary seats only), got %d — SHADOW MUTANTS REACHED THE GATE", v.MutantsTotal)
	}
	if v.RegionsTotal != 2 {
		t.Errorf("RegionsTotal: want 2 (primary seats), got %d", v.RegionsTotal)
	}
}

// TestShadowRowsArePairedAndFlagged proves the comparison data lands, marked.
func TestShadowRowsArePairedAndFlagged(t *testing.T) {
	const missionID = int64(231)
	scorer := &fakeScorer{devKillRate: 1.0}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m1", Code: "c1"}}}
	d := newShadowedRun(t, missionID, 2, "challenger-model", scorer, validator)
	sink := &fakeBugCatch{}
	d.BugCatch = sink
	d.Signer = &fakeSigner{}

	driveShardedToVerdict(t, d, missionID, "raw")

	byShard := map[int][]BugCatchObservation{}
	for _, o := range sink.obs {
		if o.Role == RoleMutantGenerator || o.Role == RoleMutantGeneratorShadow {
			byShard[o.Shard] = append(byShard[o.Shard], o)
		}
	}
	for idx, rows := range byShard {
		if len(rows) != 2 {
			t.Errorf("shard %d: want a PAIR (primary + shadow), got %d rows", idx, len(rows))
		}
		var regions []string
		shadows := 0
		for _, r := range rows {
			regions = append(regions, r.Region)
			if r.Shadow {
				shadows++
			}
		}
		if shadows != 1 {
			t.Errorf("shard %d: want exactly 1 shadow row, got %d", idx, shadows)
		}
		// The whole point: SAME region, so the comparison is not confounded.
		if regions[0] != regions[1] {
			t.Errorf("shard %d: paired rows must name the SAME region, got %q vs %q", idx, regions[0], regions[1])
		}
		// The region NAME matching is not sufficient: RegionComplexity and
		// RegionLines are the DIFFICULTY CONTROL the whole comparison rests on
		// (raw yield cannot distinguish a weak model from an easy region), and
		// they are carried as separate numeric fields. A shadow row that
		// inherited another shard's complexity would still pass the region
		// check above while silently attributing the challenger's yield to the
		// wrong difficulty — so assert the numbers pair too.
		if rows[0].RegionComplexity != rows[1].RegionComplexity {
			t.Errorf("shard %d: paired rows must carry the SAME RegionComplexity, got %d vs %d",
				idx, rows[0].RegionComplexity, rows[1].RegionComplexity)
		}
		if rows[0].RegionLines != rows[1].RegionLines {
			t.Errorf("shard %d: paired rows must carry the SAME RegionLines, got %d vs %d",
				idx, rows[0].RegionLines, rows[1].RegionLines)
		}
		// And they must be the region's REAL difficulty, not merely equal to
		// each other: a mutation that zeroed both would pass the pairing checks.
		if rows[0].RegionComplexity <= 0 || rows[0].RegionLines <= 0 {
			t.Errorf("shard %d: region difficulty control is empty (complexity=%d lines=%d)",
				idx, rows[0].RegionComplexity, rows[0].RegionLines)
		}
	}
}

// shadowResultTag is the completion result used for CHALLENGER seats by
// completeAllReadyTagged, so a fake Validator/Scorer can branch on which seat
// it is serving without guessing from call order.
const shadowResultTag = "SHADOW-RAW"

// completeAllReadyTagged completes every ready task like completeAllReady, but
// tags the challenger seats' results with shadowResultTag.
func completeAllReadyTagged(t *testing.T, d *Driver) int {
	t.Helper()
	ready := claimAllReady(t, d.Q)
	for key, task := range ready {
		result := "raw"
		if _, isShadow := ShadowShardIndexFromKey(key); isShadow {
			result = shadowResultTag
		}
		mustComplete(t, d.Q, task.ID, result)
	}
	return len(ready)
}

// driveTaggedToVerdict mirrors driveShardedToVerdict but completes challenger
// seats with shadowResultTag.
func driveTaggedToVerdict(t *testing.T, d *Driver, missionID int64) Verdict {
	t.Helper()
	for i := 0; i < 50; i++ {
		v, err := d.Tick(context.Background(), missionID)
		if err != nil {
			t.Fatalf("Tick: %v", err)
		}
		if v != nil {
			return *v
		}
		completeAllReadyTagged(t, d)
	}
	t.Fatal("run did not converge in 50 ticks")
	return Verdict{}
}

// shadowParseFailValidator parses everything EXCEPT a challenger seat's
// result, which it rejects — so a test can drive a run in which only the
// shadow parse fails.
type shadowParseFailValidator struct {
	mutants []adequacy.Mutant
}

func (v *shadowParseFailValidator) ParseMutants(raw, _ string) ([]adequacy.Mutant, error) {
	if raw == shadowResultTag {
		return nil, fmt.Errorf("challenger output is garbage")
	}
	return v.mutants, nil
}
func (v *shadowParseFailValidator) ParseTest(raw string) string { return raw }
func (v *shadowParseFailValidator) CompileTest(_ context.Context, _, _, _ string) error {
	return nil
}

// TestShadowParseFailureIsNotFatal proves a challenger seat whose output will
// not parse cannot fail the run. A shadow seat is MEASUREMENT: its failure is
// recorded (or, here, simply kept out of the scorecard as a dropped seat) and
// the primary's certification proceeds untouched. Without this, a challenger
// model's malformed reply — on a feature that is ON BY DEFAULT — would decide
// whether a good suite gets certified.
func TestShadowParseFailureIsNotFatal(t *testing.T) {
	const missionID = int64(240)
	scorer := &fakeScorer{devKillRate: 1.0}
	validator := &shadowParseFailValidator{mutants: []adequacy.Mutant{{ID: "m1", Code: "c1"}}}
	d := newShadowedRun(t, missionID, 2, "challenger-model", scorer, validator)
	d.Signer = &fakeSigner{}

	v := driveTaggedToVerdict(t, d, missionID)

	if v.Status != StatusCertified {
		t.Fatalf("a shadow PARSE failure must not change the verdict: Status = %q, want %q", v.Status, StatusCertified)
	}
	if v.DevKillRate != 1.0 {
		t.Errorf("DevKillRate = %v, want 1.0 — the primary exam is untouched by the challenger", v.DevKillRate)
	}
}

// TestShadowProviderFailureIsRecordedUnmeasuredNotDropped proves the fix for
// the data-fabrication bug: a challenger seat completed with
// ShadowProviderFailedResult (cmd/corral's stand-in for "the LLM call itself
// failed") must NOT be run through ParseMutants at all. Before this fix, the
// driver could not tell "the model was never asked" apart from "the model
// answered with garbage": ParseMutants("") always errors, which fell straight
// into the parse-failure branch and recorded a MEASURED, DROPPED, zero-yield
// row for a model that never ran — fabricated data landing in the shared
// scorecard that feeds model routing. This asserts the seat is left
// unmeasured: no row reaches BugCatch, and the shard's own telemetry beat
// carries measured=false.
func TestShadowProviderFailureIsRecordedUnmeasuredNotDropped(t *testing.T) {
	const missionID = int64(245)
	scorer := &fakeScorer{devKillRate: 1.0}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m1", Code: "c1"}}}
	d := newShadowedRun(t, missionID, 2, "challenger-model", scorer, validator)
	d.Signer = &fakeSigner{}
	sink := &fakeBugCatch{}
	d.BugCatch = sink
	events := &fakeEventSink{}
	d.Events = events

	// Complete every ready task, but hand the CHALLENGER seats the provider-
	// failure sentinel instead of a normal (or garbage) result — exactly what
	// cmd/corral's runOneTask does when the shadow model's LLM call errors.
	ready := claimAllReady(t, d.Q)
	shadowSeats := 0
	for key, task := range ready {
		result := "raw"
		if _, isShadow := ShadowShardIndexFromKey(key); isShadow {
			result = ShadowProviderFailedResult
			shadowSeats++
		}
		mustComplete(t, d.Q, task.ID, result)
	}
	if shadowSeats == 0 {
		t.Fatal("fixture is wrong: no shadow seats were claimed")
	}

	v, err := d.Tick(context.Background(), missionID)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if v == nil || v.Status != StatusCertified {
		t.Fatalf("a provider-failed challenger must not affect the primary verdict, got %+v", v)
	}

	// No BugCatch row for the failed seats — recording nothing is strictly
	// better than recording a fabricated zero-yield drop.
	for _, o := range sink.obs {
		if o.Shadow {
			t.Errorf("a provider-failed shadow seat must never be recorded (measured or dropped): %+v", o)
		}
	}
	// The telemetry beat must say so honestly: measured=false, not a silent
	// absence and not a fabricated dropped=true/mutants_planted=0 pair either.
	shadowBeats := 0
	for _, e := range events.events {
		if e.kind != "pool_shard" {
			continue
		}
		isShadow, _ := e.detail["shadow"].(bool)
		if !isShadow {
			continue
		}
		shadowBeats++
		if measured, _ := e.detail["measured"].(bool); measured {
			t.Errorf("provider-failed shadow beat reports measured=true: %+v", e.detail)
		}
		if dropped, _ := e.detail["dropped"].(bool); dropped {
			t.Errorf("provider-failed shadow beat reports dropped=true — that IS the fabrication this test guards against: %+v", e.detail)
		}
	}
	if shadowBeats == 0 {
		t.Fatal("no shadow telemetry beats were emitted — this test would pass vacuously")
	}
}

// shadowScoreFailScorer succeeds on the FIRST call and fails on every one
// after. Paired with a perfect dev suite (kill-rate 1.0 ⇒ no survivors ⇒ no
// test-writer and no pool score), the calls after the first are exactly the
// challenger seats' scoring calls.
type shadowScoreFailScorer struct{ calls int }

func (s *shadowScoreFailScorer) Score(_ context.Context, _, _, _ string, _ []adequacy.Mutant, _ string) (float64, []adequacy.Mutant, error) {
	s.calls++
	if s.calls == 1 {
		return 1.0, nil, nil
	}
	return 0, nil, fmt.Errorf("shadow jail run exploded")
}

// TestShadowScoringFailureIsNotFatal proves a challenger seat whose SCORING
// fails (the jail run the shadow pass performs against the same dev suite)
// cannot fail the run either. Scoring is the expensive half of the shadow
// pass and the one most likely to break transiently — an infra hiccup there
// must never cost a certification.
func TestShadowScoringFailureIsNotFatal(t *testing.T) {
	const missionID = int64(241)
	scorer := &shadowScoreFailScorer{}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m1", Code: "c1"}}}
	d := newShadowedRun(t, missionID, 2, "challenger-model", scorer, validator)
	sink := &fakeBugCatch{}
	d.BugCatch = sink
	d.Signer = &fakeSigner{}

	v := driveTaggedToVerdict(t, d, missionID)

	if v.Status != StatusCertified {
		t.Fatalf("a shadow SCORING failure must not change the verdict: Status = %q, want %q", v.Status, StatusCertified)
	}
	if scorer.calls < 2 {
		t.Fatalf("the shadow pass never attempted to score (%d Score calls) — this test would pass vacuously", scorer.calls)
	}
	// A seat whose scoring failed was never measured, so it must NOT appear as
	// a challenger that planted zero mutants — that is a fabricated comparison.
	for _, o := range sink.obs {
		if o.Shadow {
			t.Errorf("an UNMEASURED shadow seat was recorded to the scorecard: %+v", o)
		}
	}
}

// TestIncompleteShadowSeatDoesNotBlockDevAdequacy proves the primary
// all-shards-terminal gate in tickDevAdequacy waits only on the PRIMARY
// generator seats. The existing sharded tests complete shadow tasks along with
// everything else, so none of them exercise this: here the challenger seats
// are left CLAIMED (never completed) and dev-adequacy must still score. If a
// shadow seat could hold the gate, a single wedged challenger would stall
// every run with shadow on — which is the default.
func TestIncompleteShadowSeatDoesNotBlockDevAdequacy(t *testing.T) {
	const missionID = int64(242)
	scorer := &fakeScorer{devKillRate: 0.9, devSurvivors: []adequacy.Mutant{{ID: "s1", Code: "c1"}}}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m1", Code: "c1"}}}
	d := newShadowedRun(t, missionID, 2, "challenger-model", scorer, validator)

	// Claim everything, then complete ONLY the primary generator seats. The
	// challenger seats stay claimed with a live lease — exactly the shape of a
	// challenger whose provider is hanging.
	ready := claimAllReady(t, d.Q)
	shadowsLeftOpen := 0
	primariesDone := 0
	for key, task := range ready {
		if _, isShadow := ShadowShardIndexFromKey(key); isShadow {
			shadowsLeftOpen++
			continue
		}
		if _, isPrimary := ShardIndexFromKey(key); isPrimary {
			mustComplete(t, d.Q, task.ID, "raw")
			primariesDone++
		}
	}
	if shadowsLeftOpen == 0 || primariesDone == 0 {
		t.Fatalf("fixture is wrong: %d shadow seats left open, %d primaries completed", shadowsLeftOpen, primariesDone)
	}

	if _, err := d.Tick(context.Background(), missionID); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if !d.runs[missionID].devScored {
		t.Fatal("dev-adequacy did not score — an unfinished CHALLENGER seat is blocking the primary gate")
	}
	if len(scorer.calls) == 0 {
		t.Fatal("the Scorer never ran: the primary exam is gated on a measurement seat")
	}
}

// TestShadowBudgetSkipsRatherThanTimingOutTheRun proves the invariant the role
// key cannot enforce: shadow work can never flip a run's Status. RunDeadline
// is wall-clock from run start and exceeding it forces a needs-review TIMEOUT
// verdict, while the shadow pass runs a SECOND full Scorer.Score per shard —
// so without the guard, enabling shadow could turn a would-be-certified run
// into needs-review purely by being slower. Here the fake clock advances past
// the whole deadline DURING shadow scoring, in Tick #1; the run must still
// certify.
//
// This is a two-tick test on purpose. The original version of this test used
// a PERFECT dev suite (kill-rate 1.0, no survivors), which makes tickDevAdequacy
// skip the test-writer and go straight to poolScored=true — so the ENTIRE run
// (dev-adequacy, shadow pass, aggregate) converged inside the single Tick call
// where the clock was advanced, and the deadline guard at the top of Tick was
// never evaluated a second time with the advanced clock. That made the test
// vacuous with respect to the credit-back: removing the credit-back entirely
// left the full suite green (verified by hand before this fix). Using a
// real (non-1.0) kill-rate with an actual survivor forces a second Tick (the
// test-writer must run and be scored before the run can converge), which is
// the only way to actually exercise the deadline check AFTER the shadow pass's
// clock advance has been credited back.
//
// It is also seeded the same way TestRunDeadlineProducesNeedsReviewVerdict is:
// d.Now is set BEFORE StartRun. The prior version set it AFTER newShadowedRun
// (which calls StartRun internally), so run.startedAt was seeded from the
// REAL time.Now() while every later check read the fake clock frozen at
// time.Unix(0,0) — an offset of decades that made the deadline check
// unreachable regardless of the credit-back.
func TestShadowBudgetSkipsRatherThanTimingOutTheRun(t *testing.T) {
	const missionID = int64(243)
	now := time.Unix(0, 0)
	rs, sigs := shardedRunSpec(2)
	rs.ShadowModel = "challenger-model"
	// RunDeadline is 10 minutes below, so ShadowTimeBudget is 2.5 minutes
	// (deadline/4). perShadowCall (11 minutes) is deliberately chosen to
	// exceed RunDeadline on its own: only the FIRST shadow call actually
	// proceeds (the budget guard's pre-call check always lets the first
	// through; it skips the second once elapsed already exceeds the 2.5-minute
	// budget), so total shadow spend is 11 minutes — more than the whole
	// deadline, but less than deadline+budget (12.5 minutes). That range is
	// exactly what proves the credit-back without the MINOR-3 cap silently
	// hiding a regression: capped at budget, 2.5 of the 11 minutes are
	// credited back (8.5 min charged against the deadline, still < 10 min ⇒
	// certifies); with NO credit-back at all, the full 11 minutes would be
	// charged (> 10 min ⇒ times out) — verified by hand (see the fix commit).
	scorer := &clockAdvancingScorer{
		devKillRate:  0.5,
		devTest:      rs.DevTestCode,
		devSurvivors: []adequacy.Mutant{{ID: "s1", Code: "c1"}},
		now:          &now, perShadowCall: 11 * time.Minute,
	}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m1", Code: "c1"}}}
	q := newTestQueue(t)
	d, err := NewDriver(q, scorer, validator, decorrelatedAssign(), 0.5)
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}
	d.Now = func() time.Time { return now } // seeded BEFORE StartRun.
	d.RunDeadline = 10 * time.Minute
	d.Signer = &fakeSigner{}
	sink := &fakeBugCatch{}
	d.BugCatch = sink

	if err := d.StartRun(missionID, rs, sigs); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if _, err := q.PromoteReady(missionID); err != nil {
		t.Fatalf("PromoteReady: %v", err)
	}

	// Tick #1: dev-adequacy scores a REAL survivor, so the run cannot converge
	// this tick (the test-writer still has to run) — the shadow pass runs
	// inside this same call. Only the first shadow call proceeds (11 fake
	// minutes); the second is skipped by the pre-call budget check. That 11
	// minutes is well past the 10-minute RunDeadline, and is credited back to
	// run.startedAt (capped at the 2.5-minute shadow budget — see
	// TestShadowCreditIsCappedAtBudget for the cap itself).
	completeAllReadyTagged(t, d)
	if v, err := d.Tick(context.Background(), missionID); err != nil {
		t.Fatalf("tick 1: %v", err)
	} else if v != nil {
		t.Fatalf("tick 1 converged early (want the test-writer still pending so tick 2 exercises the deadline check): %+v", v)
	}
	if !d.runs[missionID].devScored {
		t.Fatal("dev-adequacy did not score on tick 1 — fixture is wrong")
	}
	if scorer.shadowCalls == 0 {
		t.Fatal("the shadow pass never ran — this test would pass vacuously")
	}

	// Tick #2 (and on, via driveTaggedToVerdict): the deadline check now runs
	// against the CREDITED clock. Without the credit-back (or with it applied
	// before StartRun seeded the real clock, as in the pre-fix version), this
	// observes elapsed time already past RunDeadline and times out instead of
	// certifying.
	completeAllReadyTagged(t, d)
	v := driveTaggedToVerdict(t, d, missionID)

	if v.Status != StatusCertified {
		t.Fatalf("shadow work timed the run out: Status = %q, want %q — shadow must never change Status", v.Status, StatusCertified)
	}
	// The guard SKIPS rather than pretending: an unmeasured seat is absent
	// from the scorecard, never recorded as a challenger with zero yield.
	for _, o := range sink.obs {
		if o.Shadow && o.MutantsPlanted == 0 {
			t.Errorf("a skipped shadow seat was recorded as zero yield: %+v", o)
		}
	}
}

// TestShadowCreditIsCappedAtBudget pins the min(elapsed, budget) clamp in
// runShadowPass's credit-back (driver.go, around the ShadowTimeBudget doc).
// The credit-back exists so shadow work never eats into the PRIMARY run's
// deadline, but crediting the RAW elapsed time would let a Scorer that
// ignores its own context extend the deadline arbitrarily just by running
// long — the cap is what keeps the credit bounded regardless of how badly a
// misbehaving Scorer overspends. Here perShadowCall (100 minutes) is chosen
// to vastly exceed ShadowTimeBudget(10 min) = 2.5 min, so the assertion is
// unambiguous: with the cap, run.startedAt advances by EXACTLY the 2.5-minute
// budget; with the cap removed (min(elapsed, budget) → elapsed), it would
// advance by the full 100 minutes instead — verified by hand by deleting the
// clamp (see the fix commit).
func TestShadowCreditIsCappedAtBudget(t *testing.T) {
	const missionID = int64(244)
	now := time.Unix(0, 0)
	rs, sigs := shardedRunSpec(2)
	rs.ShadowModel = "challenger-model"
	scorer := &clockAdvancingScorer{
		devKillRate:  0.5,
		devTest:      rs.DevTestCode,
		devSurvivors: []adequacy.Mutant{{ID: "s1", Code: "c1"}},
		now:          &now, perShadowCall: 100 * time.Minute,
	}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m1", Code: "c1"}}}
	q := newTestQueue(t)
	d, err := NewDriver(q, scorer, validator, decorrelatedAssign(), 0.5)
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}
	d.Now = func() time.Time { return now } // seeded BEFORE StartRun.
	d.RunDeadline = 10 * time.Minute
	d.Signer = &fakeSigner{}
	sink := &fakeBugCatch{}
	d.BugCatch = sink

	if err := d.StartRun(missionID, rs, sigs); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	startedAt := d.runs[missionID].startedAt

	if _, err := q.PromoteReady(missionID); err != nil {
		t.Fatalf("PromoteReady: %v", err)
	}
	completeAllReadyTagged(t, d)
	if _, err := d.Tick(context.Background(), missionID); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if scorer.shadowCalls == 0 {
		t.Fatal("the shadow pass never ran — this test would pass vacuously")
	}

	want := ShadowTimeBudget(d.RunDeadline)
	got := d.runs[missionID].startedAt.Sub(startedAt)
	if got != want {
		t.Fatalf("shadow credit-back not capped: startedAt advanced by %s, want exactly %s (ShadowTimeBudget) — the min(elapsed, budget) clamp is missing or wrong",
			got, want)
	}
}

// clockAdvancingScorer burns fake wall-clock on every call scored against
// devTest AFTER the first (i.e. the shadow pass's calls) — never on a call
// scored against a DIFFERENT test string, which is how tickPoolAdequacy's
// score of the test-writer's authored test is distinguished from a shadow
// call. Without that distinction, a run with real survivors (needed so the
// run does not converge inside a single Tick — see the test above) would
// have its pool-adequacy score call miscounted as a shadow call and burn
// UNCREDITED clock time, defeating the very invariant the test proves.
type clockAdvancingScorer struct {
	devKillRate   float64
	devTest       string
	devSurvivors  []adequacy.Mutant
	now           *time.Time
	perShadowCall time.Duration
	calls         int
	shadowCalls   int
}

func (s *clockAdvancingScorer) Score(_ context.Context, _, _, test string, _ []adequacy.Mutant, _ string) (float64, []adequacy.Mutant, error) {
	s.calls++
	if test != s.devTest {
		// Scored against the test-writer's authored test, not the dev suite —
		// this is tickPoolAdequacy, never a shadow call.
		return 1.0, nil, nil
	}
	if s.calls == 1 {
		return s.devKillRate, s.devSurvivors, nil
	}
	s.shadowCalls++
	*s.now = s.now.Add(s.perShadowCall)
	return 1.0, nil, nil
}

// newShadowedRun mirrors newShardedRun but sets a challenger model, so
// BuildDAG also emits the shadow seats.
// scorer/validator are interface params so a test can supply a fake that
// branches on WHICH seat it is serving (see shadowScoreFailScorer /
// shadowParseFailValidator) — the only way to prove a shadow-only failure is
// non-fatal.
func newShadowedRun(t *testing.T, missionID int64, maxShards int, shadowModel string, scorer Scorer, validator Validator) *Driver {
	t.Helper()
	q := newTestQueue(t)
	d, err := NewDriver(q, scorer, validator, decorrelatedAssign(), 0.5)
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}
	rs, sigs := shardedRunSpec(maxShards)
	rs.ShadowModel = shadowModel
	if err := d.StartRun(missionID, rs, sigs); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if _, err := q.PromoteReady(missionID); err != nil {
		t.Fatalf("PromoteReady: %v", err)
	}
	return d
}

// TestShadowShardTelemetryEmitted proves the challenger's per-seat beats reach
// the EventSink — the --record tape and the cockpit. The pool_shard emit loop
// previously iterated run.shardStats only, so a replay of a shadowed run
// carried the primary's rows and no trace of the comparison at all: the
// feature was invisible everywhere it claimed to be recorded.
func TestShadowShardTelemetryEmitted(t *testing.T) {
	const missionID = int64(244)
	scorer := &fakeScorer{devKillRate: 1.0}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m1", Code: "c1"}}}
	d := newShadowedRun(t, missionID, 2, "challenger-model", scorer, validator)
	sink := &fakeEventSink{}
	d.Events = sink

	completeAllReadyTagged(t, d)
	if _, err := d.Tick(context.Background(), missionID); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	var shadowEvents, primaryEvents []eventCall
	for _, e := range sink.events {
		if e.kind != "pool_shard" {
			continue
		}
		if isShadow, _ := e.detail["shadow"].(bool); isShadow {
			shadowEvents = append(shadowEvents, e)
		} else {
			primaryEvents = append(primaryEvents, e)
		}
	}
	if len(primaryEvents) == 0 {
		t.Fatal("the primary per-shard telemetry regressed")
	}
	if len(shadowEvents) != len(primaryEvents) {
		t.Fatalf("want a shadow beat per primary beat, got %d shadow vs %d primary — the comparison is missing from the tape",
			len(shadowEvents), len(primaryEvents))
	}
	for _, e := range shadowEvents {
		if got := e.detail["model"]; got != "challenger-model" {
			t.Errorf("shadow beat must name the challenger model, got %v", got)
		}
		if measured, _ := e.detail["measured"].(bool); !measured {
			t.Errorf("shadow beat for shard %v reports measured=false on a clean run", e.detail["shard"])
		}
		if e.detail["region_complexity"] == nil || e.detail["region_lines"] == nil {
			t.Errorf("shadow beat is missing the region difficulty control: %#v", e.detail)
		}
	}
}
