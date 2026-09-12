// SPDX-License-Identifier: Elastic-2.0

package transparency

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

// Round three of the cold review, 2026-09-12 (reviewer claude-code, verifier
// antigravity). Eight findings, all standing. R1 was high and PRE-EXISTING
// since 2026-07-10; R2 and R4 were defects in the round-two fix.

// TestSETIsRequired is R1 — the high one, and the only finding in this round
// the verifier said certverify does NOT blunt.
//
// THE DEFECT: the Signed Entry Timestamp was checked only when present. The
// Merkle proof in step 1 covers the leaf and the IN-TREE index — not the
// global LogIndex and not IntegratedTime. So anyone holding a real record
// could delete the SET, set the integrated time to any date and the index to
// any number, and VerifyInclusion still returned ok=true. `certify verify`
// then printed the invented values as "verified (publicly witnessed <time>,
// Rekor #N)". The TUF key validity-window check lives only inside VerifySET,
// so it was skipped too.
//
// Requiring a SET rejects nothing legitimate: Rekor returns one on every
// entry (checked against this project's own, log index 2759598612, 96 bytes).
func TestSETIsRequired(t *testing.T) {
	w := &rekorWitness{}
	h := strings.Repeat("ab", 32)
	proof := `{"rootHash":"` + h + `","treeSize":10,"logIndex":1,"hashes":["` + h + `"],"checkpoint":"cp\n"}`

	ok, detail := w.VerifyInclusion(Entry{
		LogID:          "00",
		LogIndex:       999999,
		IntegratedTime: 4102444800, // 2100-01-01, an invented date
		InclusionProof: []byte(proof),
		SET:            nil, // deleted
	}, []byte(`{"payload":"cGF5bG9hZA==","signatures":[{"sig":"AAAA"}]}`))

	if ok {
		t.Fatal("accepted an entry with no SET — its log index and integrated time are unauthenticated, and the CLI prints them as publicly witnessed")
	}
	if !strings.Contains(detail, "signed entry timestamp") && !strings.Contains(detail, "witnessed") {
		t.Errorf("reason %q does not say the SET was missing, so a reader cannot tell why", detail)
	}
}

// TestMalformedEnvelopeIsRefusedNotDisclosed is R2 — a defect in my own
// round-two fix.
//
// THE DEFECT: the signature binding fell back to "disclose, do not refuse"
// whenever a parse failed. That was justified by pointing at unreadable entry
// bodies on the LOG side, which is legitimate — but the same fallback fired
// when the parse failed on the ENVELOPE, which the attacker controls. One
// non-string `sig` element, or no signatures at all, turned a refused
// never-logged signature into ok=true, and the reason string then claimed
// "unrecognized entry body shape" about a body that had read perfectly.
func TestMalformedEnvelopeIsRefusedNotDisclosed(t *testing.T) {
	logged := base64.StdEncoding.EncodeToString([]byte("the signature that was logged"))
	body := []byte(`{"kind":"dsse","spec":{"signatures":[{"signature":"` + logged + `"}]}}`)

	for _, tc := range []struct{ name, env string }{
		{"a non-string sig element", `{"payload":"cA==","signatures":[{"sig":123}]}`},
		{"no signatures at all", `{"payload":"cA==","signatures":[]}`},
		{"no signatures key", `{"payload":"cA=="}`},
		{"not JSON", `}{`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := bindEntryToEnvelope(body, []byte(tc.env))
			if got != bindMalformedEnvelope {
				t.Errorf("bind = %v (%s), want bindMalformedEnvelope — the envelope is attacker input, and anything but a refusal here is a fail-open", got, reason)
			}
		})
	}
}

// TestEnvelopeHashIsCompared is R3, which also refuted my stated reason for
// not comparing it.
//
// THE DEFECT: the hash Rekor logged over the envelope was never checked, so an
// envelope Rekor never saw — a changed payloadType, say — passed as "wraps the
// given envelope". I had argued a hash comparison was byte-identity and would
// break on re-serialization. The reviewer read Rekor's source and showed it
// hashes the bytes AS SUBMITTED, and brain/buildcert.go passes one `envelope`
// variable to both Anchor and Save, so the stored bytes ARE the submitted
// bytes. The rationale was wrong.
func TestEnvelopeHashIsCompared(t *testing.T) {
	env := []byte(`{"payloadType":"application/vnd.in-toto+json","payload":"cGF5bG9hZA==","signatures":[{"sig":"QUFB"}]}`)
	sum := sha256.Sum256(env)
	body := func(hash string) []byte {
		b, _ := json.Marshal(map[string]any{"kind": "dsse", "spec": map[string]any{
			"envelopeHash": map[string]string{"algorithm": "sha256", "value": hash},
		}})
		return b
	}

	if got, why := bindEntryToEnvelope(body(hex.EncodeToString(sum[:])), env); got != bindOK {
		t.Errorf("the exact logged envelope was not accepted: %v (%s) — an over-strict binding is worse than the finding", got, why)
	}
	// A DIFFERENT envelope over the same payload: previously accepted.
	other := []byte(`{"payloadType":"application/something-else","payload":"cGF5bG9hZA==","signatures":[{"sig":"QUFB"}]}`)
	if got, _ := bindEntryToEnvelope(body(hex.EncodeToString(sum[:])), other); got != bindMismatch {
		t.Errorf("bind = %v, want bindMismatch — an envelope Rekor never saw passed as the one it logged", got)
	}
}

// TestIntotoSignaturesAreCompared is R4.
//
// THE DEFECT: sigstore-go returns a payload hash for intoto v0.0.2 as well as
// dsse, so an intoto entry passed step 3 — and step 4 then read only
// spec.signatures, found nothing, and skipped the check entirely. But intoto
// bodies DO record their signatures, at spec.content.envelope.signatures. So
// the never-logged-signature hole stayed open for any intoto entry logged over
// the same payload, and the stated reason for skipping was false for that kind.
func TestIntotoSignaturesAreCompared(t *testing.T) {
	logged := base64.StdEncoding.EncodeToString([]byte("logged sig"))
	forged := base64.StdEncoding.EncodeToString([]byte("forged sig"))
	intoto := []byte(`{"kind":"intoto","apiVersion":"0.0.2","spec":{"content":{"envelope":{"signatures":[{"sig":"` + logged + `"}]}}}}`)

	if got, why := bindEntryToEnvelope(intoto, []byte(`{"signatures":[{"sig":"`+logged+`"}]}`)); got != bindOK {
		t.Errorf("the logged intoto signature was not accepted: %v (%s)", got, why)
	}
	if got, _ := bindEntryToEnvelope(intoto, []byte(`{"signatures":[{"sig":"`+forged+`"}]}`)); got != bindMismatch {
		t.Errorf("bind = %v, want bindMismatch — an unlogged signature verified against an intoto entry", got)
	}
}

// TestOnlyAnUnreadableLogBodyDiscloses is the control that keeps the R2 fix
// from becoming over-strict: a log body this build genuinely cannot read must
// still disclose rather than refuse, because the body is already covered by
// the Merkle proof and refusing would break legitimate entry kinds.
func TestOnlyAnUnreadableLogBodyDiscloses(t *testing.T) {
	env := []byte(`{"payload":"cA==","signatures":[{"sig":"QUFB"}]}`)
	for _, body := range []string{
		`{"kind":"future","apiVersion":"9.9.9","spec":{"somethingElse":true}}`,
		"\x00\x01not json",
	} {
		got, why := bindEntryToEnvelope([]byte(body), env)
		if got != bindNotComparable {
			t.Errorf("bind = %v for an unreadable LOG body (%s), want bindNotComparable — refusing here breaks entry kinds we simply do not know", got, why)
		}
	}
}
