// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
)

// drivePresetToConvergence is drivePoolToConvergence's counterpart for a run
// with RunSpec.PresetMutants: there is NO mutant-generator seat to claim or
// complete (that is the whole point of a preset run), so the shared helper —
// which fatals when one is missing — cannot be reused here.
func drivePresetToConvergence(t *testing.T, d *Driver, missionID int64) *Verdict {
	t.Helper()
	ctx := context.Background()

	ready := claimAllReady(t, d.Q)
	tc := ready[RoleTestCritic]
	if tc == nil {
		t.Fatalf("expected test-critic ready, got: %v", keysOf(ready))
	}
	if mg := ready[RoleMutantGenerator]; mg != nil {
		t.Fatal("a preset run must not seed a mutant-generator seat")
	}
	mustComplete(t, d.Q, tc.ID, "critic findings filed")
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

// TestPresetMutantsReplaceTheGeneratorSeat is the replay half of
// `--mutants`: a run handed a fixed mutant set must generate nothing at all
// (no generator seat, no shadow-generator seat, no model call) and must grade
// the dev suite against EXACTLY the mutants it was handed — the invariant a
// concurrency claim can only be proven on.
func TestPresetMutantsReplaceTheGeneratorSeat(t *testing.T) {
	preset := []adequacy.Mutant{
		{ID: "p1", Code: "preset-one", ParentSHA256: "abc"},
		{ID: "p2", Code: "preset-two", ParentSHA256: "abc"},
	}
	rs := testRunSpec()
	rs.PresetMutants = preset
	// A shadow model is named deliberately: the shadow GENERATOR fans out over
	// the same shards the primary does, so if the preset branch only silenced
	// the primary the challenger would still spend a model call per region on
	// mutants the run has already been given.
	rs.ShadowModel = "model-shadow"

	scorer := &fakeScorer{devKillRate: 0.5, devSurvivors: []adequacy.Mutant{preset[1]}}
	// Deliberately loaded with mutants the validator would return if anything
	// ever asked it to parse: nothing must, so these must never be scored.
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "generated", Code: "must-not-appear"}}}
	d := newTestDriverWithSpec(t, 41, scorer, validator, 0.1, rs)

	var sinkCalls int
	var sinkPath string
	var sunk []adequacy.Mutant
	d.MutantSink = func(codePath string, ms []adequacy.Mutant) {
		sinkCalls++
		sinkPath = codePath
		sunk = ms
	}

	for _, role := range []string{RoleMutantGenerator, RoleMutantGeneratorShadow} {
		got, err := d.tasksByRole(41, role)
		if err != nil {
			t.Fatalf("tasksByRole(%s): %v", role, err)
		}
		if len(got) != 0 {
			t.Fatalf("a preset run seeded %d %s task(s); a replayed mutant set must cost no generation at all", len(got), role)
		}
	}

	drivePresetToConvergence(t, d, 41)

	if len(scorer.calls) == 0 {
		t.Fatal("the dev pass never scored anything")
	}
	graded := scorer.calls[0].mutants
	if len(graded) != len(preset) {
		t.Fatalf("dev pass graded %d mutant(s) (%+v), want exactly the %d preset mutants", len(graded), graded, len(preset))
	}
	for i := range preset {
		if graded[i].ID != preset[i].ID || graded[i].Code != preset[i].Code {
			t.Fatalf("dev pass mutant[%d] = %+v, want the preset %+v", i, graded[i], preset[i])
		}
	}

	if sinkCalls != 1 {
		t.Fatalf("MutantSink called %d time(s), want exactly once per run", sinkCalls)
	}
	if sinkPath != rs.CodePath {
		t.Fatalf("MutantSink got codePath %q, want %q", sinkPath, rs.CodePath)
	}
	if len(sunk) != len(preset) {
		t.Fatalf("MutantSink got %d mutant(s) (%+v), want the %d graded preset mutants", len(sunk), sunk, len(preset))
	}
}

// TestMutantSinkRecordsGeneratedMutants is the record half of
// `--record-mutants`: on an ORDINARY run (no preset) the sink is fed the
// mutants the generator produced, minus anything the compile gate rejected —
// otherwise a recorded set could not be replayed, because it would name
// mutants the scorer refused to grade.
func TestMutantSinkRecordsGeneratedMutants(t *testing.T) {
	generated := []adequacy.Mutant{
		{ID: "m1", Code: "c1"},
		{ID: "m2", Code: "c2"},
		{ID: "m3", Code: "c3"},
	}
	scorer := &fakeScorer{
		devKillRate:  0.5,
		devSurvivors: []adequacy.Mutant{generated[1]},
		devInvalid:   []string{"m3"}, // rejected by the compile gate, never graded
	}
	validator := &fakeValidator{mutants: generated}
	d, _ := newTestDriver(t, 42, scorer, validator, 0.1)

	var sinkCalls int
	var sunk []adequacy.Mutant
	d.MutantSink = func(codePath string, ms []adequacy.Mutant) {
		sinkCalls++
		sunk = ms
	}

	mgs, err := d.tasksByRole(42, RoleMutantGenerator)
	if err != nil {
		t.Fatalf("tasksByRole: %v", err)
	}
	if len(mgs) == 0 {
		t.Fatal("an ordinary run must still seed a mutant-generator seat")
	}
	mg := claimByKey(t, d.Q, RoleMutantGenerator)
	mustComplete(t, d.Q, mg.ID, "raw mutants")
	if _, err := d.Tick(context.Background(), 42); err != nil {
		t.Fatalf("Tick (dev-adequacy): %v", err)
	}

	if sinkCalls != 1 {
		t.Fatalf("MutantSink called %d time(s), want exactly once", sinkCalls)
	}
	ids := make([]string, len(sunk))
	for i, m := range sunk {
		ids[i] = m.ID
	}
	if len(sunk) != 2 || ids[0] != "m1" || ids[1] != "m2" {
		t.Fatalf("MutantSink got %v, want the GRADED mutants [m1 m2] (m3 failed the compile gate)", ids)
	}
}
