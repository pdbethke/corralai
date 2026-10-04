// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
)

// obsRun builds the runState bugCatchObservations reads for a run with two dev
// survivors, "m1" and "m2".
func obsRun() *runState {
	return &runState{
		poolScored:            true,
		primaryWriterMeasured: true,
		devSurvivors:          []adequacy.Mutant{{ID: "m1"}, {ID: "m2"}},
	}
}

func obsVerdict() Verdict {
	return Verdict{Survivors: 2, ModelsByRole: map[string]string{RoleTestWriter: "w", RoleMutantGenerator: "g"}}
}

func TestBugCatchObservationsStampLang(t *testing.T) {
	run := obsRun()
	run.rs.Lang = "python"
	run.shadowWriterMeasured = true
	run.rs.ShadowWriterModel = "challenger"
	obs := bugCatchObservations(run, obsVerdict())
	if len(obs) == 0 {
		t.Fatal("no observations")
	}
	for _, o := range obs {
		if o.Lang != "python" {
			t.Fatalf("row %s/%s has Lang %q, want python", o.Model, o.Role, o.Lang)
		}
	}
}

// The challenger writer used to write NO scorecard row: its outcomes went
// only to mutant_attempts. Without a row, a drawn challenger writer's
// posterior could never move.
func TestBugCatchObservationsEmitChallengerWriterRow(t *testing.T) {
	run := obsRun()
	run.rs.ShadowWriterModel = "challenger"
	run.writerMode = WriterModeBatched
	run.shadowWriterMeasured = true
	run.shadowWriterKilled = []MutantRef{{ID: "m1"}}
	var row *BugCatchObservation
	obs := bugCatchObservations(run, obsVerdict())
	for i := range obs {
		if obs[i].Role == RoleTestWriterShadow {
			row = &obs[i]
		}
	}
	if row == nil || row.Model != "challenger" || !row.Shadow || row.Catches != 1 || row.Opportunities != 2 || row.Dropped {
		t.Fatalf("challenger writer row = %+v, want challenger 1/2 shadow, not dropped", row)
	}
}

func TestBugCatchObservationsChallengerWriterUnmeasuredIsDropped(t *testing.T) {
	run := obsRun()
	run.rs.ShadowWriterModel = "challenger"
	run.writerMode = WriterModeBatched
	run.shadowWriterMeasured = false
	seen := false
	for _, o := range bugCatchObservations(run, obsVerdict()) {
		if o.Role == RoleTestWriterShadow {
			seen = true
			if !o.Dropped || o.Catches != 0 || o.Opportunities != 0 {
				t.Fatalf("an unmeasured challenger seat must be a DROPPED row with no counts, got %+v", o)
			}
		}
	}
	if !seen {
		t.Fatal("no challenger writer row at all for a staffed, unmeasured seat")
	}
}
