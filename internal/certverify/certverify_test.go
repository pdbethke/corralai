// SPDX-License-Identifier: Elastic-2.0

package certverify

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"

	"github.com/pdbethke/corralai/internal/certify"
	"github.com/pdbethke/corralai/internal/transparency"
)

// buildTestRecord builds a real, fully-valid Record in-process: a signed
// ledger + attestation, mirroring what report_build/--out produce. It
// returns the record alongside the signer's public key (the external trust
// anchor VerifyRecord must be given, never derived from the record itself).
func buildTestRecord(t *testing.T) (rec Record, pub ed25519.PublicKey) {
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

	stepsJSON, err := certify.MarshalSteps(built)
	if err != nil {
		t.Fatal(err)
	}
	var steppedMaps []map[string]any
	if err := json.Unmarshal(stepsJSON, &steppedMaps); err != nil {
		t.Fatal(err)
	}

	canonical, err := certify.CanonicalStatement(stmt)
	if err != nil {
		t.Fatal(err)
	}
	var stmtDecoded map[string]any
	if err := json.Unmarshal(canonical, &stmtDecoded); err != nil {
		t.Fatal(err)
	}

	rec = Record{
		Statement: stmtDecoded,
		Signature: string(envelope),
		Steps:     steppedMaps,
		Head:      head,
	}
	return rec, pub
}

// anchor anchors rec's signature to a fresh fakeWitness, embedding the
// resulting entry and marking it anchored, mirroring what a real anchor step
// produces.
func anchor(t *testing.T, rec Record) Record {
	t.Helper()
	w := transparency.NewFakeWitness()
	entry, err := w.Anchor(context.Background(), []byte(rec.Signature))
	if err != nil {
		t.Fatal(err)
	}
	entryJSON, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	rec.Rekor = string(entryJSON)
	rec.Anchored = true
	return rec
}

// witnessFactory returns a newWitness func that always hands back the given
// witness, mirroring how a real caller would wrap a lazily-constructed
// transparency.Witness for VerifyRecord.
func witnessFactory(w transparency.Witness) func() (transparency.Witness, error) {
	return func() (transparency.Witness, error) {
		return w, nil
	}
}

// failWitnessFactory returns a newWitness func that fails the test if it is
// ever called — used to lock in the offline-fast-fail guarantee: an
// anchored record with an earlier check already failing must never reach
// the rekor step, so the witness must never be constructed.
func failWitnessFactory(t *testing.T) func() (transparency.Witness, error) {
	return func() (transparency.Witness, error) {
		t.Fatal("newWitness should not be called when an earlier check already failed")
		return nil, nil
	}
}

func checkByName(checks []Check, name string) (Check, bool) {
	for _, c := range checks {
		if c.Name == name {
			return c, true
		}
	}
	return Check{}, false
}

func TestVerifyRecordAllChecksPass(t *testing.T) {
	rec, pub := buildTestRecord(t)
	rec = anchor(t, rec)
	w := transparency.NewFakeWitness()

	checks, allOK := VerifyRecord(rec, pub, witnessFactory(w), false)
	if !allOK {
		t.Fatalf("expected allOK, got checks=%+v", checks)
	}
	for _, name := range []string{"signature", "ledger", "subject", "rekor"} {
		c, ok := checkByName(checks, name)
		if !ok {
			t.Fatalf("missing check %q in %+v", name, checks)
		}
		if !c.OK {
			t.Fatalf("check %q expected OK, got %+v", name, c)
		}
	}
}

func TestVerifyRecordTamperedSignatureFails(t *testing.T) {
	rec, pub := buildTestRecord(t)
	rec = anchor(t, rec)
	w := transparency.NewFakeWitness()

	// Corrupt the DSSE envelope so it no longer verifies against pub.
	rec.Signature = rec.Signature[:len(rec.Signature)-2] + "00"

	checks, allOK := VerifyRecord(rec, pub, witnessFactory(w), false)
	if allOK {
		t.Fatalf("expected !allOK for tampered signature, got checks=%+v", checks)
	}
	c, ok := checkByName(checks, "signature")
	if !ok || c.OK {
		t.Fatalf("expected signature check to fail, got %+v (found=%v)", c, ok)
	}
}

func TestVerifyRecordTamperedInclusionProofFails(t *testing.T) {
	rec, pub := buildTestRecord(t)
	rec = anchor(t, rec)
	w := transparency.NewFakeWitness()

	// Corrupt the embedded transparency entry's inclusion proof.
	var entry transparency.Entry
	if err := json.Unmarshal([]byte(rec.Rekor), &entry); err != nil {
		t.Fatal(err)
	}
	entry.InclusionProof = []byte("tampered-proof")
	entryJSON, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	rec.Rekor = string(entryJSON)

	checks, allOK := VerifyRecord(rec, pub, witnessFactory(w), false)
	if allOK {
		t.Fatalf("expected !allOK for tampered inclusion proof, got checks=%+v", checks)
	}
	c, ok := checkByName(checks, "rekor")
	if !ok || c.OK {
		t.Fatalf("expected rekor check to fail, got %+v (found=%v)", c, ok)
	}
	// The other three checks are unaffected by the tamper.
	for _, name := range []string{"signature", "ledger", "subject"} {
		c, ok := checkByName(checks, name)
		if !ok || !c.OK {
			t.Fatalf("expected check %q to still pass, got %+v (found=%v)", name, c, ok)
		}
	}
}

func TestVerifyRecordUnanchoredFailsByDefault(t *testing.T) {
	rec, pub := buildTestRecord(t)
	// rec.Anchored is false; not anchored, no witness call expected.

	checks, allOK := VerifyRecord(rec, pub, failWitnessFactory(t), false)
	if allOK {
		t.Fatalf("expected !allOK for unanchored record without --allow-unanchored, got checks=%+v", checks)
	}
	c, ok := checkByName(checks, "rekor")
	if !ok || c.OK {
		t.Fatalf("expected rekor check to fail for unanchored record, got %+v (found=%v)", c, ok)
	}
}

// TestVerifyRecordAnchoredTamperedSignatureNeverBuildsWitness locks in the
// offline-fast-fail guarantee: an ANCHORED record whose signature check
// fails must be rejected at check 1 WITHOUT ever constructing the rekor
// witness — no network call for a record that was always going to fail.
// failWitnessFactory calls t.Fatal if invoked, so this test would fail loud
// if VerifyRecord regressed to eagerly building the witness before checks
// 1-3 run.
func TestVerifyRecordAnchoredTamperedSignatureNeverBuildsWitness(t *testing.T) {
	rec, pub := buildTestRecord(t)
	rec = anchor(t, rec) // rec.Anchored = true

	// Corrupt the DSSE envelope so check 1 (signature) fails.
	rec.Signature = rec.Signature[:len(rec.Signature)-2] + "00"

	checks, allOK := VerifyRecord(rec, pub, failWitnessFactory(t), false)
	if allOK {
		t.Fatalf("expected !allOK for tampered signature, got checks=%+v", checks)
	}
	c, ok := checkByName(checks, "signature")
	if !ok || c.OK {
		t.Fatalf("expected signature check to fail, got %+v (found=%v)", c, ok)
	}
	// If we get here without failWitnessFactory's t.Fatal firing, the
	// witness factory was never invoked — the true assertion of this test.
}

func TestVerifyRecordUnanchoredAllowedWithFlag(t *testing.T) {
	rec, pub := buildTestRecord(t)

	checks, allOK := VerifyRecord(rec, pub, failWitnessFactory(t), true)
	if !allOK {
		t.Fatalf("expected allOK for unanchored record with allowUnanchored=true, got checks=%+v", checks)
	}
	// signature/ledger/subject must still be OK.
	for _, name := range []string{"signature", "ledger", "subject"} {
		c, ok := checkByName(checks, name)
		if !ok || !c.OK {
			t.Fatalf("expected check %q to pass, got %+v (found=%v)", name, c, ok)
		}
	}
}
