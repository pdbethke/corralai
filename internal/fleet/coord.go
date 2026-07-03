// SPDX-License-Identifier: Elastic-2.0

// coord.go — fleet_brains registry + fleet_intents signed coordination plane.
//
// This file implements the MotherDuck-mediated signed coordination plane:
//   - fleet_brains: TOFU brain-identity registry (brain_id PRIMARY KEY, pubkey, registered_ts)
//   - fleet_intents: signed coordination claims ((brain_id, nonce) UNIQUE — replay defense)
//
// FAIL-CLOSED INVARIANT: ActiveClaims calls attest.Verify on every candidate row.
// A forged, impersonated, replayed, or expired row is NEVER returned — it is silently
// dropped. Any verification error → drop, do not propagate to the caller.
//
// ATOMICITY: RegisterBrain uses INSERT … ON CONFLICT (brain_id) DO NOTHING + re-read
// to ensure that in a registration race, a race loser correctly gets Conflict (not
// Registered), because the pinned key is authoritative, not the caller's key.
package fleet

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"

	"github.com/pdbethke/corralai/internal/attest"
)

// claimFreshnessDisabled is passed as attest.Verify's skew for STORED claims to
// DISABLE the anti-replay freshness window. A coordination claim is signed ONCE
// and persists for its TTL, so signature-freshness (|now-ts| <= skew) is the wrong
// concept here — expiry is gated separately by expires_ts (SQL + Go), and replay by
// UNIQUE(brain_id, nonce). It is finite (math.MaxFloat64) so attest.Verify's Task-1
// non-finite-ts guard remains fully in effect.
const claimFreshnessDisabled = math.MaxFloat64

// maxClaimTTL bounds how far in the future a claim's expires_ts may be set. A
// valid-key brain publishing a huge-TTL claim (to hog a subject) is within the
// accepted insider boundary, but an accidental or abusive multi-year TTL is worth
// clamping. Overridable via CORRALAI_CLAIM_MAX_TTL_SEC (positive integer seconds).
// A brain renews a claim by re-publishing (a fresh claim with a new nonce).
const maxClaimTTL = 24 * time.Hour

// resolveMaxClaimTTL returns the effective max claim TTL, honouring the
// CORRALAI_CLAIM_MAX_TTL_SEC override when it parses to a positive value.
func resolveMaxClaimTTL() time.Duration {
	if v := os.Getenv("CORRALAI_CLAIM_MAX_TTL_SEC"); v != "" {
		if sec, err := strconv.Atoi(v); err == nil && sec > 0 {
			return time.Duration(sec) * time.Second
		}
	}
	return maxClaimTTL
}

// Claim is a verified, unexpired coordination intent from another brain.
// Every Claim returned by ActiveClaims has passed attest.Verify against the
// registered pubkey in fleet_brains.
type Claim struct {
	BrainID   string
	IntentID  string
	Kind      string
	Subject   string
	Ts        float64
	ExpiresTs float64
	Nonce     string
	Sig       string
}

const (
	// ddlFleetBrains creates the brain-identity registry.
	// PRIMARY KEY on brain_id ensures the ON CONFLICT DO NOTHING upsert is atomic.
	ddlFleetBrains = `CREATE TABLE IF NOT EXISTS remote.fleet_brains (
		brain_id      VARCHAR PRIMARY KEY,
		pubkey        VARCHAR NOT NULL,
		registered_ts DOUBLE  NOT NULL
	)`

	// ddlFleetIntents creates the signed claims table.
	// UNIQUE (brain_id, nonce) rejects replays at the DB level.
	ddlFleetIntents = `CREATE TABLE IF NOT EXISTS remote.fleet_intents (
		brain_id   VARCHAR NOT NULL,
		intent_id  VARCHAR NOT NULL,
		kind       VARCHAR NOT NULL,
		subject    VARCHAR NOT NULL,
		ts         DOUBLE  NOT NULL,
		expires_ts DOUBLE  NOT NULL,
		nonce      VARCHAR NOT NULL,
		sig        VARCHAR NOT NULL,
		UNIQUE (brain_id, nonce)
	)`
)

// openCoordDB opens an in-memory DuckDB and attaches remoteAttach as "remote"
// (read-write). Mirrors Sync's and Compact's connection recipe exactly:
// motherduck is only installed when the attach string starts with "md:".
func openCoordDB(remoteAttach string) (*sql.DB, error) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, fmt.Errorf("coord: open in-mem db: %w", err)
	}
	if strings.HasPrefix(remoteAttach, "md:") {
		for _, stmt := range []string{"INSTALL motherduck", "LOAD motherduck"} {
			if _, err := db.Exec(stmt); err != nil {
				_ = db.Close()
				return nil, fmt.Errorf("coord: load motherduck: %w", err)
			}
		}
	}
	if _, err := db.Exec(fmt.Sprintf("ATTACH '%s' AS remote", esc(remoteAttach))); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("coord: attach remote: %w", err)
	}
	return db, nil
}

// ensureCoordTables creates fleet_brains and fleet_intents if absent.
// Called at the start of every public function so the tables are created
// lazily on first use — no separate migration step required.
func ensureCoordTables(db *sql.DB) error {
	if _, err := db.Exec(ddlFleetBrains); err != nil {
		return fmt.Errorf("create fleet_brains: %w", err)
	}
	if _, err := db.Exec(ddlFleetIntents); err != nil {
		return fmt.Errorf("create fleet_intents: %w", err)
	}
	return nil
}

// RegisterBrain atomically registers a brain's Ed25519 public key.
//
// The allowlist (when non-nil) is checked first — an unknown or mismatched
// brain/key pair is Rejected before touching the DB.
//
// The DB-level atomicity guarantee: INSERT INTO fleet_brains … ON CONFLICT
// (brain_id) DO NOTHING inserts exactly once per brain_id in a concurrent-safe
// way. After the attempt, the pinned key is re-read and compared to the caller's
// key, determining the outcome:
//
//   - 1 row inserted → Registered (we are the first registrant)
//   - 0 rows inserted, pinned == caller's key → AlreadyTrusted
//   - 0 rows inserted, pinned != caller's key → Conflict (race loser or impersonation attempt)
//
// The pinned key is NEVER overwritten on Conflict.
func RegisterBrain(remoteAttach, brainID, pubB64 string, allowlist map[string]string, now time.Time) (attest.Outcome, error) {
	// Allowlist fast-path: reject before touching DB.
	if allowlist != nil {
		want, ok := allowlist[brainID]
		if !ok || want != pubB64 {
			return attest.Rejected, nil
		}
	}

	db, err := openCoordDB(remoteAttach)
	if err != nil {
		return attest.Rejected, err
	}
	defer db.Close()

	if err := ensureCoordTables(db); err != nil {
		return attest.Rejected, err
	}

	nowF := float64(now.Unix())

	// Atomic insert-if-absent. DuckDB's ON CONFLICT DO NOTHING with PRIMARY KEY
	// is atomic with respect to concurrent writes — only one INSERT per brain_id wins.
	res, err := db.Exec(
		`INSERT INTO remote.fleet_brains (brain_id, pubkey, registered_ts)
		 VALUES (?, ?, ?)
		 ON CONFLICT (brain_id) DO NOTHING`,
		brainID, pubB64, nowF,
	)
	if err != nil {
		return attest.Rejected, fmt.Errorf("register brain insert: %w", err)
	}

	inserted, _ := res.RowsAffected()
	if inserted == 1 {
		// We just pinned a fresh key — this brain is now registered.
		return attest.Registered, nil
	}

	// Row already existed (0 rows affected). Re-read the authoritative pinned key
	// and compare: same → the caller is already trusted; different → Conflict
	// (race loser or impersonation; never overwrite the pinned key).
	var pinnedKey string
	if err := db.QueryRow(
		"SELECT pubkey FROM remote.fleet_brains WHERE brain_id = ?", brainID,
	).Scan(&pinnedKey); err != nil {
		return attest.Rejected, fmt.Errorf("register brain re-read: %w", err)
	}

	if pinnedKey == pubB64 {
		return attest.AlreadyTrusted, nil
	}
	return attest.Conflict, nil
}

// PublishIntent signs a coordination intent and INSERTs it into fleet_intents.
// A random 16-byte hex nonce ensures uniqueness; a second INSERT with the same
// (brain_id, nonce) pair is rejected by the DB's UNIQUE constraint (replay defense).
//
// The requested ttl is clamped to maxClaimTTL (default 24h, overridable via
// CORRALAI_CLAIM_MAX_TTL_SEC): expires_ts is never set further out than
// now + maxClaimTTL, bounding accidental/abusive long-lived claims. A brain
// keeps a claim alive past the cap by re-publishing (a fresh claim, new nonce).
func PublishIntent(kp attest.KeyPair, remoteAttach, brainID, kind, subject string, ttl time.Duration, now time.Time) error {
	db, err := openCoordDB(remoteAttach)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := ensureCoordTables(db); err != nil {
		return err
	}

	// Clamp the TTL to the configured maximum (griefing/accident guard).
	if cap := resolveMaxClaimTTL(); ttl > cap {
		ttl = cap
	}

	ts := float64(now.Unix())
	expiresTs := float64(now.Add(ttl).Unix())

	// Random nonce: 16 bytes hex → 32 char string, unguessable and unique per intent.
	var nonceBuf [16]byte
	if _, err := rand.Read(nonceBuf[:]); err != nil {
		return fmt.Errorf("publish intent: generate nonce: %w", err)
	}
	nonce := hex.EncodeToString(nonceBuf[:])

	// Random intent_id: same entropy source for a unique row identifier.
	var idBuf [16]byte
	if _, err := rand.Read(idBuf[:]); err != nil {
		return fmt.Errorf("publish intent: generate intent id: %w", err)
	}
	intentID := hex.EncodeToString(idBuf[:])

	// Sign over expiresTs (v2) so an attacker cannot extend the lifetime of a captured
	// row by re-inserting it with a far-future expires_ts — the sig would cover the
	// original expires_ts, not the attacker-supplied value.
	sig := attest.Sign(kp, brainID, kind, subject, ts, nonce, expiresTs)

	_, err = db.Exec(
		`INSERT INTO remote.fleet_intents
		    (brain_id, intent_id, kind, subject, ts, expires_ts, nonce, sig)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		brainID, intentID, kind, subject, ts, expiresTs, nonce, sig,
	)
	if err != nil {
		return fmt.Errorf("publish intent: insert: %w", err)
	}
	return nil
}

// ActiveClaims returns VERIFIED, non-expired claims on subject from brains other
// than exceptBrain.
//
// FAIL-CLOSED guarantee: every candidate row is checked via attest.Verify against
// the registered pubkey in fleet_brains. A row whose signature does not verify
// (forged, impersonated, tampered) is dropped silently. An expired row (expires_ts
// < now) is excluded by the SQL WHERE clause and, defense-in-depth, by an explicit
// in-Go check. Only rows that are:
//  1. unexpired (expires_ts >= now)
//  2. from a brain other than exceptBrain
//  3. whose sig verifies against the registered pubkey
//
// are returned. Any deviation from all three conditions → the row is dropped.
func ActiveClaims(remoteAttach, subject, exceptBrain string, now time.Time) ([]Claim, error) {
	db, err := openCoordDB(remoteAttach)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	if err := ensureCoordTables(db); err != nil {
		return nil, err
	}

	nowF := float64(now.Unix())

	rows, err := db.Query(
		`SELECT i.brain_id, i.intent_id, i.kind, i.subject, i.ts, i.expires_ts, i.nonce, i.sig,
		        b.pubkey
		 FROM   remote.fleet_intents i
		 JOIN   remote.fleet_brains  b ON b.brain_id = i.brain_id
		 WHERE  i.subject    = ?
		   AND  i.brain_id  != ?
		   AND  i.expires_ts >= ?
		 ORDER BY i.ts`,
		subject, exceptBrain, nowF,
	)
	if err != nil {
		return nil, fmt.Errorf("active claims query: %w", err)
	}
	defer rows.Close()

	var claims []Claim
	for rows.Next() {
		var c Claim
		var pubKey string
		if err := rows.Scan(
			&c.BrainID, &c.IntentID, &c.Kind, &c.Subject,
			&c.Ts, &c.ExpiresTs, &c.Nonce, &c.Sig,
			&pubKey,
		); err != nil {
			return nil, fmt.Errorf("active claims scan: %w", err)
		}

		// Expiry gate (the SOLE expiry concern): SQL already filtered
		// expires_ts >= now; this is the explicit defense-in-depth check.
		if c.ExpiresTs < nowF {
			continue
		}

		// FAIL-CLOSED signature check. Stored claims persist for their TTL; expiry
		// is gated by expires_ts (SQL + the Go check above), replay by
		// UNIQUE(brain_id, nonce), so signature-freshness is intentionally NOT
		// enforced here — we pass claimFreshnessDisabled to disable Verify's
		// anti-replay window (signature-freshness and claim-expiry are orthogonal;
		// coupling them via skew=expires_ts-ts was a code smell). Verify still
		// checks the Ed25519 signature, pubkey validity, and non-finite ts.
		// c.ExpiresTs is passed as a signed field (v2): a row whose expires_ts was
		// tampered (replay-resurrection attack) fails here because the sig covers
		// the original expires_ts, not the attacker-supplied one. claimFreshnessDisabled
		// remains SAFE: a resurrected row carries its original past expires_ts → the Go
		// expiry check above drops it even if sig somehow passed.
		// Any error → drop this row entirely.
		if err := attest.Verify(pubKey, c.BrainID, c.Kind, c.Subject, c.Ts, c.Nonce, c.Sig, c.ExpiresTs, nowF, claimFreshnessDisabled); err != nil {
			continue // forged / impersonated / tampered → silently drop
		}

		claims = append(claims, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("active claims iterate: %w", err)
	}
	return claims, nil
}
