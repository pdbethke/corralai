// SPDX-License-Identifier: Elastic-2.0

package certify

import (
	"testing"

	"github.com/pdbethke/corralai/internal/review"
)

// The signed statements carry where each seat was served from beside the
// roster they already sign, and say nothing when nothing was resolved.
func TestAuditStatementSignsTheSeatProviders(t *testing.T) {
	got := BuildAuditAttestation(AuditStatement{
		Repo: "r", Commit: "c",
		ModelsByRole:  map[string]string{"test-writer": "gpt-x", "mutant-generator": "qwen3:8b"},
		SeatProviders: map[string]string{"test-writer": "openai", "mutant-generator": "ollama"},
	})
	pred := got["predicate"].(map[string]any)
	sp, ok := pred["seatProviders"].(map[string]any)
	if !ok || sp["test-writer"] != "openai" || sp["mutant-generator"] != "ollama" {
		t.Fatalf("predicate seatProviders = %#v, want test-writer=openai, mutant-generator=ollama", pred["seatProviders"])
	}
	bare := BuildAuditAttestation(AuditStatement{Repo: "r", Commit: "c"})["predicate"].(map[string]any)
	if v, present := bare["seatProviders"]; present {
		t.Errorf("seatProviders signed as %#v on a scan that resolved none", v)
	}
}

func TestReviewStatementSignsTheSeatProviders(t *testing.T) {
	r := review.Review{Repo: "r", Commit: "c", Scope: "s", ReviewerModel: "gpt-x", VerifierModel: "qwen3:8b",
		SeatProviders: map[string]string{"reviewer": "openai", "verifier": "ollama"}}
	pred := BuildReviewAttestation(r)["predicate"].(map[string]any)
	sp, ok := pred["seatProviders"].(map[string]any)
	if !ok || sp["reviewer"] != "openai" || sp["verifier"] != "ollama" {
		t.Fatalf("predicate seatProviders = %#v, want reviewer=openai, verifier=ollama", pred["seatProviders"])
	}
	bare := BuildReviewAttestation(review.Review{Repo: "r", Commit: "c", Scope: "s", ReviewerModel: "m"})["predicate"].(map[string]any)
	if v, present := bare["seatProviders"]; present {
		t.Errorf("seatProviders signed as %#v on a review that recorded none", v)
	}
}
