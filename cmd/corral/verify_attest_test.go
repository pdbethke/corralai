// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
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
		ScanID: scanID, WarehouseRowsSHA256: whRowsSHA,
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
	if !strings.Contains(out, "✓ warehouse rows: the rows read back from --db hash to exactly the value the statement claims") {
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
	if !strings.Contains(out, "✗ warehouse rows: the rows read back from --db hash to a different value than the statement claims — note: a VACUUMed or re-pushed warehouse changes row order and can trip this without tampering; re-verify against a warehouse that has only been pushed to once") {
		t.Errorf("stdout = %q, want a failing warehouse-rows line carrying the false-alarm caveat", out)
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
func TestVerifyAttestRekorIndexReadFromDB(t *testing.T) {
	// Built directly (not via pushedRowsHash) so the RECEIPT is part of the
	// bundle BEFORE the hash is computed — exactly what a real
	// --attest --push --transparency run does (stamp the receipt, then push
	// the same bundle the statement's hash already covers). Retrofitting a
	// receipt onto an already-hashed bundle via UPDATE would (correctly)
	// break check 2's hash match, which is not what this test is about.
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
