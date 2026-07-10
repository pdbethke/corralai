// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/certify"
)

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
	sigHex, canonical, err := certify.SignStatement(stmt, priv)
	if err != nil {
		t.Fatal(err)
	}
	// Decode the canonical bytes back to a generic map — mirroring what the
	// MCP round trip / a JSON file read produces — so re-marshaling via
	// certify.CanonicalStatement in the verifier reproduces the same bytes
	// SignStatement signed (proven byte-stable by internal/brain's own test).
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
		Signature: sigHex,
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

	code := runCertifyVerify([]string{"--pubkey", pubHex, path}, failFetcher(t), &stdout, &stderr)

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

	code := runCertifyVerify([]string{path}, failFetcher(t), &stdout, &stderr)

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

func TestRunCertifyVerify_TamperedPredicateFailsAtSignature(t *testing.T) {
	pubHex, raw := buildTestRecord(t)
	var rec map[string]any
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	stmt := rec["statement"].(map[string]any)
	predicate := stmt["predicate"].(map[string]any)
	buildDef := predicate["buildDefinition"].(map[string]any)
	externalParams := buildDef["externalParameters"].(map[string]any)
	externalParams["command"] = "rm -rf /" // tamper: mutate the predicate, leave the signature alone

	tampered, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	path := writeRecord(t, tampered)
	var stdout, stderr bytes.Buffer

	code := runCertifyVerify([]string{"--pubkey", pubHex, path}, failFetcher(t), &stdout, &stderr)

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

	code := runCertifyVerify([]string{"--pubkey", pubHex, path}, failFetcher(t), &stdout, &stderr)

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

	code := runCertifyVerify([]string{"--brain", "https://brain.example", path}, fetch, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("expected exit 0, got %d (stderr=%s)", code, stderr.String())
	}
	if !called {
		t.Error("expected the pubkey fetcher to be called")
	}
}

func TestRunCertifyVerify_MissingRecordFileIsAnError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runCertifyVerify([]string{"--pubkey", "aa", filepath.Join(t.TempDir(), "nope.json")}, failFetcher(t), &stdout, &stderr)
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

	code := runCertify([]string{"verify", "--pubkey", realPub, path}, run, post, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("expected exit 0 via certify's verify dispatch, got %d (stderr=%s)", code, stderr.String())
	}
	if post.called {
		t.Error("verify must not go through the normal build-report path")
	}
}
