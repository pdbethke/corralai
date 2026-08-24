// SPDX-License-Identifier: Elastic-2.0

package advpool

import "testing"

// The shadow writer is a DISTINCT role key so tasksByRole(RoleTestWriter)
// structurally cannot return it. The exclusion is the gate; a boolean someone
// must remember to check at each call site would be the wrong mechanism —
// the same reasoning already written for RoleMutantGeneratorShadow.
func TestShadowWriterIsADistinctRoleKey(t *testing.T) {
	if RoleTestWriterShadow == RoleTestWriter {
		t.Fatal("shadow writer shares the primary's role key — tasksByRole could return a measurement seat as a graded one")
	}
	if RoleTestWriterShadow != "test-writer-shadow" {
		t.Errorf("RoleTestWriterShadow = %q, want \"test-writer-shadow\" — the wire name appears in mutant_attempts.role and in ledger rows", RoleTestWriterShadow)
	}
}
