// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"strings"
	"testing"
)

// THE ACCEPTANCE. A recorded mutant set is graded twice: once down the new
// path (fail-fast on, duplicates collapsed, whatever order the command gives),
// and once against an INDEPENDENT ORACLE — every mutant graded on its own, in
// its own Score call, with no fail-fast and nothing to collapse. The oracle
// cannot benefit from any of the three optimisations, so agreement between the
// two is the strongest form of the invariant available: same kill rate, same
// killed set, same survivors, same killed_by, same denominator.
//
// Hermetic: fakeSuite is an in-process simulated runner. No jail, no process,
// no network, no model call.
func TestRecordedSetGradesIdenticallyDownTheFastPath(t *testing.T) {
	// A recorded set with a duplicate in it (m1/m1-dup), a mutant only the
	// LAST selected test catches (m2), one caught by two tests (m3), and two
	// survivors.
	set := []Mutant{
		{ID: "m1", Replace: "kill1\n"},
		{ID: "m2", Replace: "kill4\n"},
		{ID: "m1-dup", Replace: "kill1\n"},
		{ID: "m3", Replace: "kill2 kill3\n"},
		{ID: "m4", Replace: "harmless\n"},
		{ID: "m5", Replace: "also harmless\n"},
	}

	fast := &fakeSuite{}
	got, err := Score(context.Background(), fast, map[string]string{}, "a.py", "ORIGINAL\n",
		set, ffCmd,
		WithFailureParser(pythonFailureParser(t)),
		WithMutantFailFast(pyFailFast(t)))
	if err != nil {
		t.Fatalf("fast path Score: %v", err)
	}
	if !got.FailFast {
		t.Fatalf("fail-fast was not in play: %q — this would not be testing anything", got.FailFastNote)
	}
	if got.DuplicateMutants != 1 {
		t.Fatalf("DuplicateMutants = %d, want 1", got.DuplicateMutants)
	}

	// THE ORACLE: one mutant per Score call, no fail-fast, no duplicates to
	// collapse, full suite every time.
	var wantKilled, wantSurvived []string
	wantKilledBy := map[string]string{}
	oracle := &fakeSuite{}
	for _, m := range set {
		rep, err := Score(context.Background(), oracle, map[string]string{}, "a.py", "ORIGINAL\n",
			[]Mutant{m}, ffCmd, WithFailureParser(pythonFailureParser(t)))
		if err != nil {
			t.Fatalf("oracle Score(%s): %v", m.ID, err)
		}
		if rep.FailFast || rep.DuplicateMutants != 0 {
			t.Fatalf("the oracle used an optimisation — it is not an oracle")
		}
		if len(rep.Killed) == 1 {
			wantKilled = append(wantKilled, m.ID)
		} else {
			wantSurvived = append(wantSurvived, m.ID)
		}
		wantKilledBy[m.ID] = rep.PerMutant[m.ID].KilledBy
	}

	if strings.Join(got.Killed, ",") != strings.Join(wantKilled, ",") {
		t.Errorf("Killed = %v, oracle says %v", got.Killed, wantKilled)
	}
	if strings.Join(got.Survived, ",") != strings.Join(wantSurvived, ",") {
		t.Errorf("Survived = %v, oracle says %v", got.Survived, wantSurvived)
	}
	if got.Total != len(set) {
		t.Errorf("Total = %d, want %d — the denominator must survive the collapse", got.Total, len(set))
	}
	wantRate := float64(len(wantKilled)) / float64(len(set))
	if got.KillRate() != wantRate {
		t.Errorf("kill rate = %v, oracle says %v", got.KillRate(), wantRate)
	}
	for id, want := range wantKilledBy {
		if g := got.PerMutant[id].KilledBy; g != want {
			t.Errorf("mutant %s killed_by = %q, oracle says %q", id, g, want)
		}
	}
	if len(got.Invalid) != 0 {
		t.Errorf("Invalid = %v, want none", got.Invalid)
	}

	// And the fast path really was cheaper.
	if fast.executed >= oracle.executed {
		t.Errorf("fast path executed %d tests, the oracle %d — no saving", fast.executed, oracle.executed)
	}
}
