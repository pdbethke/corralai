// SPDX-License-Identifier: Elastic-2.0

package certify

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
)

func TestLedgerRoundTripAndTamper(t *testing.T) {
	steps := []Step{
		{Kind: "context", Subject: "repo@abc123"},
		{Kind: "execution", Subject: "go test ./...", Detail: map[string]any{"exit_code": 0, "ok": true}},
	}
	built, head := BuildLedger(steps)
	if head == "" || built[0].Prev != "0000000000000000000000000000000000000000000000000000000000000000" {
		t.Fatalf("genesis/head wrong: head=%q prev0=%q", head, built[0].Prev)
	}
	if ok, msg := VerifyLedger(built, head); !ok {
		t.Fatalf("clean ledger should verify: %s", msg)
	}
	// Tamper: flip the recorded pass, do NOT recompute the chain.
	built[1].Detail = map[string]any{"exit_code": 1, "ok": false}
	if ok, _ := VerifyLedger(built, head); ok {
		t.Fatal("tampered ledger must fail verification")
	}
}

func TestAttestationSubjectIsLedgerHead(t *testing.T) {
	_, head := BuildLedger([]Step{{Kind: "execution", Subject: "go build"}})
	att := BuildAttestation(BuildRecord{Repo: "r", Commit: "c", Command: "go build", ExitCode: 0, ProducedBy: []string{"anthropic:claude-opus"}}, head)
	subj := att["subject"].([]map[string]any)[0]["digest"].(map[string]string)["sha256"]
	if subj != head {
		t.Fatalf("subject digest %q != ledger head %q", subj, head)
	}
	if att["predicateType"] != "https://slsa.dev/provenance/v1" {
		t.Fatalf("wrong predicateType: %v", att["predicateType"])
	}
}

func TestSignVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	head := "deadbeef"
	sig := Sign(head, priv)
	if !VerifySig(head, sig, pub) {
		t.Fatal("valid signature must verify")
	}
	if VerifySig("tampered", sig, pub) {
		t.Fatal("signature must not verify a different head")
	}
}
