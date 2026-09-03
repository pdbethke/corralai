// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
)

// TestVerdictCarriesTheProvenEvidence closes the observability gap that made a
// real "tried and missed" undebuggable on 2026-07-31: a paid pallets/flask
// audit produced a sound, collected, genuinely-grading authored test that
// killed 0 of 10 survivors, and the ENTIRE record of that attempt was the
// integer 0 in a 43-line log. `certify --repo` has no tape flag (its --record
// is the ledger bool; the tape belongs to `certify --local`), so nothing
// retained WHICH survivors were attempted, or what the test looked like — the
// only way to learn more was to pay for another run and hope.
//
// ProvenMissed is a count, and a count is not evidence. The verdict must also
// carry the authored test itself and the IDs it actually killed, so a later
// query can tell "killed these three, missed those seven" from a bare 0.
func TestVerdictCarriesTheProvenEvidence(t *testing.T) {
	// m1 and m3 die to the authored test; m2 survives it.
	mutants := []adequacy.Mutant{
		{ID: "m1", Replace: "MUT1"},
		{ID: "m2", Replace: "MUT2"},
		{ID: "m3", Replace: "MUT3"},
	}
	run := &runState{
		rs:           RunSpec{CodePath: "src/flask/cli.py", Lang: "python"},
		devSurvivors: mutants,
		authoredTest: "AUTHORED-TEST-SOURCE",
	}
	// The report names its kills, as the real scorer's does. This fixture
	// used to list only the survivor and let the driver INFER the kills by
	// subtraction — the arithmetic that counted an unmeasured mutant as a
	// proven gap. Proven now means "present in Killed", so a fixture has to
	// say who was killed, exactly as a real report would.
	rep := adequacy.Report{
		CompliantPass: true,
		CanaryKilled:  true,
		Total:         3,
		Killed:        []string{"m1", "m3"},
		Survived:      []string{"m2"}, // only m2 outlived the authored test
	}

	provenIDs := provenMutantIDs(rep, run.devSurvivors)
	if len(provenIDs) != 2 || provenIDs[0] != "m1" || provenIDs[1] != "m3" {
		t.Fatalf("provenMutantIDs = %v, want [m1 m3] — the survivors the authored test actually killed", provenIDs)
	}

	// And the count must stay consistent with the evidence: a list that
	// disagrees with ProvenMissed would be worse than no list at all.
	poolSurvivors := survivorsFrom(rep, run.devSurvivors)
	if got, want := len(run.devSurvivors)-len(poolSurvivors), len(provenIDs); got != want {
		t.Fatalf("ProvenMissed would be %d but the evidence lists %d ids — a count that contradicts its own evidence", got, want)
	}
}

// TestProvenMutantIDs_EmptyOnATriedAndMissed pins the case that motivated all
// of this: a sound run that killed nothing yields an EMPTY id list, not nil
// confusion — and crucially is distinguishable from a run that never graded,
// which never reaches this path at all (PoolTestUnsound handles that).
func TestProvenMutantIDs_EmptyOnATriedAndMissed(t *testing.T) {
	mutants := []adequacy.Mutant{{ID: "m1"}, {ID: "m2"}}
	rep := adequacy.Report{
		CompliantPass: true, CanaryKilled: true, Total: 2,
		Survived: []string{"m1", "m2"}, // the authored test killed neither
	}
	if got := provenMutantIDs(rep, mutants); len(got) != 0 {
		t.Fatalf("provenMutantIDs on a tried-and-missed = %v, want empty", got)
	}
}

// The end-to-end wiring (tickPoolAdequacy populating runState, and
// tickAggregate carrying it onto the Verdict) is asserted inside the existing
// TestTick_PoolAdequacy_ScoresProvenMissed harness in driver_test.go rather
// than rebuilt here with a second set of fakes.

// AN UNMEASURED SURVIVOR IS NOT A PROVEN GAP. The old subtraction
// (len(devSurvivors) - len(Survived)) credited as proven every survivor the
// authored pass failed to grade — and the authored pass can fail to grade one
// with no error at all, because its command fails on the compliant code
// (pytest exit 5 for a file it does not collect). Reproduced with real pytest:
// a genuinely unobservable mutant was signed as a gap the authored test proved.
func TestUnmeasuredSurvivorsAreNotProven(t *testing.T) {
	mutants := []adequacy.Mutant{{ID: "m1"}, {ID: "m2"}, {ID: "m3"}}
	rep := adequacy.Report{
		CompliantPass: true,
		CanaryKilled:  true,
		Total:         1,
		Killed:        []string{"m1"},
		Unmeasured:    []string{"m2", "m3"},
		UnmeasuredReasons: map[string]string{
			"m2": "the command that would grade this mutant FAILS ON THE COMPLIANT CODE",
			"m3": "the command that would grade this mutant FAILS ON THE COMPLIANT CODE",
		},
	}
	got := provenMutantIDs(rep, mutants)
	if len(got) != 1 || got[0] != "m1" {
		t.Fatalf("provenMutantIDs = %v, want [m1] only — m2 and m3 were never measured, and a subtraction would have called them proven", got)
	}
}
