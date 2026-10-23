// SPDX-License-Identifier: Elastic-2.0

package recordview

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/review"
)

func reviewEntry(hash string, exit int) auditpush.LedgerEntry {
	return auditpush.LedgerEntry{
		Hash: hash, Kind: auditpush.KindReview,
		Pushed: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		Review: &review.Review{Scope: "internal/x", ReviewerModel: "m1", Findings: []review.Finding{
			{ID: "R1", Claim: "it is wrong", Script: "exit 0", ExitCode: &exit},
		}},
	}
}

func adjudicationEntry(hash, ref, verdict string) auditpush.LedgerEntry {
	return auditpush.LedgerEntry{Hash: hash, Kind: auditpush.KindAdjudication,
		Pushed:       time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC),
		Adjudication: &auditpush.Adjudication{Adjudicates: ref, Verdict: verdict, By: "peter", Reason: "checked"}}
}

func retractEntry(hash, target string) auditpush.LedgerEntry {
	return auditpush.LedgerEntry{Hash: hash, Kind: auditpush.KindRetract, Retracts: target, Reason: "wrong scope",
		Pushed: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)}
}

func renderAll(t *testing.T, entries []auditpush.LedgerEntry) (Site, string) {
	t.Helper()
	var checks []auditpush.ChainCheck
	for i := range entries {
		entries[i].File = entries[i].Hash + ".json.gz"
		checks = append(checks, okCheck(entries[i]))
	}
	s := Build(entries, checks)
	out := t.TempDir()
	if err := WriteStatic(out, s); err != nil {
		t.Fatal(err)
	}
	return s, out
}

func TestARetractedReviewSaysSoAndLeavesTheTotals(t *testing.T) {
	s, out := renderAll(t, []auditpush.LedgerEntry{
		reviewEntry("aaaaaaaaaaaa", 0),
		adjudicationEntry("bbbbbbbbbbbb", "aaaaaaaaaaaa#R1", auditpush.VerdictConfirmed),
		retractEntry("cccccccccccc", "aaaaaaaaaaaa"),
	})
	if s.Findings != 0 || s.Reproduced != 0 || s.Confirmed != 0 {
		t.Fatalf("a retracted review still counts: findings=%d reproduced=%d confirmed=%d", s.Findings, s.Reproduced, s.Confirmed)
	}
	page := read(t, filepath.Join(out, "entry", "aaaaaaaaaaaa.html"))
	if !strings.Contains(page, "This review was retracted") || !strings.Contains(page, "wrong scope") {
		t.Fatal("the retracted review's page does not say it was retracted, and why")
	}
	idx := read(t, filepath.Join(out, "index.html"))
	if !strings.Contains(idx, `entry/aaaaaaaaaaaa.html`) || !strings.Contains(idx, "retracted") {
		t.Fatal("the index must still link the retracted review, marked")
	}
}

func TestARetractedVerdictNoLongerStands(t *testing.T) {
	s, out := renderAll(t, []auditpush.LedgerEntry{
		reviewEntry("aaaaaaaaaaaa", 0),
		adjudicationEntry("bbbbbbbbbbbb", "aaaaaaaaaaaa#R1", auditpush.VerdictConfirmed),
		retractEntry("cccccccccccc", "bbbbbbbbbbbb"),
	})
	if s.Confirmed != 0 {
		t.Fatalf("a retracted adjudication still counts as confirmed (%d)", s.Confirmed)
	}
	if strings.Contains(read(t, filepath.Join(out, "entry", "aaaaaaaaaaaa.html")), "confirmed by a person") {
		t.Fatal("a retracted adjudication still shows on the finding")
	}
}

// The chain lists every entry (it IS the chain), so an entry that does not
// stand must say so there, of every kind: the retracted review, and the
// adjudication that went with it. The retraction is ADDED to the
// verifier's note, never written over it (review 786e8a0c675e#R2): that
// note is what qualifies the signature chip beside it. Ported from
// corral ui's TestUIHonoursARetractedReview when its page was retired.
func TestTheChainMarksWhatDoesNotStandBesideTheVerifierNote(t *testing.T) {
	entries := []auditpush.LedgerEntry{
		reviewEntry("aaaaaaaaaaaa", 0),
		adjudicationEntry("bbbbbbbbbbbb", "aaaaaaaaaaaa#R1", auditpush.VerdictConfirmed),
		retractEntry("cccccccccccc", "aaaaaaaaaaaa"),
	}
	var checks []auditpush.ChainCheck
	for i := range entries {
		entries[i].File = entries[i].Hash + ".json.gz"
		checks = append(checks, okCheck(entries[i]))
	}
	checks[0].Note = "signer name is self-reported"
	s := Build(entries, checks)
	notes := map[string]string{}
	for _, c := range s.Chain {
		notes[c.Hash] = c.Note
	}
	if n := notes["aaaaaaaaaaaa"]; !strings.Contains(n, "RETRACTED: wrong scope") || !strings.Contains(n, "signer name is self-reported") {
		t.Fatalf("the retracted review's chain note = %q, want the retraction beside the verifier's note", n)
	}
	if n := notes["bbbbbbbbbbbb"]; !strings.Contains(n, "RETRACTED with the review it adjudicates") {
		t.Fatalf("the adjudication of a retracted review reads as standing in the chain: %q", n)
	}
	if n := notes["cccccccccccc"]; strings.Contains(n, "RETRACTED") {
		t.Fatalf("the retraction itself stands: %q", n)
	}
	// The review's own page already says it was retracted in its banner;
	// the chain note must not leak into it a second time.
	for _, r := range s.Reviews {
		if strings.Contains(r.Note, "RETRACTED") {
			t.Fatalf("the chain's retraction note leaked onto the review view: %q", r.Note)
		}
	}
}
