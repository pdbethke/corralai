// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/attest"
	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/fleet"
)

// claimsNow is the fixed reference time for fleet_claims tests.
var claimsNow = time.Unix(2_100_000_000, 0)

// mkFleetBrain generates a fresh keypair and returns (keyPair, pubB64).
func mkFleetBrain(t *testing.T) (attest.KeyPair, string) {
	t.Helper()
	kp, err := attest.LoadOrCreateKey("", filepath.Join(t.TempDir(), "key"))
	if err != nil {
		t.Fatalf("mkFleetBrain: %v", err)
	}
	return kp, attest.PubB64(kp.Pub)
}

// newFleetClaimsRemote creates a temporary DuckDB file and returns its path.
func newFleetClaimsRemote(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "remote.duckdb")
}

// newFleetClaimsBrain boots a brain with CrossSwarm enabled against target,
// presenting itself as brainID, and connects an in-mem MCP session to it.
func newFleetClaimsBrain(t *testing.T, target, brainID string, rateLimit int) *mcp.ClientSession {
	t.Helper()
	store, err := coord.Open(filepath.Join(t.TempDir(), "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	opts := Options{
		CrossSwarm:           true,
		FleetTarget:          target,
		FleetBrainID:         brainID,
		FleetClaimsRateLimit: rateLimit,
	}
	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() { _ = NewServer(store, nil, opts).Run(ctx, serverT) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "fleet-claims-test", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

// callFleetClaims invokes fleet_claims for the given subject and returns the result.
// Does NOT fatal on tool-level errors; caller inspects res.IsError.
func callFleetClaims(t *testing.T, sess *mcp.ClientSession, subject string) *mcp.CallToolResult {
	t.Helper()
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "fleet_claims",
		Arguments: map[string]any{"subject": subject},
	})
	if err != nil {
		t.Fatalf("fleet_claims transport error: %v", err)
	}
	return res
}

// decodeFleetClaims unmarshals a successful fleet_claims result into fleetClaimsOut.
func decodeFleetClaims(t *testing.T, res *mcp.CallToolResult) fleetClaimsOut {
	t.Helper()
	if res.IsError {
		t.Fatalf("fleet_claims returned tool error: %s", contentText(res))
	}
	var out fleetClaimsOut
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode fleet_claims result: %v", err)
	}
	return out
}

// mustRegisterPeer registers a peer brain in the remote DB, failing the test on error.
func mustRegisterPeer(t *testing.T, remote, brainID, pubB64 string) {
	t.Helper()
	out, err := fleet.RegisterBrain(remote, brainID, pubB64, nil, claimsNow)
	if err != nil {
		t.Fatalf("mustRegisterPeer %s: %v", brainID, err)
	}
	if out != attest.Registered && out != attest.AlreadyTrusted {
		t.Fatalf("mustRegisterPeer %s: unexpected outcome %v", brainID, out)
	}
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestFleetClaims_HappyPath: a peer brain publishes a claim on repoX;
// fleet_claims on "brainB" returns that verified claim.
func TestFleetClaims_HappyPath(t *testing.T) {
	remote := newFleetClaimsRemote(t)
	kpA, pubA := mkFleetBrain(t)

	mustRegisterPeer(t, remote, "peerA", pubA)
	if err := fleet.PublishIntent(kpA, remote, "peerA", "claim", "repoX", time.Hour, claimsNow); err != nil {
		t.Fatalf("PublishIntent: %v", err)
	}

	sess := newFleetClaimsBrain(t, remote, "brainB", 0)
	res := callFleetClaims(t, sess, "repoX")
	out := decodeFleetClaims(t, res)

	if len(out.Claims) != 1 {
		t.Fatalf("expected 1 verified claim, got %d", len(out.Claims))
	}
	if out.Claims[0].BrainID != "peerA" {
		t.Errorf("expected claim from peerA, got %s", out.Claims[0].BrainID)
	}
	if out.Claims[0].Subject != "repoX" {
		t.Errorf("expected subject=repoX, got %s", out.Claims[0].Subject)
	}
	if out.Claims[0].ExpiresTs <= float64(claimsNow.Unix()) {
		t.Errorf("expires_ts should be in the future: %v", out.Claims[0].ExpiresTs)
	}
}

// TestFleetClaims_OwnBrainExcluded: a brain never sees its own claims.
func TestFleetClaims_OwnBrainExcluded(t *testing.T) {
	remote := newFleetClaimsRemote(t)
	kpA, pubA := mkFleetBrain(t)

	// Register and publish AS "brainA" itself.
	mustRegisterPeer(t, remote, "brainA", pubA)
	if err := fleet.PublishIntent(kpA, remote, "brainA", "claim", "repoX", time.Hour, claimsNow); err != nil {
		t.Fatalf("PublishIntent: %v", err)
	}

	// Brain presenting as "brainA" queries its own subject → must see 0 claims.
	sess := newFleetClaimsBrain(t, remote, "brainA", 0)
	res := callFleetClaims(t, sess, "repoX")
	out := decodeFleetClaims(t, res)

	if len(out.Claims) != 0 {
		t.Errorf("own brain should be excluded; got %d claims", len(out.Claims))
	}
}

// TestFleetClaims_ForgedSigDropped: a manually inserted row with a bogus signature
// must NOT appear in fleet_claims results (fail-closed).
func TestFleetClaims_ForgedSigDropped(t *testing.T) {
	remote := newFleetClaimsRemote(t)
	_, pubA := mkFleetBrain(t)
	mustRegisterPeer(t, remote, "peerA", pubA)

	// Seed the remote tables directly and insert a forged row.
	db, err := sql.Open("duckdb", remote)
	if err != nil {
		t.Fatal(err)
	}
	// Create the tables (may already exist from RegisterBrain; CREATE IF NOT EXISTS).
	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS fleet_brains (brain_id VARCHAR PRIMARY KEY, pubkey VARCHAR NOT NULL, registered_ts DOUBLE NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS fleet_intents (brain_id VARCHAR NOT NULL, intent_id VARCHAR NOT NULL, kind VARCHAR NOT NULL, subject VARCHAR NOT NULL, ts DOUBLE NOT NULL, expires_ts DOUBLE NOT NULL, nonce VARCHAR NOT NULL, sig VARCHAR NOT NULL, UNIQUE (brain_id, nonce))`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			db.Close()
			t.Fatalf("DDL: %v", err)
		}
	}
	nowF := float64(claimsNow.Unix())
	_, err = db.Exec(
		`INSERT OR IGNORE INTO fleet_intents (brain_id, intent_id, kind, subject, ts, expires_ts, nonce, sig)
		 VALUES ('peerA', 'forged-id', 'claim', 'repoX', ?, ?, 'forge-nonce', 'bm90YXJlYWxzaWc=')`,
		nowF, nowF+3600,
	)
	db.Close()
	if err != nil {
		t.Fatalf("insert forged row: %v", err)
	}

	sess := newFleetClaimsBrain(t, remote, "brainB", 0)
	res := callFleetClaims(t, sess, "repoX")
	out := decodeFleetClaims(t, res)

	if len(out.Claims) != 0 {
		t.Errorf("forged sig must be dropped (fail-closed); got %d claim(s)", len(out.Claims))
	}
}

// TestFleetClaims_DisabledWhenCrossSwarmFalse: the fleet_claims tool must NOT appear
// in the MCP tool list when CrossSwarm is false.
func TestFleetClaims_DisabledWhenCrossSwarmFalse(t *testing.T) {
	store, err := coord.Open(filepath.Join(t.TempDir(), "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() { _ = NewServer(store, nil, Options{}).Run(ctx, serverT) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "no-xswarm-test", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { sess.Close() })

	lt, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	for _, tool := range lt.Tools {
		if tool.Name == "fleet_claims" {
			t.Error("fleet_claims must NOT be registered when CrossSwarm is false")
		}
	}
}

// TestFleetClaims_RateLimit: per-principal rate limit trips; DuckDB not touched
// on over-limit requests.
func TestFleetClaims_RateLimit(t *testing.T) {
	remote := newFleetClaimsRemote(t)
	kpA, pubA := mkFleetBrain(t)
	mustRegisterPeer(t, remote, "peerA", pubA)
	if err := fleet.PublishIntent(kpA, remote, "peerA", "claim", "repoX", time.Hour, claimsNow); err != nil {
		t.Fatalf("PublishIntent: %v", err)
	}

	const rateLimit = 3
	sess := newFleetClaimsBrain(t, remote, "brainB", rateLimit)

	// First rateLimit calls must succeed.
	for i := 0; i < rateLimit; i++ {
		res := callFleetClaims(t, sess, "repoX")
		if res.IsError {
			t.Fatalf("call %d should succeed (within rate limit=%d): %s", i+1, rateLimit, contentText(res))
		}
	}

	// The (rateLimit+1)-th call must be refused.
	res := callFleetClaims(t, sess, "repoX")
	if !res.IsError {
		t.Fatal("expected rate-limit error on call over limit, got success")
	}
	errText := contentText(res)
	if !strings.Contains(strings.ToLower(errText), "rate limit") {
		t.Errorf("error should mention 'rate limit', got: %s", errText)
	}
}

// TestFleetClaims_EmptyWhenNoRemoteClaims: returns an empty list (not an error)
// when there are no active peer claims on the queried subject.
func TestFleetClaims_EmptyWhenNoRemoteClaims(t *testing.T) {
	remote := newFleetClaimsRemote(t)
	sess := newFleetClaimsBrain(t, remote, "brainB", 0)

	res := callFleetClaims(t, sess, "repoX")
	out := decodeFleetClaims(t, res)
	if out.Claims == nil {
		t.Error("expected non-nil (empty) Claims slice, got nil")
	}
	if len(out.Claims) != 0 {
		t.Errorf("expected 0 claims, got %d", len(out.Claims))
	}
}

// TestFleetClaims_MultiPeer: two peer brains each claim a subject;
// the querying brain sees both verified claims.
func TestFleetClaims_MultiPeer(t *testing.T) {
	remote := newFleetClaimsRemote(t)
	kpA, pubA := mkFleetBrain(t)
	kpB, pubB := mkFleetBrain(t)
	mustRegisterPeer(t, remote, "peerA", pubA)
	mustRegisterPeer(t, remote, "peerB", pubB)

	if err := fleet.PublishIntent(kpA, remote, "peerA", "claim", "repoX", time.Hour, claimsNow); err != nil {
		t.Fatalf("PublishIntent A: %v", err)
	}
	if err := fleet.PublishIntent(kpB, remote, "peerB", "claim", "repoX", time.Hour, claimsNow); err != nil {
		t.Fatalf("PublishIntent B: %v", err)
	}

	sess := newFleetClaimsBrain(t, remote, "brainC", 0)
	res := callFleetClaims(t, sess, "repoX")
	out := decodeFleetClaims(t, res)

	if len(out.Claims) != 2 {
		t.Fatalf("expected 2 verified claims (from peerA and peerB), got %d", len(out.Claims))
	}
	seen := map[string]bool{}
	for _, c := range out.Claims {
		seen[c.BrainID] = true
	}
	if !seen["peerA"] || !seen["peerB"] {
		t.Errorf("expected claims from peerA and peerB; got brain_ids: %v", func() []string {
			var ids []string
			for id := range seen {
				ids = append(ids, id)
			}
			return ids
		}())
	}
}
