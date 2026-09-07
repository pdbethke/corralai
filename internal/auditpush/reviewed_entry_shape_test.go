// SPDX-License-Identifier: Elastic-2.0

package auditpush

import (
	"compress/gzip"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/review"
)

// A Codex reviewer's reproduction (review 559719120ef0#R1, Claude Code
// verifying), inverted: an adjudication with no finding id, an empty
// verdict and nobody deciding was placed, signed and VERIFIED when it
// entered through AppendLedgerEntry (`corral ledger append`) instead of
// WriteAdjudication. One shape rule now stands at both doors: the writer
// refuses to place it, and a chain check names it when it is already there.
func TestMalformedAdjudicationIsRefusedAtBothDoors(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer := Ed25519LedgerSigner{KeyID: "repro", Key: priv}
	dir := t.TempDir()
	if _, err := WriteReview(dir, review.Review{Commit: "abc", Scope: "internal/auditpush"}, signer); err != nil {
		t.Fatal(err)
	}
	entries, _ := ReadLedgerDir(dir)
	malformed := LedgerEntry{Kind: KindAdjudication, Adjudication: &Adjudication{Adjudicates: entries[0].Hash}}
	// The writer's door.
	if _, err := AppendLedgerEntry(dir, malformed, signer); err == nil || !strings.Contains(err.Error(), "refusing to place") {
		t.Fatalf("append placed a malformed adjudication: %v", err)
	}
	// The verifier's door: the entry written by hand, hash and signature
	// correct, as an older binary or a foreign writer could have done.
	malformed.Format, malformed.Prev, malformed.Pushed = LedgerFileFormat, entries[0].Hash, time.Now().UTC().Truncate(time.Microsecond)
	h, err := EntryHash(malformed)
	if err != nil {
		t.Fatal(err)
	}
	malformed.Hash = h
	raw, _ := hex.DecodeString(h)
	malformed.KeyID, malformed.Signature = "repro", hex.EncodeToString(ed25519.Sign(priv, raw))
	js, _ := json.Marshal(malformed)
	f, err := os.Create(filepath.Join(dir, ScansSubdir, "20991231T235959Z-adjudication-byhand.json.gz"))
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(f)
	zw.Write(js) //nolint:errcheck
	zw.Close()
	f.Close()
	checks, err := VerifyLedgerDir(dir, pub)
	if err != nil || len(checks) != 2 {
		t.Fatalf("verify: %d %v", len(checks), err)
	}
	got := checks[1]
	if !got.HashOK || !got.LinkOK || !got.SigOK {
		t.Fatalf("fixture: the hand-written entry must be hash-, link- and signature-correct: %+v", got)
	}
	if !strings.Contains(got.Problem, "names a finding as") {
		t.Fatalf("a signed, well-linked, malformed adjudication passed verification: %+v", got)
	}
	// And every other kind has a shape.
	for _, e := range []LedgerEntry{
		{Kind: KindRetract, Retracts: "x"},
		{Kind: KindReview, Review: &review.Review{Commit: "abc"}},
		{Kind: KindAdjudication, Adjudication: &Adjudication{Adjudicates: entries[0].Hash + "#R1", Verdict: "maybe", By: "p", Reason: "r"}},
		{Kind: KindAdjudication, Adjudication: &Adjudication{Adjudicates: entries[0].Hash + "#R1", Verdict: VerdictConfirmed}},
		{Kind: "lesson"},
	} {
		if EntryShapeProblem(e) == "" {
			t.Errorf("no shape problem for %+v", e)
		}
	}
}
