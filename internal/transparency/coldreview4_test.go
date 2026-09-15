// SPDX-License-Identifier: Elastic-2.0

package transparency

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/go-openapi/strfmt"
	"github.com/sigstore/rekor/pkg/generated/models"
)

// Round four of the cold review, 2026-09-13 (reviewer antigravity, verifier
// codex). Six findings, all standing: R1 high; R2, R3, R5 medium; R4, R6 low.
// R1, R4 and R5 were defects in round three's fix. R2 and R3 predate every
// round (2026-07-10); R6 predates round two (2026-08-31).
//
// The shape, again, is a rule at one door and not its sibling: three separate
// envelope parsers each with its own idea of "valid" (R3, R4), a map-order
// pick fixed in rekor.go and left in logger.go twelve lines from code that
// was edited the same day (R6), and a check I added in front of the one it
// should have followed (R5).

// TestSignatureSetMustBeEqualNotASubset is R1 — the high one.
//
// THE DEFECT: for an entry that records signatures but no envelope hash
// (intoto v0.0.2), the binding checked that every signature the envelope
// CARRIES was logged — a subset test. An envelope with its signatures
// STRIPPED down to one of several, or with its payloadType rewritten, passed
// as "the logged envelope". Rekor never saw either. The binding must be
// equality on the signature set, and the payload type must match when the
// log recorded one.
func TestSignatureSetMustBeEqualNotASubset(t *testing.T) {
	a := base64.StdEncoding.EncodeToString([]byte("signature a"))
	b := base64.StdEncoding.EncodeToString([]byte("signature b"))
	intoto := []byte(`{"kind":"intoto","apiVersion":"0.0.2","spec":{"content":{"envelope":{"payloadType":"application/vnd.in-toto+json","signatures":[{"sig":"` + a + `"},{"sig":"` + b + `"}]}}}}`)

	whole := []byte(`{"payloadType":"application/vnd.in-toto+json","payload":"cA==","signatures":[{"sig":"` + a + `"},{"sig":"` + b + `"}]}`)
	if got, why := bindEntryToEnvelope(intoto, whole); got != bindOK {
		t.Fatalf("the envelope exactly as logged was refused: %v (%s)", got, why)
	}

	for _, tc := range []struct{ name, env string }{
		{"one signature stripped", `{"payloadType":"application/vnd.in-toto+json","payload":"cA==","signatures":[{"sig":"` + a + `"}]}`},
		{"payloadType rewritten", `{"payloadType":"application/vnd.something-else","payload":"cA==","signatures":[{"sig":"` + a + `"},{"sig":"` + b + `"}]}`},
		{"payloadType removed", `{"payload":"cA==","signatures":[{"sig":"` + a + `"},{"sig":"` + b + `"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, why := bindEntryToEnvelope(intoto, []byte(tc.env))
			if got != bindMismatch {
				t.Errorf("bind = %v (%s), want bindMismatch — Rekor never logged this envelope", got, why)
			}
		})
	}
}

// TestAnchorRefusesAnEntryWithNoSET is R2.
//
// THE DEFECT: VerifyInclusion requires a SET (round three's R1), but toEntry —
// the door that BUILDS an Entry from Rekor's response — did not, so Anchor
// could hand back an Entry that its own verifier would then refuse. The rule
// belongs at the constructor as much as at the checker.
func TestAnchorRefusesAnEntryWithNoSET(t *testing.T) {
	w := &rekorWitness{}
	idx, tm := int64(7), int64(1700000000)
	logID := "00"
	le := models.LogEntryAnon{
		Body:           base64.StdEncoding.EncodeToString([]byte(`{"kind":"dsse"}`)),
		LogIndex:       &idx,
		LogID:          &logID,
		IntegratedTime: &tm,
		Verification: &models.LogEntryAnonVerification{
			InclusionProof:       &models.InclusionProof{},
			SignedEntryTimestamp: strfmt.Base64(nil), // Rekor said nothing
		},
	}
	if _, err := w.toEntry(le); err == nil {
		t.Fatal("toEntry built an Entry with no SET — VerifyInclusion refuses exactly that, so Anchor would return an entry that can never verify")
	} else if !strings.Contains(err.Error(), "signed entry timestamp") {
		t.Errorf("error %q does not say the SET is missing", err)
	}
}

// TestAMissingPayloadIsMalformedNotHashed is R3.
//
// THE DEFECT: envelopePayloadSHA256 hashed a MISSING or empty payload to
// sha256("") without an error, so an envelope with no payload field was
// "malformed" to one parser and "hashes fine" to another. An envelope with no
// payload is not a DSSE envelope; it is refused as attacker input.
func TestAMissingPayloadIsMalformedNotHashed(t *testing.T) {
	for _, tc := range []struct{ name, env string }{
		{"payload omitted", `{"signatures":[{"sig":"QUFB"}]}`},
		{"payload empty", `{"payload":"","signatures":[{"sig":"QUFB"}]}`},
		{"payload not a string", `{"payload":7,"signatures":[{"sig":"QUFB"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := envelopePayloadSHA256([]byte(tc.env)); err == nil {
				t.Errorf("hashed an envelope with no usable payload instead of refusing it")
			}
		})
	}
}

// TestASignatureWithNoSigIsMalformed is R4.
//
// THE DEFECT: envelopeSignatures accepted a signature object with a missing
// or empty `sig` as a valid empty signature, so the binding reported
// bindMismatch ("a signature it carries was never logged") for what is a
// malformed envelope. The two outcomes are kept distinct on purpose — one
// says "tampered", the other says "unreadable input" — and this one was
// filed under the wrong label.
func TestASignatureWithNoSigIsMalformed(t *testing.T) {
	logged := base64.StdEncoding.EncodeToString([]byte("logged"))
	body := []byte(`{"kind":"dsse","spec":{"signatures":[{"signature":"` + logged + `"}]}}`)
	for _, tc := range []struct{ name, env string }{
		{"sig omitted", `{"payload":"cA==","signatures":[{"keyid":"k"}]}`},
		{"sig empty", `{"payload":"cA==","signatures":[{"sig":""}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, why := bindEntryToEnvelope(body, []byte(tc.env))
			if got != bindMalformedEnvelope {
				t.Errorf("bind = %v (%s), want bindMalformedEnvelope", got, why)
			}
		})
	}
}

// TestEnvelopeParsersAgree is the control behind R3 and R4: there is ONE
// envelope parser now, so no two doors can disagree about whether an envelope
// is readable. Every malformed envelope above must be malformed to both the
// hash door and the signature door.
func TestEnvelopeParsersAgree(t *testing.T) {
	for _, env := range []string{
		`{"signatures":[{"sig":"QUFB"}]}`,
		`{"payload":"","signatures":[{"sig":"QUFB"}]}`,
		`{"payload":"cA==","signatures":[{"keyid":"k"}]}`,
		`{"payload":"cA==","signatures":[{"sig":""}]}`,
		`{"payload":"cA==","signatures":[{"sig":123}]}`,
		`{"payload":"cA=="}`,
	} {
		_, hashErr := envelopePayloadSHA256([]byte(env))
		_, sigOK := envelopeSignatures([]byte(env))
		if (hashErr == nil) != sigOK {
			t.Errorf("%s: the hash door says malformed=%v and the signature door says malformed=%v — two parsers, two rules", env, hashErr != nil, !sigOK)
		}
	}
}

// TestIndexMismatchIsReportedBeforeEntryKind is R5 — a defect I introduced
// while fixing round three's R8.
//
// THE DEFECT: Get checked "is this a dsse entry" BEFORE "is this the index I
// asked for", so when Rekor returned an entry from a different index the
// error blamed the entry's kind ("not a dsse entry") instead of the index. A
// misleading error created while fixing a misleading error.
func TestIndexMismatchIsReportedBeforeEntryKind(t *testing.T) {
	other := int64(5)
	tm := int64(1700000000)
	payload := models.LogEntry{
		"u1": {LogIndex: &other, IntegratedTime: &tm, Body: base64.StdEncoding.EncodeToString([]byte(`{"kind":"hashedrekord","spec":{}}`))},
	}
	_, err := logEntryFromResponse(payload, 9)
	if err == nil {
		t.Fatal("accepted an entry from index 5 when index 9 was asked for")
	}
	if !strings.Contains(err.Error(), "asked for log index 9") || strings.Contains(err.Error(), "not a dsse entry") {
		t.Errorf("error %q blames the entry kind; the real problem is the index mismatch", err)
	}
}

// TestGetRefusesToGuessAmongSeveralEntries is R6 — the map-order pick that
// round three fixed in rekor.go (fetchEntryByUUID) and that sat unchanged in
// logger.go, in code edited the same day. Both doors now call soleEntry.
func TestGetRefusesToGuessAmongSeveralEntries(t *testing.T) {
	idx := int64(9)
	tm := int64(1700000000)
	body := base64.StdEncoding.EncodeToString([]byte(`{"kind":"dsse","spec":{"envelopeHash":{"value":"ab"}}}`))
	two := models.LogEntry{
		"u1": {LogIndex: &idx, IntegratedTime: &tm, Body: body},
		"u2": {LogIndex: &idx, IntegratedTime: &tm, Body: body},
	}
	if _, err := logEntryFromResponse(two, 9); err == nil {
		t.Fatal("picked one of two entries in map order instead of refusing")
	}
	if _, err := logEntryFromResponse(models.LogEntry{}, 9); err == nil {
		t.Fatal("an empty response was not an error")
	}
	one := models.LogEntry{"u1": {LogIndex: &idx, IntegratedTime: &tm, Body: body}}
	got, err := logEntryFromResponse(one, 9)
	if err != nil {
		t.Fatalf("the one-entry response was refused: %v", err)
	}
	if got.UUID != "u1" || got.EnvelopeSHA256 != "ab" {
		t.Errorf("got %+v", got)
	}

	// The same helper is the one fetchEntryByUUID relies on, so the two doors
	// cannot drift: a multi-entry response with no key match is an error there
	// too, and a single unkeyed entry is accepted.
	if _, _, err := soleEntry(two); !errors.Is(err, errSeveralEntries) {
		t.Errorf("soleEntry on two entries: err = %v, want errSeveralEntries", err)
	}
	if u, _, err := soleEntry(one); err != nil || u != "u1" {
		t.Errorf("soleEntry on one entry: %q, %v", u, err)
	}
}
