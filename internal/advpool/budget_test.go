// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/repoindex"
)

func sigsOf(cx ...int) []repoindex.Signature {
	out := make([]repoindex.Signature, len(cx))
	for i, c := range cx {
		out[i] = repoindex.Signature{Name: string(rune('a' + i)), Complexity: c, Lines: 3}
	}
	return out
}

// psf/requests, 2026-09-04: api.py is eight one-line wrappers (complexity
// 8) and was planted with 39 mutants — one seat per function, five each —
// then 36 survived and the proof phase timed out. auth.py (complexity 56)
// got 34 and converged with 10 of 10 proven. The exam must scale with what
// there is to get wrong, not with how many names a file has.
func TestPlanShardsBudgetsByComplexityNotSymbolCount(t *testing.T) {
	// api.py's shape: eight trivial functions.
	shards, b := PlanShards(sigsOf(1, 1, 1, 1, 1, 1, 1, 1), RunSpec{MaxShards: 8})
	if b.Rule != BudgetRuleComplexity || b.Complexity != 8 || b.Total != 8 {
		t.Fatalf("budget = %+v, want complexity rule, complexity 8, total 8", b)
	}
	if b.Floor != BudgetFloor || b.Ceiling != BudgetCeiling {
		t.Errorf("budget must disclose its clamp: %+v", b)
	}
	sum := 0
	for _, sh := range shards {
		if sh.Mutants < 1 {
			t.Errorf("shard %d asked for %d mutants; every seat asks for at least one", sh.Index, sh.Mutants)
		}
		sum += sh.Mutants
	}
	if sum != b.Total {
		t.Errorf("shards ask for %d in total, budget says %d", sum, b.Total)
	}
	// Every symbol still lands in exactly one shard: the budget bounds
	// mutants, never coverage of the surface.
	seen := map[string]bool{}
	for _, sh := range shards {
		for _, s := range sh.Symbols {
			seen[s] = true
		}
	}
	if len(seen) != 8 {
		t.Errorf("only %d of 8 symbols are in a shard", len(seen))
	}

	// auth.py's shape: real logic. The ceiling is today's default exam, so a
	// hard file loses nothing.
	_, b = PlanShards(sigsOf(23, 9, 8, 6, 4, 3, 2, 1), RunSpec{MaxShards: 8})
	if b.Complexity != 56 || b.Total != BudgetCeiling {
		t.Errorf("hard file: budget = %+v, want the ceiling %d", b, BudgetCeiling)
	}

	// A trivial file never goes below the floor: five mutants is the least
	// that says anything.
	_, b = PlanShards(sigsOf(1, 1), RunSpec{MaxShards: 8})
	if b.Total != BudgetFloor {
		t.Errorf("trivial file: total = %d, want the floor %d", b.Total, BudgetFloor)
	}
}

// Within a file the budget follows the difficulty too: the shard holding the
// branch-heavy function is asked for more than the shard of getters.
func TestPlanShardsSplitsTheBudgetByShardComplexity(t *testing.T) {
	shards, b := PlanShards(sigsOf(20, 1, 1, 1), RunSpec{MaxShards: 2})
	if len(shards) != 2 || b.Total != 23 {
		t.Fatalf("shards = %+v, budget = %+v", shards, b)
	}
	// hardest-first packing puts 20 alone and 1+1+1 together
	if shards[0].Mutants <= shards[1].Mutants {
		t.Errorf("the hard shard must be asked for more: %+v", shards)
	}
	if shards[0].Mutants+shards[1].Mutants != 23 {
		t.Errorf("split must sum to the budget: %+v", shards)
	}
}

// An operator who names --n-mutants chose the exam by hand: it stays the
// PER-SEAT budget the flag has always documented, and the record says so.
func TestPlanShardsHonoursAnExplicitPerSeatBudget(t *testing.T) {
	shards, b := PlanShards(sigsOf(1, 1, 1, 1, 1, 1, 1, 1), RunSpec{MaxShards: 8, NMutants: 5})
	if b.Rule != BudgetRuleExplicit || b.Total != 40 {
		t.Fatalf("budget = %+v, want explicit, total 40", b)
	}
	for _, sh := range shards {
		if sh.Mutants != 5 {
			t.Errorf("shard %d asked for %d, want the explicit 5", sh.Index, sh.Mutants)
		}
	}
}

// No complexity evidence (an extractor without it, or a file with no named
// symbols) cannot derive a budget: the run falls back to the documented
// default and SAYS it did, rather than deriving a number from zeros.
func TestPlanShardsWithoutComplexityFallsBackAndDiscloses(t *testing.T) {
	shards, b := PlanShards(sigsOf(0, 0, 0), RunSpec{MaxShards: 8})
	if b.Rule != BudgetRuleDefault || b.Complexity != 0 {
		t.Fatalf("budget = %+v, want the default rule with complexity 0", b)
	}
	for _, sh := range shards {
		if sh.Mutants != DefaultNMutants {
			t.Errorf("shard %d asked for %d, want %d", sh.Index, sh.Mutants, DefaultNMutants)
		}
	}
	// Unsharded: no named symbols at all.
	shards, b = PlanShards(nil, RunSpec{MaxShards: 8})
	if shards != nil || b.Rule != BudgetRuleDefault || b.Total != DefaultNMutants {
		t.Errorf("unsharded, no evidence: shards=%v budget=%+v", shards, b)
	}
}

// The plan is deterministic and identical for both callers (BuildDAG and
// the driver's stats seeding), which share the partition by construction.
func TestPlanShardsIsDeterministic(t *testing.T) {
	a, ba := PlanShards(sigsOf(5, 3, 3, 2, 1), RunSpec{MaxShards: 4})
	b, bb := PlanShards(sigsOf(5, 3, 3, 2, 1), RunSpec{MaxShards: 4})
	if !reflect.DeepEqual(a, b) || ba != bb {
		t.Errorf("two plans of the same input differ: %+v / %+v", a, b)
	}
}

// The clock. A measured round cost and a per-file deadline bound what the
// dev pass can grade in time; a derived budget above that is lowered, never
// below the floor, under its own rule name. An explicit budget is the
// operator's — it is marked as not fitting, never changed.
func TestPlanShardsFitsTheBudgetToTheDeadline(t *testing.T) {
	sigs := sigsOf(23, 9, 8, 6, 4, 3, 2, 1) // complexity 56 → ceiling 40
	// 6 trees, 5 minutes per round, 30-minute deadline: half the deadline
	// is 3 rounds → 18 mutants.
	rs := RunSpec{MaxShards: 8, Deadline: 30 * time.Minute, MutantRoundCost: 5 * time.Minute, Concurrency: Concurrency{Trees: 6}}
	shards, b := PlanShards(sigs, rs)
	if b.Rule != BudgetRuleFitted || b.Total != 18 || b.BeforeFit != 40 {
		t.Fatalf("budget = %+v, want fitted to 18 from 40", b)
	}
	if b.DeadlineMillis != 30*60*1000 || b.RoundCostMillis != 5*60*1000 || b.Trees != 6 {
		t.Errorf("a fitted budget must carry what it was fitted with: %+v", b)
	}
	sum := 0
	for _, sh := range shards {
		sum += sh.Mutants
	}
	if sum != 18 {
		t.Errorf("shards ask for %d, want 18", sum)
	}

	// Plenty of time: nothing is cut and the rule stays complexity.
	rs.MutantRoundCost = 30 * time.Second
	_, b = PlanShards(sigs, rs)
	if b.Rule != BudgetRuleComplexity || b.Total != 40 {
		t.Errorf("a budget that fits must not be touched: %+v", b)
	}

	// Never below the floor, even when the clock says one round of one.
	rs = RunSpec{MaxShards: 8, Deadline: time.Minute, MutantRoundCost: 10 * time.Minute, Concurrency: Concurrency{Trees: 1}}
	_, b = PlanShards(sigs, rs)
	if b.Rule != BudgetRuleFitted || b.Total != BudgetFloor {
		t.Errorf("the floor holds under any clock: %+v", b)
	}

	// Explicit: kept, marked.
	rs = RunSpec{MaxShards: 8, NMutants: 5, Deadline: 30 * time.Minute, MutantRoundCost: 5 * time.Minute, Concurrency: Concurrency{Trees: 6}}
	_, b = PlanShards(sigs, rs)
	if b.Rule != BudgetRuleExplicit || b.Total != 40 || !b.Exceeds {
		t.Errorf("an explicit budget is the operator's, marked as not fitting: %+v", b)
	}

	// No measurement: nothing to fit against, and no claim that it fits.
	rs = RunSpec{MaxShards: 8, Deadline: 30 * time.Minute}
	_, b = PlanShards(sigs, rs)
	if b.Rule != BudgetRuleComplexity || b.Exceeds {
		t.Errorf("without a measured round cost the plan must not pretend to have fitted: %+v", b)
	}
}

// A prior is a constraint the generator reads after the goal and after any
// shard aiming, and the verdict says it was given. A spec without one
// renders byte-for-byte as before.
func TestPriorReachesTheGeneratorPromptAndTheVerdict(t *testing.T) {
	sigs := []repoindex.Signature{{Name: "get", Line: 1, Lines: 2, Complexity: 1}, {Name: "post", Line: 4, Lines: 2, Complexity: 1}}
	rs := RunSpec{Goal: "g", CodePath: "api.py", Code: "def get():\n    pass\n\ndef post():\n    pass\n", Lang: "python", MaxShards: 2}
	plain := BuildDAG(rs, RoleAssignment{RoleMutantGenerator: "m", RoleTestWriter: "w", RoleTestCritic: "c"}, sigs)
	rs.Prior = "ALREADY TRIED on this exact version of the file (1 edit(s) from earlier runs). Do NOT repeat these edits: line 2, constant-changed — KILLED by tests/test_api.py::test_get"
	rs.PriorsApplied, rs.PriorDigest, rs.PriorSource = 1, "abc123", "ledger/"
	primed := BuildDAG(rs, RoleAssignment{RoleMutantGenerator: "m", RoleTestWriter: "w", RoleTestCritic: "c"}, sigs)
	seen := 0
	for i, spec := range primed {
		if spec.Role != RoleMutantGenerator {
			if spec.Instruction != plain[i].Instruction {
				t.Errorf("%s's prompt must not change with a prior", spec.Role)
			}
			continue
		}
		seen++
		if !strings.Contains(spec.Instruction, "ALREADY TRIED") || !strings.Contains(spec.Instruction, "KILLED by tests/test_api.py::test_get") {
			t.Errorf("generator seat %s did not receive the prior:\n%s", spec.Key, spec.Instruction)
		}
		if strings.Contains(plain[i].Instruction, "ALREADY TRIED") {
			t.Errorf("an unprimed spec must not mention a prior")
		}
	}
	if seen == 0 {
		t.Fatal("no generator seats rendered")
	}
	v := verdictFromSpec(rs)
	if v.PriorsApplied != 1 || v.PriorDigest != "abc123" || v.PriorSource != "ledger/" {
		t.Errorf("verdict does not carry the prior's disclosure: %+v", v)
	}
}
