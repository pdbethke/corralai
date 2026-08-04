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
// newWitness) and returns one Check per check, in order (signature, ledger,
// subject, rekor), plus allOK = every applicable check passed.
//
// pub is the published Ed25519 key the caller obtained out-of-band — never
// derived from rec (a record's own embedded public_key must never be
// trusted to authenticate itself). newWitness lazily constructs the
// transparency.Witness (TUF-rooted for a real Rekor instance) used for the
// Rekor inclusion check. It is invoked AT MOST ONCE, and ONLY when checks
// 1-3 (signature, ledger, subject) have ALL passed AND rec.Anchored is
// true — so a locally-invalid anchored record fails fast, entirely
// offline, at the true first-failing check, instead of paying for a
// network round-trip (TUF root fetch + Rekor) whose result can't change
// the outcome. If newWitness errors, the rekor check fails with that error
// as its detail rather than the underlying HTTP/TUF plumbing leaking out
// of VerifyRecord.
// allowUnanchored, if true, accepts a signed-but-unwitnessed record (a
// materially weaker claim than "publicly witnessed"); if false, an
// unanchored record fails the rekor check.
func VerifyRecord(rec Record, pub ed25519.PublicKey, newWitness func() (transparency.Witness, error), allowUnanchored bool) (checks []Check, allOK bool) {
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

	// Check 2b: the record's HUMAN-READABLE statement must be the statement
	// that was actually signed.
	//
	// The envelope carries its own embedded copy, and checks 1/3 use that
	// copy — correctly, because it is the authenticated one. But a published
	// record file also carries rec.Statement, and that is the half a person
	// READS: the repo, the kill rate, which model sat in which seat. Nothing
	// compared the two, so the readable half could be edited to say anything
	// at all and `certify verify` still printed "verified". A reader checks
	// the signature, sees it pass, and then believes doctored numbers — which
	// is a worse outcome than no signature at all, because the checkmark did
	// the convincing.
	//
	// Found while publishing real records for strangers to verify: a record
	// whose model name was changed by hand still passed every check.
	//
	// An ABSENT readable statement is fine (nothing is being claimed to a
	// reader); a PRESENT one that disagrees with the envelope is a hard fail.
	if sigCheck.OK {
		readableCheck := Check{Name: "statement"}
		switch {
		case len(rec.Statement) == 0:
			readableCheck.OK = true // nothing shown to a human; nothing to mislead
		default:
			// SUBSET, not equality: every field the envelope SIGNED must
			// appear unchanged in the readable copy. Extra unsigned keys are
			// tolerated because real consumers legitimately carry them — the
			// cockpit hands this a whole database row (pass, anchored, rekor,
			// steps) alongside the statement fields. What must never pass is a
			// SIGNED value that has been altered, which is the hole this
			// closes.
			bad, err := firstAlteredSignedField(rec.Statement, envelopeStmt)
			switch {
			case err != nil:
				readableCheck.Detail = "statement could not be compared: " + err.Error()
			case bad != "":
				readableCheck.Detail = "the readable statement disagrees with the signature at " + quote(bad) + " — a signed value has been altered"
			default:
				readableCheck.OK = true
			}
		}
		checks = append(checks, readableCheck)
		if !readableCheck.OK {
			allOK = false
		}
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
	//
	// newWitness is called ONLY when we reach here with checks 1-3 all
	// passed AND rec.Anchored — never for an unanchored record (no witness
	// needed) and never when an earlier check already failed (offline
	// fast-fail: no point paying for a network round-trip to confirm a
	// check whose outcome can no longer change allOK).
	rekorCheck := Check{Name: "rekor"}
	switch {
	case !rec.Anchored:
		rekorCheck.Detail = "signed, NOT publicly witnessed (this build's attestation was never submitted to, or never included in, a public transparency log)"
		rekorCheck.OK = allowUnanchored
	case !allOK:
		rekorCheck.Detail = "skipped: an earlier check failed"
	default:
		var entry transparency.Entry
		if err := json.Unmarshal([]byte(rec.Rekor), &entry); err != nil {
			rekorCheck.Detail = "record's transparency entry is malformed: " + err.Error()
		} else if w, werr := newWitness(); werr != nil {
			rekorCheck.Detail = "constructing witness: " + werr.Error()
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

// canonicalJSON renders a decoded JSON value in a stable form so two
// independently-decoded copies of the same statement compare equal regardless
// of the key order they arrived in. encoding/json sorts map keys on marshal,
// which is exactly the property needed here.
func canonicalJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// firstAlteredSignedField reports the first key path where the readable
// statement disagrees with the statement that was actually signed, or "" when
// every signed field is present and identical.
//
// Extra keys in `readable` are allowed (see the call site); missing or changed
// SIGNED keys are not. Comparison is by canonical JSON of each value, so two
// independently-decoded copies compare equal regardless of key order.
func firstAlteredSignedField(readable, signed map[string]any) (string, error) {
	for k, want := range signed {
		got, present := readable[k]
		if !present {
			return k, nil
		}
		wj, err := canonicalJSON(want)
		if err != nil {
			return "", err
		}
		gj, err := canonicalJSON(got)
		if err != nil {
			return "", err
		}
		if wj != gj {
			return k, nil
		}
	}
	return "", nil
}
