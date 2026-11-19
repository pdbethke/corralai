// SPDX-License-Identifier: Elastic-2.0

package recordview

import (
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/ledger"
	"github.com/pdbethke/corralai/internal/review"
)

// A review that recorded where its seats ran is labelled by that record, not
// by its model names: gpt-oss on a local daemon is not an OpenAI seat, and a
// hosted model under an unrecognised name is still the vendor it ran on. An
// entry written before the field existed keeps the name-based label.
func TestReviewVendorsPreferTheRecordedProvider(t *testing.T) {
	at := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	recorded := auditpush.LedgerEntry{Hash: "aa11", Kind: auditpush.KindReview, Pushed: at,
		Review: &review.Review{Scope: "s", ReviewerModel: "gpt-oss:20b", VerifierModel: "house-model",
			SeatProviders: map[string]string{"reviewer": "ollama", "verifier": "anthropic"}}}
	old := auditpush.LedgerEntry{Hash: "bb22", Kind: auditpush.KindReview, Pushed: at.Add(-time.Hour),
		Review: &review.Review{Scope: "s", ReviewerModel: "gemini-3.6-flash", VerifierModel: "claude-sonnet-5"}}
	site, _ := renderAll(t, []auditpush.LedgerEntry{recorded, old})
	got := map[string][2]string{}
	for _, r := range site.Reviews {
		got[r.Review.ReviewerModel] = [2]string{r.ReviewerVendor, r.VerifierVendor}
	}
	if g := got["gpt-oss:20b"]; g != [2]string{"ollama", "anthropic"} {
		t.Errorf("recorded entry: vendors %v, want the recorded providers [ollama anthropic]", g)
	}
	if g := got["gemini-3.6-flash"]; g != [2]string{"google", "anthropic"} {
		t.Errorf("pre-field entry: vendors %v, want the name-based labels [google anthropic]", g)
	}
	// The record's own tier table labels the field, so the page and the
	// record-fields doc say what it is.
	if tier, ok := ledger.TierOf("Review", "SeatProviders"); !ok || tier != ledger.TierMeasured {
		t.Errorf("Review.SeatProviders tier = %q (%v), want measured", tier, ok)
	}
}
