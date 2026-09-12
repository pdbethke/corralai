// SPDX-License-Identifier: Elastic-2.0

package transparency

import (
	"encoding/json"
	"strings"
	"testing"
)

// jsonStr quotes a string as a JSON literal, so the fixtures above can embed a
// multi-line checkpoint without hand-escaping it.
func jsonStr(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// Both tests here close findings from the cold review of 2026-09-12
// (reviewer codex, verifier claude-code), and both are written so that
// removing the fix makes them FAIL — the negative control is the point, after
// a credential-scrub test in this repo derived its expectations from the list
// it was testing and passed over nothing.

// TestVerifyInclusionRejectsAnIncompleteProof is R1, which the reviewer
// reproduced with a script that recovered a nil-pointer panic.
//
// THE DEFECT: `{}` unmarshals into a zero models.InclusionProof — RootHash,
// TreeSize, LogIndex, Hashes and Checkpoint all nil — and
// tle.GenerateTransparencyLogEntry dereferences RootHash. VerifyInclusion
// therefore PANICKED on a malformed `rekor` field instead of returning
// (false, reason), and no caller recovers: the only recover() in the tree is
// in internal/mission, and certverify.go calls this directly. A record with a
// corrupt proof took `certify verify` down rather than failing its rekor check.
//
// The cases are deliberately a SET rather than the single `{}` the reviewer
// found: the fix validates the model's own required fields, so a proof missing
// any one of them must be refused, not just the one that happened to panic.
// realProofCheckpoint stands in for the checkpoint Rekor actually returns —
// origin, tree size, root hash, then a signature block. Its exact contents do
// not matter to the required-field validation; its PRESENCE does, and every
// real proof has one (verified against Rekor index 2759598612, where it is 220
// bytes). Rekor's own schema marks it required and step 1 verifies its
// signature, so refusing a proof without one is the contract, not strictness.
const realProofCheckpoint = "rekor.sigstore.dev - 1193050959916656506\n" +
	"123456\nDEADBEEF/base64roothash=\n\n— rekor.sigstore.dev sig+base64==\n"

func TestVerifyInclusionRejectsAnIncompleteProof(t *testing.T) {
	// A structurally valid hash, so a case fails for the field under test and
	// not for the shape of its neighbours.
	h := strings.Repeat("ab", 32)
	cp := realProofCheckpoint

	for _, tc := range []struct {
		name  string
		proof string
	}{
		{"empty object — the reproduced case", `{}`},
		{"no root hash", `{"treeSize":10,"logIndex":1,"hashes":["` + h + `"],"checkpoint":` + jsonStr(cp) + `}`},
		{"no tree size", `{"rootHash":"` + h + `","logIndex":1,"hashes":["` + h + `"],"checkpoint":` + jsonStr(cp) + `}`},
		{"no log index", `{"rootHash":"` + h + `","treeSize":10,"hashes":["` + h + `"],"checkpoint":` + jsonStr(cp) + `}`},
		{"no hashes", `{"rootHash":"` + h + `","treeSize":10,"logIndex":1,"checkpoint":` + jsonStr(cp) + `}`},
		// A proof missing ONLY the checkpoint is refused too, and that is
		// correct rather than strict: tlog.VerifyInclusion in step 1 verifies
		// the checkpoint's signature, so a proof without one could never have
		// passed anyway — it would just fail later and less legibly.
		{"no checkpoint", `{"rootHash":"` + h + `","treeSize":10,"logIndex":1,"hashes":["` + h + `"]}`},
		{"null literal", `null`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &rekorWitness{}
			// Recover so a regression is reported as a FAILING TEST naming the
			// panic, rather than taking the whole package's test binary down
			// and reading as an unrelated crash.
			defer func() {
				if p := recover(); p != nil {
					t.Fatalf("VerifyInclusion PANICKED on proof %s: %v — it must return (false, reason)", tc.proof, p)
				}
			}()
			// A SET is supplied because it is now REQUIRED and checked
			// first (round three, R1); without one these cases would be
			// refused for the missing SET before the proof is ever read,
			// and this test would silently stop testing the proof.
			ok, detail := w.VerifyInclusion(Entry{
				LogID:          "00",
				InclusionProof: []byte(tc.proof),
				SET:            []byte("a signed entry timestamp"),
			}, []byte(`{}`))
			if ok {
				t.Fatalf("accepted an incomplete inclusion proof %s", tc.proof)
			}
			if !strings.Contains(detail, "incomplete") && !strings.Contains(detail, "not well-formed") {
				t.Errorf("reason %q names neither an incomplete nor a malformed proof — a reader cannot tell why it failed", detail)
			}
		})
	}
}

// TestVerifyInclusionRejectsAnIncompleteProofFailsWithoutTheGuard is the
// NEGATIVE CONTROL for the test above: it asserts the property the guard adds
// is actually absent from the unguarded path, by calling the validation the
// fix relies on and confirming it rejects `{}`. If a future edit makes
// Validate() permissive, this fails and the test above stops meaning anything.
func TestProofValidationItselfRejectsTheZeroValue(t *testing.T) {
	if _, err := parseInclusionProof([]byte(`{}`)); err == nil {
		t.Fatal("the model's own Validate() accepted a zero InclusionProof — the guard in VerifyInclusion is then vacuous, and R1 is open again")
	}
	// This fixture was WRONG on first write: it omitted the checkpoint, the
	// control failed, and the failure was the fixture's rather than the
	// guard's — Rekor always returns a checkpoint and its schema requires one.
	// Keeping the note because a control that fires is only useful if you then
	// work out WHICH side is broken.
	h := strings.Repeat("ab", 32)
	complete := `{"rootHash":"` + h + `","treeSize":10,"logIndex":1,"hashes":["` + h +
		`"],"checkpoint":` + jsonStr(realProofCheckpoint) + `}`
	if _, err := parseInclusionProof([]byte(complete)); err != nil {
		t.Fatalf("a COMPLETE proof was rejected (%v) — the guard is over-strict and would refuse good records", err)
	}
}

// The round-two tests that stood here exercised loggedSignaturesCover, whose
// contract round three refuted: it funnelled a MALFORMED ENVELOPE — attacker
// input — into the same "disclose, do not refuse" path as an unreadable log
// body. Tests that assert a wrong contract are worse than no tests, so they
// are replaced rather than patched. See coldreview3_test.go in this package.
