// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/marcboeker/go-duckdb/v2"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/certify"
	"github.com/pdbethke/corralai/internal/transparency"
)

// signedFixtureStatement writes a plain statement + its signed sibling
// envelope (via the real writeSignedStatementEnvelope — same as
// certify --repo --attest itself calls) for one scan, seeding a
// deterministic fixture key so tests are reproducible. Returns the PLAIN
// statement's path — resolveEnvelopePath derives the envelope from it, the
// same convention `corral verify --attest` documents.
func signedFixtureStatement(t *testing.T, repo string, scanID int64, whRowsSHA string) string {
	t.Helper()
	fixtureCertifyKey(t)

	stmt := certify.BuildAuditAttestation(certify.AuditStatement{
		Repo: repo, Commit: "deadbeef", Audited: 1, Candidates: 1,
		ScanID: scanID, WarehouseRowsSHA256: whRowsSHA, WarehouseRowsHashVersion: WarehouseRowsHashVersion,
		Files: []certify.AuditedFile{{Path: "pkg/a.go", KillRate: ptrF(0.5), Survivors: 2, ProvenMissed: 0}},
	})
	b, err := json.MarshalIndent(stmt, "", "  ")
	if err != nil {
		t.Fatalf("marshal fixture statement: %v", err)
	}
	stmtPath := filepath.Join(t.TempDir(), "statement.json")
	if err := os.WriteFile(stmtPath, b, 0o600); err != nil {
		t.Fatalf("write fixture statement: %v", err)
	}
	if _, err := writeSignedStatementEnvelope(stmtPath, stmt); err != nil {
		t.Fatalf("writeSignedStatementEnvelope: %v", err)
	}
	return stmtPath
}

// pushedRowsHash builds and pushes a tiny one-file bundle for (repo,
// scanID) to a fresh temp warehouse, returning its target path and the
// EXACT warehouseRowsSHA256 a statement referring to it should carry —
// computed by the SAME function verify's own check 2 recomputes with, so a
// test that pushes this bundle and signs a statement with this hash proves
// the round trip, not a coincidence.
func pushedRowsHash(t *testing.T, repo string, scanID int64) (target, hash string) {
	t.Helper()
	bundle := auditpush.Bundle{
		Scan: auditpush.ScanRow{Repo: repo, ScanID: scanID, Commit: "deadbeef", Audited: 1, Candidates: 1},
		Files: []auditpush.Row{
			{Repo: repo, ScanID: scanID, Path: "pkg/a.go", Commit: "deadbeef",
				KillRate: ptrF(0.5), Survivors: 2, Disposition: "audited", Evidence: "proven"},
		},
	}
	got, err := warehouseRowsSHA256(bundle)
	if err != nil {
		t.Fatalf("warehouseRowsSHA256: %v", err)
	}
	target = filepath.Join(t.TempDir(), "warehouse.duckdb")
	if _, err := auditpush.PushBundle(target, bundle); err != nil {
		t.Fatalf("PushBundle: %v", err)
	}
	return target, got
}

func runVerify(t *testing.T, args []string) (code int, out, errb string) {
	t.Helper()
	var o, e bytes.Buffer
	code = runVerifyAttest(args, &o, &e)
	return code, o.String(), e.String()
}

// TestVerifyAttestGoodEnvelopePasses is the whole-pipeline good case: a
// freshly signed, untampered envelope verifies, exit 0, and the signer's
// keyid is named.
func TestVerifyAttestGoodEnvelopePasses(t *testing.T) {
	stmtPath := signedFixtureStatement(t, "o/r", 7, "")
	code, out, errb := runVerify(t, []string{"--attest", stmtPath})
	if code != 0 {
		t.Fatalf("exit %d, want 0; stdout=%s stderr=%s", code, out, errb)
	}
	if !strings.Contains(out, "✓ signature: signed by corral-certify, verified against the local certify key") {
		t.Errorf("stdout = %q, want a passing signature line naming the signer", out)
	}
}

// TestVerifyAttestTamperedEnvelopeFails: flipping a byte in the envelope's
// signature must fail the signature check and exit 1 — the whole point of
// signing it at all.
func TestVerifyAttestTamperedEnvelopeFails(t *testing.T) {
	stmtPath := signedFixtureStatement(t, "o/r", 7, "")
	envPath := dsseEnvelopePathFor(stmtPath)
	raw, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("reading the envelope: %v", err)
	}
	var env struct {
		Payload     string `json:"payload"`
		PayloadType string `json:"payloadType"`
		Signatures  []struct {
			KeyID string `json:"keyid"`
			Sig   string `json:"sig"`
		} `json:"signatures"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	// Corrupt the signature itself — a tamper that must be CAUGHT, not one
	// that merely changes the payload (which would also break parsing).
	env.Signatures[0].Sig = "dGFtcGVyZWQ=" // "tampered", base64
	tampered, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("remarshal: %v", err)
	}
	if err := os.WriteFile(envPath, tampered, 0o600); err != nil {
		t.Fatalf("writing the tampered envelope: %v", err)
	}

	code, out, errb := runVerify(t, []string{"--attest", stmtPath})
	if code != 1 {
		t.Fatalf("exit %d, want 1; stdout=%s stderr=%s", code, out, errb)
	}
	if !strings.Contains(out, "✗ signature:") {
		t.Errorf("stdout = %q, want a failing signature line", out)
	}
}

// TestVerifyAttestAcceptsAnExplicitEnvelopePath: pointing --attest directly
// at the .dsse.json file (rather than the plain statement) must work too —
// resolveEnvelopePath detects it by content, not by filename convention.
func TestVerifyAttestAcceptsAnExplicitEnvelopePath(t *testing.T) {
	stmtPath := signedFixtureStatement(t, "o/r", 7, "")
	code, out, errb := runVerify(t, []string{"--attest", dsseEnvelopePathFor(stmtPath)})
	if code != 0 {
		t.Fatalf("exit %d, want 0; stdout=%s stderr=%s", code, out, errb)
	}
	if !strings.Contains(out, "✓ signature:") {
		t.Errorf("stdout = %q, want the signature check to pass", out)
	}
}

// TestVerifyAttestNoDBOrIndexIsNotCheckedNeverFailed pins the "missing
// input = not checked, never a failure" contract for checks 2 and 3: with
// neither --db nor --rekor-index, both print · not-checked lines, exit
// stays 0 (the signature alone passes), and no ✗ appears anywhere.
func TestVerifyAttestNoDBOrIndexIsNotCheckedNeverFailed(t *testing.T) {
	stmtPath := signedFixtureStatement(t, "o/r", 7, "")
	code, out, errb := runVerify(t, []string{"--attest", stmtPath})
	if code != 0 {
		t.Fatalf("exit %d, want 0; stdout=%s stderr=%s", code, out, errb)
	}
	if !strings.Contains(out, "· warehouse rows: not checked (no --db given)") {
		t.Errorf("stdout = %q, want the warehouse-rows not-checked line", out)
	}
	if !strings.Contains(out, "· rekor inclusion: not checked (no --rekor-index given and no --db to read one from)") {
		t.Errorf("stdout = %q, want the rekor-inclusion not-checked line", out)
	}
	if strings.Contains(out, "✗") {
		t.Errorf("a ✗ appeared with nothing actually checked to fail:\n%s", out)
	}
}

// TestVerifyAttestRowsHashMatch: a statement whose warehouseRowsSha256
// claim matches what --db actually holds passes check 2.
func TestVerifyAttestRowsHashMatch(t *testing.T) {
	target, hash := pushedRowsHash(t, "o/r", 7)
	stmtPath := signedFixtureStatement(t, "o/r", 7, hash)

	code, out, errb := runVerify(t, []string{"--attest", stmtPath, "--db", target})
	if code != 0 {
		t.Fatalf("exit %d, want 0; stdout=%s stderr=%s", code, out, errb)
	}
	if !strings.Contains(out, "✓ warehouse rows: the rows read back from --db (push ") || !strings.Contains(out, ") hash to exactly the value the statement claims") {
		t.Errorf("stdout = %q, want a passing warehouse-rows line", out)
	}
}

// TestVerifyAttestRowsHashMismatch: a warehouse row altered after the push
// (a raw UPDATE — the direct SQL access this table's own append-only
// convention normally forbids at the application layer, standing in for
// tampering) must be CAUGHT, not silently accepted.
func TestVerifyAttestRowsHashMismatch(t *testing.T) {
	target, hash := pushedRowsHash(t, "o/r", 7)
	stmtPath := signedFixtureStatement(t, "o/r", 7, hash)

	db, err := sql.Open("duckdb", target)
	if err != nil {
		t.Fatalf("open %s: %v", target, err)
	}
	if _, err := db.Exec(`UPDATE corral_audits SET kill_rate = 0.99 WHERE path = 'pkg/a.go'`); err != nil {
		t.Fatalf("tamper the warehouse row: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	code, out, errb := runVerify(t, []string{"--attest", stmtPath, "--db", target})
	if code != 1 {
		t.Fatalf("exit %d, want 1; stdout=%s stderr=%s", code, out, errb)
	}
	if !strings.Contains(out, "✗ warehouse rows: the rows read back from --db hash to a different value than the statement claims (1 push(es) of this scan tried: ") ||
		!strings.Contains(out, "a VACUUMed warehouse can change row order and trip this without tampering") {
		t.Errorf("stdout = %q, want a failing warehouse-rows line naming the pushes tried and carrying the VACUUM caveat", out)
	}
}

// TestVerifyAttestFindsItsOwnPushAmongUnrecordedRuns: an Action run has no
// --record, so it pushes under scan_id 0 — the SAME key as every other
// Action run of the repo. Reading by (repo, scan_id) unioned all of them
// into one bundle no statement ever hashed, so the second run's statement
// (and the first's, once the second had pushed) failed check 2 against an
// untouched warehouse. Each push has its own scan_uid and stamps the
// statement's hash on its rows; verify locates the push by that hash.
func TestVerifyAttestFindsItsOwnPushAmongUnrecordedRuns(t *testing.T) {
	target := filepath.Join(t.TempDir(), "warehouse.duckdb")
	var stmts []string
	for i, commit := range []string{"c0ffee01", "c0ffee02"} {
		bundle := auditpush.Bundle{
			Scan: auditpush.ScanRow{Repo: "o/r", ScanID: 0, Commit: commit, Audited: 1, Candidates: 1, PushedBy: auditpush.PushedByCertify,
				RunURL: fmt.Sprintf("https://github.com/o/r/actions/runs/%d", 100+i)},
			Files: []auditpush.Row{
				{Repo: "o/r", ScanID: 0, Path: "pkg/a.go", Commit: commit,
					KillRate: ptrF(0.5 + float64(i)/10), Survivors: 2 + i, Disposition: "audited", Evidence: "proven"},
			},
		}
		hash, err := warehouseRowsSHA256(bundle)
		if err != nil {
			t.Fatalf("warehouseRowsSHA256: %v", err)
		}
		stmtPath := signedFixtureStatement(t, "o/r", 0, hash)
		raw, _ := os.ReadFile(stmtPath)
		sum := sha256.Sum256(raw)
		// Exactly what certify --repo --attest --push does: the push carries
		// the plain statement's hash on every row (Link.Require, since a
		// statement was written).
		bundle.Link = auditpush.Link{StatementSHA256: hex.EncodeToString(sum[:]), Require: true}
		if _, err := auditpush.PushBundle(target, bundle); err != nil {
			t.Fatalf("PushBundle %d: %v", i, err)
		}
		stmts = append(stmts, stmtPath)
	}
	for i, stmtPath := range stmts {
		code, out, errb := runVerify(t, []string{"--attest", stmtPath, "--db", target})
		if code != 0 || !strings.Contains(out, "✓ warehouse rows: the rows read back from --db (push ") {
			t.Errorf("statement %d: exit %d; stdout=%s stderr=%s — want its own push found among the scan_id-0 runs", i, code, out, errb)
		}
	}
}

// TestVerifyAttestSaysBackfillWhenOnlyAScansPushIsThere: `corral scans
// push` reconstructs a scan from the local ledger — passed NULL, no source,
// no thresholds — and stamps the ORIGINAL statement's hash on the rows, so
// the statement's warehouseRowsSha256 can never match them. verify used to
// report that as ✗ with advice to "re-verify against a warehouse pushed
// once". It is not a mismatch; it is a different push, and the row says so.
func TestVerifyAttestSaysBackfillWhenOnlyAScansPushIsThere(t *testing.T) {
	run := auditpush.Bundle{
		Scan: auditpush.ScanRow{Repo: "o/r", ScanID: 7, Commit: "deadbeef", Audited: 1, Candidates: 1, Passed: boolPtr(true), PushedBy: auditpush.PushedByCertify},
		Files: []auditpush.Row{
			{Repo: "o/r", ScanID: 7, Path: "pkg/a.go", Commit: "deadbeef",
				KillRate: ptrF(0.5), Survivors: 2, Disposition: "audited", Evidence: "proven", Passed: boolPtr(true)},
		},
	}
	hash, err := warehouseRowsSHA256(run)
	if err != nil {
		t.Fatalf("warehouseRowsSHA256: %v", err)
	}
	stmtPath := signedFixtureStatement(t, "o/r", 7, hash)
	raw, _ := os.ReadFile(stmtPath)
	sum := sha256.Sum256(raw)

	backfill := run
	backfill.Scan.Passed = nil
	backfill.Scan.PushedBy = auditpush.PushedByBackfill
	backfill.Files = []auditpush.Row{run.Files[0]}
	backfill.Files[0].Passed = nil
	backfill.Link = auditpush.Link{ScanID: 7, StatementSHA256: hex.EncodeToString(sum[:])}
	target := filepath.Join(t.TempDir(), "warehouse.duckdb")
	if _, err := auditpush.PushBundle(target, backfill); err != nil {
		t.Fatalf("PushBundle: %v", err)
	}

	code, out, errb := runVerify(t, []string{"--attest", stmtPath, "--db", target})
	if code == 1 || strings.Contains(out, "✗ warehouse rows") {
		t.Fatalf("exit %d; stdout=%s stderr=%s — a backfill is not a mismatch", code, out, errb)
	}
	if !strings.Contains(out, "· warehouse rows: not checked (the only rows in --db for this statement were written by `corral scans push` (1 backfill(s)), not by the run that signed it") {
		t.Errorf("stdout = %q, want the not-checked line to name the backfill", out)
	}

	// And when the run's own push is ALSO there, it is the one checked.
	run.Link = backfill.Link
	if _, err := auditpush.PushBundle(target, run); err != nil {
		t.Fatalf("PushBundle run: %v", err)
	}
	code, out, errb = runVerify(t, []string{"--attest", stmtPath, "--db", target})
	if code != 0 || !strings.Contains(out, "✓ warehouse rows:") {
		t.Errorf("exit %d; stdout=%s stderr=%s — want the run's own push found beside the backfill", code, out, errb)
	}
}

// TestVerifyAttestRowsHashNotCheckedWhenStatementCarriesNone: a statement
// signed without a --push alongside it (WarehouseRowsSHA256 == "") has
// nothing for --db to check against — not-checked, not a mismatch.
func TestVerifyAttestRowsHashNotCheckedWhenStatementCarriesNone(t *testing.T) {
	target, _ := pushedRowsHash(t, "o/r", 7)
	stmtPath := signedFixtureStatement(t, "o/r", 7, "")

	code, out, errb := runVerify(t, []string{"--attest", stmtPath, "--db", target})
	if code != 0 {
		t.Fatalf("exit %d, want 0; stdout=%s stderr=%s", code, out, errb)
	}
	if !strings.Contains(out, "· warehouse rows: not checked (the statement carries no warehouseRowsSha256") {
		t.Errorf("stdout = %q, want the no-claim not-checked line", out)
	}
}

// TestVerifyAttestRekorMatch: a FakeLogger.Get answering with THIS
// envelope's own sha256 passes check 3 — no network, ever.
func TestVerifyAttestRekorMatch(t *testing.T) {
	stmtPath := signedFixtureStatement(t, "o/r", 7, "")
	envPath := dsseEnvelopePathFor(stmtPath)
	envBytes, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("reading the envelope: %v", err)
	}
	sum := sha256.Sum256(envBytes)
	fake := &transparency.FakeLogger{GetEntry: transparency.LogEntry{
		LogIndex: 55, UUID: "uuid-xyz", IntegratedTime: 42, EnvelopeSHA256: hex.EncodeToString(sum[:]),
	}}
	orig := newTransparencyLogger
	t.Cleanup(func() { newTransparencyLogger = orig })
	newTransparencyLogger = func(string) transparency.Logger { return fake }

	code, out, errb := runVerify(t, []string{"--attest", stmtPath, "--rekor-index", "55"})
	if code != 0 {
		t.Fatalf("exit %d, want 0; stdout=%s stderr=%s", code, out, errb)
	}
	if !strings.Contains(out, "✓ rekor inclusion: rekor index 55 (uuid uuid-xyz) matches this envelope exactly") {
		t.Errorf("stdout = %q, want a passing rekor-inclusion line", out)
	}
	if len(fake.GetCalls) != 1 || fake.GetCalls[0] != 55 {
		t.Errorf("FakeLogger.Get was called with %v, want [55]", fake.GetCalls)
	}
}

// TestVerifyAttestRekorMismatch: a fetched entry whose logged hash does NOT
// match this envelope must fail — the entry exists, but it is not proof of
// THIS file.
func TestVerifyAttestRekorMismatch(t *testing.T) {
	stmtPath := signedFixtureStatement(t, "o/r", 7, "")
	fake := &transparency.FakeLogger{GetEntry: transparency.LogEntry{
		LogIndex: 55, UUID: "uuid-xyz", IntegratedTime: 42, EnvelopeSHA256: "0000000000000000000000000000000000000000000000000000000000000000",
	}}
	orig := newTransparencyLogger
	t.Cleanup(func() { newTransparencyLogger = orig })
	newTransparencyLogger = func(string) transparency.Logger { return fake }

	code, out, errb := runVerify(t, []string{"--attest", stmtPath, "--rekor-index", "55"})
	if code != 1 {
		t.Fatalf("exit %d, want 1; stdout=%s stderr=%s", code, out, errb)
	}
	if !strings.Contains(out, "✗ rekor inclusion: rekor index 55 (uuid uuid-xyz) does not match this envelope's hash") {
		t.Errorf("stdout = %q, want a failing rekor-inclusion line", out)
	}
}

// TestVerifyAttestRekorIndexReadFromDB: without --rekor-index, the index
// --db recorded for this scan (auditpush's rekor_log_index column, per
// T1) is used automatically — one fewer thing an operator has to copy by
// hand off the --transparency print line.
//
// The bundle here is built with the receipt ALREADY set before the hash is
// computed, for simplicity — this test is about the index-lookup wiring,
// not about hash correctness across the receipt-stamped-after-hashing
// order. It works whether or not warehouseRowsSHA256 blanks the receipt
// fields, so it does NOT by itself prove anything about that order — see
// TestWarehouseRowsSHA256IgnoresTheReceiptStampedAfterHashing for the test
// that actually pins the real production order (build → hash with the
// receipt nil → stamp → push with the receipt set → verify still passes).
func TestVerifyAttestRekorIndexReadFromDB(t *testing.T) {
	idx := int64(99)
	bundle := auditpush.Bundle{
		Scan: auditpush.ScanRow{Repo: "o/r", ScanID: 7, Commit: "deadbeef", Audited: 1, Candidates: 1,
			RekorLogIndex: &idx, RekorUUID: "uuid-from-db"},
		Files: []auditpush.Row{
			{Repo: "o/r", ScanID: 7, Path: "pkg/a.go", Commit: "deadbeef",
				KillRate: ptrF(0.5), Survivors: 2, Disposition: "audited", Evidence: "proven"},
		},
	}
	hash, err := warehouseRowsSHA256(bundle)
	if err != nil {
		t.Fatalf("warehouseRowsSHA256: %v", err)
	}
	target := filepath.Join(t.TempDir(), "warehouse.duckdb")
	if _, err := auditpush.PushBundle(target, bundle); err != nil {
		t.Fatalf("PushBundle: %v", err)
	}
	stmtPath := signedFixtureStatement(t, "o/r", 7, hash)

	envPath := dsseEnvelopePathFor(stmtPath)
	envBytes, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("reading the envelope: %v", err)
	}
	sum := sha256.Sum256(envBytes)
	fake := &transparency.FakeLogger{GetEntry: transparency.LogEntry{
		LogIndex: 99, UUID: "uuid-from-db", IntegratedTime: 1, EnvelopeSHA256: hex.EncodeToString(sum[:]),
	}}
	orig := newTransparencyLogger
	t.Cleanup(func() { newTransparencyLogger = orig })
	newTransparencyLogger = func(string) transparency.Logger { return fake }

	code, out, errb := runVerify(t, []string{"--attest", stmtPath, "--db", target})
	if code != 0 {
		t.Fatalf("exit %d, want 0; stdout=%s stderr=%s", code, out, errb)
	}
	if len(fake.GetCalls) != 1 || fake.GetCalls[0] != 99 {
		t.Fatalf("Get was called with %v, want [99] (the index --db carried)", fake.GetCalls)
	}
	if !strings.Contains(out, "✓ rekor inclusion:") {
		t.Errorf("stdout = %q, want a passing rekor-inclusion line", out)
	}
}

// TestVerifyAttestMissingFlagsExit2 pins the usage-error contract: no
// --attest at all is a hard exit-2 refusal, before any check runs.
func TestVerifyAttestMissingFlagsExit2(t *testing.T) {
	code, _, errb := runVerify(t, nil)
	if code != 2 {
		t.Fatalf("exit %d, want 2; stderr=%s", code, errb)
	}
	if !strings.Contains(errb, "--attest is required") {
		t.Errorf("stderr = %q, want it to name --attest", errb)
	}
}

// TestVerifyAttestNoEnvelopeExit2: --attest naming a real, non-DSSE JSON
// file with no signed sibling is a usage error (nothing to check), not a
// silent ✗.
func TestVerifyAttestNoEnvelopeExit2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "statement.json")
	if err := os.WriteFile(path, []byte(`{"not":"an envelope"}`), 0o600); err != nil {
		t.Fatalf("seed a plain, unsigned statement: %v", err)
	}
	code, _, errb := runVerify(t, []string{"--attest", path})
	if code != 2 {
		t.Fatalf("exit %d, want 2; stderr=%s", code, errb)
	}
	if !strings.Contains(errb, "no signed DSSE envelope found") {
		t.Errorf("stderr = %q, want it to say no envelope was found", errb)
	}
}

// TestVerifyDBFlagHelpNamesTheOrderingCaveat pins the coordinator's second
// requirement: an operator staring only at --help — never having seen the
// mismatch line itself — must already know a ✗ here is not automatically
// tampering.
func TestVerifyDBFlagHelpNamesTheOrderingCaveat(t *testing.T) {
	var out, errb bytes.Buffer
	runVerifyAttest([]string{"--help"}, &out, &errb)
	help := out.String() + errb.String()
	if !strings.Contains(help, "--db") {
		t.Fatalf("--help output does not mention --db at all:\n%s", help)
	}
	if !strings.Contains(help, "VACUUM") && !strings.Contains(help, "vacuum") {
		t.Errorf("--db's help text does not name the VACUUM/re-push false-alarm caveat:\n%s", help)
	}
}

// TestWarehouseRowsSHA256IgnoresTheReceiptStampedAfterHashing pins F1: the
// PRODUCTION order in runCertifyRepo is build bundle → compute
// warehouseRowsSHA256 (the receipt is nil — --transparency has not
// uploaded anything yet) → sign the statement with that hash → upload →
// STAMP the receipt onto the bundle → push the bundle, now carrying the
// receipt, to the warehouse. A verifier that reads the PUSHED rows back
// (which DO carry the receipt) and recomputes the hash must get the SAME
// value the statement signed (computed when the receipt was still nil) —
// otherwise every real --transparency run's own statement fails its own
// --db check, which is exactly the bug this test catches.
func TestWarehouseRowsSHA256IgnoresTheReceiptStampedAfterHashing(t *testing.T) {
	bundleNoReceipt := auditpush.Bundle{
		Scan: auditpush.ScanRow{Repo: "o/r", ScanID: 7, Commit: "deadbeef", Audited: 1, Candidates: 1},
		Files: []auditpush.Row{
			{Repo: "o/r", ScanID: 7, Path: "pkg/a.go", Commit: "deadbeef",
				KillRate: ptrF(0.5), Survivors: 2, Disposition: "audited", Evidence: "proven"},
		},
	}
	// The hash the statement actually signs — computed BEFORE the receipt
	// exists, exactly like writeAuditStatement calling warehouseRowsSHA256
	// before --transparency has uploaded anything.
	signedHash, err := warehouseRowsSHA256(bundleNoReceipt)
	if err != nil {
		t.Fatalf("warehouseRowsSHA256 (no receipt): %v", err)
	}

	// The receipt is stamped AFTER — onto a copy, the way runCertifyRepo
	// stamps bundle.Scan.RekorLogIndex/RekorUUID after a successful upload —
	// and THIS is the bundle that actually gets pushed.
	idx := int64(2666822278)
	bundleWithReceipt := bundleNoReceipt
	bundleWithReceipt.Scan.RekorLogIndex = &idx
	bundleWithReceipt.Scan.RekorUUID = "uuid-from-a-real-upload"

	target := filepath.Join(t.TempDir(), "warehouse.duckdb")
	if _, err := auditpush.PushBundle(target, bundleWithReceipt); err != nil {
		t.Fatalf("PushBundle: %v", err)
	}

	db, err := sql.Open("duckdb", target)
	if err != nil {
		t.Fatalf("open %s: %v", target, err)
	}
	defer db.Close()
	readBack, err := auditpush.ReadBundle(db, "o/r", 7)
	if err != nil {
		t.Fatalf("ReadBundle: %v", err)
	}
	if readBack.Scan.RekorLogIndex == nil || *readBack.Scan.RekorLogIndex != idx {
		t.Fatalf("read-back bundle lost the receipt — fixture is not exercising the real case")
	}

	gotHash, err := warehouseRowsSHA256(readBack)
	if err != nil {
		t.Fatalf("warehouseRowsSHA256 (read back, with receipt): %v", err)
	}
	if gotHash != signedHash {
		t.Errorf("recomputed hash %q != the hash the statement signed %q — the receipt, stamped AFTER hashing, is leaking into the hash", gotHash, signedHash)
	}
}

// TestVerifyAttestRowsHashMatchAcrossRealProductionOrder is the same fix,
// exercised through corral verify itself end to end: a statement signed
// with warehouseRowsSha256 computed BEFORE the receipt existed must still
// pass check 2 against a warehouse whose pushed row carries the receipt —
// the shape every real --attest --push --transparency run actually
// produces.
func TestVerifyAttestRowsHashMatchAcrossRealProductionOrder(t *testing.T) {
	bundleNoReceipt := auditpush.Bundle{
		Scan: auditpush.ScanRow{Repo: "o/r", ScanID: 7, Commit: "deadbeef", Audited: 1, Candidates: 1},
		Files: []auditpush.Row{
			{Repo: "o/r", ScanID: 7, Path: "pkg/a.go", Commit: "deadbeef",
				KillRate: ptrF(0.5), Survivors: 2, Disposition: "audited", Evidence: "proven"},
		},
	}
	signedHash, err := warehouseRowsSHA256(bundleNoReceipt)
	if err != nil {
		t.Fatalf("warehouseRowsSHA256: %v", err)
	}
	stmtPath := signedFixtureStatement(t, "o/r", 7, signedHash)

	idx := int64(2666822278)
	bundleWithReceipt := bundleNoReceipt
	bundleWithReceipt.Scan.RekorLogIndex = &idx
	bundleWithReceipt.Scan.RekorUUID = "uuid-from-a-real-upload"
	target := filepath.Join(t.TempDir(), "warehouse.duckdb")
	if _, err := auditpush.PushBundle(target, bundleWithReceipt); err != nil {
		t.Fatalf("PushBundle: %v", err)
	}

	// The pushed bundle carries a real-looking RekorLogIndex (needed to
	// exercise the same read-back path a real --transparency run leaves
	// behind), which means --db alone would make check 3 look one up and
	// fetch it — this test is about check 2 only, so a FakeLogger keeps it
	// off the network regardless of what check 3 does with it.
	orig := newTransparencyLogger
	t.Cleanup(func() { newTransparencyLogger = orig })
	newTransparencyLogger = func(string) transparency.Logger { return &transparency.FakeLogger{} }

	// check 3 (rekor inclusion) is irrelevant here — the bundle's
	// RekorLogIndex makes --db's auto-lookup fire, and the FakeLogger's
	// zero-value entry will not match, which is fine: this test asserts
	// only check 2's own line, not the overall exit code.
	_, out, errb := runVerify(t, []string{"--attest", stmtPath, "--db", target})
	if !strings.Contains(out, "✓ warehouse rows:") {
		t.Errorf("stdout = %q (stderr=%s), want the warehouse-rows check to pass despite the receipt being stamped after hashing", out, errb)
	}
	if strings.Contains(out, "✗ warehouse rows:") {
		t.Errorf("stdout = %q, warehouse-rows check failed — the receipt stamped after hashing leaked into the hash", out)
	}
}

// TestVerifyAttestRekorFetchErrorIsNotCheckedNotFailed pins F2: a fetch
// failure (network down, the log unreachable, the entry not found) is a
// MISSING input — "not checked" — never a ✗. Only a FETCHED entry whose
// logged hash disagrees (TestVerifyAttestRekorMismatch, above) is a real
// mismatch. The exit code must not move for a fetch error alone.
func TestVerifyAttestRekorFetchErrorIsNotCheckedNotFailed(t *testing.T) {
	stmtPath := signedFixtureStatement(t, "o/r", 7, "")
	fake := &transparency.FakeLogger{GetErr: errors.New("rekor: connection refused")}
	orig := newTransparencyLogger
	t.Cleanup(func() { newTransparencyLogger = orig })
	newTransparencyLogger = func(string) transparency.Logger { return fake }

	code, out, errb := runVerify(t, []string{"--attest", stmtPath, "--rekor-index", "55"})
	if code != 0 {
		t.Fatalf("exit %d, want 0 (a fetch error must not fail the run); stdout=%s stderr=%s", code, out, errb)
	}
	if !strings.Contains(out, "· rekor inclusion: not checked (fetching the log entry failed: rekor: connection refused)") {
		t.Errorf("stdout = %q, want a not-checked line naming the fetch failure", out)
	}
	if strings.Contains(out, "✗ rekor inclusion:") {
		t.Errorf("stdout = %q, a fetch error must never print as a ✗", out)
	}
}

// TestVerifyAttestPubFlagIsTheKeyItVerifiesAgainst covers --pub, which had no
// test at all and sits on the path a THIRD PARTY runs to check a corral
// record. Everything else in a signed statement is a claim; this flag decides
// which key that claim is checked against, so it is the one surface where
// "parses and is then ignored" would be actively dangerous — a verifier would
// believe they had checked against the signer's published key while actually
// checking against whatever local key they happen to hold.
//
// Three branches, and the middle one is the security-relevant one:
//
//  1. the RIGHT key verifies;
//  2. a well-formed but WRONG key must FAIL, not silently fall back to the
//     local key and pass;
//  3. a malformed key must be REFUSED as unchecked, never quietly ignored.
func TestVerifyAttestPubFlagIsTheKeyItVerifiesAgainst(t *testing.T) {
	priv := fixtureCertifyKey(t)
	stmtPath := signedFixtureStatement(t, "o/r", 7, "")
	envelope, err := os.ReadFile(dsseEnvelopePathFor(stmtPath))
	if err != nil {
		t.Fatalf("reading the envelope: %v", err)
	}
	right := hex.EncodeToString(priv.Public().(ed25519.PublicKey))

	// A different key, generated the same deterministic way with a different
	// seed, so this is a genuine other signer rather than noise.
	otherSeed := make([]byte, ed25519.SeedSize)
	for i := range otherSeed {
		otherSeed[i] = byte(200 - i)
	}
	wrong := hex.EncodeToString(ed25519.NewKeyFromSeed(otherSeed).Public().(ed25519.PublicKey))

	t.Run("the right key verifies", func(t *testing.T) {
		got := verifySignature(envelope, "corral-certify", right)
		if !got.checked || !got.ok {
			t.Errorf("checked=%v ok=%v detail=%q — a correct --pub must verify", got.checked, got.ok, got.detail)
		}
	})

	t.Run("a wrong key must FAIL, not fall back", func(t *testing.T) {
		got := verifySignature(envelope, "corral-certify", wrong)
		if !got.checked {
			t.Fatalf("checked=false detail=%q — --pub was given, so the check must happen", got.detail)
		}
		if got.ok {
			t.Errorf("a signature verified against a key that did NOT sign it — either --pub is being ignored in favour of the local key, or the comparison is inverted. detail=%q", got.detail)
		}
	})

	t.Run("a malformed key is refused, not ignored", func(t *testing.T) {
		got := verifySignature(envelope, "corral-certify", "not-hex")
		if got.checked || got.ok {
			t.Errorf("checked=%v ok=%v — a malformed --pub must be reported as UNCHECKED. Silently falling through would let a verifier believe they checked against a published key when they checked against their own", got.checked, got.ok)
		}
	})
}

// AN ENVELOPE NOBODY COULD VERIFY MUST NOT EXIT 0. `failed` was set only by a
// check that ran and failed, so with no key, no --db and no Rekor index every
// check printed "not checked" and the command exited 0 — and a CI step keyed on
// that exit code accepted a forged statement. This is the same "absence of
// evidence read as evidence" defect this repository has found in a receipt
// check, a report parser and a release gate in one day.
func TestVerifyAttestRefusesWhenNothingWasChecked(t *testing.T) {
	// Sign with the fixture key, then make it unreachable: no --pub, and both
	// env variables cleared so no local key can be found. The envelope is
	// real and its signature is valid — the verifier simply has nothing to
	// check it against, which is the "not checked" state, not a pass.
	stmtPath := signedFixtureStatement(t, "o/r", 7, "")
	if _, err := os.Stat(stmtPath + ".dsse.json"); err != nil {
		t.Fatalf("fixture envelope not written: %v", err)
	}
	t.Setenv("CORRALAI_CERTIFY_KEY", "")
	t.Setenv("CORRALAI_CERTIFY_KEY_FILE", "")
	// And no key at the default path either: it lives under the home
	// directory, so point HOME at an empty one. The first version of this
	// test found a real key on the developer's workstation, the signature
	// check RAN and failed, and the exit was a correct 1 — which is not the
	// state this test is about.
	t.Setenv("HOME", t.TempDir())

	var stdout, stderr bytes.Buffer
	code := runVerifyAttest([]string{"--attest", stmtPath}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("exit 0 with nothing verified:\n%s", stdout.String())
	}
	if code != 2 {
		t.Errorf("exit = %d, want 2 (refusal), stdout:\n%s", code, stdout.String())
	}
	if !strings.Contains(stderr.String(), "NOTHING was verified") {
		t.Errorf("stderr must say nothing was checked, got: %q", stderr.String())
	}
}
