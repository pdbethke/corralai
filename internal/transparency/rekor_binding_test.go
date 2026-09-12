// SPDX-License-Identifier: Elastic-2.0

package transparency

import (
	"encoding/base64"
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
			ok, detail := w.VerifyInclusion(Entry{LogID: "00", InclusionProof: []byte(tc.proof)}, []byte(`{}`))
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

// TestLoggedSignaturesCover is R2: the payload hash alone does not bind an
// entry to an envelope.
//
// THE DEFECT: step 3 compared only the DSSE payload hash, while the comment
// above it claimed the check confirms the entry "wraps THIS envelope". It did
// not — nothing compared the logged signatures — so a replacement envelope
// signed by a DIFFERENT key over the same payload reused the original
// inclusion proof, with its own signature never having been logged. The
// verifier narrowed the practical harm (certverify checks a pinned Ed25519 key
// first) but could not refute the claim, and any other caller inherits it.
//
// The body shape here is copied from corral's OWN entry in the public log,
// Rekor index 2759598612: kind dsse, apiVersion 0.0.1, spec.signatures[] with
// base64 `signature` and `verifier`.
func TestLoggedSignaturesCover(t *testing.T) {
	sigA := base64.StdEncoding.EncodeToString([]byte("signature-from-the-real-key"))
	sigB := base64.StdEncoding.EncodeToString([]byte("signature-from-a-different-key"))

	body := func(sigs ...string) []byte {
		type s struct {
			Signature string `json:"signature"`
		}
		var list []s
		for _, x := range sigs {
			list = append(list, s{x})
		}
		b, err := json.Marshal(map[string]any{
			"apiVersion": "0.0.1",
			"kind":       "dsse",
			"spec":       map[string]any{"signatures": list},
		})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	env := func(sigs ...string) []byte {
		type s struct {
			Sig string `json:"sig"`
		}
		var list []s
		for _, x := range sigs {
			list = append(list, s{x})
		}
		b, err := json.Marshal(map[string]any{"payload": "cGF5bG9hZA==", "signatures": list})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	for _, tc := range []struct {
		name        string
		body, env   []byte
		wantChecked bool
		wantCovered bool
		why         string
	}{
		{
			name: "THE ATTACK: same payload, a signature that was never logged",
			body: body(sigA), env: env(sigB),
			wantChecked: true, wantCovered: false,
			why: "this is R2 — it must be refused, and before the fix it was accepted",
		},
		{
			name: "the honest case: the signature that was logged",
			body: body(sigA), env: env(sigA),
			wantChecked: true, wantCovered: true,
			why: "a real record must keep verifying — an over-strict fix is worse than the finding",
		},
		{
			name: "a signature ADDED after anchoring",
			body: body(sigA), env: env(sigA, sigB),
			wantChecked: true, wantCovered: false,
			why: "the log vouches for sigA only; sigB rides along unlogged",
		},
		{
			name:        "URL-safe base64 on one side",
			body:        body(base64.StdEncoding.EncodeToString([]byte{0xfb, 0xff, 0xbf})),
			env:         env(base64.URLEncoding.EncodeToString([]byte{0xfb, 0xff, 0xbf})),
			wantChecked: true, wantCovered: true,
			why: "the same bytes in the other alphabet must not read as a mismatch",
		},
		{
			name:        "an entry body shape with no signature set (intoto, or a future kind)",
			body:        []byte(`{"apiVersion":"0.0.2","kind":"intoto","spec":{"content":{}}}`),
			env:         env(sigA),
			wantChecked: false,
			why:         "NOT refused — checked=false, so the caller discloses a weaker binding instead of breaking a legitimate entry kind",
		},
		{
			name: "a body that is not JSON at all",
			body: []byte("\x00\x01not json"), env: env(sigA),
			wantChecked: false,
			why:         "unreadable is not forged: the body is already covered by the Merkle proof",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checked, covered := loggedSignaturesCover(tc.body, tc.env)
			if checked != tc.wantChecked {
				t.Errorf("checked = %v, want %v — %s", checked, tc.wantChecked, tc.why)
			}
			if tc.wantChecked && covered != tc.wantCovered {
				t.Errorf("covered = %v, want %v — %s", covered, tc.wantCovered, tc.why)
			}
		})
	}
}

// TestLoggedSignaturesCoverIsNotVacuous is the negative control for R2's fix.
// The comparison must be able to say NO: if some edit made it always report
// covered (or always report checked=false, which the caller treats as "do not
// refuse"), every case above would still pass while the hole was wide open.
func TestLoggedSignaturesCoverIsNotVacuous(t *testing.T) {
	logged := base64.StdEncoding.EncodeToString([]byte("logged"))
	forged := base64.StdEncoding.EncodeToString([]byte("forged"))
	body := []byte(`{"kind":"dsse","spec":{"signatures":[{"signature":"` + logged + `"}]}}`)

	checked, covered := loggedSignaturesCover(body, []byte(`{"signatures":[{"sig":"`+forged+`"}]}`))
	if !checked {
		t.Fatal("checked=false on a well-formed dsse body — the caller would NOT refuse, so the fix is inert")
	}
	if covered {
		t.Fatal("an unlogged signature reported as covered — the fix is inert")
	}
	okChecked, okCovered := loggedSignaturesCover(body, []byte(`{"signatures":[{"sig":"`+logged+`"}]}`))
	if !okChecked || !okCovered {
		t.Fatalf("the logged signature was not recognized (checked=%v covered=%v) — the comparison cannot tell yes from no", okChecked, okCovered)
	}
}
