// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/certify"
	"github.com/pdbethke/corralai/internal/transparency"
)

// fakeWitnessFactory is a witnessFactory that always returns a fresh
// transparency.NewFakeWitness(), so verify tests never touch the network
// (fakeWitness.VerifyInclusion is a pure function of its inputs, so any
// instance — not necessarily the one that anchored the entry — works).
func fakeWitnessFactory(string) (transparency.Witness, error) {
	return transparency.NewFakeWitness(), nil
}

func failWitnessFactory(t *testing.T) witnessFactory {
	return func(string) (transparency.Witness, error) {
		t.Fatal("witness factory should not be called for an unanchored record")
		return nil, nil
	}
}

// buildAnchoredTestRecord builds on buildTestRecord by additionally anchoring
// the record's DSSE envelope to a fakeWitness and embedding the resulting
// transparency.Entry + anchored=true, mirroring what report_build/--out
// produce for an anchored build.
func buildAnchoredTestRecord(t *testing.T) (pubHex string, raw []byte) {
	t.Helper()
	pubHex, raw = buildTestRecord(t)
	var rec map[string]any
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	w := transparency.NewFakeWitness()
	entry, err := w.Anchor(context.Background(), []byte(rec["signature"].(string)))
	if err != nil {
		t.Fatal(err)
	}
	entryJSON, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	rec["rekor"] = string(entryJSON)
	rec["anchored"] = true
	out, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	return pubHex, out
}

// buildTestRecord builds an in-process, fully valid certRecord — mirroring
// what report_build/--out produce — signed under a fresh keypair, and
// returns its pubkey hex alongside the raw JSON bytes.
func buildTestRecord(t *testing.T) (pubHex string, raw []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	steps := []certify.Step{
		{Kind: "context", Actor: "ci", Subject: "repo@abc123", Detail: map[string]any{"repo": "r"}},
		{Kind: "execution", Actor: "ci", Subject: "go test ./...", Detail: map[string]any{"exit_code": 0.0, "ok": true}},
	}
	built, head := certify.BuildLedger(steps)
	stmt := certify.BuildAttestation(certify.BuildRecord{
		Repo: "pdbethke/corralai", Commit: "abc123", Command: "go test ./...", ExitCode: 0,
	}, head)
	envelope, err := certify.SignDSSE(stmt, priv, "brain")
	if err != nil {
		t.Fatal(err)
	}
	// Decode the canonical statement back to a generic map for the record's
	// human-readable "statement" field — mirroring what the MCP round trip /
	// a JSON file read produces. Verification itself never uses this field;
	// it checks the envelope's own embedded statement (see verify.go).
	canonical, err := certify.CanonicalStatement(stmt)
	if err != nil {
		t.Fatal(err)
	}
	var stmtDecoded map[string]any
	if err := json.Unmarshal(canonical, &stmtDecoded); err != nil {
		t.Fatal(err)
	}

	stepsJSON, err := certify.MarshalSteps(built)
	if err != nil {
		t.Fatal(err)
	}
	var stepsDecoded []map[string]any
	if err := json.Unmarshal(stepsJSON, &stepsDecoded); err != nil {
		t.Fatal(err)
	}

	rec := certRecord{
		Statement: stmtDecoded,
		Signature: string(envelope),
		Steps:     stepsDecoded,
		Head:      head,
		PublicKey: hex.EncodeToString(pub),
	}
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(pub), b
}

func writeRecord(t *testing.T, raw []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "record.json")
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func failFetcher(t *testing.T) pubkeyFetcher {
	return func(brainURL string) (string, error) {
		t.Fatal("fetch should not be called when --pubkey is given")
		return "", nil
	}
}

func TestRunCertifyVerify_ValidRecordPasses(t *testing.T) {
	pubHex, raw := buildTestRecord(t)
	path := writeRecord(t, raw)
	var stdout, stderr bytes.Buffer

	code := runCertifyVerify([]string{"--pubkey", pubHex, "--allow-unanchored", path}, failFetcher(t), failWitnessFactory(t), &stdout, &stderr)

	if code != 0 {
		t.Fatalf("expected exit 0, got %d (stderr=%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "verified") {
		t.Errorf("expected \"verified\" on stdout, got %q", stdout.String())
	}
}

// TestRunCertifyVerify_UsesRecordEmbeddedPublicKeyWhenNoFlagsGiven guards
// against the circular-trust-anchor bug: a record's own embedded
// public_key must NEVER be usable to verify that same record — an attacker
// who forges a record just signs it with their own key and writes that key
// into public_key, and every check would pass. With neither --pubkey nor
// --brain given, verify must refuse (non-zero exit, no "verified" on
// stdout) rather than fall back to the record's self-reported key.
func TestRunCertifyVerify_UsesRecordEmbeddedPublicKeyWhenNoFlagsGiven(t *testing.T) {
	_, raw := buildTestRecord(t)
	path := writeRecord(t, raw)
	var stdout, stderr bytes.Buffer

	code := runCertifyVerify([]string{path}, failFetcher(t), failWitnessFactory(t), &stdout, &stderr)

	if code == 0 {
		t.Fatalf("expected a non-zero exit when no --pubkey/--brain is given (the record's embedded public_key must not be a trust anchor), got 0")
	}
	if strings.Contains(stdout.String(), "verified") {
		t.Errorf("expected no \"verified\" on stdout when falling back to the record's own key is refused, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "--pubkey") || !strings.Contains(stderr.String(), "--brain") {
		t.Errorf("expected stderr to point at --pubkey/--brain, got %q", stderr.String())
	}
}

// TestRunCertifyVerify_TamperedPredicateFailsAtSignature proves a forger
// can't get a pass by editing the record's cosmetic "statement" field alone
// (verification never reads it) NOR by editing the predicate embedded
// inside the DSSE envelope's own payload without re-signing — either way,
// the signature check must catch the mutation.
func TestRunCertifyVerify_TamperedPredicateFailsAtSignature(t *testing.T) {
	pubHex, raw := buildTestRecord(t)
	var rec map[string]any
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}

	// Mutating the record's cosmetic "statement" field must have no effect
	// on verification — it's not the source of truth.
	stmt := rec["statement"].(map[string]any)
	predicate := stmt["predicate"].(map[string]any)
	buildDef := predicate["buildDefinition"].(map[string]any)
	externalParams := buildDef["externalParameters"].(map[string]any)
	externalParams["command"] = "rm -rf /"

	// The real tamper: mutate the predicate INSIDE the DSSE envelope's own
	// payload, leaving the signature bytes alone — this is what an actual
	// forger of the signed artifact would have to do.
	var env map[string]any
	if err := json.Unmarshal([]byte(rec["signature"].(string)), &env); err != nil {
		t.Fatal(err)
	}
	payload, err := base64.StdEncoding.DecodeString(env["payload"].(string))
	if err != nil {
		t.Fatal(err)
	}
	var envStmt map[string]any
	if err := json.Unmarshal(payload, &envStmt); err != nil {
		t.Fatal(err)
	}
	envPredicate := envStmt["predicate"].(map[string]any)
	envBuildDef := envPredicate["buildDefinition"].(map[string]any)
	envExternalParams := envBuildDef["externalParameters"].(map[string]any)
	envExternalParams["command"] = "rm -rf /"
	tamperedPayload, err := json.Marshal(envStmt)
	if err != nil {
		t.Fatal(err)
	}
	env["payload"] = base64.StdEncoding.EncodeToString(tamperedPayload)
	tamperedEnvelope, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	rec["signature"] = string(tamperedEnvelope)

	tampered, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	path := writeRecord(t, tampered)
	var stdout, stderr bytes.Buffer

	code := runCertifyVerify([]string{"--pubkey", pubHex, path}, failFetcher(t), failWitnessFactory(t), &stdout, &stderr)

	if code == 0 {
		t.Fatal("expected verify to FAIL on a tampered predicate")
	}
	if !strings.Contains(stderr.String(), "signature") {
		t.Errorf("expected the signature check to be named as the failure, got %q", stderr.String())
	}
}

func TestRunCertifyVerify_TamperedStepFailsAtLedger(t *testing.T) {
	pubHex, raw := buildTestRecord(t)
	var rec map[string]any
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	steps := rec["steps"].([]any)
	step0 := steps[0].(map[string]any)
	step0["subject"] = "tampered-subject" // mutate a step without recomputing its hash

	tampered, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	path := writeRecord(t, tampered)
	var stdout, stderr bytes.Buffer

	code := runCertifyVerify([]string{"--pubkey", pubHex, path}, failFetcher(t), failWitnessFactory(t), &stdout, &stderr)

	if code == 0 {
		t.Fatal("expected verify to FAIL on a tampered step")
	}
	if !strings.Contains(stderr.String(), "ledger") {
		t.Errorf("expected the ledger check to be named as the failure, got %q", stderr.String())
	}
}

func TestRunCertifyVerify_FetchesPubkeyFromBrain(t *testing.T) {
	pubHex, raw := buildTestRecord(t)
	path := writeRecord(t, raw)
	var stdout, stderr bytes.Buffer

	called := false
	fetch := func(brainURL string) (string, error) {
		called = true
		if brainURL != "https://brain.example" {
			t.Errorf("fetch called with brainURL = %q, want https://brain.example", brainURL)
		}
		return pubHex, nil
	}

	code := runCertifyVerify([]string{"--brain", "https://brain.example", "--allow-unanchored", path}, fetch, failWitnessFactory(t), &stdout, &stderr)

	if code != 0 {
		t.Fatalf("expected exit 0, got %d (stderr=%s)", code, stderr.String())
	}
	if !called {
		t.Error("expected the pubkey fetcher to be called")
	}
}

func TestRunCertifyVerify_MissingRecordFileIsAnError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runCertifyVerify([]string{"--pubkey", "aa", filepath.Join(t.TempDir(), "nope.json")}, failFetcher(t), failWitnessFactory(t), &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected a non-zero exit for a missing record file")
	}
}

// TestCertifyDispatchesVerifySubcommand proves `corral certify verify ...`
// is routed by runCertify itself (the certify dispatch), not a separate
// top-level main.go branch.
func TestCertifyDispatchesVerifySubcommand(t *testing.T) {
	_, raw := buildTestRecord(t)
	path := writeRecord(t, raw)
	pubHex, _ := buildTestRecord(t) // any pubkey works for the plumbing check below (wrong key still exercises dispatch)
	_ = pubHex

	// Recover the real pubkey used for `raw`'s signature.
	var rec map[string]any
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	realPub := rec["public_key"].(string)

	run := &fakeRunner{}
	post := &fakePoster{}
	var stdout, stderr bytes.Buffer

	code := runCertify([]string{"verify", "--pubkey", realPub, "--allow-unanchored", path}, run, post, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("expected exit 0 via certify's verify dispatch, got %d (stderr=%s)", code, stderr.String())
	}
	if post.called {
		t.Error("verify must not go through the normal build-report path")
	}
}

// TestRunCertifyVerify_AnchoredRecordPasses proves a fully-anchored record —
// signature, ledger, and subject all valid, PLUS a Rekor inclusion proof that
// verifies — passes with no --allow-unanchored needed, and names the Rekor
// log index/time on stdout.
func TestRunCertifyVerify_AnchoredRecordPasses(t *testing.T) {
	pubHex, raw := buildAnchoredTestRecord(t)
	path := writeRecord(t, raw)
	var stdout, stderr bytes.Buffer

	code := runCertifyVerify([]string{"--pubkey", pubHex, path}, failFetcher(t), fakeWitnessFactory, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("expected exit 0, got %d (stderr=%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Rekor #") {
		t.Errorf("expected the Rekor log index named on stdout, got %q", stdout.String())
	}
}

// TestRunCertifyVerify_TamperedInclusionProofFailsAtRekorStep proves a
// record whose stored transparency.Entry has been tampered (so
// VerifyInclusion no longer matches) fails at the Rekor step, non-zero exit,
// naming "rekor" on stderr.
func TestRunCertifyVerify_TamperedInclusionProofFailsAtRekorStep(t *testing.T) {
	pubHex, raw := buildAnchoredTestRecord(t)
	var rec map[string]any
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(rec["rekor"].(string)), &entry); err != nil {
		t.Fatal(err)
	}
	// Corrupt the inclusion proof bytes without touching the envelope — the
	// exact tamper a forger of the transparency evidence (not the build
	// itself) would produce.
	entry["InclusionProof"] = base64.StdEncoding.EncodeToString([]byte("not the real proof"))
	entryJSON, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	rec["rekor"] = string(entryJSON)
	tampered, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	path := writeRecord(t, tampered)
	var stdout, stderr bytes.Buffer

	code := runCertifyVerify([]string{"--pubkey", pubHex, path}, failFetcher(t), fakeWitnessFactory, &stdout, &stderr)

	if code == 0 {
		t.Fatal("expected verify to FAIL on a tampered inclusion proof")
	}
	if !strings.Contains(stderr.String(), "rekor") {
		t.Errorf("expected the Rekor step to be named as the failure, got %q", stderr.String())
	}
}

// TestRunCertifyVerify_UnanchoredRecordFailsWithoutAllowFlag proves an
// unwitnessed (anchored=false) record is rejected by default — trustless
// certify's whole point is that "signed" and "publicly witnessed" are
// different claims, and verify must not silently conflate them.
func TestRunCertifyVerify_UnanchoredRecordFailsWithoutAllowFlag(t *testing.T) {
	pubHex, raw := buildTestRecord(t) // anchored=false (zero value)
	path := writeRecord(t, raw)
	var stdout, stderr bytes.Buffer

	code := runCertifyVerify([]string{"--pubkey", pubHex, path}, failFetcher(t), failWitnessFactory(t), &stdout, &stderr)

	if code == 0 {
		t.Fatal("expected verify to FAIL on an unanchored record without --allow-unanchored")
	}
	if !strings.Contains(stderr.String(), "NOT publicly witnessed") {
		t.Errorf("expected stderr to explain the record was signed but not witnessed, got %q", stderr.String())
	}
	if strings.Contains(stdout.String(), "verified") {
		t.Errorf("expected no \"verified\" on stdout without --allow-unanchored, got %q", stdout.String())
	}
}

// TestRunCertifyVerify_UnanchoredRecordPassesWithAllowFlag proves the same
// unanchored record is accepted, with a clear caveat, when the operator
// explicitly opts in via --allow-unanchored.
func TestRunCertifyVerify_UnanchoredRecordPassesWithAllowFlag(t *testing.T) {
	pubHex, raw := buildTestRecord(t)
	path := writeRecord(t, raw)
	var stdout, stderr bytes.Buffer

	code := runCertifyVerify([]string{"--pubkey", pubHex, "--allow-unanchored", path}, failFetcher(t), failWitnessFactory(t), &stdout, &stderr)

	if code != 0 {
		t.Fatalf("expected exit 0 with --allow-unanchored, got %d (stderr=%s)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "NOT publicly witnessed") {
		t.Errorf("expected a clear caveat on stderr even when continuing, got %q", stderr.String())
	}
}
