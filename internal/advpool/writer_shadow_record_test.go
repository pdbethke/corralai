// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
)

// fakeMutantAttemptSink records the (recordID, attempts) it was fed, mirroring
// fakeBugCatch (driver_test.go:2170).
type fakeMutantAttemptSink struct {
	recordID int64
	attempts []MutantAttempt
}

func (f *fakeMutantAttemptSink) Record(recordID int64, _ string, attempts []MutantAttempt) {
	f.recordID = recordID
	f.attempts = append(f.attempts, attempts...)
}

// runAndCaptureAttempts drives one run of rs to its signed Verdict with a
// fake MutantAttemptSink wired on the Driver, and returns whatever it
// captured. rs.ShadowWriterModel selects the fixture: the zero value leaves
// the challenger off; "challenger-model" is the clean measured path (see
// writerShadowScorer); the "challenger-<cause>" names below reproduce each of
// the unmeasured shapes TestShadowWriterUngradedSuiteIsUnmeasuredNotZero
// already pins, plus the challenger's own compile failure.
func runAndCaptureAttempts(t *testing.T, rs RunSpec) []MutantAttempt {
	t.Helper()
	mutants := writerShadowMutants()
	scorer := &writerShadowScorer{}
	var validator Validator = &fakeValidator{mutants: mutants}

	switch rs.ShadowWriterModel {
	case "challenger-compile-failure":
		validator = shadowCompileFailValidator{fakeValidator: &fakeValidator{mutants: mutants}}
	case "challenger-canary-not-killed":
		rep := adequacy.Report{CompliantPass: true, CanaryKilled: false, Total: 2}
		scorer.shadowRep = &rep
	case "challenger-authored-test-unreached":
		rep := adequacy.Report{CompliantPass: true, CanaryKilled: true, AuthoredTestUnreached: true, Total: 2}
		scorer.shadowRep = &rep
	case "challenger-scoring-error":
		rep := adequacy.Report{CompliantPass: false, CanaryKilled: true, Total: 2}
		scorer.shadowRep = &rep
	}

	missionID := writerShadowNextMissionID
	writerShadowNextMissionID++
	d := newWriterShadowRun(t, missionID, rs, scorer, validator)
	sink := &fakeMutantAttemptSink{}
	d.MutantAttempts = sink
	driveWriterShadow(t, d, missionID)
	return sink.attempts
}

// PAIR OR NOTHING. An unpaired primary vector cannot contribute to a
// within-run correlation, and a table half-full of unpairable rows would
// invite exactly the cross-run pooling this design rejects.
func TestNoShadowConfiguredWritesNoAttemptRows(t *testing.T) {
	rs := newTestRunSpec(t) // ShadowWriterModel stays ""
	rows := runAndCaptureAttempts(t, rs)
	if len(rows) != 0 {
		t.Fatalf("wrote %d attempt rows with no challenger configured, want 0 — an unpaired vector is not a measurement", len(rows))
	}
}

func TestUnmeasuredShadowEmitsNoAttemptRows(t *testing.T) {
	for _, cause := range []string{"compile-failure", "canary-not-killed", "authored-test-unreached", "scoring-error"} {
		t.Run(cause, func(t *testing.T) {
			rs := newTestRunSpec(t)
			rs.ShadowWriterModel = "challenger-" + cause // fixtures keyed by cause
			rows := runAndCaptureAttempts(t, rs)
			if len(rows) != 0 {
				t.Fatalf("wrote %d attempt rows for an unmeasured challenger (%s), want 0 — a zero-kill vector for a seat that never ran is a fabricated comparison", len(rows), cause)
			}
		})
	}
}

func TestMeasuredShadowWritesBothSeats(t *testing.T) {
	rs := newTestRunSpec(t)
	rs.ShadowWriterModel = "challenger-model"
	rows := runAndCaptureAttempts(t, rs)
	if len(rows) == 0 {
		t.Fatal("a measured challenger wrote no rows")
	}
	byRole := map[string]int{}
	for _, r := range rows {
		byRole[r.Role]++
	}
	if byRole[RoleTestWriter] == 0 {
		t.Error("no primary rows — the pair needs both vectors in one table to be joinable")
	}
	if byRole[RoleTestWriterShadow] == 0 {
		t.Error("no challenger rows")
	}
	if byRole[RoleTestWriter] != byRole[RoleTestWriterShadow] {
		t.Errorf("row counts differ (primary=%d, shadow=%d) — both seats faced the same mutant set so both vectors must be the same length", byRole[RoleTestWriter], byRole[RoleTestWriterShadow])
	}
}

// salvageableWriterShadowScorer combines writerShadowScorer's honest,
// mutant-ID-keyed reports with the driver_salvage_test.go fixture shape: the
// PRIMARY writer's authored test fails the compliant check on the first
// (non-deselected) call, and grades sound once the failing selector is
// deselected (testCmd contains "--deselect") — the exact salvageByDeselect
// path exercised by TestTick_PoolAdequacy_SalvagesPassingTestsFromABrokenFile.
// The CHALLENGER's report is untouched by any of this: a clean, honestly
// measured miss, so the test proves the pair-or-nothing rule fires even when
// the challenger side is perfectly fine.
type salvageableWriterShadowScorer struct {
	calls []scoreCall
}

func (s *salvageableWriterShadowScorer) ScoreReport(_ context.Context, _, code, test string, mutants []adequacy.Mutant, _ string) (adequacy.Report, error) {
	s.calls = append(s.calls, scoreCall{code, test, mutants})
	return gradedReport(mutants, writerShadowDevSurvives), nil
}

func (s *salvageableWriterShadowScorer) ScoreAuthoredReport(_ context.Context, _, code, test string, mutants []adequacy.Mutant, testCmd string) (adequacy.Report, error) {
	s.calls = append(s.calls, scoreCall{code, test, mutants})
	if test == shadowWriterResultTag {
		return gradedReport(mutants, writerShadowChallengerMiss), nil
	}
	if strings.Contains(testCmd, "--deselect") {
		// Sound remainder, proves everything it was handed.
		return gradedReport(mutants, nil), nil
	}
	return adequacy.Report{CompliantPass: false, Total: 0}, nil
}

func (s *salvageableWriterShadowScorer) Score(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (float64, []adequacy.Mutant, error) {
	rep, err := s.ScoreReport(ctx, codePath, code, test, mutants, testCmd)
	if err != nil {
		return 0, nil, err
	}
	return rep.KillRate(), survivorsFrom(rep, mutants), nil
}

// CompliantFailure satisfies CompliantFailureExplainer, supplying the pytest
// "FAILED <selector> - ..." line the python plugin's FailedTests parses into
// a deselectable selector.
func (s *salvageableWriterShadowScorer) CompliantFailure(context.Context, string, string, string, string) string {
	return "FAILED tests/test_x_corral.py::test_bad - boom"
}

// RULING P11: a SALVAGED primary is not comparable to an unsalvaged
// challenger — the pair is confounded in the primary's favour and must not
// be recorded even though the challenger was cleanly measured.
func TestSalvagedPrimaryWritesNoAttemptRows(t *testing.T) {
	rs := newTestRunSpec(t)
	// PYTHON specifically: salvageByDeselect is gated on the language plugin
	// implementing lang.FailureDeselector, and only python does (see
	// driver_salvage_test.go's pythonSalvageSpec).
	rs.Lang = "python"
	rs.TestCmd = "python3 -m pytest -q"
	rs.ShadowWriterModel = "challenger-model"
	mutants := writerShadowMutants()
	scorer := &salvageableWriterShadowScorer{}
	validator := &fakeValidator{mutants: mutants}
	missionID := writerShadowNextMissionID
	writerShadowNextMissionID++
	d := newWriterShadowRun(t, missionID, rs, scorer, validator)
	sink := &fakeMutantAttemptSink{}
	d.MutantAttempts = sink
	driveWriterShadow(t, d, missionID)
	st := d.runs[missionID]

	if !st.writerSalvaged {
		t.Fatal("fixture did not exercise the salvage path — writerSalvaged is false")
	}
	if !st.shadowWriterMeasured {
		t.Fatal("fixture did not exercise a clean challenger — shadowWriterMeasured is false")
	}
	if len(sink.attempts) != 0 {
		t.Fatalf("wrote %d attempt rows for a salvaged primary, want 0 — the pair is confounded", len(sink.attempts))
	}
}
