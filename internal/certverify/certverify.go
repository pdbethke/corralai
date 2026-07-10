// SPDX-License-Identifier: Elastic-2.0

// Package certverify is the single, shared implementation of corral's
// build-record verification: the same four checks the CLI (`corral certify
// verify`) and, later, the web UI run against a certify record. Extracting
// this out of cmd/corral/verify.go keeps the two surfaces from drifting —
// one verifier, no duplicated check logic.
package certverify

import (
	"crypto/ed25519"
	"encoding/json"

	"github.com/pdbethke/corralai/internal/certify"
	"github.com/pdbethke/corralai/internal/transparency"
)

// Check is the outcome of one of the four checks VerifyRecord runs.
type Check struct {
	// Name identifies the check: "signature", "ledger", "subject", or
	// "rekor".
	Name string
	OK   bool
	// Detail is a human-readable explanation, populated on failure (and, for
	// "rekor", also on a successful anchored verification).
	Detail string
}

// Record is the shape a `corral certify --out` file (and report_build's tool
// response) carries: everything needed to verify a build attestation
// completely offline except the verifying public key, which VerifyRecord
// always takes from an external trust anchor — never from the record.
type Record struct {
	// Statement is kept purely for human readability; verification checks
	// the DSSE envelope's own embedded statement, never this field.
	Statement map[string]any
	// Signature is a DSSE envelope (JSON, as text) that embeds its own copy
	// of the signed statement.
	Signature string
	Steps     []map[string]any
	Head      string
	// Rekor is the marshaled transparency.Entry (JSON), present when
	// Anchored is true.
	Rekor    string
	Anchored bool
}

// VerifyRecord runs the four checks against an EXTERNAL trust anchor (pub +
// w) and returns one Check per check, in order (signature, ledger, subject,
// rekor), plus allOK = every applicable check passed.
//
// pub is the published Ed25519 key the caller obtained out-of-band — never
// derived from rec (a record's own embedded public_key must never be
// trusted to authenticate itself). w is a transparency.Witness (TUF-rooted
// for a real Rekor instance) used for the Rekor inclusion check.
// allowUnanchored, if true, accepts a signed-but-unwitnessed record (a
// materially weaker claim than "publicly witnessed"); if false, an
// unanchored record fails the rekor check.
func VerifyRecord(rec Record, pub ed25519.PublicKey, w transparency.Witness, allowUnanchored bool) (checks []Check, allOK bool) {
	allOK = true

	// Check 1: the DSSE envelope's Ed25519 signature — binds the FULL
	// predicate (repo/commit/command/exit code), not just the head. The
	// envelope (rec.Signature) carries its own embedded copy of the
	// statement it signed; that embedded copy — not rec.Statement, which is
	// kept only for human readability — is what checks 2 and 3 below verify
	// against.
	envelopeStmt, ok, err := certify.VerifyDSSE([]byte(rec.Signature), pub)
	sigCheck := Check{Name: "signature"}
	switch {
	case err != nil:
		sigCheck.Detail = err.Error()
	case !ok:
		sigCheck.Detail = "signature does not verify against the statement"
	default:
		sigCheck.OK = true
	}
	checks = append(checks, sigCheck)
	if !sigCheck.OK {
		allOK = false
		// Checks 2/3 depend on envelopeStmt from a verified envelope; without
		// it, still run check 2 (it only needs rec.Steps/rec.Head) but check
		// 3 has no statement to check the subject digest against.
	}

	// Check 2: the ledger's hash chain recomputes to the recorded head.
	ledgerCheck := Check{Name: "ledger"}
	stepsJSON, err := json.Marshal(rec.Steps)
	if err != nil {
		ledgerCheck.Detail = err.Error()
	} else if steps, err := certify.UnmarshalSteps(stepsJSON); err != nil {
		ledgerCheck.Detail = err.Error()
	} else if ok, msg := certify.VerifyLedger(steps, rec.Head); !ok {
		ledgerCheck.Detail = msg
	} else {
		ledgerCheck.OK = true
	}
	checks = append(checks, ledgerCheck)
	if !ledgerCheck.OK {
		allOK = false
	}

	// Check 3: the statement is bound to THIS exact ledger — its subject
	// digest must equal the ledger head, or a valid statement could be
	// paired with an unrelated (even individually valid) ledger. Checked
	// against envelopeStmt (the envelope's own embedded statement), the same
	// source of truth as check 1 — not rec.Statement.
	subjectCheck := Check{Name: "subject"}
	subjDigest, subjOK := statementSubjectDigest(envelopeStmt)
	if !subjOK || subjDigest != rec.Head {
		subjectCheck.Detail = "statement subject digest " + quote(subjDigest) + " does not match ledger head " + quote(rec.Head)
	} else {
		subjectCheck.OK = true
	}
	checks = append(checks, subjectCheck)
	if !subjectCheck.OK {
		allOK = false
	}

	// Check 4: public transparency. "Signed" (checks 1-3) is a claim about
	// what the brain says; "publicly witnessed" is an independently
	// checkable claim that a third party can confirm without trusting the
	// brain at all. A record that was never anchored is a materially weaker
	// artifact, so it is rejected by default unless allowUnanchored.
	rekorCheck := Check{Name: "rekor"}
	switch {
	case !rec.Anchored:
		rekorCheck.Detail = "signed, NOT publicly witnessed (this build's attestation was never submitted to, or never included in, a public transparency log)"
		rekorCheck.OK = allowUnanchored
	default:
		var entry transparency.Entry
		if err := json.Unmarshal([]byte(rec.Rekor), &entry); err != nil {
			rekorCheck.Detail = "record's transparency entry is malformed: " + err.Error()
		} else if ok, detail := w.VerifyInclusion(entry, []byte(rec.Signature)); !ok {
			rekorCheck.Detail = detail
		} else {
			rekorCheck.OK = true
			rekorCheck.Detail = detail
		}
	}
	checks = append(checks, rekorCheck)
	if !rekorCheck.OK {
		allOK = false
	}

	return checks, allOK
}

// statementSubjectDigest extracts statement.subject[0].digest.sha256 from a
// decoded in-toto statement map.
func statementSubjectDigest(stmt map[string]any) (string, bool) {
	subjects, ok := stmt["subject"].([]any)
	if !ok || len(subjects) == 0 {
		return "", false
	}
	subj, ok := subjects[0].(map[string]any)
	if !ok {
		return "", false
	}
	digest, ok := subj["digest"].(map[string]any)
	if !ok {
		return "", false
	}
	sha, ok := digest["sha256"].(string)
	return sha, ok
}

// quote wraps s in double quotes for a Detail message, mirroring the CLI's
// prior %q formatting.
func quote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `"` + s + `"`
	}
	return string(b)
}
