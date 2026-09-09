// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
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
