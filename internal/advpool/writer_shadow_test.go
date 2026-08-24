// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
)

// shadowWriterResultTag is the completion result used for the CHALLENGER
// writer seat, so a fake Validator/Scorer can branch on WHICH writer it is
// serving without guessing from call order — the same trick shadowResultTag
// plays for the challenger generator seats.
const shadowWriterResultTag = "SHADOW-WRITER-RAW"

// writerShadowNextMissionID hands each helper-built run its own mission id.
// The Verdict carries no mission id, so this cannot perturb the digest the
// load-bearing test compares.
var writerShadowNextMissionID int64 = 5000

// newTestRunSpec is the base spec for the challenger-writer tests: the shared
// testRunSpec() with the challenger OFF, so a caller opts in by setting
// ShadowWriterModel and nothing else changes between the two arms.
func newTestRunSpec(t *testing.T) RunSpec {
	t.Helper()
	return testRunSpec()
}

// writerShadowMutants is the exam both writers must face: two mutants, one of
// which (m1) the dev suite misses.
func writerShadowMutants() []adequacy.Mutant {
	return []adequacy.Mutant{
		{ID: "m1", Code: "c1", ParentSHA256: "p1"},
		{ID: "m2", Code: "c2", ParentSHA256: "p2"},
	}
}

// writerShadowSurvivorID is the one mutant the dev suite does not kill.
const writerShadowSurvivorID = "m1"

// writerShadowScorer returns HONEST reports — Killed/Survived naming the real
// mutant ids — rather than fakeScorer's scripted (killRate, survivors) pairs,
// whose synthetic "k0..kN" kill ids match no mutant and therefore leave
// run.devKilled (and so Verdict.DevKilledMutants) empty on every run. An empty
// vector cannot detect a leak INTO it, which is exactly what
// TestShadowWriterNeverChangesVerdict has to be able to see.
type writerShadowScorer struct {
	calls []scoreCall
	// shadowRep, when non-nil, is returned for the CHALLENGER writer's test —
	// the knob the ungraded-suite cases use to script a report that never
	// genuinely graded.
	shadowRep *adequacy.Report
}

// graded is the report a suite that genuinely ran produces: every mutant given
// is killed except the dev suite's known survivor, which only the dev suite
// misses.
func gradedReport(mutants []adequacy.Mutant, missSurvivor bool) adequacy.Report {
	rep := adequacy.Report{CompliantPass: true, CanaryKilled: true, Total: len(mutants)}
	for _, m := range mutants {
		if missSurvivor && m.ID == writerShadowSurvivorID {
			rep.Survived = append(rep.Survived, m.ID)
			continue
		}
		rep.Killed = append(rep.Killed, m.ID)
	}
	return rep
}

// ScoreReport is the DEV suite's score: it misses the survivor.
func (s *writerShadowScorer) ScoreReport(_ context.Context, _, code, test string, mutants []adequacy.Mutant, _ string) (adequacy.Report, error) {
	s.calls = append(s.calls, scoreCall{code, test, mutants})
	return gradedReport(mutants, true), nil
}

// ScoreAuthoredReport is an AUTHORED suite's score — the primary writer's, and
// the challenger's. Both kill everything they are handed unless a case scripts
// otherwise.
func (s *writerShadowScorer) ScoreAuthoredReport(_ context.Context, _, code, test string, mutants []adequacy.Mutant, _ string) (adequacy.Report, error) {
	s.calls = append(s.calls, scoreCall{code, test, mutants})
	if test == shadowWriterResultTag && s.shadowRep != nil {
		return *s.shadowRep, nil
	}
	return gradedReport(mutants, false), nil
}

// Score satisfies the rest of the Scorer interface off the same reports, so
// this fake can never disagree with itself.
func (s *writerShadowScorer) Score(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (float64, []adequacy.Mutant, error) {
	rep, err := s.ScoreReport(ctx, codePath, code, test, mutants, testCmd)
	if err != nil {
		return 0, nil, err
	}
	return rep.KillRate(), survivorsFrom(rep, mutants), nil
}

// newWriterShadowRun mirrors newShadowedRun (the challenger-GENERATOR harness)
// for the challenger WRITER: scorer/validator are interface params so a test
// can supply a fake that branches on which writer seat it is serving — the
// only way to prove a challenger-only compile failure is non-fatal and does
// not spend the primary's budget.
func newWriterShadowRun(t *testing.T, missionID int64, rs RunSpec, scorer Scorer, validator Validator) *Driver {
	t.Helper()
	q := newTestQueue(t)
	d, err := NewDriver(q, scorer, validator, decorrelatedAssign(), 0.5)
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}
	if err := d.StartRun(missionID, rs, nil); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if _, err := q.PromoteReady(missionID); err != nil {
		t.Fatalf("PromoteReady: %v", err)
	}
	d.Signer = &fakeSigner{}
	return d
}

// completeAllReadyWriterTagged completes every ready task, tagging the
// CHALLENGER writer seat's result so the fakes can tell the two writers apart.
// Tagged by ROLE, not key: SupersedeTask uniquifies a replacement's key on
// every retry.
func completeAllReadyWriterTagged(t *testing.T, d *Driver) int {
	t.Helper()
	ready := claimAllReady(t, d.Q)
	for _, task := range ready {
		result := "raw"
		if task.Role == RoleTestWriterShadow {
			result = shadowWriterResultTag
		}
		mustComplete(t, d.Q, task.ID, result)
	}
	return len(ready)
}

// driveWriterShadow ticks to convergence, completing whatever becomes
// claimable (challenger seats tagged), and returns the terminal Verdict.
func driveWriterShadow(t *testing.T, d *Driver, missionID int64) Verdict {
	t.Helper()
	for i := 0; i < 50; i++ {
		v, err := d.Tick(context.Background(), missionID)
		if err != nil {
			t.Fatalf("Tick: %v", err)
		}
		if v != nil {
			return *v
		}
		completeAllReadyWriterTagged(t, d)
	}
	t.Fatal("run did not converge in 50 ticks")
	return Verdict{}
}

// runWriterShadow is the shared body of the three helpers below: one run of rs
// driven to convergence, handing back everything the tests assert on.
func runWriterShadow(t *testing.T, rs RunSpec, validator Validator) (Verdict, *writerShadowScorer, *runState) {
	t.Helper()
	mutants := writerShadowMutants()
	scorer := &writerShadowScorer{}
	if validator == nil {
		validator = &fakeValidator{mutants: mutants}
	}
	missionID := writerShadowNextMissionID
	writerShadowNextMissionID++
	d := newWriterShadowRun(t, missionID, rs, scorer, validator)
	v := driveWriterShadow(t, d, missionID)
	return v, scorer, d.runs[missionID]
}

// runToVerdict drives one run of rs to its signed Verdict.
func runToVerdict(t *testing.T, rs RunSpec) Verdict {
	t.Helper()
	v, _, _ := runWriterShadow(t, rs, nil)
	return v
}

// runAndCaptureState drives one run of rs and hands back the driver's own
// runState — no production hook exists purely for observability; these are the
// fields the driver already keeps.
func runAndCaptureState(t *testing.T, rs RunSpec) *runState {
	t.Helper()
	_, _, st := runWriterShadow(t, rs, nil)
	return st
}

// runAndCaptureScoredMutantSets drives one run of rs and returns the mutant
// set the PRIMARY exam was scored against and the one the CHALLENGER writer
// was scored against, read off the scorer's own recorded calls (the fake
// records every Score/ScoreReport/ScoreAuthoredReport invocation).
func runAndCaptureScoredMutantSets(t *testing.T, rs RunSpec) (primary, shadow []adequacy.Mutant) {
	t.Helper()
	_, scorer, _ := runWriterShadow(t, rs, nil)
	for _, c := range scorer.calls {
		switch c.test {
		case rs.DevTestCode:
			primary = c.mutants
		case shadowWriterResultTag:
			shadow = c.mutants
		}
	}
	if shadow == nil {
		t.Fatal("the challenger writer was never scored — the fixture is not exercising the seat")
	}
	return primary, shadow
}

// shadowCompileFailValidator fails CompileTest for the CHALLENGER writer's
// test ONLY, leaving the primary's compiling — the only shape that can prove
// the two retry budgets are separate.
type shadowCompileFailValidator struct {
	*fakeValidator
}

func (v shadowCompileFailValidator) CompileTest(ctx context.Context, codePath, code, test string) error {
	if test == shadowWriterResultTag {
		return &CompileError{Output: "challenger test does not compile"}
	}
	return v.fakeValidator.CompileTest(ctx, codePath, code, test)
}

// digestOf reproduces exactly what CertSigner.SignVerdict hashes, so this test
// fails if a shadow seat ever changes the signed bytes.
func digestOf(t *testing.T, v Verdict) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal verdict: %v", err)
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// THE load-bearing test. SignVerdict hashes json.Marshal over the WHOLE
// Verdict, so a shadow seat that leaked even one field would change every
// record's digest and put a non-gating measurement inside a gating artifact.
//
// Run the identical scenario with the challenger writer off and on; the
// Verdict and its digest must be byte-identical.
func TestShadowWriterNeverChangesVerdict(t *testing.T) {
	base := newTestRunSpec(t)

	withoutShadow := runToVerdict(t, base)

	withShadow := base
	withShadow.ShadowWriterModel = "challenger-model"
	got := runToVerdict(t, withShadow)

	if digestOf(t, got) != digestOf(t, withoutShadow) {
		t.Fatalf("signed digest changed when the challenger writer ran:\n without = %s\n with    = %s\na measurement seat has leaked into a gating artifact", digestOf(t, withoutShadow), digestOf(t, got))
	}
	if got.DevKillRate != withoutShadow.DevKillRate {
		t.Errorf("DevKillRate changed %v -> %v — the shadow reached the grade", withoutShadow.DevKillRate, got.DevKillRate)
	}
	if got.TestWriterFailed != withoutShadow.TestWriterFailed {
		t.Error("TestWriterFailed changed — the shadow's compile outcome reached the primary's diagnosis")
	}
}

// The seat must actually run when named — otherwise every isolation assertion
// above is vacuously true.
func TestShadowWriterMeasuredWhenNamed(t *testing.T) {
	rs := newTestRunSpec(t)
	rs.ShadowWriterModel = "challenger-model"
	st := runAndCaptureState(t, rs)
	if !st.shadowWriterMeasured {
		t.Fatal("shadowWriterMeasured = false with a clean challenger run — the seat never ran")
	}
	if st.devKilled == nil {
		t.Fatal("the primary's devKilled vector went missing")
	}

	off := runAndCaptureState(t, newTestRunSpec(t))
	if off.shadowWriterMeasured {
		t.Error("shadowWriterMeasured = true with no ShadowWriterModel — the seat is not off unless named")
	}
	if len(off.shadowWriterKilled) != 0 {
		t.Errorf("shadowWriterKilled = %v with the challenger off", off.shadowWriterKilled)
	}
}

// The controlled-comparison invariant. If anyone later regenerates mutants for
// the challenger, this must fail loudly: two writers facing different mutants
// is confounded by mutant difficulty.
func TestShadowWriterScoredAgainstSameMutantSet(t *testing.T) {
	rs := newTestRunSpec(t)
	rs.ShadowWriterModel = "challenger-model"
	primary, shadow := runAndCaptureScoredMutantSets(t, rs)
	if len(primary) == 0 {
		t.Fatal("primary scored no mutants — the fixture is not exercising the path")
	}
	if len(primary) != len(shadow) {
		t.Fatalf("mutant sets differ in size (primary=%d, shadow=%d) — the comparison is confounded", len(primary), len(shadow))
	}
	for i := range primary {
		if primary[i].ID != shadow[i].ID {
			t.Fatalf("mutant %d differs (primary=%q, shadow=%q) — both seats must face the IDENTICAL set", i, primary[i].ID, shadow[i].ID)
		}
	}
}

// A challenger that never compiles must not consume the primary's budget.
func TestShadowWriterRetriesDoNotConsumePrimaryBudget(t *testing.T) {
	rs := newTestRunSpec(t)
	rs.ShadowWriterModel = "always-fails-to-compile"
	mutants := writerShadowMutants()
	scorer := &writerShadowScorer{}
	validator := shadowCompileFailValidator{fakeValidator: &fakeValidator{mutants: mutants}}
	missionID := writerShadowNextMissionID
	writerShadowNextMissionID++
	d := newWriterShadowRun(t, missionID, rs, scorer, validator)
	driveWriterShadow(t, d, missionID)
	st := d.runs[missionID]

	if st.testWriterAttempts > 0 {
		t.Errorf("primary testWriterAttempts = %d after a challenger compile failure, want 0 — the budgets are shared", st.testWriterAttempts)
	}
	if st.testWriterFailed {
		t.Error("testWriterFailed = true — the challenger's compile outcome reached the primary's diagnosis")
	}
	if st.shadowWriterMeasured {
		t.Error("shadowWriterMeasured = true after every attempt failed to compile")
	}
	if st.shadowWriterAttempts == 0 {
		t.Error("shadowWriterAttempts = 0 — the challenger's own budget was never charged")
	}
	if st.shadowWriterAttempts > MaxShadowWriterAttempts {
		t.Errorf("shadowWriterAttempts = %d, over its own budget of %d", st.shadowWriterAttempts, MaxShadowWriterAttempts)
	}
}

// An unsound challenger suite — one whose canary survived, or whose own file
// the command never reached — must be UNMEASURED, never an all-survive vector
// that would read as a catastrophic blind spot.
func TestShadowWriterUngradedSuiteIsUnmeasuredNotZero(t *testing.T) {
	for _, tc := range []struct {
		name string
		rep  adequacy.Report
	}{
		{"canary survived", adequacy.Report{CompliantPass: true, CanaryKilled: false, Total: 2}},
		{"authored test unreached", adequacy.Report{CompliantPass: true, CanaryKilled: false, AuthoredTestUnreached: true, Total: 2}},
		{"baseline failed", adequacy.Report{CompliantPass: false}},
		{"nothing scored", adequacy.Report{CompliantPass: true, CanaryKilled: true, Total: 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rs := newTestRunSpec(t)
			rs.ShadowWriterModel = "challenger-model"
			mutants := writerShadowMutants()
			rep := tc.rep
			scorer := &writerShadowScorer{shadowRep: &rep}
			missionID := writerShadowNextMissionID
			writerShadowNextMissionID++
			d := newWriterShadowRun(t, missionID, rs, scorer, &fakeValidator{mutants: mutants})
			driveWriterShadow(t, d, missionID)
			st := d.runs[missionID]

			if st.shadowWriterMeasured {
				t.Error("shadowWriterMeasured = true for a suite that never genuinely graded")
			}
			if len(st.shadowWriterKilled) != 0 {
				t.Errorf("shadowWriterKilled = %v, want empty — an ungraded suite is UNMEASURED, not zero kills", st.shadowWriterKilled)
			}
		})
	}
}
