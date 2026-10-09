// SPDX-License-Identifier: Elastic-2.0

package certify

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"math"
)

// SignedBuild is a build record turned into its signed, self-verifying parts.
// Anchoring and storage are the caller's business; this is only the part that
// has to be byte-identical wherever a build is signed.
type SignedBuild struct {
	Steps []map[string]any // MarshalSteps(built), decoded
	// StepsJSON is MarshalSteps(built) verbatim — what the brain persists. It
	// is carried rather than re-marshaled from Steps because a map re-encodes
	// with its keys sorted, which need not match the bytes MarshalSteps wrote.
	StepsJSON []byte
	Head      string
	Statement map[string]any
	Envelope  []byte // DSSE
	Canonical []byte // CanonicalStatement(Statement)
}

// SignBuild is the one build-signing recipe: steps -> BuildLedger ->
// BuildAttestation -> SignDSSE -> CanonicalStatement -> MarshalSteps.
//
// It used to be written out twice, in cmd/corral (key id "corral-certify")
// and internal/brain (key id "brain"), under a comment promising the two
// "must produce byte-identical records". A promise two copies make to each
// other is only as good as the next edit to one of them; one function keeps
// it by construction. rec.Actor is the principal named in the ledger steps.
//
// rawDurationS is the duration as both callers hold it — a document field
// where 0 means "not given". SignBuild normalizes it ONCE, with
// SecondsOrUnmeasured, and both the statement and the execution step carry
// the result: a measured duration on both, an unmeasured one on neither.
// The step used to carry the raw value, so the hash-linked record the
// statement's subject digest commits to still signed "duration_s": 0 after
// the statement stopped (review f5015cf48e54#R1). SignBuild sets rec.DurationS
// itself so the two cannot disagree. A value JSON cannot carry (NaN, ±Inf) is
// refused, not left to panic inside BuildLedger (f5015cf48e54#R4).
func SignBuild(rec BuildRecord, rawDurationS float64, priv ed25519.PrivateKey, keyID string) (SignedBuild, error) {
	if math.IsNaN(rawDurationS) || math.IsInf(rawDurationS, 0) {
		return SignedBuild{}, fmt.Errorf("duration %v is not a number a signed record can carry", rawDurationS)
	}
	rec.DurationS = SecondsOrUnmeasured(rawDurationS)
	execution := map[string]any{
		"exit_code":     rec.ExitCode,
		"ok":            rec.ExitCode == 0,
		"output_digest": rec.OutputDigest,
	}
	if rec.DurationS != nil {
		execution["duration_s"] = *rec.DurationS
	}
	steps := []Step{
		{
			Kind:    "context",
			Actor:   rec.Actor,
			Subject: rec.Repo + "@" + rec.Commit,
			Detail: map[string]any{
				"repo":   rec.Repo,
				"commit": rec.Commit,
				"branch": rec.Branch,
			},
		},
		{
			Kind:    "execution",
			Actor:   rec.Actor,
			Subject: rec.Command,
			Detail:  execution,
		},
	}
	built, head := BuildLedger(steps)
	stmt := BuildAttestation(rec, head)

	// Sign the FULL canonical statement (not just the head) as a DSSE
	// envelope: a head-only signature leaves the predicate
	// (repo/commit/command/exit code) freely editable in storage
	// without invalidating the signature. The envelope embeds its
	// own copy of the canonical statement bytes it signed, so a
	// later VerifyDSSE call checks the identical bytes the
	// signature covers with no separate canonical-bytes column to
	// keep in sync.
	envelope, err := SignDSSE(stmt, priv, keyID)
	if err != nil {
		return SignedBuild{}, fmt.Errorf("signing statement: %w", err)
	}
	canonical, err := CanonicalStatement(stmt)
	if err != nil {
		return SignedBuild{}, fmt.Errorf("canonicalizing statement: %w", err)
	}
	stepsJSON, err := MarshalSteps(built)
	if err != nil {
		return SignedBuild{}, fmt.Errorf("marshaling steps: %w", err)
	}
	var stepsOut []map[string]any
	if err := json.Unmarshal(stepsJSON, &stepsOut); err != nil {
		return SignedBuild{}, fmt.Errorf("decoding steps: %w", err)
	}
	return SignedBuild{Steps: stepsOut, StepsJSON: stepsJSON, Head: head, Statement: stmt, Envelope: envelope, Canonical: canonical}, nil
}
