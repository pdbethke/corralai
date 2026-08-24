// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
)

// shadowWriterResultTag is the completion result used for the CHALLENGER
// writer seat, so a fake Validator/Scorer can branch on WHICH writer it is
// serving without guessing from call order — the same trick shadowResultTag
// plays for the challenger generator seats.
const shadowWriterResultTag = "SHADOW-WRITER-RAW"

// primaryWriterResultTag is every non-challenger seat's completion result. The
// primary writer's authored test IS this string, which is how the scorer's
// recorded calls tell the two writers' scores apart.
const primaryWriterResultTag = "raw"

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

// writerShadowMutants is the run's exam: three mutants, two of which (m1, m2)
// the dev suite misses and both writers are therefore asked to kill.
func writerShadowMutants() []adequacy.Mutant {
	return []adequacy.Mutant{
		{ID: "m1", Code: "c1", ParentSHA256: "p1"},
		{ID: "m2", Code: "c2", ParentSHA256: "p2"},
		{ID: "m3", Code: "c3", ParentSHA256: "p3"},
	}
}

// The fixture's three deliberately DIFFERENT outcomes. The dev suite misses m1
// and m2; the primary writer proves both; the challenger proves only m1.
//
// The two writers must differ, and the survivor set must be a proper subset of
// the exam. A fixture where every vector coincides cannot detect a leak: an
// earlier version of this file had one writer's result overwrite the other's
// and every assertion still passed, because the two values were equal.
var (
	writerShadowDevSurvives     = []string{"m1", "m2"}
	writerShadowChallengerMiss  = []string{"m2"}
	writerShadowPrimaryProvesID = []string{"m1", "m2"}
)

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
	// primaryRep is the same knob for the PRIMARY writer's test, so a fixture
	// can script a primary that compiled but never genuinely graded while the
	// challenger is measured cleanly — the shape that proves the
	// unmeasured-is-not-zero guard is enforced on BOTH seats.
	primaryRep *adequacy.Report
}

// gradedReport is the report a suite that genuinely ran produces: everything it
// was handed is killed except the ids named in `survives`.
func gradedReport(mutants []adequacy.Mutant, survives []string) adequacy.Report {
	alive := make(map[string]bool, len(survives))
	for _, id := range survives {
		alive[id] = true
	}
	rep := adequacy.Report{CompliantPass: true, CanaryKilled: true, Total: len(mutants)}
	for _, m := range mutants {
		if alive[m.ID] {
			rep.Survived = append(rep.Survived, m.ID)
			continue
		}
		rep.Killed = append(rep.Killed, m.ID)
	}
	return rep
}

// ScoreReport is the DEV suite's score: it misses m1 and m2.
func (s *writerShadowScorer) ScoreReport(_ context.Context, _, code, test string, mutants []adequacy.Mutant, _ string) (adequacy.Report, error) {
	s.calls = append(s.calls, scoreCall{code, test, mutants})
	return gradedReport(mutants, writerShadowDevSurvives), nil
}

// ScoreAuthoredReport is an AUTHORED suite's score — the primary writer's, and
// the challenger's. The primary proves every survivor; the challenger proves
// strictly fewer, so the two vectors are distinguishable.
func (s *writerShadowScorer) ScoreAuthoredReport(_ context.Context, _, code, test string, mutants []adequacy.Mutant, _ string) (adequacy.Report, error) {
	s.calls = append(s.calls, scoreCall{code, test, mutants})
	if test == shadowWriterResultTag {
		if s.shadowRep != nil {
			return *s.shadowRep, nil
		}
		return gradedReport(mutants, writerShadowChallengerMiss), nil
	}
	if s.primaryRep != nil {
		return *s.primaryRep, nil
	}
	return gradedReport(mutants, nil), nil
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
	// Mirror production: cmd/corral puts the challenger writer's model in the
	// RoleAssignment whenever the flag names one, and aggregate() copies the
	// whole assignment onto Verdict.ModelsByRole. Building the arms with an
	// IDENTICAL assignment would test a configuration that never ships — see
	// TestShadowWriterNeverChangesVerdict, which pins the amended constraint
	// (RULING P10): the provenance entry may differ, no outcome field may.
	assign := decorrelatedAssign()
	if m := strings.TrimSpace(rs.ShadowWriterModel); m != "" {
		assign[RoleTestWriterShadow] = m
	}
	d, err := NewDriver(q, scorer, validator, assign, 0.5)
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
		result := primaryWriterResultTag
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

// driveWriterShadowTolerant is driveWriterShadow for fixtures in which the
// PRIMARY writer's test fails to compile. That path is not an error condition
// — the driver reissues the writer with the compiler's feedback — but it
// SIGNALS through a returned error (see tickPoolAdequacy: "test-writer result
// does not compile, reissued for retry"), which driveWriterShadow treats as
// fatal. Errors are ignored here rather than asserted on because the assertion
// this fixture exists for is about the rows the run does or does not write.
func driveWriterShadowTolerant(t *testing.T, d *Driver, missionID int64) {
	t.Helper()
	for i := 0; i < 50; i++ {
		v, _ := d.Tick(context.Background(), missionID)
		if v != nil {
			return
		}
		completeAllReadyWriterTagged(t, d)
	}
	t.Fatal("run did not converge in 50 ticks")
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
func runAndCaptureScoredMutantSets(t *testing.T, rs RunSpec) (primary, shadow []adequacy.Mutant, st *runState) {
	t.Helper()
	_, scorer, st := runWriterShadow(t, rs, nil)
	for _, c := range scorer.calls {
		switch c.test {
		case primaryWriterResultTag:
			primary = c.mutants
		case shadowWriterResultTag:
			shadow = c.mutants
		}
	}
	if primary == nil {
		t.Fatal("the primary writer was never scored — the fixture is not exercising the path")
	}
	if shadow == nil {
		t.Fatal("the challenger writer was never scored — the fixture is not exercising the seat")
	}
	return primary, shadow, st
}

// primaryCompileFailValidator is shadowCompileFailValidator's mirror: it fails
// CompileTest for the PRIMARY writer's test ONLY, leaving the challenger's
// compiling. That is the only shape that reaches a signed verdict with
// run.testWriterFailed set and a cleanly MEASURED challenger — the pairing the
// primary-side unmeasured guard has to refuse.
type primaryCompileFailValidator struct {
	*fakeValidator
}

func (v primaryCompileFailValidator) CompileTest(ctx context.Context, codePath, code, test string) error {
	if test == shadowWriterResultTag {
		return v.fakeValidator.CompileTest(ctx, codePath, code, test)
	}
	return &CompileError{Output: "primary test does not compile"}
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

// THE load-bearing test, in its amended form (RULING P10). SignVerdict hashes
// json.Marshal over the WHOLE Verdict, so a shadow seat that leaked an outcome
// field would change every record's digest and put a non-gating measurement
// inside a gating artifact.
//
// Two arms, because the challenger's model NAME legitimately rides the record:
//
//  1. challenger OFF vs OFF — the digest must be byte-identical, which is what
//     proves the run is deterministic enough for arm 2 to mean anything;
//  2. challenger NAMED — the ONLY permitted difference is the ModelsByRole
//     provenance entry (a signed record should say what ran). Every OUTCOME
//     field is asserted individually rather than through the digest, so a field
//     added to Verdict later cannot silently pass this test.
func TestShadowWriterNeverChangesVerdict(t *testing.T) {
	base := newTestRunSpec(t)

	off := runToVerdict(t, base)
	offAgain := runToVerdict(t, base)
	if digestOf(t, off) != digestOf(t, offAgain) {
		t.Fatalf("two identical challenger-OFF runs signed different digests:\n a = %s\n b = %s\nthe fixture is not deterministic, so nothing below can be trusted",
			digestOf(t, off), digestOf(t, offAgain))
	}

	withShadow := base
	withShadow.ShadowWriterModel = "challenger-model"
	got := runToVerdict(t, withShadow)

	// The ONE permitted difference: the provenance entry, and nothing else in
	// the map.
	if got.ModelsByRole[RoleTestWriterShadow] != "challenger-model" {
		t.Errorf("ModelsByRole[%s] = %q, want the challenger's model — a signed record must say what ran",
			RoleTestWriterShadow, got.ModelsByRole[RoleTestWriterShadow])
	}
	trimmed := map[string]string{}
	for k, v := range got.ModelsByRole {
		if k == RoleTestWriterShadow {
			continue
		}
		trimmed[k] = v
	}
	if !reflect.DeepEqual(trimmed, off.ModelsByRole) {
		t.Errorf("ModelsByRole changed beyond the challenger entry:\n without = %v\n with    = %v", off.ModelsByRole, trimmed)
	}

	// Every OUTCOME field, one at a time. Not a digest comparison: a digest
	// would go on passing for a field nobody thought to check here, but it
	// would also FAIL for the permitted provenance entry above, so the two
	// cannot be the same assertion.
	if got.Status != off.Status {
		t.Errorf("Status changed %q -> %q — the shadow reached the gate", off.Status, got.Status)
	}
	if got.DevKillRate != off.DevKillRate {
		t.Errorf("DevKillRate changed %v -> %v — the shadow reached the grade", off.DevKillRate, got.DevKillRate)
	}
	if !reflect.DeepEqual(got.DevKilledMutants, off.DevKilledMutants) {
		t.Errorf("DevKilledMutants changed %v -> %v — the shadow's kills reached the dev suite's vector", off.DevKilledMutants, got.DevKilledMutants)
	}
	if !reflect.DeepEqual(got.DevSurvivedMutants, off.DevSurvivedMutants) {
		t.Errorf("DevSurvivedMutants changed %v -> %v", off.DevSurvivedMutants, got.DevSurvivedMutants)
	}
	if got.ProvenMissed != off.ProvenMissed {
		t.Errorf("ProvenMissed changed %d -> %d — the challenger's proven kills reached the primary's count", off.ProvenMissed, got.ProvenMissed)
	}
	if !reflect.DeepEqual(got.ProvenMutantIDs, off.ProvenMutantIDs) {
		t.Errorf("ProvenMutantIDs changed %v -> %v — the challenger's vector reached the primary's evidence", off.ProvenMutantIDs, got.ProvenMutantIDs)
	}
	if got.MutantsTotal != off.MutantsTotal {
		t.Errorf("MutantsTotal changed %d -> %d", off.MutantsTotal, got.MutantsTotal)
	}
	if got.Survivors != off.Survivors {
		t.Errorf("Survivors changed %d -> %d", off.Survivors, got.Survivors)
	}
	if got.TestWriterFailed != off.TestWriterFailed {
		t.Error("TestWriterFailed changed — the shadow's compile outcome reached the primary's diagnosis")
	}
	if got.PoolTestUnsound != off.PoolTestUnsound {
		t.Error("PoolTestUnsound changed — the shadow's scoring outcome reached the primary's diagnosis")
	}
	if !reflect.DeepEqual(got.VacuousFindings, off.VacuousFindings) {
		t.Errorf("VacuousFindings changed %v -> %v", off.VacuousFindings, got.VacuousFindings)
	}
	if got.AuthoredTest != off.AuthoredTest {
		t.Errorf("AuthoredTest changed %q -> %q — the CHALLENGER's suite is being handed back as the pool's", off.AuthoredTest, got.AuthoredTest)
	}

	// Belt and braces: with the provenance entry removed, the whole verdict
	// must hash identically — this catches any field the list above forgot.
	sameProvenance := got
	sameProvenance.ModelsByRole = off.ModelsByRole
	if digestOf(t, sameProvenance) != digestOf(t, off) {
		t.Fatalf("a Verdict field OTHER than ModelsByRole changed when the challenger ran:\n without = %s\n with    = %s\na measurement seat has leaked into a gating artifact",
			digestOf(t, off), digestOf(t, sameProvenance))
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

// The controlled-comparison invariant (RULING P9). BOTH writers are scored
// against run.devSurvivors — the set they were each asked to kill. If anyone
// later scores the challenger against a different set (a regenerated one, or
// run.mutants, which is the DEV SUITE's exam and not the writers'), this must
// fail loudly: two writers facing different mutants is confounded by mutant
// difficulty.
func TestShadowWriterScoredAgainstSameMutantSet(t *testing.T) {
	rs := newTestRunSpec(t)
	rs.ShadowWriterModel = "challenger-model"
	primary, shadow, st := runAndCaptureScoredMutantSets(t, rs)
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
	// And the set is the SURVIVORS, not the whole exam: the universe the two
	// writers' vectors are paired over has to be the one they were asked about.
	if len(st.devSurvivors) == 0 {
		t.Fatal("the fixture left no survivors — the writers were never asked anything")
	}
	if len(shadow) != len(st.devSurvivors) {
		t.Fatalf("challenger scored against %d mutants, want run.devSurvivors (%d) — the universe is the survivors, not the full exam", len(shadow), len(st.devSurvivors))
	}
	for i := range st.devSurvivors {
		if shadow[i].ID != st.devSurvivors[i].ID {
			t.Fatalf("survivor %d differs (devSurvivors=%q, challenger=%q)", i, st.devSurvivors[i].ID, shadow[i].ID)
		}
	}
	if len(st.mutants) == len(st.devSurvivors) {
		t.Fatal("the fixture's survivor set equals its full mutant set — this test cannot tell the two universes apart")
	}
	// The two PAIRED vectors: the primary's proven set (run.provenIDs) and the
	// challenger's (run.shadowWriterKilled), both produced by provenMutantIDs
	// over that same survivor set. Their CONTENTS are allowed to differ — that
	// difference IS the measurement — but every element of each must name a
	// member of the shared universe, or the pairing is meaningless.
	if len(st.provenIDs) == 0 {
		t.Fatal("the primary writer proved nothing — there is no vector to pair against")
	}
	if len(st.shadowWriterKilled) == 0 {
		t.Fatal("the challenger proved nothing — the fixture is not exercising the vector")
	}
	inUniverse := map[string]bool{}
	for _, m := range st.devSurvivors {
		inUniverse[m.ID] = true
	}
	for _, id := range st.provenIDs {
		if !inUniverse[id] {
			t.Errorf("primary vector names %q, which is not a survivor — the universe is run.devSurvivors", id)
		}
	}
	for _, ref := range st.shadowWriterKilled {
		if !inUniverse[ref.ID] {
			t.Errorf("challenger vector names %q, which is not a survivor — the universe is run.devSurvivors", ref.ID)
		}
	}
	// A fixture in which the two vectors coincide cannot detect one seat's
	// result overwriting the other's — see writerShadowMutants.
	if len(st.provenIDs) == len(st.shadowWriterKilled) {
		t.Fatalf("the fixture's two writer vectors are the same length (%d) — it cannot tell a leak from a correct result", len(st.provenIDs))
	}
	if want := len(writerShadowPrimaryProvesID); len(st.provenIDs) != want {
		t.Errorf("primary proved %d survivor(s), want %d", len(st.provenIDs), want)
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
		// EXACTLY ONE clause of the guard may fire per case. A case that
		// satisfies two of them pins neither: deleting either clause leaves the
		// other to catch it and the test goes on passing. That is how
		// `|| rep.AuthoredTestUnreached` came to have no coverage at all — the
		// case named for it also carried CanaryKilled:false, which
		// short-circuits first.
		//
		// Some of these shapes are therefore deliberately UNREAL — a real
		// adequacy.Score returns before the canary when the baseline fails, so
		// it would never report CompliantPass:false with CanaryKilled:true.
		// Isolating the clause is the point; the driver must refuse to measure
		// on each condition INDEPENDENTLY, since each is a different diagnosis
		// (see adequacy.Report.AuthoredTestUnreached: "your test fails on
		// correct code" and "your command never collected that file" send an
		// operator to completely different places).
		{"canary survived", adequacy.Report{CompliantPass: true, CanaryKilled: false, Total: 2}},
		{"authored test unreached", adequacy.Report{CompliantPass: true, CanaryKilled: true, AuthoredTestUnreached: true, Total: 2}},
		{"baseline failed", adequacy.Report{CompliantPass: false, CanaryKilled: true, Total: 2}},
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
