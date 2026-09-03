// SPDX-License-Identifier: Elastic-2.0

package certify

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/secure-systems-lab/go-securesystemslib/dsse"
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

func TestSignVerifyDSSE(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)

	_, head := BuildLedger([]Step{{Kind: "execution", Subject: "go build"}})
	stmt := BuildAttestation(BuildRecord{
		Repo: "r", Commit: "c", Command: "go build", ExitCode: 0,
		ProducedBy: []string{"anthropic:claude-opus"},
	}, head)

	env, err := SignDSSE(stmt, priv, "brain")
	if err != nil {
		t.Fatalf("SignDSSE returned error: %v", err)
	}

	got, ok, err := VerifyDSSE(env, pub)
	if err != nil {
		t.Fatalf("VerifyDSSE returned error: %v", err)
	}
	if !ok {
		t.Fatal("valid DSSE envelope must verify")
	}
	gotSubj, wantSubj := got["subject"], stmt["subject"]
	gotSubjJSON, err := json.Marshal(gotSubj)
	if err != nil {
		t.Fatal(err)
	}
	wantSubjJSON, err := json.Marshal(wantSubj)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotSubjJSON) != string(wantSubjJSON) {
		t.Fatalf("VerifyDSSE statement subject = %s, want %s", gotSubjJSON, wantSubjJSON)
	}

	// Tamper: flip a byte in the envelope's base64 payload field.
	var envMap map[string]any
	if err := json.Unmarshal(env, &envMap); err != nil {
		t.Fatal(err)
	}
	payload, ok := envMap["payload"].(string)
	if !ok || payload == "" {
		t.Fatalf("envelope missing payload: %v", envMap)
	}
	payloadBytes := []byte(payload)
	// Flip a character that is not the trailing base64 padding, to guarantee
	// the decoded bytes actually change.
	idx := len(payloadBytes) / 2
	if payloadBytes[idx] == 'A' {
		payloadBytes[idx] = 'B'
	} else {
		payloadBytes[idx] = 'A'
	}
	envMap["payload"] = string(payloadBytes)
	tampered, err := json.Marshal(envMap)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := VerifyDSSE(tampered, pub); ok {
		t.Fatal("a tampered envelope payload must not verify")
	}

	// Wrong key must not verify.
	if _, ok, _ := VerifyDSSE(env, otherPub); ok {
		t.Fatal("envelope must not verify under the wrong public key")
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

// --- helpers shared by the tamper-evidence tests below ---------------------

// signedAuditStatement builds a small ledger, wraps it in an attestation for
// a passing run, and returns the statement, its ledger head and a signed DSSE
// envelope over it, along with the key pair used.
func signedAuditStatement(t *testing.T) (stmt map[string]any, head string, env []byte, pub ed25519.PublicKey, priv ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	_, head = BuildLedger([]Step{
		{Kind: "context", Subject: "repo@abc123"},
		{Kind: "execution", Subject: "go test ./...", Detail: map[string]any{"exit_code": 0, "ok": true}},
	})
	stmt = BuildAttestation(BuildRecord{
		Repo: "github.com/corralai/corral", Commit: "abc123", Command: "go test ./...",
		ExitCode: 0, ProducedBy: []string{"anthropic:claude-opus", "google:gemini"},
	}, head)
	env, err = SignDSSE(stmt, priv, "brain")
	if err != nil {
		t.Fatalf("SignDSSE: %v", err)
	}
	return stmt, head, env, pub, priv
}

// tamperStatement rewrites the statement inside a signed DSSE envelope,
// re-encoding the payload but leaving the original signature in place. It is
// the shape of the attack the signature exists to catch: an edit to what the
// statement says, not to the bytes of the signature.
func tamperStatement(t *testing.T, env []byte, edit func(stmt map[string]any)) []byte {
	t.Helper()
	var envMap map[string]any
	if err := json.Unmarshal(env, &envMap); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	payloadB64, ok := envMap["payload"].(string)
	if !ok || payloadB64 == "" {
		t.Fatalf("envelope missing payload: %v", envMap)
	}
	payload, err := base64.StdEncoding.DecodeString(payloadB64)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var stmt map[string]any
	if err := json.Unmarshal(payload, &stmt); err != nil {
		t.Fatalf("unmarshal payload statement: %v", err)
	}

	edit(stmt)

	edited, err := json.Marshal(stmt)
	if err != nil {
		t.Fatalf("marshal edited statement: %v", err)
	}
	envMap["payload"] = base64.StdEncoding.EncodeToString(edited)
	out, err := json.Marshal(envMap)
	if err != nil {
		t.Fatalf("marshal tampered envelope: %v", err)
	}
	return out
}

// TestVerifyDSSERejectsATamperedStatement is the package's headline
// guarantee: the three things a reader of an audit trusts — the verdict, the
// subject digest that binds the statement to the ledger, and the names of the
// models that produced the change — cannot be edited after signing. The
// existing round-trip test flips one base64 character; this edits the
// statement's own fields and re-encodes a well-formed payload, which is what
// a forger would actually do.
func TestVerifyDSSERejectsATamperedStatement(t *testing.T) {
	_, _, env, pub, _ := signedAuditStatement(t)

	if _, ok, err := VerifyDSSE(env, pub); err != nil || !ok {
		t.Fatalf("the untampered envelope must verify: ok=%v err=%v", ok, err)
	}

	t.Run("verdict", func(t *testing.T) {
		tampered := tamperStatement(t, env, func(stmt map[string]any) {
			byproducts := stmt["predicate"].(map[string]any)["runDetails"].(map[string]any)["byproducts"].([]any)
			for _, b := range byproducts {
				bp := b.(map[string]any)
				if bp["name"] == "certification/execution" {
					ann := bp["annotations"].(map[string]any)
					ann["exitCode"] = float64(1)
					ann["passed"] = false
				}
			}
		})
		if _, ok, _ := VerifyDSSE(tampered, pub); ok {
			t.Fatal("a flipped verdict must break verification")
		}
	})

	t.Run("subject digest", func(t *testing.T) {
		tampered := tamperStatement(t, env, func(stmt map[string]any) {
			subject := stmt["subject"].([]any)[0].(map[string]any)
			subject["digest"].(map[string]any)["sha256"] = genesisPrev
		})
		if _, ok, _ := VerifyDSSE(tampered, pub); ok {
			t.Fatal("a rewritten subject digest must break verification")
		}
	})

	t.Run("model names", func(t *testing.T) {
		tampered := tamperStatement(t, env, func(stmt map[string]any) {
			buildDef := stmt["predicate"].(map[string]any)["buildDefinition"].(map[string]any)
			buildDef["resolvedDependencies"] = []any{map[string]any{"uri": "model:some-other-model"}}
		})
		if _, ok, _ := VerifyDSSE(tampered, pub); ok {
			t.Fatal("a rewritten model name must break verification")
		}
	})
}

// TestVerifyDSSEFailsClosedOnAMalformedEnvelope pins the documented contract
// that unreadable input is a failed verification, not an error the caller can
// mistake for "could not check" and skip past.
func TestVerifyDSSEFailsClosedOnAMalformedEnvelope(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	for _, envelope := range []string{"", "not json at all", "{", `{"payload":`, "null"} {
		stmt, ok, err := VerifyDSSE([]byte(envelope), pub)
		if ok {
			t.Fatalf("VerifyDSSE(%q) = ok, want a failed verification", envelope)
		}
		if err != nil {
			t.Fatalf("VerifyDSSE(%q) returned err %v, want a plain ok=false", envelope, err)
		}
		if stmt != nil {
			t.Fatalf("VerifyDSSE(%q) returned a statement on failure: %v", envelope, stmt)
		}
	}
}

// TestVerifyDSSEFailsClosedOnAnUnusableKey guards the branch where the
// verifying key is the wrong size: a key that cannot verify anything must
// never be read as a verification that passed.
func TestVerifyDSSEFailsClosedOnAnUnusableKey(t *testing.T) {
	_, _, env, _, _ := signedAuditStatement(t)

	for _, pub := range []ed25519.PublicKey{nil, {}, ed25519.PublicKey("too short")} {
		stmt, ok, err := VerifyDSSE(env, pub)
		if ok {
			t.Fatalf("VerifyDSSE with a %d-byte key = ok, want a failed verification", len(pub))
		}
		if err != nil {
			t.Fatalf("VerifyDSSE with a %d-byte key returned err %v, want a plain ok=false", len(pub), err)
		}
		if stmt != nil {
			t.Fatalf("VerifyDSSE with a %d-byte key returned a statement: %v", len(pub), stmt)
		}
	}
}

// TestVerifyDSSEFailsClosedOnANonJSONPayload covers the last gate: a payload
// whose signature is genuinely valid but whose bytes are not an in-toto
// statement must not be handed back as one.
func TestVerifyDSSEFailsClosedOnANonJSONPayload(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	es, err := dsse.NewEnvelopeSigner(&ed25519SigVerifier{priv: priv, keyID: "brain"})
	if err != nil {
		t.Fatalf("NewEnvelopeSigner: %v", err)
	}
	signed, err := es.SignPayload(context.Background(), intotoPayloadType, []byte("{this is not a statement"))
	if err != nil {
		t.Fatalf("SignPayload: %v", err)
	}
	env, err := json.Marshal(signed)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	stmt, ok, err := VerifyDSSE(env, pub)
	if err != nil {
		t.Fatalf("VerifyDSSE returned err %v, want a plain ok=false", err)
	}
	if ok || stmt != nil {
		t.Fatalf("a validly signed non-JSON payload must not verify: ok=%v stmt=%v", ok, stmt)
	}
}

// TestSignDSSEPreservesTheKeyID matters more than it looks: VerifyDSSE picks
// its candidate verifier by matching the envelope's keyID, so a SignDSSE that
// dropped or rewrote the caller's keyID would leave signatures that are
// skipped rather than checked.
func TestSignDSSEPreservesTheKeyID(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	_, head := BuildLedger([]Step{{Kind: "execution", Subject: "go build"}})
	stmt := BuildAttestation(BuildRecord{Repo: "r", Commit: "c"}, head)

	const keyID = "corral-brain-2026"
	env, err := SignDSSE(stmt, priv, keyID)
	if err != nil {
		t.Fatalf("SignDSSE: %v", err)
	}

	var envMap map[string]any
	if err := json.Unmarshal(env, &envMap); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	sigs, _ := envMap["signatures"].([]any)
	if len(sigs) == 0 {
		t.Fatalf("envelope carries no signatures: %v", envMap)
	}
	if got, _ := sigs[0].(map[string]any)["keyid"].(string); got != keyID {
		t.Fatalf("signature keyid = %q, want %q", got, keyID)
	}
}

// TestSignDSSEUsesTheInTotoPayloadType locks the interop contract: the DSSE
// pre-authentication encoding covers payloadType, so an independent verifier
// that expects an in-toto statement can only check this envelope if the type
// is the registered one.
func TestSignDSSEUsesTheInTotoPayloadType(t *testing.T) {
	_, _, env, _, _ := signedAuditStatement(t)

	var envMap map[string]any
	if err := json.Unmarshal(env, &envMap); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if got := envMap["payloadType"]; got != "application/vnd.in-toto+json" {
		t.Fatalf("payloadType = %v, want application/vnd.in-toto+json", got)
	}
}

// TestSignDSSEPayloadIsExactlyTheCanonicalStatement is what lets a third
// party re-derive the signed bytes: the envelope payload must be
// CanonicalStatement's output byte for byte, with nothing added around it.
func TestSignDSSEPayloadIsExactlyTheCanonicalStatement(t *testing.T) {
	stmt, _, env, _, _ := signedAuditStatement(t)

	canonical, err := CanonicalStatement(stmt)
	if err != nil {
		t.Fatalf("CanonicalStatement: %v", err)
	}

	var envMap map[string]any
	if err := json.Unmarshal(env, &envMap); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	payload, err := base64.StdEncoding.DecodeString(envMap["payload"].(string))
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if string(payload) != string(canonical) {
		t.Fatalf("signed payload is not the canonical statement:\n got %s\nwant %s", payload, canonical)
	}
}

// TestSignDSSERejectsAMalformedPrivateKey holds SignDSSE to its documented
// promise of an error rather than a panic, and to never putting key material
// in the message it returns.
func TestSignDSSERejectsAMalformedPrivateKey(t *testing.T) {
	_, good, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	truncated := ed25519.PrivateKey(good[:16])

	_, head := BuildLedger([]Step{{Kind: "execution", Subject: "go build"}})
	stmt := BuildAttestation(BuildRecord{Repo: "r", Commit: "c"}, head)

	env, err := SignDSSE(stmt, truncated, "brain")
	if err == nil {
		t.Fatalf("SignDSSE with a %d-byte private key must fail, got envelope %s", len(truncated), env)
	}
	if env != nil {
		t.Fatalf("SignDSSE returned an envelope alongside its error: %s", env)
	}
	for _, leak := range []string{string(truncated), hex.EncodeToString(truncated), base64.StdEncoding.EncodeToString(truncated)} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("SignDSSE error text leaks key material: %v", err)
		}
	}
}

// TestSignDSSERejectsAnUnmarshalableStatement: SignDSSE is on the path of a
// CLI run, so a statement it cannot canonicalize has to surface as an error,
// never as a panic that takes the run down without a verdict.
func TestSignDSSERejectsAnUnmarshalableStatement(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if _, err := SignDSSE(map[string]any{"ch": make(chan int)}, priv, "brain"); err == nil {
		t.Fatal("SignDSSE must return an error for a statement that cannot be marshaled")
	}
}

// TestVerifySigRejectsAMalformedSignature: VerifySig's contract is that a
// signature it cannot even decode is a failed verification, not a panic and
// not a decode error the caller has to remember to handle.
func TestVerifySigRejectsAMalformedSignature(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	head := "deadbeef"
	for _, sig := range []string{"", "not hex", "zz", "abc", "GHIJKL", "00"} {
		if VerifySig(head, sig, pub) {
			t.Errorf("VerifySig accepted the malformed signature %q", sig)
		}
	}
}

// TestVerifyLedgerRejectsAHeadMismatch covers the case the round-trip test
// does not: every link in the chain is intact, but the head being claimed is
// not the one the steps produce. A ledger is only evidence for the head it
// actually lands on.
func TestVerifyLedgerRejectsAHeadMismatch(t *testing.T) {
	built, head := BuildLedger([]Step{
		{Kind: "context", Subject: "repo@abc123"},
		{Kind: "execution", Subject: "go test ./..."},
	})
	if ok, msg := VerifyLedger(built, head); !ok {
		t.Fatalf("the clean ledger must verify against its own head: %s", msg)
	}
	if ok, _ := VerifyLedger(built, strings.Repeat("f", 64)); ok {
		t.Fatal("a ledger must not verify against a head it does not produce")
	}
	if ok, _ := VerifyLedger(built, ""); ok {
		t.Fatal("a ledger must not verify against an empty head")
	}
}

// TestVerifyLedgerRejectsARehashedStep is the attack the prev chain exists
// for: an editor who alters a step and recomputes that step's own hash
// produces a self-consistent step, and is still caught because the next
// step's Prev no longer points at it.
func TestVerifyLedgerRejectsARehashedStep(t *testing.T) {
	built, head := BuildLedger([]Step{
		{Kind: "execution", Subject: "go test ./...", Detail: map[string]any{"exit_code": 1, "ok": false}},
		{Kind: "review", Subject: "verdict"},
	})

	built[0].Detail = map[string]any{"exit_code": 0, "ok": true}
	built[0].Hash = stepHash(built[0])

	if stepHash(built[0]) != built[0].Hash {
		t.Fatal("test precondition: the edited step should hash to its own recorded hash")
	}
	if ok, _ := VerifyLedger(built, head); ok {
		t.Fatal("a re-hashed step must still be caught by the following step's prev link")
	}
}

// TestBuildLedgerNumbersStepsInOrder: Seq is what VerifyLedger's diagnostics
// name and what a reader uses to place a step in the run, so it has to be the
// step's real position, assigned by BuildLedger rather than trusted from the
// caller.
func TestBuildLedgerNumbersStepsInOrder(t *testing.T) {
	built, _ := BuildLedger([]Step{
		{Seq: 99, Kind: "context", Subject: "step0"},
		{Seq: 99, Kind: "execution", Subject: "step1"},
		{Seq: 99, Kind: "review", Subject: "step2"},
	})
	if len(built) != 3 {
		t.Fatalf("got %d steps, want 3", len(built))
	}
	for i, s := range built {
		if s.Seq != i {
			t.Errorf("step %d has Seq %d, want %d", i, s.Seq, i)
		}
	}
}

// TestStepHashIsAFullSha256Digest: a truncated or otherwise shortened digest
// would still chain and still verify, while quietly costing the ledger most
// of its collision resistance.
func TestStepHashIsAFullSha256Digest(t *testing.T) {
	built, head := BuildLedger([]Step{
		{Kind: "context", Subject: "repo@abc123"},
		{Kind: "execution", Subject: "go test ./..."},
	})
	for i, s := range built {
		if len(s.Hash) != 64 {
			t.Errorf("step %d hash %q is %d chars, want 64", i, s.Hash, len(s.Hash))
		}
		if _, err := hex.DecodeString(s.Hash); err != nil {
			t.Errorf("step %d hash %q is not hex: %v", i, s.Hash, err)
		}
	}
	if len(head) != 64 {
		t.Errorf("head %q is %d chars, want 64", head, len(head))
	}
	if built[0].Hash == built[1].Hash {
		t.Error("two different steps must not share a hash")
	}
}

// TestAttestationSubjectNamesRepoAtCommit pins the other half of the subject:
// the digest binds the statement to the ledger, and the name binds it to the
// exact repo and commit that ledger was recorded for.
func TestAttestationSubjectNamesRepoAtCommit(t *testing.T) {
	_, head := BuildLedger([]Step{{Kind: "execution", Subject: "go build"}})
	att := BuildAttestation(BuildRecord{Repo: "github.com/corralai/corral", Commit: "abc123"}, head)

	subjects, ok := att["subject"].([]map[string]any)
	if !ok || len(subjects) != 1 {
		t.Fatalf("attestation subject = %v, want exactly one subject", att["subject"])
	}
	if got := subjects[0]["name"]; got != "github.com/corralai/corral@abc123" {
		t.Fatalf("subject name = %v, want github.com/corralai/corral@abc123", got)
	}
}

// TestAttestationVerdictReflectsTheExitCode: "passed" is the line a reader
// acts on, and it must be derived from the recorded exit code rather than set
// independently of it.
func TestAttestationVerdictReflectsTheExitCode(t *testing.T) {
	_, head := BuildLedger([]Step{{Kind: "execution", Subject: "go test ./..."}})

	for _, tc := range []struct {
		exitCode   int
		wantPassed bool
	}{{0, true}, {1, false}, {2, false}, {-1, false}} {
		att := BuildAttestation(BuildRecord{Repo: "r", Commit: "c", Command: "go test ./...", ExitCode: tc.exitCode}, head)
		ann := executionAnnotations(t, att)
		if got := ann["passed"]; got != tc.wantPassed {
			t.Errorf("exit code %d: passed = %v, want %v", tc.exitCode, got, tc.wantPassed)
		}
		if got := ann["exitCode"]; got != tc.exitCode {
			t.Errorf("exit code %d: annotated exitCode = %v", tc.exitCode, got)
		}
	}
}

// TestAttestationByproductsCarryTheLedgerHead: the ledger byproduct is how a
// verifier finds which ledger to re-chain, so its digest must be the same
// head the subject is bound to, not an independently computed value.
func TestAttestationByproductsCarryTheLedgerHead(t *testing.T) {
	_, head := BuildLedger([]Step{{Kind: "execution", Subject: "go build"}})
	att := BuildAttestation(BuildRecord{Repo: "r", Commit: "c"}, head)

	byproducts := att["predicate"].(map[string]any)["runDetails"].(map[string]any)["byproducts"].([]map[string]any)
	var found bool
	for _, bp := range byproducts {
		if bp["name"] != "accountability/tamper-evident-ledger" {
			continue
		}
		found = true
		if got := bp["digest"].(map[string]string)["sha256"]; got != head {
			t.Fatalf("ledger byproduct digest = %q, want the ledger head %q", got, head)
		}
	}
	if !found {
		t.Fatalf("attestation has no accountability/tamper-evident-ledger byproduct: %v", byproducts)
	}
}

// executionAnnotations returns the annotations of the certification/execution
// byproduct, failing the test if the attestation does not carry one.
func executionAnnotations(t *testing.T, att map[string]any) map[string]any {
	t.Helper()
	byproducts := att["predicate"].(map[string]any)["runDetails"].(map[string]any)["byproducts"].([]map[string]any)
	for _, bp := range byproducts {
		if bp["name"] == "certification/execution" {
			ann, ok := bp["annotations"].(map[string]any)
			if !ok {
				t.Fatalf("certification/execution byproduct has no annotations: %v", bp)
			}
			return ann
		}
	}
	t.Fatalf("attestation has no certification/execution byproduct: %v", byproducts)
	return nil
}

// TestUnmarshalStepsRejectsInvalidJSON: a persisted ledger that cannot be
// parsed must surface as an error, not as an empty slice that VerifyLedger
// would then happily call a valid, empty ledger.
func TestUnmarshalStepsRejectsInvalidJSON(t *testing.T) {
	for _, b := range []string{"", "not json", `{"seq":0}`, `[{"seq":"not an int"}]`} {
		steps, err := UnmarshalSteps([]byte(b))
		if err == nil {
			t.Errorf("UnmarshalSteps(%q) = %v, want an error", b, steps)
		}
	}
}

// TestBuildAttestationSignsTheMeasuredNumbers pins the fix for a signed
// `certify --local` record whose headline number no reader could check.
//
// The kill rate was carried ONLY inside output_digest — a sha256 over the
// marshalled Verdict. That digest is genuinely bound (the ledger head commits
// to it, and certverify checks subject-digest == head), but it is an OPAQUE
// commitment: no shipped artifact carried the Verdict bytes and nothing
// recomputes the digest, so a third party could confirm the record was
// authentic and still not read the number corral exists to produce.
// `--repo --attest` signed these in the clear all along; this makes the two
// signing paths mean the same thing.
func TestBuildAttestationSignsTheMeasuredNumbers(t *testing.T) {
	rate := 0.75
	stmt := BuildAttestation(BuildRecord{
		Repo: "r", Commit: "c", Command: "corral/adversarial-pool",
		OutputDigest: "sha256:deadbeef",
		Scored: &ScoredCertification{
			KillRate: &rate, MutantsTotal: 40, Survivors: 10,
			ProvenMissed: 3, TestWriterFailed: true,
		},
	}, "head")

	a := certificationByproduct(t, stmt)
	for k, want := range map[string]any{
		"killRate": 0.75, "mutantsTotal": 40, "survivors": 10,
		"provenMissed": 3, "testWriterFailed": true,
	} {
		if got, ok := a[k]; !ok || got != want {
			t.Errorf("annotation %q = %v (present=%v), want %v — a reader of the signed statement cannot see this number", k, got, ok, want)
		}
	}
}

// TestBuildAttestationOmitsNumbersNobodyMeasured is the other half, and the
// more important one: a record for an ORDINARY build (certify_change, the
// brain's buildcert, a human submission) has no adequacy run behind it.
// Emitting "survivors: 0" there would sign a measurement nobody made — the
// fabricated-zero failure this project exists to refuse, committed by its own
// attestation. Absent, never zero-filled.
func TestBuildAttestationOmitsNumbersNobodyMeasured(t *testing.T) {
	stmt := BuildAttestation(BuildRecord{Repo: "r", Commit: "c", Command: "go test"}, "head")
	a := certificationByproduct(t, stmt)
	for _, k := range []string{"killRate", "mutantsTotal", "survivors", "provenMissed", "testWriterFailed"} {
		if _, present := a[k]; present {
			t.Errorf("annotation %q is present on a record with no adequacy run — a zero here is a claim about a measurement that never happened", k)
		}
	}
	// The always-present set must survive the refactor.
	for _, k := range []string{"command", "exitCode", "passed", "durationS", "outputDigest"} {
		if _, present := a[k]; !present {
			t.Errorf("annotation %q disappeared", k)
		}
	}
}

// TestBuildAttestationOmitsAnUnmeasuredKillRate: a run that could not grade
// has a real 0.0 in the struct. Signing it as killRate 0 would report "the
// suite caught nothing" for a run that measured nothing.
func TestBuildAttestationOmitsAnUnmeasuredKillRate(t *testing.T) {
	stmt := BuildAttestation(BuildRecord{
		Repo: "r", Commit: "c",
		Scored: &ScoredCertification{MutantsTotal: 12, BaselineFailed: true},
	}, "head")
	a := certificationByproduct(t, stmt)
	if _, present := a["killRate"]; present {
		t.Error("killRate is present with no rate measured — an absent rate says 'nothing was measured'; a zero says 'the suite caught nothing'")
	}
	if a["baselineFailed"] != true {
		t.Error("baselineFailed did not ride the statement, so a reader cannot tell WHY the rate is absent")
	}
}

func certificationByproduct(t *testing.T, stmt map[string]any) map[string]any {
	t.Helper()
	pred := stmt["predicate"].(map[string]any)
	run := pred["runDetails"].(map[string]any)
	for _, bp := range run["byproducts"].([]map[string]any) {
		if bp["name"] == "certification/execution" {
			return bp["annotations"].(map[string]any)
		}
	}
	t.Fatal("no certification/execution byproduct on the statement")
	return nil
}

// TestStatementCarriesTheSignedVerdictBytes pins the half of the signing fix
// that took three attempts to get right.
//
// The record needs the exact document outputDigest is the sha256 of, or the
// digest is a commitment nobody can open. Reconstructing those bytes later —
// re-marshalling the verdict the caller still holds — failed twice on real
// runs, because the driver assigns RecordID, RecordHead, ChallengerAgreement
// and at least one more field AFTER signing. So the bytes are captured where
// they are hashed and ride the STATEMENT, which the DSSE envelope signs.
func TestStatementCarriesTheSignedVerdictBytes(t *testing.T) {
	const doc = `{"DevKillRate":0.625,"Survivors":3}`
	stmt := BuildAttestation(BuildRecord{Repo: "r", Commit: "c", VerdictJSON: doc}, "head")

	got, ok := namedByproduct(t, stmt, "certification/verdict")
	if !ok {
		t.Fatal("no certification/verdict byproduct — the record cannot substantiate its own outputDigest")
	}
	if got["json"] != doc {
		t.Errorf("verdict bytes = %v, want the exact document that was digested", got["json"])
	}
}

// TestStatementOmitsAVerdictNobodySupplied: certify_change, the brain's
// buildcert and a human submission have no verdict. An empty byproduct there
// would assert a document that does not exist.
func TestStatementOmitsAVerdictNobodySupplied(t *testing.T) {
	stmt := BuildAttestation(BuildRecord{Repo: "r", Commit: "c"}, "head")
	if _, ok := namedByproduct(t, stmt, "certification/verdict"); ok {
		t.Error("a verdict byproduct appeared for a record with no verdict behind it")
	}
	// The two that every record has must survive.
	for _, n := range []string{"accountability/tamper-evident-ledger", "certification/execution"} {
		if _, ok := namedByproduct(t, stmt, n); !ok {
			t.Errorf("byproduct %q disappeared", n)
		}
	}
}

func namedByproduct(t *testing.T, stmt map[string]any, name string) (map[string]any, bool) {
	t.Helper()
	pred := stmt["predicate"].(map[string]any)
	run := pred["runDetails"].(map[string]any)
	for _, bp := range run["byproducts"].([]map[string]any) {
		if bp["name"] == name {
			a, _ := bp["annotations"].(map[string]any)
			return a, true
		}
	}
	return nil, false
}

// TestSuiteIgnoresFileWithholdsTheRateAndSaysSo pins the second route by which a
// zero denominator reached a SIGNED statement.
//
// Verdict.SuiteIgnoresFile means the suite provably never compiles or imports the
// file under audit — its own doc says "DevKillRate is meaningless". Every other
// site treats it as BaselineFailed's peer; only the signing guard read one and not
// the other, so a run with SuiteIgnoresFile=true and BaselineFailed=false signed
// killRate 0 over N mutants with 0 survivors. Not merely unmeasured: internally
// inconsistent.
//
// Absence alone is not enough. An omitted rate with no flag beside it is a hole a
// reader cannot interpret, so the flag rides with it — that is how the record
// distinguishes "nothing to catch" from "caught nothing".
func TestSuiteIgnoresFileWithholdsTheRateAndSaysSo(t *testing.T) {
	stmt := BuildAttestation(BuildRecord{
		Repo: "r", Commit: "c",
		Scored: &ScoredCertification{MutantsTotal: 2, Survivors: 0, SuiteIgnoresFile: true},
	}, "head")
	a := certificationByproduct(t, stmt)

	if _, present := a["killRate"]; present {
		t.Errorf("killRate = %v is signed for a file the suite never exercises — a 0 over 2 mutants with 0 survivors is not a measurement, it is a contradiction", a["killRate"])
	}
	if a["suiteIgnoresFile"] != true {
		t.Error("the rate is absent and nothing says WHY — a reader cannot tell an unexercised file from a missing field")
	}
}
