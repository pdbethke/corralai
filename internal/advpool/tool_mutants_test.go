// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/mutantsources"
	"github.com/pdbethke/corralai/internal/queue"
)

// toolFixture is two tool mutants in the shape mutantsources.ReadStryker
// produces them: IDs under t/stryker/, each with a tool-kind source.
func toolFixture() ([]adequacy.Mutant, map[string]mutantsources.MutantSource) {
	ms := []adequacy.Mutant{
		{ID: "t/stryker/7", Search: "return nil", Replace: "TOOL-MARKER-7", ParentSHA256: "abc"},
		{ID: "t/stryker/9", Search: "pw string", Replace: "TOOL-MARKER-9", ParentSHA256: "abc"},
	}
	srcs := map[string]mutantsources.MutantSource{
		"t/stryker/7": {Kind: mutantsources.KindTool, Tool: "stryker@10.0.0", Operator: "ConditionalExpression", Status: "Survived"},
		"t/stryker/9": {Kind: mutantsources.KindTool, Tool: "stryker@10.0.0", Operator: "StringLiteral", Status: "NoCoverage"},
	}
	return ms, srcs
}

func gradedIDs(ms []adequacy.Mutant) []string {
	ids := make([]string, len(ms))
	for i, m := range ms {
		ids[i] = m.ID
	}
	return ids
}

func sourceKeys(srcs map[string]mutantsources.MutantSource) []string {
	keys := make([]string, 0, len(srcs))
	for k := range srcs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TestToolMutantsJoinTheGeneratedExam is the both-sources run: the
// generator's mutants and a tool's sit ONE exam. The dev pass grades all of
// them in one call, a tool mutant that survives is handed to the writer like
// any other survivor, and the verdict says where every graded mutant came
// from — the model ones under the generator seat's model, the tool ones as
// the report stated them.
func TestToolMutantsJoinTheGeneratedExam(t *testing.T) {
	const missionID = int64(7101)
	generated := []adequacy.Mutant{
		{ID: "m1", Replace: "MODEL-MARKER-1"},
		{ID: "m2", Replace: "MODEL-MARKER-2"},
	}
	tool, srcs := toolFixture()
	rs := testRunSpec()
	rs.ToolMutants = tool
	rs.MutantSources = srcs

	scorer := &fakeScorer{devKillRate: 0.5, devSurvivors: []adequacy.Mutant{tool[0], generated[1]}}
	validator := &fakeValidator{mutants: generated}
	d := newTestDriverWithSpec(t, missionID, scorer, validator, 0.1, rs)

	ready := claimAllReady(t, d.Q)
	tc, mg := ready[RoleTestCritic], ready[RoleMutantGenerator]
	if tc == nil || mg == nil {
		t.Fatalf("a both-sources run still seats the generator; ready: %v", keysOf(ready))
	}
	mustComplete(t, d.Q, tc.ID, "critic findings filed")
	mustComplete(t, d.Q, mg.ID, "raw mutants")
	if _, err := d.Tick(context.Background(), missionID); err != nil {
		t.Fatalf("Tick (dev-adequacy): %v", err)
	}

	if len(scorer.calls) != 1 {
		t.Fatalf("dev pass made %d score call(s), want ONE exam for both sources", len(scorer.calls))
	}
	want := []string{"m1", "m2", "t/stryker/7", "t/stryker/9"}
	if got := gradedIDs(scorer.calls[0].mutants); !reflect.DeepEqual(got, want) {
		t.Fatalf("dev pass graded %v, want %v (generated first, then the tool's)", got, want)
	}

	live, err := d.Q.TaskByID(d.runs[missionID].testWriterTaskID)
	if err != nil || live == nil {
		t.Fatalf("no live test-writer task: %v", err)
	}
	if live.Status != queue.StatusReady {
		t.Fatalf("test-writer status = %s, want ready", live.Status)
	}
	if !strings.Contains(live.Instruction, "TOOL-MARKER-7") {
		t.Fatalf("the surviving tool mutant never reached the writer:\n%s", live.Instruction)
	}

	tw := claimTaskByID(t, d.Q, live.ID)
	mustComplete(t, d.Q, tw.ID, "pool test source")
	v, err := d.Tick(context.Background(), missionID)
	if err != nil || v == nil {
		t.Fatalf("Tick (pool + aggregate): v=%v err=%v", v, err)
	}
	wantSrcs := map[string]mutantsources.MutantSource{
		"m1":          {Kind: mutantsources.KindModel, Model: "model-gen"},
		"m2":          {Kind: mutantsources.KindModel, Model: "model-gen"},
		"t/stryker/7": srcs["t/stryker/7"],
		"t/stryker/9": srcs["t/stryker/9"],
	}
	if !reflect.DeepEqual(v.MutantSources, wantSrcs) {
		t.Fatalf("Verdict.MutantSources = %+v\nwant %+v", v.MutantSources, wantSrcs)
	}
}

// TestToolMutantsGradeWhenTheGeneratorProducedNothing: a tool source is a
// source. A generator that parsed cleanly and produced nothing, or whose only
// region was dropped, leaves the tool's mutants as the whole exam — the
// no-usable-mutants guard is about the exam, not about the model.
func TestToolMutantsGradeWhenTheGeneratorProducedNothing(t *testing.T) {
	t.Run("zero generated", func(t *testing.T) {
		const missionID = int64(7102)
		tool, srcs := toolFixture()
		rs := testRunSpec()
		rs.ToolMutants, rs.MutantSources = tool, srcs
		scorer := &fakeScorer{devKillRate: 1.0}
		d := newTestDriverWithSpec(t, missionID, scorer, &fakeValidator{}, 0.1, rs)
		for _, task := range claimAllReady(t, d.Q) {
			mustComplete(t, d.Q, task.ID, "raw")
		}
		if _, err := d.Tick(context.Background(), missionID); err != nil {
			t.Fatalf("Tick: %v — a run with tool mutants has an exam even when the model planted none", err)
		}
		if len(scorer.calls) != 1 || !reflect.DeepEqual(gradedIDs(scorer.calls[0].mutants), []string{"t/stryker/7", "t/stryker/9"}) {
			t.Fatalf("dev pass calls %+v, want one call grading the two tool mutants", scorer.calls)
		}
	})

	t.Run("every region dropped", func(t *testing.T) {
		const missionID = int64(7103)
		tool, srcs := toolFixture()
		rs := testRunSpec()
		rs.ToolMutants, rs.MutantSources = tool, srcs
		scorer := &fakeScorer{devKillRate: 1.0}
		validator := &fakeValidator{parseErr: errors.New("unparseable")}
		d := newTestDriverWithSpec(t, missionID, scorer, validator, 0.1, rs)
		completeAll := func() {
			for _, task := range claimAllReady(t, d.Q) {
				mustComplete(t, d.Q, task.ID, "junk")
			}
		}
		completeAll()
		for i := 0; i < MaxShardRetries; i++ {
			if _, err := d.Tick(context.Background(), missionID); err == nil {
				t.Fatalf("Tick %d: want a retry error while the region has budget left", i)
			}
			completeAll()
		}
		if _, err := d.Tick(context.Background(), missionID); err != nil {
			t.Fatalf("Tick after the only region dropped: %v — the tool's mutants still grade", err)
		}
		run := d.runs[missionID]
		if len(run.droppedRegions) != 1 || !run.devScored {
			t.Fatalf("dropped=%v devScored=%v, want the region dropped and the tool exam scored", run.droppedRegions, run.devScored)
		}
		if got := sourceKeys(run.mutantSources); !reflect.DeepEqual(got, []string{"t/stryker/7", "t/stryker/9"}) {
			t.Fatalf("mutantSources keys %v, want only the tool mutants that were graded", got)
		}
	})
}

// TestToolOnlyRunUsesPresetMutantsAndSpendsNoGeneration: --mutant-model off
// with a tool source is the replay door (TM-R1). No generator seat exists, no
// generation phase is timed, the exam is exactly the tool's mutants, and the
// verdict's sources name the tool and nothing else — no model entry for a
// seat that never sat.
func TestToolOnlyRunUsesPresetMutantsAndSpendsNoGeneration(t *testing.T) {
	const missionID = int64(7104)
	tool, srcs := toolFixture()
	rs := testRunSpec()
	rs.PresetMutants = tool
	rs.MutantSources = srcs
	scorer := &fakeScorer{devKillRate: 0.5, devSurvivors: []adequacy.Mutant{tool[1]}}
	d := newTestDriverWithSpec(t, missionID, scorer, &fakeValidator{mutants: []adequacy.Mutant{{ID: "generated", Replace: "must-not-appear"}}}, 0.1, rs)

	for _, role := range []string{RoleMutantGenerator, RoleMutantGeneratorShadow} {
		got, err := d.tasksByRole(missionID, role)
		if err != nil {
			t.Fatalf("tasksByRole(%s): %v", role, err)
		}
		if len(got) != 0 {
			t.Fatalf("a tool-only run seeded %d %s task(s)", len(got), role)
		}
	}
	v := drivePresetToConvergence(t, d, missionID)
	if got := gradedIDs(scorer.calls[0].mutants); !reflect.DeepEqual(got, []string{"t/stryker/7", "t/stryker/9"}) {
		t.Fatalf("dev pass graded %v, want exactly the tool's mutants", got)
	}
	if v.Timing.Generation != 0 {
		t.Errorf("Timing.Generation = %v on a run that generated nothing", v.Timing.Generation)
	}
	if !reflect.DeepEqual(v.MutantSources, srcs) {
		t.Fatalf("Verdict.MutantSources = %+v, want exactly the tool's sources %+v", v.MutantSources, srcs)
	}
}

// TestPresetReplayWithoutSourcesSignsNone: a --mutants replay predates this
// run and its provenance is the run that recorded it. With no sources handed
// in, the verdict carries none rather than inventing a "model" claim.
func TestPresetReplayWithoutSourcesSignsNone(t *testing.T) {
	const missionID = int64(7105)
	rs := testRunSpec()
	rs.PresetMutants = []adequacy.Mutant{{ID: "p1", Replace: "preset-one", ParentSHA256: "abc"}}
	scorer := &fakeScorer{devKillRate: 0.0, devSurvivors: rs.PresetMutants}
	d := newTestDriverWithSpec(t, missionID, scorer, &fakeValidator{}, 0.1, rs)
	v := drivePresetToConvergence(t, d, missionID)
	if v.MutantSources != nil {
		t.Fatalf("Verdict.MutantSources = %+v on a replay nobody said the provenance of", v.MutantSources)
	}
}

// TestToolMutantIdenticalToAModelMutantIsCountedNotDropped: dedupe COUNTS an
// identical hunk and the scorer grades it once and attributes the answer to
// both (adequacy.DedupeMutants, adequacy.Score) — nothing leaves the exam. So
// a tool mutant identical to a model one stays, with its own source, which is
// exactly the overlap the statistics want; and no source is carried for an ID
// the exam does not hold.
func TestToolMutantIdenticalToAModelMutantIsCountedNotDropped(t *testing.T) {
	const missionID = int64(7106)
	same := adequacy.Mutant{Search: "return nil", Replace: "return errors.New(\"x\")", ParentSHA256: "abc"}
	model := same
	model.ID = "m1"
	tool := same
	tool.ID = "t/stryker/3"
	rs := testRunSpec()
	rs.ToolMutants = []adequacy.Mutant{tool}
	rs.MutantSources = map[string]mutantsources.MutantSource{
		"t/stryker/3": {Kind: mutantsources.KindTool, Tool: "stryker@10.0.0", Operator: "BlockStatement", Status: "Survived"},
	}
	scorer := &fakeScorer{devKillRate: 1.0}
	d := newTestDriverWithSpec(t, missionID, scorer, &fakeValidator{mutants: []adequacy.Mutant{model}}, 0.1, rs)
	for _, task := range claimAllReady(t, d.Q) {
		mustComplete(t, d.Q, task.ID, "raw")
	}
	if _, err := d.Tick(context.Background(), missionID); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	run := d.runs[missionID]
	if run.dupMutants != 1 {
		t.Errorf("dupMutants = %d, want the identical pair counted once", run.dupMutants)
	}
	exam := gradedIDs(scorer.calls[0].mutants)
	sort.Strings(exam)
	if got := sourceKeys(run.mutantSources); !reflect.DeepEqual(got, exam) {
		t.Fatalf("mutantSources keys %v, want exactly the exam's IDs %v", got, exam)
	}
	if run.mutantSources["m1"].Kind != mutantsources.KindModel || run.mutantSources["t/stryker/3"].Kind != mutantsources.KindTool {
		t.Fatalf("sources %+v: the model mutant must stay model, the tool's stay tool", run.mutantSources)
	}
}

// TestTimeoutVerdictCarriesMutantSources pins the field on the SECOND Verdict
// construction site (AGENTS.md "Verdict is constructed in two places"): a run
// that graded its exam and only then stalled must still sign where its
// mutants came from.
func TestTimeoutVerdictCarriesMutantSources(t *testing.T) {
	_, srcs := toolFixture()
	d := &Driver{}
	run := &runState{
		rs:            RunSpec{Repo: "r", Commit: "c", Lang: "go"},
		devScored:     true,
		mutantSources: srcs,
	}
	v := d.timeoutVerdict(run)
	if !reflect.DeepEqual(v.MutantSources, srcs) {
		t.Fatalf("timeout Verdict.MutantSources = %+v, want %+v", v.MutantSources, srcs)
	}
}

// TestStartRunRefusesAnIncoherentMutantSourceSpec: the combinations cmd/ never
// builds are programming errors, refused before anything is enqueued rather
// than resolved by a rule nobody chose.
func TestStartRunRefusesAnIncoherentMutantSourceSpec(t *testing.T) {
	tool, srcs := toolFixture()
	cases := map[string]func(rs *RunSpec){
		"preset and tool mutants both": func(rs *RunSpec) {
			rs.PresetMutants, rs.ToolMutants, rs.MutantSources = tool, tool, srcs
		},
		"tool mutant without a source": func(rs *RunSpec) {
			rs.ToolMutants = tool
			rs.MutantSources = map[string]mutantsources.MutantSource{"t/stryker/7": srcs["t/stryker/7"]}
		},
		"tool mutant with a model source": func(rs *RunSpec) {
			rs.ToolMutants = tool[:1]
			rs.MutantSources = map[string]mutantsources.MutantSource{"t/stryker/7": {Kind: mutantsources.KindModel, Model: "x"}}
		},
		"source for a mutant the run was not given": func(rs *RunSpec) {
			rs.ToolMutants = tool
			rs.MutantSources = map[string]mutantsources.MutantSource{"t/stryker/7": srcs["t/stryker/7"], "t/stryker/9": srcs["t/stryker/9"], "m1": {Kind: mutantsources.KindModel}}
		},
		"preset with sources missing one": func(rs *RunSpec) {
			rs.PresetMutants = tool
			rs.MutantSources = map[string]mutantsources.MutantSource{"t/stryker/7": srcs["t/stryker/7"]}
		},
		"source with an unknown kind": func(rs *RunSpec) {
			rs.PresetMutants = tool[:1]
			rs.MutantSources = map[string]mutantsources.MutantSource{"t/stryker/7": {Kind: "oracle"}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			d, err := NewDriver(newTestQueue(t), &fakeScorer{}, &fakeValidator{}, decorrelatedAssign(), 0.1)
			if err != nil {
				t.Fatal(err)
			}
			rs := testRunSpec()
			mutate(&rs)
			if err := d.StartRun(1, rs, nil); err == nil {
				t.Fatal("StartRun accepted it")
			}
			if tasks, _ := d.Q.List(1); len(tasks) != 0 {
				t.Fatalf("refused run enqueued %d task(s)", len(tasks))
			}
		})
	}
}

// TestGeneratedIDCollidingWithAToolIDIsRefused: an ID is a mutant's identity
// on the record, so a generated mutant carrying a tool mutant's ID would make
// two edits indistinguishable. Generated IDs are corral-assigned (m<n>,
// s<k>/m<n>) and cannot reach t/, so this is a guard, not a path.
func TestGeneratedIDCollidingWithAToolIDIsRefused(t *testing.T) {
	const missionID = int64(7107)
	tool, srcs := toolFixture()
	rs := testRunSpec()
	rs.ToolMutants, rs.MutantSources = tool, srcs
	collide := adequacy.Mutant{ID: "t/stryker/7", Replace: "model-made"}
	d := newTestDriverWithSpec(t, missionID, &fakeScorer{devKillRate: 1.0}, &fakeValidator{mutants: []adequacy.Mutant{collide}}, 0.1, rs)
	for _, task := range claimAllReady(t, d.Q) {
		mustComplete(t, d.Q, task.ID, "raw")
	}
	if _, err := d.Tick(context.Background(), missionID); err == nil || !strings.Contains(err.Error(), "t/stryker/7") {
		t.Fatalf("Tick err = %v, want a refusal naming the colliding ID", err)
	}
}
