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
	s := Build(entries, checks, true)
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
