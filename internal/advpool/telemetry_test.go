// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/queue"
)

// telemetryRecorder captures every RunTelemetry the driver hands its
// Telemetry callback, so a test can assert both WHAT arrived and HOW OFTEN.
type telemetryRecorder struct {
	calls []RunTelemetry
}

func (r *telemetryRecorder) record(t RunTelemetry) { r.calls = append(r.calls, t) }

// TestTelemetryDeliveredWithoutRecord is the defect this callback exists to
// close: `certify --repo` wires no Signer, so RecordID stays 0 and every
// RecordID-gated sink stays silent. The run's measurements must still reach
// the caller.
func TestTelemetryDeliveredWithoutRecord(t *testing.T) {
	d, missionID := newScoredRun(t, scoredRun{
		survivors: 2, provenMissed: 1, mutantsTotal: 4, vacuous: 1,
		writerModel: "claude-sonnet-5", criticModel: "gemini-3.5-flash",
	})
	d.Signer = nil // the case under test: no build store, no record
	rec := &telemetryRecorder{}
	d.Telemetry = rec.record

	v := drivePoolToConvergence(t, d, missionID)
	if v.RecordID != 0 {
		t.Fatalf("RecordID = %d, want 0 (no Signer wired)", v.RecordID)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("Telemetry called %d times, want 1", len(rec.calls))
	}
	got := rec.calls[0]
	if len(got.Seats) == 0 {
		t.Fatal("Telemetry delivered no seat observations for a run that measured them")
	}
	tw, ok := obsFor(got.Seats, RoleTestWriter)
	if !ok || tw.Catches != 1 || tw.Opportunities != 2 {
		t.Fatalf("test-writer seat = %+v, want catches=1 opportunities=2", tw)
	}
	// The critic's finding was adjudicated before the verdict was signed (or,
	// here, not signed); it must ride the callback rather than be computed
	// and dropped, which is what happened to it with no record.
	if len(got.CriticFindings) != 1 {
		t.Fatalf("CriticFindings = %+v, want the one adjudicated finding", got.CriticFindings)
	}
}

// fakeCriticSinkWithID is fakeCriticSink plus the record id, so a test can
// prove the sink was fed under a real record.
type fakeCriticSinkWithID struct {
	recordID int64
	obs      []CriticFindingObservation
}

func (f *fakeCriticSinkWithID) Record(id int64, _ string, o []CriticFindingObservation) {
	f.recordID = id
	f.obs = append(f.obs, o...)
}

// TestTelemetryMatchesSinks proves the callback and the record-keyed sinks
// read ONE computation: with a Signer and all three sinks wired, what the
// callback receives is exactly what the sinks received. A second copy of the
// row construction would be free to drift from the first.
func TestTelemetryMatchesSinks(t *testing.T) {
	rs := newTestRunSpec(t)
	rs.ShadowWriterModel = "challenger-model" // the measured challenger path, so MutantAttempts has rows
	missionID := writerShadowNextMissionID
	writerShadowNextMissionID++
	d := newWriterShadowRun(t, missionID, rs, &writerShadowScorer{}, &fakeValidator{mutants: writerShadowMutants()})

	bc := &fakeBugCatch{}
	ma := &fakeMutantAttemptSink{}
	cf := &fakeCriticSinkWithID{}
	d.BugCatch, d.MutantAttempts, d.CriticFindings = bc, ma, cf
	rec := &telemetryRecorder{}
	d.Telemetry = rec.record

	// File one critic finding before the critic task completes, so the
	// critic-findings sink has something to receive.
	ready := claimAllReady(t, d.Q)
	tc := ready[RoleTestCritic]
	if tc == nil {
		t.Fatalf("expected a ready test-critic task, got: %v", keysOf(ready))
	}
	if _, err := d.Q.AddFinding(queue.Finding{
		MissionID: missionID, TaskID: tc.ID, Reporter: "test-critic", Type: "bug",
		Severity: "high", Target: "TestVacuous", Evidence: "asserts nothing",
	}); err != nil {
		t.Fatalf("AddFinding: %v", err)
	}
	for _, task := range ready {
		result := primaryWriterResultTag
		if task.Role == RoleTestWriterShadow {
			result = shadowWriterResultTag
		}
		mustComplete(t, d.Q, task.ID, result)
	}
	v := driveWriterShadow(t, d, missionID)
	if v.RecordID == 0 {
		t.Fatal("setup: the verdict was not signed, so the sinks cannot have been fed")
	}

	if len(rec.calls) != 1 {
		t.Fatalf("Telemetry called %d times, want 1", len(rec.calls))
	}
	got := rec.calls[0]
	// Each sink must actually have received rows, or DeepEqual of two empty
	// slices proves nothing about sharing a computation.
	if len(bc.obs) == 0 || len(ma.attempts) == 0 || len(cf.obs) == 0 {
		t.Fatalf("setup: a sink was not fed (bugcatch=%d attempts=%d critic=%d)", len(bc.obs), len(ma.attempts), len(cf.obs))
	}
	if !reflect.DeepEqual(got.Seats, bc.obs) {
		t.Errorf("Seats differ from what BugCatch received:\n got %+v\nsink %+v", got.Seats, bc.obs)
	}
	if !reflect.DeepEqual(got.MutantAttempts, ma.attempts) {
		t.Errorf("MutantAttempts differ from what the sink received:\n got %+v\nsink %+v", got.MutantAttempts, ma.attempts)
	}
	if !reflect.DeepEqual(got.CriticFindings, cf.obs) {
		t.Errorf("CriticFindings differ from what the sink received:\n got %+v\nsink %+v", got.CriticFindings, cf.obs)
	}
}

// TestTelemetryOnTimeout proves a run that ends through timeoutVerdict — the
// second Verdict construction site — still hands its measurements over
// exactly once, and hands over ONLY what it measured.
func TestTelemetryOnTimeout(t *testing.T) {
	t.Run("nothing scored", func(t *testing.T) {
		const mission int64 = 4201
		q := newTestQueue(t)
		clk := &fakeClock{t: time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)}
		d, err := NewDriver(q, &fakeScorer{devKillRate: 0.9}, &fakeValidator{}, decorrelatedAssign(), 0.5)
		if err != nil {
			t.Fatalf("NewDriver: %v", err)
		}
		d.Now = clk.Now
		d.RunDeadline = 10 * time.Minute
		rec := &telemetryRecorder{}
		d.Telemetry = rec.record
		if err := d.StartRun(mission, testRunSpec(), nil); err != nil {
			t.Fatalf("StartRun: %v", err)
		}
		clk.advance(d.RunDeadline + time.Second)
		v, err := d.Tick(context.Background(), mission)
		if err != nil || v == nil {
			t.Fatalf("deadline Tick: v=%v err=%v", v, err)
		}
		if _, err := d.Tick(context.Background(), mission); err != nil {
			t.Fatalf("second Tick: %v", err)
		}
		if len(rec.calls) != 1 {
			t.Fatalf("Telemetry called %d times, want exactly 1", len(rec.calls))
		}
		// Nothing was scored, so nothing may be reported: a mutant-generator
		// row of "planted 0" or a writer row of "authored 1, sound 0" here
		// would be a measurement nobody took.
		got := rec.calls[0]
		if len(got.Seats) != 0 || len(got.MutantAttempts) != 0 || len(got.CriticFindings) != 0 {
			t.Fatalf("a run that scored nothing reported %+v", got)
		}
	})

	t.Run("dev scored, writer never finished", func(t *testing.T) {
		const mission int64 = 4202
		survivors := []adequacy.Mutant{{ID: "m1", Replace: "c1"}}
		scorer := &fakeScorer{devKillRate: 0.5, devSurvivors: survivors}
		validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m0", Replace: "c0"}, survivors[0]}}
		d, _ := newTestDriver(t, mission, scorer, validator, 0.5)
		d.Signer = &fakeSigner{}
		clk := &fakeClock{t: time.Now()}
		d.Now = clk.Now
		d.RunDeadline = 10 * time.Minute
		rec := &telemetryRecorder{}
		d.Telemetry = rec.record

		ready := claimAllReady(t, d.Q)
		mg := ready[RoleMutantGenerator]
		if mg == nil {
			t.Fatalf("expected a ready mutant-generator task, got: %v", keysOf(ready))
		}
		mustComplete(t, d.Q, mg.ID, "raw mutants")
		if _, err := d.Tick(context.Background(), mission); err != nil {
			t.Fatalf("Tick (dev-adequacy): %v", err)
		}
		if run := d.runs[mission]; !run.devScored || run.poolScored {
			t.Fatalf("setup: want dev scored and pool not, got dev=%v pool=%v", run.devScored, run.poolScored)
		}
		if len(rec.calls) != 0 {
			t.Fatalf("Telemetry fired before the run finished: %+v", rec.calls)
		}

		clk.advance(d.RunDeadline + time.Minute)
		v, err := d.Tick(context.Background(), mission)
		if err != nil || v == nil || v.Status != StatusNeedsReview {
			t.Fatalf("deadline Tick: v=%+v err=%v", v, err)
		}
		if len(rec.calls) != 1 {
			t.Fatalf("Telemetry called %d times, want 1", len(rec.calls))
		}
		got := rec.calls[0]
		mgRow, ok := obsFor(got.Seats, RoleMutantGenerator)
		if !ok || mgRow.MutantsPlanted != 2 || mgRow.MutantsSurvived != 1 {
			t.Fatalf("mutant-generator seat = %+v (found=%v), want planted=2 survived=1 — the generator WAS measured", mgRow, ok)
		}
		if tw, ok := obsFor(got.Seats, RoleTestWriter); ok {
			t.Fatalf("a writer that never finished got a seat row %+v — that is a charge for a test it was never allowed to write", tw)
		}
		if cr, ok := obsFor(got.Seats, RoleTestCritic); ok {
			t.Fatalf("the timeout path never reads the critic's findings, yet it got a seat row %+v", cr)
		}
		if len(got.CriticFindings) != 0 || len(got.MutantAttempts) != 0 {
			t.Fatalf("unmeasured critic findings / attempts reported: %+v", got)
		}
	})
}

// TestTelemetryCalledOncePerRun proves Tick's idempotence covers the
// callback: once a run has a verdict, further Ticks return it and must not
// hand the same measurements over again (a second delivery would count one
// run twice wherever the callback lands).
func TestTelemetryCalledOncePerRun(t *testing.T) {
	d, missionID := newScoredRun(t, scoredRun{
		survivors: 2, provenMissed: 1, mutantsTotal: 4,
		writerModel: "claude-sonnet-5", criticModel: "gemini-3.5-flash",
	})
	rec := &telemetryRecorder{}
	d.Telemetry = rec.record
	drivePoolToConvergence(t, d, missionID)
	for i := 0; i < 3; i++ {
		if _, err := d.Tick(context.Background(), missionID); err != nil {
			t.Fatalf("Tick after convergence: %v", err)
		}
	}
	if len(rec.calls) != 1 {
		t.Fatalf("Telemetry called %d times across one run, want 1", len(rec.calls))
	}
}
