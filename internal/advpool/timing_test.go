// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
)

// clockScorer is the fake-role harness's scorer with a CLOCK: every scored
// pass advances the injected time by a known amount, so a phase's measured
// duration is an exact number rather than a flaky "greater than zero".
type clockScorer struct {
	*fakeScorer
	clk           *fakeClock
	dev, authored time.Duration
}

func (c *clockScorer) ScoreReport(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (adequacy.Report, error) {
	if !c.devReported {
		c.clk.advance(c.dev)
	}
	return c.fakeScorer.ScoreReport(ctx, codePath, code, test, mutants, testCmd)
}

func (c *clockScorer) ScoreAuthoredReport(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (adequacy.Report, error) {
	c.clk.advance(c.authored)
	return c.fakeScorer.ScoreAuthoredReport(ctx, codePath, code, test, mutants, testCmd)
}

// TestVerdictTimingSumsWithinTotal is the whole point of this change stated as
// an invariant: every phase says what it spent, and the phases live INSIDE the
// run's own wall clock. Today a file that took 43 minutes reports one number
// (the compliant baseline) and the operator subtracts log timestamps to learn
// that 35 of those minutes were the dev pass.
func TestVerdictTimingSumsWithinTotal(t *testing.T) {
	const mission int64 = 7701
	clk := &fakeClock{t: time.Date(2026, 8, 30, 9, 0, 0, 0, time.UTC)}
	survivors := []adequacy.Mutant{{ID: "m2", Replace: "s"}}
	scorer := &clockScorer{
		fakeScorer: &fakeScorer{devKillRate: 0.5, devSurvivors: survivors},
		clk:        clk, dev: 5 * time.Minute, authored: 2 * time.Minute,
	}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m1", Replace: "k"}, {ID: "m2", Replace: "s"}}}
	q := newTestQueue(t)
	d, err := NewDriver(q, scorer, validator, decorrelatedAssign(), 0.9)
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}
	d.Now = clk.Now // BEFORE StartRun: startedAt is the run's own zero.
	d.Signer = &fakeSigner{}
	sink := &fakeEventSink{}
	d.Events = sink
	// The two phases the CALLER measured. Non-zero on purpose: they are spent
	// BEFORE StartRun, so a Total read as "now minus startedAt" excludes them
	// and the sum-within-total invariant below would pass vacuously on the
	// only shape it exists to constrain — a real `certify --repo` scan.
	rs := testRunSpec()
	rs.SelectionDuration = 92 * time.Second
	rs.PoolDuration = 12 * time.Second
	if err := d.StartRun(mission, rs, nil); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if _, err := q.PromoteReady(mission); err != nil {
		t.Fatalf("PromoteReady: %v", err)
	}

	ready := claimAllReady(t, q)
	tc, mg := ready[RoleTestCritic], ready[RoleMutantGenerator]
	if tc == nil || mg == nil {
		t.Fatalf("expected test-critic and mutant-generator ready, got %v", keysOf(ready))
	}

	// GENERATION: the model seat is out for four minutes before its result
	// is consumed.
	clk.advance(4 * time.Minute)
	mustComplete(t, q, mg.ID, "raw mutants")
	if _, err := d.Tick(context.Background(), mission); err != nil {
		t.Fatalf("Tick (dev-adequacy): %v", err)
	}

	// AUTHORED: one minute waiting for the writer, then the scored pass.
	clk.advance(time.Minute)
	tw := claimTaskByID(t, d.Q, d.runs[mission].testWriterTaskID)
	mustComplete(t, q, tw.ID, "pool test source")
	if v, err := d.Tick(context.Background(), mission); err != nil || v != nil {
		t.Fatalf("Tick (pool-adequacy) = %+v, %v — the critic is not done, so nothing should converge", v, err)
	}

	// CRITIC: the run waits half a minute on the critic before aggregating.
	clk.advance(30 * time.Second)
	mustComplete(t, q, tc.ID, "critic findings filed")
	v, err := d.Tick(context.Background(), mission)
	if err != nil || v == nil {
		t.Fatalf("Tick (aggregate) = %+v, %v", v, err)
	}

	tm := v.Timing
	if tm.Selection != 92*time.Second || tm.Pool != 12*time.Second {
		t.Errorf("Timing = %+v, want the spec's own selection/pool durations", tm)
	}
	if tm.Generation != 4*time.Minute {
		t.Errorf("Timing.Generation = %v, want 4m", tm.Generation)
	}
	if tm.DevPass != 5*time.Minute {
		t.Errorf("Timing.DevPass = %v, want the 5m the dev pass spent", tm.DevPass)
	}
	if tm.DevPass <= 0 {
		t.Error("the dev pass — where most of an audit's wall clock goes — measured nothing")
	}
	if tm.AuthoredPass != 3*time.Minute {
		t.Errorf("Timing.AuthoredPass = %v, want 1m of waiting + the 2m scored pass", tm.AuthoredPass)
	}
	if tm.Critic != 30*time.Second {
		t.Errorf("Timing.Critic = %v, want 30s", tm.Critic)
	}
	// SELECTION IS DELIBERATELY EXCLUDED from this sum, and from Total. It is
	// one instrumented run shared by every file of the scan, so charging it
	// to each file would make `sum(total_ms)` over a scan count it once per
	// file — time nobody spent. It is reported per file (a readout must be
	// able to name every phase of that file's audit) and RECORDED once, on
	// the scan header. Pool is per file and IS included.
	sum := tm.Pool + tm.Generation + tm.DevPass + tm.AuthoredPass + tm.Critic
	if tm.Total < sum {
		t.Fatalf("Timing.Total %v is less than this file's own phases %v — a phase is being counted outside the run", tm.Total, sum)
	}
	// 4m generation + 5m dev + 3m authored + 30s critic = 12m30s of driver
	// elapsed, plus the 12s pool the caller measured before StartRun.
	if want := 12*time.Minute + 42*time.Second; tm.Total != want {
		t.Errorf("Timing.Total = %v, want %v — the driver's elapsed time plus Pool, and NOT the per-scan selection run", tm.Total, want)
	}
	if tm.Selection == 0 {
		t.Fatal("the fixture measured no selection, so nothing above proves it is EXCLUDED from Total")
	}

	// Every phase boundary this fixture actually crossed must have EMITTED a
	// beat with the SAME duration Timing itself carries — the ledger's tape
	// and the signed verdict must never disagree about what a phase spent.
	byKind := map[string]map[string]any{}
	for _, e := range sink.events {
		byKind[e.kind] = e.detail
	}
	wantDurations := map[string]time.Duration{
		"phase_pool":          tm.Pool,
		"phase_generation":    tm.Generation,
		"pool_dev_adequacy":   tm.DevPass,
		"phase_authored_pass": tm.AuthoredPass,
		"phase_critic":        tm.Critic,
	}
	for kind, want := range wantDurations {
		detail, ok := byKind[kind]
		if !ok {
			t.Fatalf("no %q event emitted; got kinds %v", kind, keysOf2(byKind))
		}
		got, ok := detail["duration_ms"].(int64)
		if !ok {
			t.Fatalf("%s detail.duration_ms = %v (%T), want int64", kind, detail["duration_ms"], detail["duration_ms"])
		}
		if got != want.Milliseconds() {
			t.Errorf("%s detail.duration_ms = %d, want %d (Timing's own %v)", kind, got, want.Milliseconds(), want)
		}
	}
}

// TestTimedOutVerdictCarriesWhatItSpent: a run that stalls has still spent
// everything it spent. Dropping the clock on the timeout path would make the
// slowest runs — the ones an operator most needs to explain — the only ones
// that say nothing about where their time went.
func TestTimedOutVerdictCarriesWhatItSpent(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 8, 30, 9, 0, 0, 0, time.UTC)}
	d := &Driver{Now: clk.Now, Assign: decorrelatedAssign()}

	rs := testRunSpec()
	rs.SelectionDuration = 92 * time.Second
	rs.PoolDuration = 12 * time.Second

	// verdictFromSpec carries the two phases the DRIVER did not run: the
	// caller measured them and handed them over on the spec.
	spec := verdictFromSpec(rs)
	if spec.Timing.Selection != 92*time.Second || spec.Timing.Pool != 12*time.Second {
		t.Fatalf("verdictFromSpec Timing = %+v, want the spec's selection/pool durations", spec.Timing)
	}

	run := &runState{rs: rs, startedAt: clk.Now(), devScored: true}
	run.timing.Generation = 4 * time.Minute
	run.timing.DevPass = 35 * time.Minute
	clk.advance(43 * time.Minute)

	v := d.timeoutVerdict(run)
	if !v.TimedOut {
		t.Fatal("timeoutVerdict did not mark the verdict timed out")
	}
	if v.Timing.Generation != 4*time.Minute || v.Timing.DevPass != 35*time.Minute {
		t.Errorf("the timeout verdict dropped what the run spent: %+v", v.Timing)
	}
	if v.Timing.Selection != 92*time.Second || v.Timing.Pool != 12*time.Second {
		t.Errorf("the timeout verdict dropped the spec's own durations: %+v", v.Timing)
	}
	// The 43m the run was alive plus the 12s pool it was handed — and NOT the
	// 1m32s selection, which belongs to the scan, not to this file.
	if want := 43*time.Minute + 12*time.Second; v.Timing.Total != want {
		t.Errorf("Timing.Total = %v, want %v", v.Timing.Total, want)
	}
	if v.Timing.AuthoredPass != 0 || v.Timing.Critic != 0 {
		t.Errorf("phases that never ran reported time: %+v", v.Timing)
	}
}

// TestTimedOutVerdictAttributesAnOpenPhase is issue #201: a run whose writer
// fan-out was genuinely IN FLIGHT when RunDeadline fired used to report that
// phase as "—" — the same rendering as a phase that never ran at all —
// because timeoutVerdict only ever copied the phases that had already
// CLOSED (endPhase already called) and left anything still open in
// run.phaseStart untouched, at its zero value. A phase that opened and spent
// real wall clock before the deadline hit is not "did not run"; it is
// "ran, and did not finish" — and the invariant is that no phase that
// consumed time may render as one that did not.
func TestTimedOutVerdictAttributesAnOpenPhase(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)}
	d := &Driver{Now: clk.Now, Assign: decorrelatedAssign()}

	rs := testRunSpec()
	run := &runState{rs: rs, startedAt: clk.Now(), devScored: true, phaseStart: map[string]time.Time{}}
	run.timing.Generation = 4 * time.Minute
	run.timing.DevPass = 5 * time.Minute
	// The writer fan-out opened its phase (survivors were found and the
	// writer seat(s) were promoted) 52 minutes before the deadline fired —
	// exactly the shape from the field defect: a per-survivor writer that
	// compiled, failed on the unmutated code, and was reissued, all inside
	// this still-open window.
	d.beginPhase(run, phaseAuthored)
	clk.advance(52 * time.Minute)

	v := d.timeoutVerdict(run)
	if !v.TimedOut {
		t.Fatal("timeoutVerdict did not mark the verdict timed out")
	}
	if v.Timing.AuthoredPass != 52*time.Minute {
		t.Errorf("Timing.AuthoredPass = %v, want 52m — the phase RAN for that long even though it never converged", v.Timing.AuthoredPass)
	}
	if _, open := run.phaseStart[phaseAuthored]; open {
		t.Error("the authored phase is still open after timeoutVerdict — it must be closed so a later read cannot double-count it")
	}
	// Critic never opened at all (the writer never converged, so the run
	// never got to promote the critic) — that is a genuine "did not run"
	// and must stay zero/unmeasured.
	if v.Timing.Critic != 0 {
		t.Errorf("Timing.Critic = %v, want 0 — the critic phase never opened", v.Timing.Critic)
	}
}

// TestTimingRoundTripsThroughTheVerdictJSON: the whole Verdict is marshalled
// into the ledger's verdict_json and read back on a cache hit. A Timing that
// serialized as Go's default (nanosecond integers under Go field names) would
// be unreadable by every other consumer of that column; one that failed to
// round-trip would silently serve a reused verdict with no clock at all.
func TestTimingRoundTripsThroughTheVerdictJSON(t *testing.T) {
	v := Verdict{Timing: Timing{
		Selection: 92 * time.Second, Generation: 4 * time.Minute, Pool: 12 * time.Second,
		DevPass: 35*time.Minute + 4*time.Second, AuthoredPass: 109 * time.Second,
		Total: 43*time.Minute + 13*time.Second,
	}}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var probe struct {
		Timing map[string]int64 `json:"timing"`
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		t.Fatalf("unmarshal into the wire shape: %v", err)
	}
	if probe.Timing["selection_ms"] != 92000 || probe.Timing["dev_pass_ms"] != 2104000 {
		t.Errorf("timing wire shape = %v, want millisecond integers under snake_case keys", probe.Timing)
	}
	if _, ok := probe.Timing["critic_ms"]; ok {
		t.Error("a phase that did not run was serialized as a zero — NULL is not 0s")
	}
	var back Verdict
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Timing != v.Timing {
		t.Errorf("Timing did not round-trip: %+v vs %+v", back.Timing, v.Timing)
	}
}
