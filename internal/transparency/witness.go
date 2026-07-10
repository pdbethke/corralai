// SPDX-License-Identifier: Elastic-2.0

// Package transparency anchors a certify DSSE envelope to a public
// append-only transparency log (Sigstore Rekor) and verifies the log's
// inclusion proof offline, so a third party can confirm a build attestation
// was publicly logged without trusting the corral that produced it.
//
// The Witness abstraction has two implementations: a real RekorWitness that
// talks to a Rekor instance and verifies inclusion against the Sigstore TUF
// trust root, and a deterministic, hermetic fakeWitness used by downstream
// tests to exercise the anchor/verify wiring without any network.
package transparency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

// Entry is the witness's record of an anchored DSSE envelope: the log
// coordinates plus the opaque material a verifier needs to confirm inclusion
// offline. Its fields carry only public data (log indices, proofs, signed
// timestamps, canonical body bytes) — never private key material.
type Entry struct {
	// LogIndex is the entry's position in the transparency log.
	LogIndex int64
	// LogID identifies the log (Rekor's public-key-derived tree ID).
	LogID string
	// IntegratedTime is the Unix time the log committed the entry.
	IntegratedTime int64
	// InclusionProof is opaque proof material; the witness that produced it
	// knows how to verify it (a Merkle inclusion proof for Rekor).
	InclusionProof []byte
	// SET is the Signed Entry Timestamp: the log's signature over the entry,
	// letting a verifier confirm the log vouched for it offline.
	SET []byte
	// Body is the canonicalized log-entry body, used to confirm the entry
	// actually wraps the DSSE envelope in question.
	Body []byte
}

// Witness anchors a DSSE envelope to a transparency log and verifies the
// resulting inclusion proof.
type Witness interface {
	// Anchor submits dsseEnvelope to the log and returns its Entry.
	Anchor(ctx context.Context, dsseEnvelope []byte) (Entry, error)
	// VerifyInclusion checks that entry is a valid inclusion proof for
	// dsseEnvelope. It returns ok=false with a human-readable detail on any
	// mismatch rather than an error, so callers can surface the reason.
	VerifyInclusion(entry Entry, dsseEnvelope []byte) (ok bool, detail string)
}

// --- fakeWitness: hermetic, deterministic, NOT a real Merkle proof ---

// fakeWitness is an in-memory stand-in whose Entry fields are a pure function
// of the anchored envelope (InclusionProof/Body = sha256(envelope)). It exists
// so downstream tasks can test the anchor→verify wiring — Anchor succeeds,
// VerifyInclusion passes on an exact match and fails on any tamper — with zero
// network and byte-stable results. It provides NO real transparency guarantee;
// only RekorWitness does, and only the integration test proves that.
type fakeWitness struct {
	nextIndex int64
}

// NewFakeWitness returns a deterministic, hermetic Witness for tests.
func NewFakeWitness() Witness { return &fakeWitness{} }

// fakeDigest is the deterministic proof/body material for an envelope.
func fakeDigest(dsseEnvelope []byte) []byte {
	sum := sha256.Sum256(dsseEnvelope)
	out := make([]byte, hex.EncodedLen(len(sum)))
	hex.Encode(out, sum[:])
	return out
}

func (f *fakeWitness) Anchor(_ context.Context, dsseEnvelope []byte) (Entry, error) {
	if len(dsseEnvelope) == 0 {
		return Entry{}, errors.New("transparency: cannot anchor an empty envelope")
	}
	digest := fakeDigest(dsseEnvelope)
	idx := f.nextIndex
	f.nextIndex++
	return Entry{
		LogIndex:       idx,
		LogID:          "fake-witness",
		IntegratedTime: 0,
		InclusionProof: digest,
		SET:            nil,
		Body:           digest,
	}, nil
}

func (f *fakeWitness) VerifyInclusion(entry Entry, dsseEnvelope []byte) (bool, string) {
	want := fakeDigest(dsseEnvelope)
	// The proof must equal the recomputed digest (catches a tampered proof)
	// AND the body must match it (catches a tampered envelope).
	if !bytesEqual(entry.InclusionProof, want) {
		return false, "inclusion proof does not match the envelope digest"
	}
	if !bytesEqual(entry.Body, want) {
		return false, "entry body does not wrap the given envelope"
	}
	return true, "fake inclusion verified"
}

// bytesEqual is a small constant-shape comparison used instead of pulling in
// bytes just for Equal; correctness, not timing, matters here.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
