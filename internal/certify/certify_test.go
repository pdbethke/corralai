// SPDX-License-Identifier: Elastic-2.0

package certify

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
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

func TestSignVerifyStatement(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)

	_, head := BuildLedger([]Step{{Kind: "execution", Subject: "go build"}})
	stmt := BuildAttestation(BuildRecord{
		Repo: "r", Commit: "c", Command: "go build", ExitCode: 0,
		ProducedBy: []string{"anthropic:claude-opus"},
	}, head)

	sig, canonical, err := SignStatement(stmt, priv)
	if err != nil {
		t.Fatalf("SignStatement returned error: %v", err)
	}
	if !VerifyStatement(canonical, sig, pub) {
		t.Fatal("valid signature over canonical statement must verify")
	}

	// Mutate a byte in the predicate region (the exit code digit) and confirm
	// the signature no longer verifies over the tampered bytes.
	mutated := make([]byte, len(canonical))
	copy(mutated, canonical)
	idx := -1
	needle := []byte(`"exitCode":0`)
	for i := 0; i+len(needle) <= len(mutated); i++ {
		match := true
		for j := range needle {
			if mutated[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			idx = i
			break
		}
	}
	if idx == -1 {
		t.Fatalf("could not locate exitCode field in canonical bytes: %s", canonical)
	}
	// Flip "exitCode":0 -> "exitCode":1
	mutated[idx+len(needle)-1] = '1'
	if VerifyStatement(mutated, sig, pub) {
		t.Fatal("mutated canonical bytes must not verify")
	}

	if VerifyStatement(canonical, sig, otherPub) {
		t.Fatal("signature must not verify under the wrong public key")
	}
}

func TestCanonicalStatementDeterministic(t *testing.T) {
	_, head := BuildLedger([]Step{{Kind: "execution", Subject: "go build"}})
	stmt := BuildAttestation(BuildRecord{
		Repo: "r", Commit: "c", Command: "go build", ExitCode: 0,
		ProducedBy: []string{"anthropic:claude-opus", "google:gemini"},
	}, head)

	a, err := CanonicalStatement(stmt)
	if err != nil {
		t.Fatalf("CanonicalStatement returned error: %v", err)
	}
	b, err := CanonicalStatement(stmt)
	if err != nil {
		t.Fatalf("CanonicalStatement returned error: %v", err)
	}
	if string(a) != string(b) {
		t.Fatalf("CanonicalStatement not deterministic:\n%s\nvs\n%s", a, b)
	}
}

// TestMarshalUnmarshalStepsRoundTrip locks the DRY hoist of the persisted
// ledger-step shape: Step.Hash is deliberately json:"-" (its own hash must
// never be part of the input to computing that hash), so a plain
// json.Marshal/Unmarshal of []Step silently drops Hash. MarshalSteps must
// round-trip Hash/Prev explicitly so UnmarshalSteps's output still verifies.
func TestMarshalUnmarshalStepsRoundTrip(t *testing.T) {
	steps := []Step{
		{Kind: "context", Actor: "ci", Subject: "repo@abc123", Detail: map[string]any{"repo": "r"}},
		{Kind: "execution", Actor: "ci", Subject: "go test ./...", Detail: map[string]any{"exit_code": 0.0, "ok": true}},
	}
	built, head := BuildLedger(steps)

	b, err := MarshalSteps(built)
	if err != nil {
		t.Fatalf("MarshalSteps: %v", err)
	}

	got, err := UnmarshalSteps(b)
	if err != nil {
		t.Fatalf("UnmarshalSteps: %v", err)
	}
	if len(got) != len(built) {
		t.Fatalf("got %d steps, want %d", len(got), len(built))
	}
	for i := range built {
		if got[i].Hash != built[i].Hash {
			t.Errorf("step %d: Hash = %q, want %q", i, got[i].Hash, built[i].Hash)
		}
		if got[i].Prev != built[i].Prev {
			t.Errorf("step %d: Prev = %q, want %q", i, got[i].Prev, built[i].Prev)
		}
	}
	if ok, msg := VerifyLedger(got, head); !ok {
		t.Fatalf("round-tripped steps must still verify: %s", msg)
	}
}

// TestUnmarshalStepsTamperDetected proves a byte-level tamper of the
// marshaled steps is caught by VerifyLedger after UnmarshalSteps — the
// round trip itself must not silently repair or ignore corruption.
func TestUnmarshalStepsTamperDetected(t *testing.T) {
	built, head := BuildLedger([]Step{{Kind: "execution", Subject: "go build"}})
	b, err := MarshalSteps(built)
	if err != nil {
		t.Fatalf("MarshalSteps: %v", err)
	}
	tampered := strings.Replace(string(b), `"go build"`, `"go BUILD"`, 1)

	got, err := UnmarshalSteps([]byte(tampered))
	if err != nil {
		t.Fatalf("UnmarshalSteps: %v", err)
	}
	if ok, _ := VerifyLedger(got, head); ok {
		t.Fatal("tampered steps must fail verification")
	}
}
