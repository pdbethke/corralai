// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/repoindex"
	"github.com/pdbethke/corralai/internal/testgen"
)

func testRunSpec() RunSpec {
	return RunSpec{
		Repo:        "example/repo",
		Commit:      "deadbeef",
		Goal:        "passwords >= 12 chars",
		CodePath:    "target.go",
		Code:        "package target\nfunc ValidatePassword(pw string) error { return nil }",
		DevTestPath: "target_test.go",
		DevTestCode: "package target\nfunc TestAlwaysPasses(t *testing.T) {}",
		TestCmd:     "go test ./...",
		NMutants:    3,
	}
}

func TestBuildDAG(t *testing.T) {
	rs := testRunSpec()
	assign := RoleAssignment{
		RoleMutantGenerator: "B",
		RoleTestCritic:      "C",
		RoleTestWriter:      "A",
	}
	var sigs []repoindex.Signature

	specs := BuildDAG(rs, assign, sigs)

	byKey := make(map[string]int)
	for i, s := range specs {
		byKey[s.Key] = i
	}

	mutGen := specs[byKey[RoleMutantGenerator]]
	if len(mutGen.DependsOn) != 0 {
		t.Fatalf("mutant-generator should have no deps, got %v", mutGen.DependsOn)
	}
	if mutGen.Model != "B" {
		t.Fatalf("mutant-generator model = %q, want B", mutGen.Model)
	}
	if mutGen.Verify != "" {
		t.Fatalf("mutant-generator must not have Verify set (structured fast path), got %q", mutGen.Verify)
	}
	wantSystem, wantUser := testgen.GenerateMutantsPrompt(rs.Goal, rs.Code, sigs, rs.NMutants)
	if !strings.Contains(mutGen.Instruction, wantSystem) || !strings.Contains(mutGen.Instruction, wantUser) {
		t.Fatalf("mutant-generator instruction missing GenerateMutants prompt text:\n%s", mutGen.Instruction)
	}

	critic := specs[byKey[RoleTestCritic]]
	if len(critic.DependsOn) != 0 {
		t.Fatalf("test-critic should have no deps, got %v", critic.DependsOn)
	}
	if critic.Model != "C" {
		t.Fatalf("test-critic model = %q, want C", critic.Model)
	}
	if !strings.Contains(critic.Instruction, rs.DevTestCode) {
		t.Fatalf("test-critic instruction does not reference the dev's tests:\n%s", critic.Instruction)
	}

	writer := specs[byKey[RoleTestWriter]]
	if len(writer.DependsOn) != 1 || writer.DependsOn[0] != DevAdequacyKey {
		t.Fatalf("test-writer DependsOn = %v, want [%q]", writer.DependsOn, DevAdequacyKey)
	}
	if writer.Model != "A" {
		t.Fatalf("test-writer model = %q, want A", writer.Model)
	}
	if writer.Verify != "" {
		t.Fatalf("test-writer must not have Verify set (structured fast path), got %q", writer.Verify)
	}
}

func TestRoles(t *testing.T) {
	roles := Roles()
	if len(roles) != 3 {
		t.Fatalf("Roles() returned %d roles, want 3", len(roles))
	}
	byName := make(map[string]Role, len(roles))
	for _, r := range roles {
		byName[r.Name] = r
	}
	if !byName[RoleMutantGenerator].Structured {
		t.Fatal("mutant-generator must be structured")
	}
	if !byName[RoleTestWriter].Structured {
		t.Fatal("test-writer must be structured")
	}
	if byName[RoleTestCritic].Structured {
		t.Fatal("test-critic must be freeform")
	}
	if len(byName[RoleMutantGenerator].Deps) != 0 {
		t.Fatal("mutant-generator must have no deps")
	}
	if len(byName[RoleTestCritic].Deps) != 0 {
		t.Fatal("test-critic must have no deps")
	}
	if got := byName[RoleTestWriter].Deps; len(got) != 1 || got[0] != DevAdequacyKey {
		t.Fatalf("test-writer deps = %v, want [%q]", got, DevAdequacyKey)
	}
}
