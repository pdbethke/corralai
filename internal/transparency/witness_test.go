// SPDX-License-Identifier: Elastic-2.0

package transparency

import (
	"context"
	"crypto/ed25519"
	"os"
	"testing"

	"github.com/pdbethke/corralai/internal/certify"
)

// TestFakeWitnessRoundTrip exercises the hermetic wiring contract that all
// downstream tasks (report_build, certify verify) test against: Anchor
// succeeds and returns a usable Entry, VerifyInclusion passes on an exact
// match, and fails on any tamper — to the envelope or to the proof itself.
func TestFakeWitnessRoundTrip(t *testing.T) {
	ctx := context.Background()
	w := NewFakeWitness()

	env := []byte(`{"payload":"eyJmb28iOiJiYXIifQ==","payloadType":"application/vnd.in-toto+json","signatures":[{"keyid":"k1","sig":"abc"}]}`)
	tampered := []byte(`{"payload":"eyJmb28iOiJiYXoifQ==","payloadType":"application/vnd.in-toto+json","signatures":[{"keyid":"k1","sig":"abc"}]}`)

	e, err := w.Anchor(ctx, env)
	if err != nil {
		t.Fatalf("Anchor: unexpected error: %v", err)
	}
	if e.LogIndex < 0 {
		t.Fatalf("Anchor: LogIndex = %d, want >= 0", e.LogIndex)
	}
	if len(e.InclusionProof) == 0 {
		t.Fatalf("Anchor: InclusionProof is empty")
	}
	if len(e.Body) == 0 {
		t.Fatalf("Anchor: Body is empty")
	}

	if ok, detail := w.VerifyInclusion(e, env); !ok {
		t.Fatalf("VerifyInclusion(matching): ok=false, detail=%q, want true", detail)
	}

	if ok, _ := w.VerifyInclusion(e, tampered); ok {
		t.Fatalf("VerifyInclusion(tampered envelope): ok=true, want false")
	}

	// Corrupt the inclusion proof; verification must fail even for the
	// original envelope.
	bad := e
	bad.InclusionProof = append([]byte(nil), e.InclusionProof...)
	bad.InclusionProof[0] ^= 0xff
	if ok, _ := w.VerifyInclusion(bad, env); ok {
		t.Fatalf("VerifyInclusion(corrupt proof): ok=true, want false")
	}
}

// TestFakeWitnessDeterministic asserts the fake is a pure function of the
// envelope: two anchors of the same bytes yield the same proof/body, so
// downstream golden tests are stable.
func TestFakeWitnessDeterministic(t *testing.T) {
	ctx := context.Background()
	w := NewFakeWitness()
	env := []byte(`{"hello":"world"}`)

	e1, err := w.Anchor(ctx, env)
	if err != nil {
		t.Fatal(err)
	}
	e2, err := w.Anchor(ctx, env)
	if err != nil {
		t.Fatal(err)
	}
	if string(e1.InclusionProof) != string(e2.InclusionProof) {
		t.Fatalf("InclusionProof not deterministic")
	}
	if string(e1.Body) != string(e2.Body) {
		t.Fatalf("Body not deterministic")
	}
}

// TestRekorWitnessIntegration proves the real RekorWitness end-to-end against
// the public Sigstore Rekor instance. It stays skipped in CI (hermetic) and
// only runs when CORRALAI_REKOR_INTEGRATION=1 is set, since it makes network
// calls that write a real entry to a public transparency log.
func TestRekorWitnessIntegration(t *testing.T) {
	if os.Getenv("CORRALAI_REKOR_INTEGRATION") != "1" {
		t.Skip("set CORRALAI_REKOR_INTEGRATION=1 to run the real Rekor integration test")
	}
	ctx := context.Background()

	// Build a real DSSE envelope via the certify package.
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	// Rekor's dsse entry type verifies the envelope signature at submission
	// and stores the verifier; the witness must be told the signer's public
	// key, since a DSSE envelope does not carry one.
	w, err := NewRekorWitness("https://rekor.sigstore.dev", WithSignerPublicKey(pub))
	if err != nil {
		t.Fatalf("NewRekorWitness: %v", err)
	}

	stmt := map[string]any{
		"_type":         "https://in-toto.io/Statement/v1",
		"predicateType": "https://corralai.dev/attestation/test/v1",
		"subject": []map[string]any{
			{"name": "corralai-transparency-test", "digest": map[string]string{"sha256": "abc123"}},
		},
	}
	env, err := certify.SignDSSE(stmt, priv, "test-key")
	if err != nil {
		t.Fatalf("SignDSSE: %v", err)
	}

	entry, err := w.Anchor(ctx, env)
	if err != nil {
		t.Fatalf("Anchor: %v", err)
	}
	t.Logf("anchored: logIndex=%d logID=%s integratedTime=%d proofBytes=%d setBytes=%d",
		entry.LogIndex, entry.LogID, entry.IntegratedTime, len(entry.InclusionProof), len(entry.SET))

	ok, detail := w.VerifyInclusion(entry, env)
	if !ok {
		t.Fatalf("VerifyInclusion(matching): ok=false, detail=%q", detail)
	}
	t.Logf("VerifyInclusion(matching): ok=true, detail=%q", detail)

	// Tamper: a different envelope must not verify against this entry.
	tampered := certifyResign(t, priv)
	if ok, _ := w.VerifyInclusion(entry, tampered); ok {
		t.Fatalf("VerifyInclusion(tampered envelope): ok=true, want false")
	}
	t.Logf("VerifyInclusion(tampered): ok=false as expected")
}

// certifyResign builds a second, distinct DSSE envelope (different subject) so
// the integration test has a genuine non-matching envelope to tamper-check.
func certifyResign(t *testing.T, priv ed25519.PrivateKey) []byte {
	t.Helper()
	stmt := map[string]any{
		"_type":         "https://in-toto.io/Statement/v1",
		"predicateType": "https://corralai.dev/attestation/test/v1",
		"subject": []map[string]any{
			{"name": "DIFFERENT-subject", "digest": map[string]string{"sha256": "def456"}},
		},
	}
	env, err := certify.SignDSSE(stmt, priv, "test-key")
	if err != nil {
		t.Fatalf("SignDSSE(resign): %v", err)
	}
	return env
}
