// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"strconv"
	"testing"

	"github.com/pdbethke/corralai/internal/ledger"
)

// The proof floor above the running generation would mean no run corral can
// produce today qualifies as proof of anything.
func TestProofFloorNeverAboveVerdictGeneration(t *testing.T) {
	floor, err := strconv.Atoi(ledger.ProofFloor)
	if err != nil {
		t.Fatal(err)
	}
	gen, err := strconv.Atoi(VerdictGeneration)
	if err != nil {
		t.Fatal(err)
	}
	if floor > gen {
		t.Fatalf("ledger.ProofFloor %d > VerdictGeneration %d", floor, gen)
	}
}
