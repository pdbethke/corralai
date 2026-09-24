// SPDX-License-Identifier: Elastic-2.0

package transparency

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/go-openapi/strfmt"
	"github.com/sigstore/rekor/pkg/generated/models"
)

// A review of main at 6951ca4c, 2026-09-15 (reviewer codex:gpt-6-astra,
// verifier claude-code:claude-fable-5-1), entry a2a3af29d0c0 on the ledger.

// TestAnchorRefusesAnEmptyInclusionProof is R1.
//
// THE DEFECT: toEntry checked only that the inclusion proof was non-nil, so
// an empty proof — RootHash, TreeSize, LogIndex, Hashes all absent — became
// an Entry, Anchor returned success, and the build recorded anchored=true
// for an entry VerifyInclusion rejects as incomplete. The rule "the proof is
// complete" was held at the door that CHECKS an entry and not at the door
// that BUILDS one; round four fixed the same shape for the SET.
func TestAnchorRefusesAnEmptyInclusionProof(t *testing.T) {
	w := &rekorWitness{}
	idx, tm := int64(7), int64(1700000000)
	logID := "00"
	le := models.LogEntryAnon{
		Body:           base64.StdEncoding.EncodeToString([]byte(`{"kind":"dsse"}`)),
		LogIndex:       &idx,
		LogID:          &logID,
		IntegratedTime: &tm,
		Verification: &models.LogEntryAnonVerification{
			InclusionProof:       &models.InclusionProof{}, // present, and empty
			SignedEntryTimestamp: strfmt.Base64([]byte("a set is present")),
		},
	}
	if _, err := w.toEntry(le); err == nil {
		t.Fatal("toEntry built an Entry from an empty inclusion proof; Anchor would report anchored=true for an entry VerifyInclusion refuses")
	} else if !strings.Contains(err.Error(), "inclusion proof") {
		t.Errorf("error %q does not name the inclusion proof", err)
	}
}
