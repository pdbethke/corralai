// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/buildstore"
	"github.com/pdbethke/corralai/internal/certify"
	"github.com/pdbethke/corralai/internal/queue"
)

// canonScorer is a hermetic, fake advpool.Scorer: no jail, no network. Its
// first call is the dev-adequacy score (the dev's own tests against the
// mutant-generator's mutants) and its second is the pool-adequacy score
// (the pool's authored test against the survivors) — the same call-order
// contract internal/advpool's own fakeScorer relies on (see driver_test.go).
type canonScorer struct {
	calls int

	devKillRate  float64
	devSurvivors []adequacy.Mutant

	poolSurvivors []adequacy.Mutant
}

func (s *canonScorer) Score(_ context.Context, _, _, _ string, _ []adequacy.Mutant, _ string) (float64, []adequacy.Mutant, error) {
	s.calls++
	if s.calls == 1 {
		return s.devKillRate, s.devSurvivors, nil
	}
	return 1.0, s.poolSurvivors, nil
}

// canonValidator is a hermetic, fake advpool.Validator: it always accepts
// (test "compiles") and its ParseMutants returns a canned mutant set — no
// jail, no real go vet.
type canonValidator struct {
	mutants []adequacy.Mutant
}

func (v *canonValidator) CompileTest(_ context.Context, _, _, _ string) error { return nil }
func (v *canonValidator) ParseMutants(_ string) ([]adequacy.Mutant, error)    { return v.mutants, nil }
func (v *canonValidator) ParseTest(raw string) string                         { return raw }

// integrationRunSpec is the canned code+dev-test pair driving the end-to-end
// run: a trivial "always passes" dev test standing in for a weak developer
// suite the pool is meant to catch.
func integrationRunSpec() advpool.RunSpec {
	return advpool.RunSpec{
		Repo: "example/repo", Commit: "deadbeef01",
		Goal:        "passwords >= 12 chars",
		CodePath:    "internal/auth/login.go",
		Code:        "package auth\nfunc ValidatePassword(pw string) error { return nil }",
		DevTestPath: "internal/auth/login_test.go",
		DevTestCode: "package auth\nfunc TestAlwaysPasses(t *testing.T) {}",
		TestCmd:     "go test ./...",
		NMutants:    3,
	}
}

// setupIntegrationDriver wires a REAL advpool.Driver over a fresh queue and a
// REAL advpoolSigner (the actual certify chain — buildstore + ed25519 CertifyKey,
// exactly what StartAdversarialPool wires in production) but a FAKE
// Scorer/Validator, so the run is fully hermetic (no network, no real LLM, no
// sandbox jail) while the sign/verify path is the genuine one. Returns the
// driver, the queue, the mission id used, and the buildstore + public key so
// the caller can independently verify the resulting record.
func setupIntegrationDriver(t *testing.T, scorer *canonScorer, validator *canonValidator, threshold float64) (*advpool.Driver, *queue.Store, int64, *buildstore.Store, ed25519.PublicKey) {
	t.Helper()
	dir := t.TempDir()

	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = q.Close() })

	bs, err := buildstore.Open(filepath.Join(dir, "build.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bs.Close() })

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	assign := advpool.RoleAssignment{
		advpool.RoleMutantGenerator: "model-gen",
		advpool.RoleTestWriter:      "model-writer",
		advpool.RoleTestCritic:      "model-critic",
	}
	d, err := advpool.NewDriver(q, scorer, validator, assign, threshold)
	if err != nil {
		t.Fatal(err)
	}
	d.Signer = advpool.CertSigner{Key: priv, Store: bs}
	d.Leaderboard = advpoolLeaderboardSink{tel: nil} // nil telemetry is a documented no-op

	const missionID = 42
	if err := d.StartRun(missionID, integrationRunSpec(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := q.PromoteReady(missionID); err != nil {
		t.Fatal(err)
	}
	return d, q, missionID, bs, pub
}

// driveFullRun completes the three role tasks — mutant-generator with a
// canned mutants artifact, test-critic with a canned finding, test-writer
// with a canned authored test — and ticks the driver until it converges,
// mirroring internal/advpool's own completeFullRun helper (driver_test.go)
// but over the brain's real Signer.
func driveFullRun(t *testing.T, d *advpool.Driver, q *queue.Store, missionID int64, criticSeverity string) *advpool.Verdict {
	t.Helper()
	ctx := context.Background()

	// mutant-generator and test-critic have no deps and are both ready from
	// the start; claim both in one sweep (mirrors claimAllReady in
	// internal/advpool/driver_test.go — a mismatched single claim can't be
	// released, so draining the whole ready set is the only safe pattern).
	ready := map[string]*queue.Task{}
	for {
		task, err := q.ClaimNext("bee", nil, 300)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if task == nil {
			break
		}
		ready[task.Key] = task
	}
	tc, mg := ready[advpool.RoleTestCritic], ready[advpool.RoleMutantGenerator]
	if tc == nil || mg == nil {
		t.Fatalf("expected test-critic and mutant-generator both ready, got: %+v", ready)
	}

	// test-critic's designed-to-pass finding, filed against its own task —
	// this is what surfaces as Verdict.VacuousFindings.
	if criticSeverity != "" {
		if _, err := q.AddFinding(queue.Finding{
			MissionID: missionID, TaskID: tc.ID, Reporter: advpool.RoleTestCritic, Type: "bug",
			Severity: criticSeverity, Target: "TestAlwaysPasses",
			Evidence: "vacuous — asserts nothing, designed to pass",
		}); err != nil {
			t.Fatalf("AddFinding: %v", err)
		}
	}
	mustCompleteTask(t, q, tc.ID, "flagged TestAlwaysPasses as designed-to-pass/vacuous")
	mustCompleteTask(t, q, mg.ID, "canned mutants artifact")

	if _, err := d.Tick(ctx, missionID); err != nil {
		t.Fatalf("Tick (dev-adequacy): %v", err)
	}

	// test-writer's live task id may have changed under SupersedeTask; claim
	// tasks until the promoted one is won.
	var tw *queue.Task
	for i := 0; i < 5 && tw == nil; i++ {
		task, err := q.ClaimNext("bee", nil, 300)
		if err != nil {
			t.Fatalf("claim test-writer: %v", err)
		}
		if task == nil {
			t.Fatal("no claimable test-writer task after dev-adequacy")
		}
		// The promoted test-writer's KEY is auto-uniquified by SupersedeTask
		// (e.g. "test-writer-r2") since the original key isn't freed until
		// the same transaction that inserts the replacement — but its ROLE
		// is stable, so match on that (see internal/advpool/driver.go's note
		// on tickDevAdequacy / claimTaskByID in driver_test.go).
		if task.Role == advpool.RoleTestWriter {
			tw = task
		}
	}
	mustCompleteTask(t, q, tw.ID, "canned pool-authored test source")

	v, err := d.Tick(ctx, missionID)
	if err != nil {
		t.Fatalf("Tick (pool-adequacy + aggregate): %v", err)
	}
	if v == nil {
		t.Fatal("expected a terminal verdict once test-critic + pool-adequacy are both done")
	}
	return v
}

func mustCompleteTask(t *testing.T, q *queue.Store, id int64, result string) {
	t.Helper()
	ok, err := q.Complete(id, "bee", result)
	if err != nil || !ok {
		t.Fatalf("complete task %d: ok=%v err=%v", id, ok, err)
	}
}

// TestAdversarialPoolEndToEnd_Certified drives a FULL run over the real
// advpool.Driver + the real certify-chain Signer with a hermetic fake
// Scorer/Validator standing in for the jail/LLM: a strong dev suite
// (DevKillRate above threshold, no blocking finding) must converge to a
// CERTIFIED, SIGNED verdict whose DevKillRate/ProvenMissed/VacuousFindings/
// ModelsByRole are exactly what the fakes produced, and whose signed record
// independently verifies via the certify chain (VerifyDSSE + VerifyLedger) —
// the same checks `corral certify verify`/buildcert_test.go's TestReportBuild
// run over report_build's own output.
func TestAdversarialPoolEndToEnd_Certified(t *testing.T) {
	survivors := []adequacy.Mutant{{ID: "m1", Code: "SURVIVOR-1"}, {ID: "m2", Code: "SURVIVOR-2"}}
	scorer := &canonScorer{
		devKillRate:  0.9,
		devSurvivors: survivors,
		// the pool's authored test kills m1 but not m2 -> ProvenMissed=1
		poolSurvivors: []adequacy.Mutant{{ID: "m2", Code: "SURVIVOR-2"}},
	}
	validator := &canonValidator{mutants: []adequacy.Mutant{{ID: "m0", Code: "killed-m0"}, survivors[0], survivors[1]}}

	d, q, missionID, bs, pub := setupIntegrationDriver(t, scorer, validator, 0.5)
	v := driveFullRun(t, d, q, missionID, "low") // low severity: surfaced, but never blocks certification

	if v.Status != advpool.StatusCertified {
		t.Fatalf("Status = %q, want %q", v.Status, advpool.StatusCertified)
	}
	if v.DevKillRate != 0.9 {
		t.Fatalf("DevKillRate = %v, want 0.9", v.DevKillRate)
	}
	if v.MutantsTotal != 3 {
		t.Fatalf("MutantsTotal = %d, want 3", v.MutantsTotal)
	}
	if v.Survivors != 2 {
		t.Fatalf("Survivors = %d, want 2", v.Survivors)
	}
	if v.ProvenMissed != 1 {
		t.Fatalf("ProvenMissed = %d, want 1 (pool killed m1, m2 still survives)", v.ProvenMissed)
	}
	if len(v.VacuousFindings) != 1 {
		t.Fatalf("VacuousFindings = %d, want 1 (the low-severity designed-to-pass finding)", len(v.VacuousFindings))
	}

	// models_by_role must be decorrelation-enforced: test-critic != test-writer.
	if v.ModelsByRole[advpool.RoleTestWriter] == "" || v.ModelsByRole[advpool.RoleTestCritic] == "" || v.ModelsByRole[advpool.RoleMutantGenerator] == "" {
		t.Fatalf("expected all three roles stamped in ModelsByRole, got %+v", v.ModelsByRole)
	}
	if v.ModelsByRole[advpool.RoleTestCritic] == v.ModelsByRole[advpool.RoleTestWriter] {
		t.Fatalf("decorrelation violated in signed verdict: test-critic == test-writer model (%q)", v.ModelsByRole[advpool.RoleTestCritic])
	}

	// The certified verdict must produce exactly one signed buildstore record
	// (advpoolSigner.SignVerdict -> certifyBuild -> bs); find it and verify it
	// independently, the same way a `corral certify verify` CLI invocation
	// would: DSSE envelope signature checks out under the brain's public key,
	// and the embedded ledger's recomputed head is internally consistent.
	summaries, err := bs.List(buildstore.ListFilter{})
	if err != nil {
		t.Fatalf("bs.List: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected exactly one signed record, got %d", len(summaries))
	}
	if !summaries[0].Pass {
		t.Fatal("expected the certified verdict's record to be stored pass=true")
	}
	rec, found, err := bs.Get(summaries[0].ID)
	if err != nil || !found {
		t.Fatalf("bs.Get(%d): found=%v err=%v", summaries[0].ID, found, err)
	}

	stmt, ok, verr := certify.VerifyDSSE([]byte(rec["signature"].(string)), pub)
	if verr != nil {
		t.Fatalf("VerifyDSSE: %v", verr)
	}
	if !ok {
		t.Fatal("VerifyDSSE must succeed over the signed advpool verdict record under the brain's public key")
	}
	if stmt == nil {
		t.Fatal("expected a non-nil verified statement")
	}

	stepsJSON, err := json.Marshal(rec["steps"])
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := certify.UnmarshalSteps(stepsJSON)
	if err != nil {
		t.Fatalf("certify.UnmarshalSteps: %v", err)
	}
	subjects, ok := rec["subject"].([]any)
	if !ok || len(subjects) != 1 {
		t.Fatalf("stored statement missing subject: %v", rec["subject"])
	}
	subj := subjects[0].(map[string]any)
	digest := subj["digest"].(map[string]any)
	head, _ := digest["sha256"].(string)
	if okV, msg := certify.VerifyLedger(ledger, head); !okV {
		t.Fatalf("VerifyLedger over the stored advpool record failed: %s", msg)
	}

	// The leaderboard must have been fed — CERTIFIED only (soundness #5/#6) —
	// exactly once per role. advpoolLeaderboardSink.Record is a best-effort
	// wrapper around rec() with nil telemetry, so there's nothing further to
	// assert here beyond "the driver didn't panic/error doing so"; the pure
	// driver's own TestTick_Aggregate_Certified_SignsAndFeedsLeaderboard
	// (internal/advpool/driver_test.go) already locks the call contract with a
	// fakeLeaderboard. This test's job is proving the REAL sign path verifies.
}

// TestAdversarialPoolEndToEnd_LowDevKillRate_NeedsReview proves the human
// gate: when the DEV'S OWN TESTS catch essentially nothing (DevKillRate far
// below threshold), the run converges to needs-review — never certified, and
// never fed to the leaderboard — even though the pool's own authored test
// still proves the survivors are real/catchable (ProvenMissed > 0). A signed
// EVIDENCE record is still produced (needs-review is not silence), but its
// Status must never read "certified".
func TestAdversarialPoolEndToEnd_LowDevKillRate_NeedsReview(t *testing.T) {
	survivors := []adequacy.Mutant{{ID: "m1", Code: "SURVIVOR-1"}, {ID: "m2", Code: "SURVIVOR-2"}, {ID: "m3", Code: "SURVIVOR-3"}}
	scorer := &canonScorer{
		devKillRate:   0.1, // the dev's tests catch almost nothing
		devSurvivors:  survivors,
		poolSurvivors: nil, // the pool's test kills every survivor it was targeted at
	}
	validator := &canonValidator{mutants: append([]adequacy.Mutant{{ID: "m0", Code: "killed-m0"}}, survivors...)}

	d, q, missionID, bs, _ := setupIntegrationDriver(t, scorer, validator, 0.8)
	v := driveFullRun(t, d, q, missionID, "") // no critic finding needed for this path

	if v.Status != advpool.StatusNeedsReview {
		t.Fatalf("Status = %q, want %q (DevKillRate 0.1 < threshold 0.8)", v.Status, advpool.StatusNeedsReview)
	}
	if v.ProvenMissed != 3 {
		t.Fatalf("ProvenMissed = %d, want 3 (the pool's test proves every survivor real)", v.ProvenMissed)
	}

	summaries, err := bs.List(buildstore.ListFilter{})
	if err != nil {
		t.Fatalf("bs.List: %v", err)
	}
	// A needs-review verdict is still signed as an evidence record...
	if len(summaries) != 1 {
		t.Fatalf("expected exactly one signed (needs-review) record, got %d", len(summaries))
	}
	// ...but NEVER stored as a "pass" record: advpoolSigner.SignVerdict bakes a
	// non-zero exit code into certifyBuild for anything other than
	// StatusCertified (see advpool.go's SignVerdict), and pass is that exact
	// exit_code==0 denormalization — a low dev kill-rate must never slip past
	// the human gate into a passing/certified record or leaderboard credit,
	// regardless of how strong the pool's own proof is.
	if summaries[0].Pass {
		t.Fatal("expected the needs-review verdict's record to be stored pass=false (non-zero exit code)")
	}
}
