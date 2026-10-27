// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"testing"

	"github.com/pdbethke/corralai/internal/ledger"
)

// internal/ledger compiles to WASM and cannot import this package, so the
// results page's model comparison names the mutant-generator role by its own
// literal. A rename here would leave that comparison finding no seat and
// reading as "no comparison possible" ([] models) rather than failing; this
// is what fails instead.
func TestLedgerComparesTheMutantGeneratorRole(t *testing.T) {
	if ledger.ComparedRole != RoleMutantGenerator {
		t.Fatalf("ledger.ComparedRole = %q, advpool.RoleMutantGenerator = %q: the results page would compare no seat", ledger.ComparedRole, RoleMutantGenerator)
	}
}
