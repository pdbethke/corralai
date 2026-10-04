// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"github.com/pdbethke/corralai/internal/shadowpool"
	"testing"
)

// capturingSigner records the Verdict it was handed, by value, exactly as
// the real signer receives it.
type capturingSigner struct{ got Verdict }

func (s *capturingSigner) SignVerdict(_ context.Context, v Verdict) (int64, string, error) {
	s.got = v
	return 1, "head", nil
}

// The SIGNED snapshot must carry the challenger measurement, not only the
// returned verdict.
//
// tickAggregate called Signer.SignVerdict and only THEN assigned
// v.ChallengerAgreement — 41 lines later. Verdict is passed by value, so the
// signer received nil while the verdict handed back to the caller had the
// measurement: the report showed a number the signature did not cover.
// timeoutVerdict, the other construction path, assigns it BEFORE its caller
// signs, so the two paths disagreed about what a signed record contains.
//
// Reported by an outside reviewer (GPT/Codex) against this repository,
// 2026-09-08, and reproduced here. The existing shadow-writer test asserts on
// the RETURNED verdict and so could never have seen it.
func TestSignedVerdictCarriesTheChallengerMeasurement(t *testing.T) {
	rs := newTestRunSpec(t)
	rs.ShadowWriterModel = "challenger-model"

	mutants := writerShadowMutants()
	scorer := &writerShadowScorer{}
	validator := &fakeValidator{mutants: mutants}
	missionID := writerShadowNextMissionID
	writerShadowNextMissionID++

	d := newWriterShadowRun(t, missionID, rs, scorer, validator)
	sig := &capturingSigner{}
	d.Signer = sig

	returned := driveWriterShadow(t, d, missionID)

	if returned.ChallengerAgreement == nil {
		t.Fatal("fixture: the returned verdict has no challenger measurement, so this test cannot tell the two apart")
	}
	if sig.got.ChallengerAgreement == nil {
		t.Fatal("the SIGNED verdict has no challenger measurement while the returned one does — the record is signed without a number the report shows")
	}
	if *sig.got.ChallengerAgreement != *returned.ChallengerAgreement {
		t.Errorf("signed measurement %+v != returned %+v", sig.got.ChallengerAgreement, returned.ChallengerAgreement)
	}
}

// ShadowSelection must survive BOTH verdict construction paths. This is the
// converged one (tickAggregate); the timed-out one is in driver_test.go.
func TestShadowSelectionSurvivesTickAggregate(t *testing.T) {
	rs := newTestRunSpec(t)
	rs.ShadowWriterModel = "challenger-model"
	rs.ShadowSelection = []shadowpool.Selection{{Role: RoleTestWriterShadow, Chosen: "c", Seed: "0x01"}}

	missionID := writerShadowNextMissionID
	writerShadowNextMissionID++
	d := newWriterShadowRun(t, missionID, rs, &writerShadowScorer{}, &fakeValidator{mutants: writerShadowMutants()})
	sig := &capturingSigner{}
	d.Signer = sig

	v := driveWriterShadow(t, d, missionID)
	for name, got := range map[string]Verdict{"returned": v, "signed": sig.got} {
		if len(got.ShadowSelection) != 1 || got.ShadowSelection[0].Chosen != "c" {
			t.Fatalf("ShadowSelection did not survive this construction path (%s): %+v", name, got.ShadowSelection)
		}
	}
}

// A challenger DRAWN and then never seated (here: the generator, on an
// unsharded run that dispatches no challenger) must not sign its selection.
// modelsByRole already drops that seat from the roster — "a model that was
// never asked is not in the roster" — and a selection naming a seat the
// roster omits is the same rule kept at one door and not the other. The
// seated writer's selection stays.
func TestTickAggregateSignsOnlySeatedSelections(t *testing.T) {
	rs := newTestRunSpec(t)
	rs.ShadowWriterModel = "challenger-model"
	rs.ShadowSelection = []shadowpool.Selection{
		{Role: RoleMutantGeneratorShadow, Chosen: "drawn-gen", Seed: "0x01"},
		{Role: RoleTestWriterShadow, Chosen: "c", Seed: "0x01"},
	}
	missionID := writerShadowNextMissionID
	writerShadowNextMissionID++
	d := newWriterShadowRun(t, missionID, rs, &writerShadowScorer{}, &fakeValidator{mutants: writerShadowMutants()})
	// Production puts a drawn generator in the assignment; this run is
	// unsharded, so it never dispatches it.
	d.Assign[RoleMutantGeneratorShadow] = "drawn-gen"
	sig := &capturingSigner{}
	d.Signer = sig

	v := driveWriterShadow(t, d, missionID)
	for name, got := range map[string]Verdict{"returned": v, "signed": sig.got} {
		if _, named := got.ModelsByRole[RoleMutantGeneratorShadow]; named {
			t.Fatalf("fixture (%s): the unseated generator is in the roster, so this test cannot see the filter", name)
		}
		if _, ok := shadowpool.Drawn(got.ShadowSelection, RoleMutantGeneratorShadow); ok {
			t.Errorf("%s verdict signs a selection for %s, a seat its roster omits: %+v", name, RoleMutantGeneratorShadow, got.ShadowSelection)
		}
		if sel, ok := shadowpool.Drawn(got.ShadowSelection, RoleTestWriterShadow); !ok || sel.Chosen != "c" {
			t.Errorf("%s verdict lost the SEATED writer's selection: %+v", name, got.ShadowSelection)
		}
	}
}
