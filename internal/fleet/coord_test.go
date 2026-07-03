// SPDX-License-Identifier: Elastic-2.0

package fleet

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"

	"github.com/pdbethke/corralai/internal/attest"
)

// ── Test fixtures ─────────────────────────────────────────────────────────────

// coordNow is a fixed reference time used across all coord tests.
var coordNow = time.Unix(2_000_000_000, 0)

// mkBrain generates a fresh keypair and returns (keyPair, pubB64).
func mkBrain(t *testing.T) (attest.KeyPair, string) {
	t.Helper()
	kp, err := attest.LoadOrCreateKey("", filepath.Join(t.TempDir(), "key"))
	if err != nil {
		t.Fatalf("mkBrain: %v", err)
	}
	return kp, attest.PubB64(kp.Pub)
}

// newRemote creates a temporary DuckDB file and returns its path.
func newRemote(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "remote.duckdb")
}

// coordRowCount returns the number of rows in a coord table (fleet_brains or
// fleet_intents) matching the given brain column name + value.
func coordRowCount(t *testing.T, db *sql.DB, table, col, brainID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(
		"SELECT count(*) FROM "+table+" WHERE "+col+" = ?", brainID,
	).Scan(&n); err != nil {
		t.Fatalf("coordRowCount %s.%s=%s: %v", table, col, brainID, err)
	}
	return n
}

// ── RegisterBrain tests ───────────────────────────────────────────────────────

// TestRegisterBrain_FirstTime verifies that the first RegisterBrain for a new
// brain returns Registered and pins the key in fleet_brains.
func TestRegisterBrain_FirstTime(t *testing.T) {
	remote := newRemote(t)
	_, pubA := mkBrain(t)

	out, err := RegisterBrain(remote, "brainA", pubA, nil, coordNow)
	if err != nil {
		t.Fatalf("RegisterBrain: %v", err)
	}
	if out != attest.Registered {
		t.Fatalf("expected Registered, got %v", out)
	}

	// Verify the key is pinned in fleet_brains.
	db, err := sql.Open("duckdb", remote)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if n := coordRowCount(t, db, "fleet_brains", "brain_id", "brainA"); n != 1 {
		t.Fatalf("expected 1 row in fleet_brains for brainA, got %d", n)
	}
	var pinned string
	db.QueryRow("SELECT pubkey FROM fleet_brains WHERE brain_id = 'brainA'").Scan(&pinned)
	if pinned != pubA {
		t.Fatalf("pinned key mismatch: want %s, got %s", pubA, pinned)
	}
}

// TestRegisterBrain_AlreadyTrusted verifies that re-registering with the SAME key
// returns AlreadyTrusted (idempotent).
func TestRegisterBrain_AlreadyTrusted(t *testing.T) {
	remote := newRemote(t)
	_, pubA := mkBrain(t)

	if out, err := RegisterBrain(remote, "brainA", pubA, nil, coordNow); err != nil || out != attest.Registered {
		t.Fatalf("first registration: out=%v err=%v", out, err)
	}

	out, err := RegisterBrain(remote, "brainA", pubA, nil, coordNow)
	if err != nil {
		t.Fatalf("re-register same key: %v", err)
	}
	if out != attest.AlreadyTrusted {
		t.Fatalf("expected AlreadyTrusted, got %v", out)
	}

	// Row count must remain 1 (no duplication).
	db, err := sql.Open("duckdb", remote)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if n := coordRowCount(t, db, "fleet_brains", "brain_id", "brainA"); n != 1 {
		t.Fatalf("expected 1 row after re-register, got %d", n)
	}
}

// TestRegisterBrain_Conflict verifies that registering brainA's id with B's key
// (after A is already pinned) returns Conflict and leaves the pinned key unchanged.
// This covers both the race-loser scenario and impersonation attempts.
func TestRegisterBrain_Conflict(t *testing.T) {
	remote := newRemote(t)
	_, pubA := mkBrain(t)
	_, pubB := mkBrain(t)

	// Pin A's key first.
	if out, err := RegisterBrain(remote, "brainA", pubA, nil, coordNow); err != nil || out != attest.Registered {
		t.Fatalf("first registration: out=%v err=%v", out, err)
	}

	// Attempt to register the same brain_id with B's key → Conflict.
	out, err := RegisterBrain(remote, "brainA", pubB, nil, coordNow)
	if err != nil {
		t.Fatalf("conflict registration: %v", err)
	}
	if out != attest.Conflict {
		t.Fatalf("expected Conflict, got %v", out)
	}

	// The pinned key must remain A's key (NEVER overwritten).
	db, err := sql.Open("duckdb", remote)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var pinned string
	db.QueryRow("SELECT pubkey FROM fleet_brains WHERE brain_id = 'brainA'").Scan(&pinned)
	if pinned != pubA {
		t.Fatalf("pinned key was overwritten! want A's key, got %s", pinned)
	}
}

// TestRegisterBrain_AllowlistRejects verifies that a brain not in the allowlist
// is Rejected before touching the DB.
func TestRegisterBrain_AllowlistRejects(t *testing.T) {
	remote := newRemote(t)
	_, pubA := mkBrain(t)
	_, pubB := mkBrain(t)

	allowlist := map[string]string{"brainA": pubA}

	// brainB is not in the allowlist → Rejected.
	out, err := RegisterBrain(remote, "brainB", pubB, allowlist, coordNow)
	if err != nil {
		t.Fatalf("allowlist reject: %v", err)
	}
	if out != attest.Rejected {
		t.Fatalf("expected Rejected, got %v", out)
	}

	// brainA with the right key → Registered.
	out, err = RegisterBrain(remote, "brainA", pubA, allowlist, coordNow)
	if err != nil {
		t.Fatalf("allowlist accept: %v", err)
	}
	if out != attest.Registered {
		t.Fatalf("expected Registered, got %v", out)
	}
}

// ── PublishIntent + ActiveClaims tests ───────────────────────────────────────

// TestActiveClaims_HappyPath verifies the core publish→read flow:
//   - BrainA publishes a claim on repoX.
//   - ActiveClaims(repoX, exceptBrain="brainB") returns A's claim (verified).
//   - ActiveClaims(repoX, exceptBrain="brainA") excludes A's own claim.
func TestActiveClaims_HappyPath(t *testing.T) {
	remote := newRemote(t)
	kpA, pubA := mkBrain(t)
	_, pubB := mkBrain(t)

	// Register both brains.
	mustRegister(t, remote, "brainA", pubA)
	mustRegister(t, remote, "brainB", pubB)

	// A publishes a claim.
	ttl := time.Hour
	if err := PublishIntent(kpA, remote, "brainA", "claim", "repoX", ttl, coordNow); err != nil {
		t.Fatalf("PublishIntent: %v", err)
	}

	// B queries claims on repoX (should see A's claim).
	claims, err := ActiveClaims(remote, "repoX", "brainB", coordNow)
	if err != nil {
		t.Fatalf("ActiveClaims(exceptB): %v", err)
	}
	if len(claims) != 1 {
		t.Fatalf("expected 1 claim from A (excepting B), got %d", len(claims))
	}
	if claims[0].BrainID != "brainA" {
		t.Fatalf("expected claim from brainA, got %s", claims[0].BrainID)
	}
	if claims[0].Subject != "repoX" {
		t.Fatalf("expected subject=repoX, got %s", claims[0].Subject)
	}

	// A queries claims on repoX, excluding itself → should be empty.
	claims, err = ActiveClaims(remote, "repoX", "brainA", coordNow)
	if err != nil {
		t.Fatalf("ActiveClaims(exceptA): %v", err)
	}
	if len(claims) != 0 {
		t.Fatalf("expected 0 claims when excepting own brain, got %d", len(claims))
	}
}

// TestActiveClaims_ForgedSigDropped verifies that a manually-inserted row with
// a bogus signature is silently dropped (fail-closed).
func TestActiveClaims_ForgedSigDropped(t *testing.T) {
	remote := newRemote(t)
	_, pubA := mkBrain(t)
	mustRegister(t, remote, "brainA", pubA)

	// Ensure coord tables exist.
	db := openRemote(t, remote)
	defer db.Close()
	mustExec(t, db, ddlFleetBrains)
	mustExec(t, db, ddlFleetIntents)

	// Manually insert a fleet_intents row with a completely bogus signature.
	nowF := float64(coordNow.Unix())
	mustExec(t, db,
		`INSERT INTO fleet_intents (brain_id, intent_id, kind, subject, ts, expires_ts, nonce, sig)
		 VALUES ('brainA', 'forged-id', 'claim', 'repoX', `+fmtF(nowF)+`, `+fmtF(nowF+3600)+`, 'forge-nonce', 'bm90YXJlYWxzaWc=')`)

	claims, err := ActiveClaims(remote, "repoX", "brainB", coordNow)
	if err != nil {
		t.Fatalf("ActiveClaims with forged row: %v", err)
	}
	if len(claims) != 0 {
		t.Fatalf("forged sig: expected 0 verified claims, got %d", len(claims))
	}
}

// TestActiveClaims_ImpersonationDropped verifies that a row claiming brain_id=A
// but signed by B's key is dropped (impersonation — Verify fails).
func TestActiveClaims_ImpersonationDropped(t *testing.T) {
	remote := newRemote(t)
	_, pubA := mkBrain(t)
	kpB, _ := mkBrain(t)

	// Register A (not B — B is an attacker not in registry, but that's irrelevant
	// since we're signing with B's key but claiming to be A).
	mustRegister(t, remote, "brainA", pubA)

	db := openRemote(t, remote)
	defer db.Close()
	mustExec(t, db, ddlFleetBrains)
	mustExec(t, db, ddlFleetIntents)

	// B signs an intent but uses brain_id="brainA" (impersonation).
	nowF := float64(coordNow.Unix())
	nonce := "impersonate-nonce"
	// Sign with B's key over A's brain_id → Verify(pubA, ...) will fail.
	badSig := attest.Sign(kpB, "brainA", "claim", "repoX", nowF, nonce, nowF+3600)

	mustExec(t, db,
		`INSERT INTO fleet_intents (brain_id, intent_id, kind, subject, ts, expires_ts, nonce, sig)
		 VALUES ('brainA', 'imp-id', 'claim', 'repoX', `+fmtF(nowF)+`, `+fmtF(nowF+3600)+`, '`+nonce+`', '`+esc(badSig)+`')`)

	claims, err := ActiveClaims(remote, "repoX", "brainB", coordNow)
	if err != nil {
		t.Fatalf("ActiveClaims with impersonated row: %v", err)
	}
	if len(claims) != 0 {
		t.Fatalf("impersonation: expected 0 verified claims, got %d", len(claims))
	}
}

// TestActiveClaims_ExpiredDropped verifies that a claim with expires_ts < now
// is excluded from ActiveClaims.
func TestActiveClaims_ExpiredDropped(t *testing.T) {
	remote := newRemote(t)
	kpA, pubA := mkBrain(t)
	mustRegister(t, remote, "brainA", pubA)

	// Publish with a TTL that places expires_ts in the past.
	pastTime := coordNow.Add(-2 * time.Hour)
	if err := PublishIntent(kpA, remote, "brainA", "claim", "repoX", time.Hour, pastTime); err != nil {
		t.Fatalf("PublishIntent (past): %v", err)
	}

	// The claim's expires_ts = pastTime + 1h < coordNow → should be excluded.
	claims, err := ActiveClaims(remote, "repoX", "brainB", coordNow)
	if err != nil {
		t.Fatalf("ActiveClaims with expired claim: %v", err)
	}
	if len(claims) != 0 {
		t.Fatalf("expired claim: expected 0 claims, got %d", len(claims))
	}
}

// TestPublishIntent_ReplayRejected verifies that a second INSERT with the same
// (brain_id, nonce) is rejected by the UNIQUE constraint (replay defense).
func TestPublishIntent_ReplayRejected(t *testing.T) {
	remote := newRemote(t)
	_, pubA := mkBrain(t)
	mustRegister(t, remote, "brainA", pubA)

	// Open the remote directly and insert a row with a known nonce.
	db := openRemote(t, remote)
	defer db.Close()
	mustExec(t, db, ddlFleetBrains)
	mustExec(t, db, ddlFleetIntents)

	nowF := float64(coordNow.Unix())
	mustExec(t, db,
		`INSERT INTO fleet_intents (brain_id, intent_id, kind, subject, ts, expires_ts, nonce, sig)
		 VALUES ('brainA', 'orig-id', 'claim', 'repoX', `+fmtF(nowF)+`, `+fmtF(nowF+3600)+`, 'static-nonce', 'sig1')`)

	// Replay: same (brain_id, nonce) → must fail.
	_, err := db.Exec(
		`INSERT INTO fleet_intents (brain_id, intent_id, kind, subject, ts, expires_ts, nonce, sig)
		 VALUES ('brainA', 'replay-id', 'claim', 'repoX', ` + fmtF(nowF) + `, ` + fmtF(nowF+3600) + `, 'static-nonce', 'sig2')`)
	if err == nil {
		t.Fatal("replay INSERT should have been rejected by UNIQUE(brain_id, nonce), but succeeded")
	}
	if !strings.Contains(err.Error(), "Constraint Error") && !strings.Contains(err.Error(), "UNIQUE") && !strings.Contains(err.Error(), "unique") {
		t.Logf("replay rejection error (expected unique/constraint violation): %v", err)
		// We accept any error here — the insert must simply fail.
	}
}

// TestActiveClaims_MultipleBrains verifies that when two brains publish claims on
// the same subject, each only sees the other's claim when it excludes itself.
func TestActiveClaims_MultipleBrains(t *testing.T) {
	remote := newRemote(t)
	kpA, pubA := mkBrain(t)
	kpB, pubB := mkBrain(t)

	mustRegister(t, remote, "brainA", pubA)
	mustRegister(t, remote, "brainB", pubB)

	ttl := time.Hour
	if err := PublishIntent(kpA, remote, "brainA", "claim", "repoX", ttl, coordNow); err != nil {
		t.Fatalf("PublishIntent A: %v", err)
	}
	if err := PublishIntent(kpB, remote, "brainB", "claim", "repoX", ttl, coordNow); err != nil {
		t.Fatalf("PublishIntent B: %v", err)
	}

	// A queries: should see only B's claim.
	claimsA, err := ActiveClaims(remote, "repoX", "brainA", coordNow)
	if err != nil {
		t.Fatalf("ActiveClaims exceptA: %v", err)
	}
	if len(claimsA) != 1 || claimsA[0].BrainID != "brainB" {
		t.Fatalf("A should see 1 claim from B, got %v", claimsA)
	}

	// B queries: should see only A's claim.
	claimsB, err := ActiveClaims(remote, "repoX", "brainB", coordNow)
	if err != nil {
		t.Fatalf("ActiveClaims exceptB: %v", err)
	}
	if len(claimsB) != 1 || claimsB[0].BrainID != "brainA" {
		t.Fatalf("B should see 1 claim from A, got %v", claimsB)
	}
}

// TestActiveClaims_WrongSubjectExcluded verifies that claims on a different
// subject are not returned.
func TestActiveClaims_WrongSubjectExcluded(t *testing.T) {
	remote := newRemote(t)
	kpA, pubA := mkBrain(t)
	mustRegister(t, remote, "brainA", pubA)

	if err := PublishIntent(kpA, remote, "brainA", "claim", "repoY", time.Hour, coordNow); err != nil {
		t.Fatalf("PublishIntent: %v", err)
	}

	claims, err := ActiveClaims(remote, "repoX", "brainB", coordNow) // different subject
	if err != nil {
		t.Fatalf("ActiveClaims wrong subject: %v", err)
	}
	if len(claims) != 0 {
		t.Fatalf("expected 0 claims for repoX, got %d", len(claims))
	}
}

// TestPublishIntent_TTLClamped verifies that a requested TTL exceeding maxClaimTTL
// (default 24h) is clamped: expires_ts is set to now + maxClaimTTL, not the huge
// requested value. A within-cap claim is left unclamped.
func TestPublishIntent_TTLClamped(t *testing.T) {
	remote := newRemote(t)
	kpA, pubA := mkBrain(t)
	mustRegister(t, remote, "brainA", pubA)

	// Request a 10-year TTL — must be clamped to now + 24h.
	hugeTTL := 10 * 365 * 24 * time.Hour
	if err := PublishIntent(kpA, remote, "brainA", "claim", "repoX", hugeTTL, coordNow); err != nil {
		t.Fatalf("PublishIntent (huge TTL): %v", err)
	}

	db := openRemote(t, remote)
	defer db.Close()

	var expiresTs float64
	db.QueryRow("SELECT expires_ts FROM fleet_intents WHERE brain_id='brainA' AND subject='repoX'").Scan(&expiresTs)

	wantCap := float64(coordNow.Add(24 * time.Hour).Unix())
	if expiresTs != wantCap {
		t.Fatalf("TTL clamp: expires_ts=%v, want capped at %v (now+24h)", expiresTs, wantCap)
	}

	// The clamped claim is still active at now (well within the 24h window).
	claims, err := ActiveClaims(remote, "repoX", "brainB", coordNow)
	if err != nil {
		t.Fatalf("ActiveClaims: %v", err)
	}
	if len(claims) != 1 {
		t.Fatalf("clamped claim should still be active, got %d claims", len(claims))
	}
}

// TestPublishIntent_TTLOverride verifies CORRALAI_CLAIM_MAX_TTL_SEC overrides the
// default cap.
func TestPublishIntent_TTLOverride(t *testing.T) {
	t.Setenv("CORRALAI_CLAIM_MAX_TTL_SEC", "3600") // 1h cap
	remote := newRemote(t)
	kpA, pubA := mkBrain(t)
	mustRegister(t, remote, "brainA", pubA)

	// Request 5h — must be clamped to 1h by the override.
	if err := PublishIntent(kpA, remote, "brainA", "claim", "repoX", 5*time.Hour, coordNow); err != nil {
		t.Fatalf("PublishIntent: %v", err)
	}

	db := openRemote(t, remote)
	defer db.Close()

	var expiresTs float64
	db.QueryRow("SELECT expires_ts FROM fleet_intents WHERE brain_id='brainA' AND subject='repoX'").Scan(&expiresTs)

	wantCap := float64(coordNow.Add(time.Hour).Unix())
	if expiresTs != wantCap {
		t.Fatalf("TTL override: expires_ts=%v, want capped at %v (now+1h)", expiresTs, wantCap)
	}
}

// TestActiveClaims_ReplayResurrectionRejected is the REQUIRED reproduction test for the
// replay-resurrection attack described in the security review.
//
// Attack vector (v1, now closed): an attacker with MotherDuck write access captures brain
// A's genuine signed fleet_intents row. After retention deletes the expired row (freeing
// the nonce), the attacker re-inserts it with an attacker-chosen far-future expires_ts.
// Under v1 (expires_ts NOT signed), attest.Verify would pass — the sig was genuine over
// the other fields and ts staleness is unbounded (claimFreshnessDisabled). The attacker
// could therefore publish a claim attributed to brain A with a lifetime A never agreed to.
//
// Fix (v2): expires_ts is now part of the signed canonical message. A re-inserted row with
// a tampered expires_ts fails Verify — the sig covers the original expires_ts, not the
// attacker-supplied one.
func TestActiveClaims_ReplayResurrectionRejected(t *testing.T) {
	remote := newRemote(t)
	kpA, pubA := mkBrain(t)
	mustRegister(t, remote, "brainA", pubA)

	// 1. Brain A publishes a legitimate short-lived claim (1h TTL).
	ttl := time.Hour
	if err := PublishIntent(kpA, remote, "brainA", "claim", "repoX", ttl, coordNow); err != nil {
		t.Fatalf("PublishIntent: %v", err)
	}

	// Positive: the legitimate claim IS returned before any attack.
	claimsBefore, err := ActiveClaims(remote, "repoX", "brainB", coordNow)
	if err != nil {
		t.Fatalf("ActiveClaims (pre-attack): %v", err)
	}
	if len(claimsBefore) != 1 {
		t.Fatalf("positive: expected 1 legitimate claim, got %d", len(claimsBefore))
	}

	// 2. Capture the genuine row (ts, expires_ts, nonce, sig) from the DB.
	db := openRemote(t, remote)
	defer db.Close()
	var origNonce, origSig string
	var origTS, origExpiresTS float64
	if err := db.QueryRow(
		"SELECT ts, expires_ts, nonce, sig FROM fleet_intents WHERE brain_id='brainA'",
	).Scan(&origTS, &origExpiresTS, &origNonce, &origSig); err != nil {
		t.Fatalf("capture original row: %v", err)
	}

	// 3. Simulate retention cleanup: delete the original row to free the nonce.
	if _, err := db.Exec("DELETE FROM fleet_intents WHERE brain_id='brainA'"); err != nil {
		t.Fatalf("simulate retention delete: %v", err)
	}

	// 4. Attacker re-inserts the genuine (sig, ts, nonce) with a far-future expires_ts.
	// Under v1 this would pass Verify (expires_ts not signed).
	// Under v2 Verify must REJECT it — the sig covers origExpiresTS, not farFuture.
	farFuture := float64(coordNow.Add(365 * 24 * time.Hour).Unix())
	mustExec(t, db,
		`INSERT INTO fleet_intents (brain_id, intent_id, kind, subject, ts, expires_ts, nonce, sig)
		 VALUES ('brainA', 'attack-id', 'claim', 'repoX', `+fmtF(origTS)+`, `+fmtF(farFuture)+`, '`+esc(origNonce)+`', '`+esc(origSig)+`')`)

	// ActiveClaims must NOT return the forged row.
	claimsAfter, err := ActiveClaims(remote, "repoX", "brainB", coordNow)
	if err != nil {
		t.Fatalf("ActiveClaims (post-attack): %v", err)
	}
	if len(claimsAfter) != 0 {
		t.Fatalf("replay-resurrection with forged expires_ts: expected 0 claims (attack REJECTED), got %d", len(claimsAfter))
	}
}

// ── Retention tests for fleet_intents ─────────────────────────────────────────

// TestFleetIntentsRetention_ExpiredCleaned verifies that expired claims are
// removed by Compact (TTLDays=0, expired-claims cleanup always runs), cursor-safe.
func TestFleetIntentsRetention_ExpiredCleaned(t *testing.T) {
	remote := newRemote(t)

	// Create tables in the remote directly.
	db := openRemote(t, remote)
	mustExec(t, db, ddlFleetBrains)
	mustExec(t, db, ddlFleetIntents)

	now := retentionNow // Unix 1_000_000_000
	nowF := float64(now.Unix())

	// Insert 3 rows for brainA:
	//   row 1: expired (expires_ts < now), ts=100 → should be deleted
	//   row 2: expired (expires_ts < now), ts=200 (MAX ts) → must SURVIVE (cursor safety)
	//   row 3: not expired (expires_ts > now), ts=50 → should survive
	mustExec(t, db, `
		INSERT INTO fleet_intents VALUES
		  ('brainA', 'id1', 'claim', 'repoX', 100.0, `+fmtF(nowF-1000)+`, 'n1', 's1'),
		  ('brainA', 'id2', 'claim', 'repoX', 200.0, `+fmtF(nowF-500)+`,  'n2', 's2'),
		  ('brainA', 'id3', 'claim', 'repoX',  50.0, `+fmtF(nowF+3600)+`, 'n3', 's3')`)
	// Also a row for brainB to verify isolation.
	mustExec(t, db, `
		INSERT INTO fleet_intents VALUES
		  ('brainB', 'id4', 'claim', 'repoX', 10.0, `+fmtF(nowF-100)+`, 'n4', 's4')`)
	db.Close()

	cfg := RetentionConfig{TTLDays: 0} // TTL off, but expired cleanup still runs.
	deleted, err := Compact(cfg, remote, "brainA", now)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}

	db2 := openRemote(t, remote)
	defer db2.Close()

	// row 1 (ts=100, expired) must be deleted.
	var c1 int
	db2.QueryRow("SELECT count(*) FROM fleet_intents WHERE brain_id='brainA' AND intent_id='id1'").Scan(&c1)
	if c1 != 0 {
		t.Errorf("row1 (expired, not-max-ts) must be deleted, still present")
	}

	// row 2 (ts=200, expired, MAX ts) must SURVIVE — cursor safety.
	var c2 int
	db2.QueryRow("SELECT count(*) FROM fleet_intents WHERE brain_id='brainA' AND intent_id='id2'").Scan(&c2)
	if c2 != 1 {
		t.Errorf("row2 (expired, MAX ts) must survive (cursor-safety watermark), got count=%d", c2)
	}

	// row 3 (ts=50, not expired) must survive.
	var c3 int
	db2.QueryRow("SELECT count(*) FROM fleet_intents WHERE brain_id='brainA' AND intent_id='id3'").Scan(&c3)
	if c3 != 1 {
		t.Errorf("row3 (not expired) must survive, got count=%d", c3)
	}

	// brainB's row must be untouched (per-brain isolation).
	var c4 int
	db2.QueryRow("SELECT count(*) FROM fleet_intents WHERE brain_id='brainB'").Scan(&c4)
	if c4 != 1 {
		t.Errorf("brainB's row must be untouched (isolation), got count=%d", c4)
	}

	// 1 row deleted for brainA.
	if n := deleted["fleet_intents"]; n != 1 {
		t.Errorf("deleted[fleet_intents]: expected 1, got %d", n)
	}
}

// TestFleetIntentsRetention_TTLCleansOld verifies that old-by-age rows (ts < cutoff)
// are removed when TTLDays > 0, per-brain isolated, cursor-safe.
// Seed: id1(ts=100,old), id2(ts=150,old), id3(ts=tsRecent,not-old), brainB-id4(ts=100,old).
// max(ts) for brainA = tsRecent (id3). id1 and id2 are both < cutoff AND < max-ts → deleted.
// id3 (ts > cutoff) → survives. brainB isolated.
func TestFleetIntentsRetention_TTLCleansOld(t *testing.T) {
	remote := newRemote(t)

	db := openRemote(t, remote)
	mustExec(t, db, ddlFleetBrains)
	mustExec(t, db, ddlFleetIntents)

	now := retentionNow // Unix 1_000_000_000; TTLDays=30 → cutoff ≈ 997_400_000
	nowF := float64(now.Unix())

	mustExec(t, db, `
		INSERT INTO fleet_intents VALUES
		  ('brainA', 'id1', 'claim', 'repoX', `+fmtF(tsOld)+`,    `+fmtF(nowF+7200)+`, 'n1', 's1'),
		  ('brainA', 'id2', 'claim', 'repoX', `+fmtF(tsOld+50)+`, `+fmtF(nowF+7200)+`, 'n2', 's2'),
		  ('brainA', 'id3', 'claim', 'repoX', `+fmtF(tsRecent)+`, `+fmtF(nowF+7200)+`, 'n3', 's3'),
		  ('brainB', 'id4', 'claim', 'repoX', `+fmtF(tsOld)+`,    `+fmtF(nowF+7200)+`, 'n4', 's4')`)
	db.Close()

	cfg := RetentionConfig{TTLDays: 30}
	deleted, err := Compact(cfg, remote, "brainA", now)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}

	db2 := openRemote(t, remote)
	defer db2.Close()

	// id1 and id2 (old, not-max-ts) → both deleted.
	for _, id := range []string{"id1", "id2"} {
		var c int
		db2.QueryRow("SELECT count(*) FROM fleet_intents WHERE brain_id='brainA' AND intent_id=?", id).Scan(&c)
		if c != 0 {
			t.Errorf("%s (old, not-max-ts): expected deleted, still present", id)
		}
	}

	// id3 (ts=tsRecent, not old) → survives.
	var c3 int
	db2.QueryRow("SELECT count(*) FROM fleet_intents WHERE brain_id='brainA' AND intent_id='id3'").Scan(&c3)
	if c3 != 1 {
		t.Errorf("id3 (recent): must survive, got %d", c3)
	}

	// Per-brain isolation: brainB untouched.
	var c4 int
	db2.QueryRow("SELECT count(*) FROM fleet_intents WHERE brain_id='brainB'").Scan(&c4)
	if c4 != 1 {
		t.Errorf("brainB isolation: expected 1 row, got %d", c4)
	}

	// CURSOR SAFETY: max(ts) for brainA = tsRecent (unchanged).
	var maxTS float64
	db2.QueryRow("SELECT max(ts) FROM fleet_intents WHERE brain_id='brainA'").Scan(&maxTS)
	if maxTS != tsRecent {
		t.Errorf("cursor safety: max(ts) changed to %v (want tsRecent=%v)", maxTS, tsRecent)
	}

	// 2 rows deleted by TTL (id1 + id2).
	if n := deleted["fleet_intents"]; n != 2 {
		t.Errorf("deleted[fleet_intents]: expected 2, got %d", n)
	}
}

// TestFleetIntentsRetention_TTLCleansOldMaxCursorSurvives verifies the critical
// cursor-safety edge case: the MAX-ts row survives even when it is below the TTL cutoff.
// This requires no recent rows — the max-ts must itself be old.
func TestFleetIntentsRetention_TTLCleansOldMaxCursorSurvives(t *testing.T) {
	remote := newRemote(t)

	db := openRemote(t, remote)
	mustExec(t, db, ddlFleetBrains)
	mustExec(t, db, ddlFleetIntents)

	now := retentionNow // Unix 1_000_000_000
	nowF := float64(now.Unix())

	// Only old rows (all below cutoff):
	//   id1: ts=100, not-max → deleted
	//   id2: ts=150, MAX ts (no recent rows) → survives (watermark guard)
	mustExec(t, db, `
		INSERT INTO fleet_intents VALUES
		  ('brainA', 'id1', 'claim', 'repoX', 100.0, `+fmtF(nowF+7200)+`, 'n1', 's1'),
		  ('brainA', 'id2', 'claim', 'repoX', 150.0, `+fmtF(nowF+7200)+`, 'n2', 's2')`)
	db.Close()

	cfg := RetentionConfig{TTLDays: 30}
	deleted, err := Compact(cfg, remote, "brainA", now)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}

	db2 := openRemote(t, remote)
	defer db2.Close()

	// id1 (old, not-max-ts=100 < 150) → deleted.
	var c1 int
	db2.QueryRow("SELECT count(*) FROM fleet_intents WHERE brain_id='brainA' AND intent_id='id1'").Scan(&c1)
	if c1 != 0 {
		t.Errorf("id1 (old, not-max): expected deleted, still present")
	}

	// id2 (old, MAX ts=150) → must survive (cursor-safety watermark).
	var c2 int
	db2.QueryRow("SELECT count(*) FROM fleet_intents WHERE brain_id='brainA' AND intent_id='id2'").Scan(&c2)
	if c2 != 1 {
		t.Errorf("id2 (old, MAX ts): must survive watermark guard, got %d", c2)
	}

	// CURSOR SAFETY: max(ts) for brainA must still be 150.
	var maxTS float64
	db2.QueryRow("SELECT max(ts) FROM fleet_intents WHERE brain_id='brainA'").Scan(&maxTS)
	if maxTS != 150.0 {
		t.Errorf("cursor safety: max(ts) changed to %v (want 150.0)", maxTS)
	}

	if n := deleted["fleet_intents"]; n != 1 {
		t.Errorf("deleted[fleet_intents] TTL: expected 1, got %d", n)
	}
}

// TestFleetIntentsRetention_DisabledNoDeletes verifies that Disabled=true is a
// strict no-op for fleet_intents too.
func TestFleetIntentsRetention_DisabledNoDeletes(t *testing.T) {
	remote := newRemote(t)

	db := openRemote(t, remote)
	mustExec(t, db, ddlFleetBrains)
	mustExec(t, db, ddlFleetIntents)

	now := retentionNow
	nowF := float64(now.Unix())

	mustExec(t, db, `
		INSERT INTO fleet_intents VALUES
		  ('brainA', 'id1', 'claim', 'repoX', 1.0, `+fmtF(nowF-100)+`, 'n1', 's1')`)
	db.Close()

	cfg := RetentionConfig{Disabled: true, TTLDays: 30}
	result, err := Compact(cfg, remote, "brainA", now)
	if err != nil {
		t.Fatalf("Compact disabled: %v", err)
	}
	if result != nil {
		t.Errorf("Disabled=true: expected nil map, got %v", result)
	}

	db2 := openRemote(t, remote)
	defer db2.Close()
	var c int
	db2.QueryRow("SELECT count(*) FROM fleet_intents WHERE brain_id='brainA'").Scan(&c)
	if c != 1 {
		t.Errorf("Disabled=true: row must not be deleted, got count=%d", c)
	}
}

// ── Local helpers for coord tests ─────────────────────────────────────────────

// mustRegister calls RegisterBrain and fails the test if an error occurs.
func mustRegister(t *testing.T, remote, brainID, pubB64 string) {
	t.Helper()
	out, err := RegisterBrain(remote, brainID, pubB64, nil, coordNow)
	if err != nil {
		t.Fatalf("mustRegister %s: %v", brainID, err)
	}
	if out != attest.Registered && out != attest.AlreadyTrusted {
		t.Fatalf("mustRegister %s: unexpected outcome %v", brainID, out)
	}
}

// openRemote opens the remote .duckdb file for direct seeding/inspection.
func openRemote(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("duckdb", path)
	if err != nil {
		t.Fatalf("openRemote %s: %v", path, err)
	}
	return db
}

// mustExec executes a SQL statement on db, failing the test on error.
func mustExec(t *testing.T, db *sql.DB, stmt string) {
	t.Helper()
	if _, err := db.Exec(stmt); err != nil {
		t.Fatalf("mustExec: %v\n  stmt: %s", err, stmt)
	}
}
