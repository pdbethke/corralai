// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"strconv"
	"strings"
	"testing"

	golang "github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/queue"
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
		Lang:        "go",
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
	goP, _ := golang.ByName("go")
	wantSystem, wantUser := testgen.GenerateMutantsPrompt(goP.MutantSystem(), rs.Goal, rs.Code, sigs, rs.NMutants)
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
	// The critic must be GROUNDED in the code under review, not just the tests —
	// otherwise it speculates about the API and files false positives against
	// valid tests (the tabulate() false-positive). It must also be calibrated
	// to only flag the demonstrably vacuous.
	if !strings.Contains(critic.Instruction, rs.Code) {
		t.Fatalf("test-critic instruction must include the code under review (grounding against API-guess false positives):\n%s", critic.Instruction)
	}
	if !strings.Contains(critic.Instruction, "DEMONSTRABLY vacuous") {
		t.Fatalf("test-critic instruction must be calibrated to flag ONLY the demonstrably vacuous:\n%s", critic.Instruction)
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

func mutantSpecs(specs []queue.TaskSpec) []queue.TaskSpec {
	var out []queue.TaskSpec
	for _, s := range specs {
		if s.Role == RoleMutantGenerator {
			out = append(out, s)
		}
	}
	return out
}

func TestBuildDAGUnshardedIsByteIdenticalPrompt(t *testing.T) {
	rs := RunSpec{Goal: "g", CodePath: "a.go", Code: "package p\nfunc A() {}\n", Lang: "go"}
	sigs := []repoindex.Signature{{Name: "A", Complexity: 1, Lines: 1}}
	assign := RoleAssignment{RoleMutantGenerator: "m", RoleTestWriter: "w", RoleTestCritic: "c"}

	// MaxShards unset => unsharded.
	got := mutantSpecs(BuildDAG(rs, assign, sigs))
	if len(got) != 1 {
		t.Fatalf("want 1 mutant-generator spec, got %d", len(got))
	}
	if got[0].Key != RoleMutantGenerator {
		t.Errorf("key: want %q, got %q", RoleMutantGenerator, got[0].Key)
	}
	want := renderMutantGenerator(rs, sigs, nil)
	if got[0].Instruction != want {
		t.Errorf("unsharded instruction must be byte-identical to renderMutantGenerator\nwant:\n%s\ngot:\n%s", want, got[0].Instruction)
	}
}

func TestBuildDAGShardedEmitsOneSpecPerShard(t *testing.T) {
	rs := RunSpec{
		Goal: "g", CodePath: "a.go", Lang: "go", MaxShards: 3,
		Code: "package p\nfunc A() {}\nfunc B() {}\nfunc C() {}\n",
	}
	sigs := []repoindex.Signature{
		{Name: "A", Complexity: 5, Lines: 10},
		{Name: "B", Complexity: 3, Lines: 6},
		{Name: "C", Complexity: 1, Lines: 2},
	}
	assign := RoleAssignment{RoleMutantGenerator: "m", RoleTestWriter: "w", RoleTestCritic: "c"}
	got := mutantSpecs(BuildDAG(rs, assign, sigs))
	if len(got) != 3 {
		t.Fatalf("want 3 mutant-generator specs, got %d", len(got))
	}
	keys := map[string]bool{}
	for i, s := range got {
		keys[s.Key] = true
		if s.Role != RoleMutantGenerator {
			t.Errorf("spec[%d].Role: want %q, got %q", i, RoleMutantGenerator, s.Role)
		}
		if s.Model != "m" {
			t.Errorf("spec[%d].Model: want %q, got %q", i, "m", s.Model)
		}
		if len(s.DependsOn) != 0 {
			t.Errorf("spec[%d].DependsOn: want none, got %v", i, s.DependsOn)
		}
		// Whole file is still the context — sharding changes aim, not context.
		if !strings.Contains(s.Instruction, rs.Code) {
			t.Errorf("spec[%d] must carry the whole file as context", i)
		}
	}
	for i := 0; i < 3; i++ {
		if !keys[ShardTaskKey(i)] {
			t.Errorf("missing shard key %q (got %v)", ShardTaskKey(i), keys)
		}
	}
	// Every symbol is aimed at by exactly one shard.
	for _, name := range []string{"A", "B", "C"} {
		hits := 0
		for _, s := range got {
			if strings.Contains(s.Title, name) {
				hits++
			}
		}
		if hits != 1 {
			t.Errorf("symbol %q aimed at by %d shards, want 1", name, hits)
		}
	}
}

func TestBuildDAGSameNamedMethodsOnDifferentReceiversStayDistinct(t *testing.T) {
	// (*Engine).String and (*Store).String share a bare Name of "String". If
	// shard packing/matching used the bare name, both signatures could leak
	// into both shards' filtered signature list and aiming directive, making
	// "ATTACK ONLY THESE FUNCTIONS: String" ambiguous about which one a seat
	// owns. Force them into separate shards (maxShards == symbol count) and
	// assert each shard's rendered instruction names only its own method.
	rs := RunSpec{
		Goal: "g", CodePath: "a.go", Lang: "go", MaxShards: 2,
		Code: "package p\ntype Engine struct{}\nfunc (*Engine) String() string { return \"e\" }\ntype Store struct{}\nfunc (*Store) String() string { return \"s\" }\n",
	}
	sigs := []repoindex.Signature{
		{Name: "String", Receiver: "*Engine", Complexity: 1, Lines: 1},
		{Name: "String", Receiver: "*Store", Complexity: 1, Lines: 1},
	}
	assign := RoleAssignment{RoleMutantGenerator: "m", RoleTestWriter: "w", RoleTestCritic: "c"}
	got := mutantSpecs(BuildDAG(rs, assign, sigs))
	if len(got) != 2 {
		t.Fatalf("want 2 mutant-generator specs, got %d", len(got))
	}
	for i, s := range got {
		hasEngine := strings.Contains(s.Instruction, "*Engine.String")
		hasStore := strings.Contains(s.Instruction, "*Store.String")
		if hasEngine == hasStore {
			t.Errorf("spec[%d] instruction must name exactly one of *Engine.String / *Store.String, got hasEngine=%v hasStore=%v:\n%s", i, hasEngine, hasStore, s.Instruction)
		}
	}
}

func TestShardTaskKeyRoundTrip(t *testing.T) {
	if got := ShardTaskKey(3); got != "mutant-generator/3" {
		t.Errorf("ShardTaskKey(3): want %q, got %q", "mutant-generator/3", got)
	}
	if idx, sharded := ShardIndexFromKey("mutant-generator/3"); !sharded || idx != 3 {
		t.Errorf("ShardIndexFromKey: want (3,true), got (%d,%v)", idx, sharded)
	}
	if idx, sharded := ShardIndexFromKey(RoleMutantGenerator); sharded || idx != 0 {
		t.Errorf("unsharded key: want (0,false), got (%d,%v)", idx, sharded)
	}
	if _, sharded := ShardIndexFromKey("mutant-generator/bogus"); sharded {
		t.Error("malformed shard key must report unsharded, not panic")
	}
}

// TestShadowShardTaskKeyRoundTrip covers the CHALLENGER key pair, which had no
// direct test at all. The ok return is load-bearing: the driver's shadow pass
// uses it to decide whether a seat has a real shard index, and a silently
// discarded false there collapses that seat onto shard 0 — attributing one
// region's difficulty control to another.
func TestShadowShardTaskKeyRoundTrip(t *testing.T) {
	for _, idx := range []int{0, 1, 7, 10, 42} {
		key := ShadowShardTaskKey(idx)
		want := "mutant-generator-shadow/" + strconv.Itoa(idx)
		if key != want {
			t.Errorf("ShadowShardTaskKey(%d): want %q, got %q", idx, want, key)
		}
		got, ok := ShadowShardIndexFromKey(key)
		if !ok || got != idx {
			t.Errorf("ShadowShardIndexFromKey(%q): want (%d,true), got (%d,%v)", key, idx, got, ok)
		}
	}
	// A PRIMARY key is not a shadow key: the two roles must not be
	// interchangeable through these parsers, or the structural exclusion that
	// keeps shadow mutants out of the exam stops being structural.
	if idx, ok := ShadowShardIndexFromKey(ShardTaskKey(2)); ok || idx != 0 {
		t.Errorf("a primary shard key must not parse as a shadow key, got (%d,%v)", idx, ok)
	}
	for _, bad := range []string{
		RoleMutantGeneratorShadow,       // bare role, no index
		"mutant-generator-shadow/",      // empty index
		"mutant-generator-shadow/bogus", // non-numeric
		"mutant-generator-shadow/-1",    // negative
		"mutant-generator-shadowX/1",    // prefix is not the role
		"test-writer/1",                 // another role entirely
		"",                              // empty
		"mutant-generator-shadow/1/2",   // trailing garbage
		" mutant-generator-shadow/1",    // leading space
		"MUTANT-GENERATOR-SHADOW/1",     // wrong case
	} {
		if idx, ok := ShadowShardIndexFromKey(bad); ok || idx != 0 {
			t.Errorf("ShadowShardIndexFromKey(%q): want (0,false), got (%d,%v)", bad, idx, ok)
		}
	}
}
