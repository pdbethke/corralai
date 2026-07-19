// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
)

// TestMirroredConstantsMatchAdvpool pins the hand-mirrored local constants
// (roleMutantGeneratorShadow / shadowProviderFailedResult, see their doc
// comment in main.go) against their internal/advpool origin
// (RoleMutantGeneratorShadow / ShadowProviderFailedResult).
//
// Nothing else enforces this. The two copies exist because internal/advpool
// pulls in internal/buildstore (DuckDB) as a whole-package dependency, and
// corral-agent must stay duckdb-free — see the note at the bottom of this
// file for why this file's own import of internal/advpool doesn't violate
// that: `go list -deps` (and the CI check that gates on it) only walks the
// non-test package, so a _test.go-only import never reaches the shipped
// binary.
//
// Without this test, a drifted local sentinel leaves ./cmd/corral-agent
// green on its own (its tests reference the local constant and stay
// self-consistent) while silently breaking cross-package behavior: a
// drifted shadowProviderFailedResult is no longer matched by the driver's
// ShadowProviderFailedResult check (internal/advpool/driver.go), falls
// through to the parse-failure branch that sets measured=true, dropped=true,
// and reintroduces exactly the fabricated zero-yield row commit ef3addb
// fixed — in the shared scorecard store that drives model routing.
func TestMirroredConstantsMatchAdvpool(t *testing.T) {
	if roleMutantGeneratorShadow != advpool.RoleMutantGeneratorShadow {
		t.Fatalf("roleMutantGeneratorShadow (%q) has drifted from advpool.RoleMutantGeneratorShadow (%q)",
			roleMutantGeneratorShadow, advpool.RoleMutantGeneratorShadow)
	}
	if shadowProviderFailedResult != advpool.ShadowProviderFailedResult {
		t.Fatalf("shadowProviderFailedResult (%q) has drifted from advpool.ShadowProviderFailedResult (%q)",
			shadowProviderFailedResult, advpool.ShadowProviderFailedResult)
	}
}

// Note: this file's import of internal/advpool (which transitively pulls in
// internal/buildstore's DuckDB driver) does NOT put DuckDB in the shipped
// worker binary — `go list -deps ./cmd/corral-agent` only walks the
// package's non-test dependency graph, and a _test.go-only import never
// joins it. Verified by hand via `go list -deps ./cmd/corral-agent | grep -c
// duckdb` (must print 0) both before and after this file was added.
