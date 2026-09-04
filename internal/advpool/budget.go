// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"fmt"
	"sort"
	"time"

	"github.com/pdbethke/corralai/internal/repoindex"
)

// The mutant budget: how many faults a run asks its generator seats to
// plant, and why that many.
//
// Until this existed the exam was flat: every generator seat asked for
// NMutants (5), and a file got one seat per named symbol up to MaxShards, so
// a file of eight one-line wrappers was planted with as many faults as a
// file of eight branch-heavy functions. On psf/requests (2026-09-04) that
// put 39 mutants into api.py — complexity 8 — of which 36 survived, and the
// proof phase spent its whole budget on them and timed out; auth.py,
// complexity 56, got 34 and converged with every survivor proven. The cost
// of an audit is mutants × suite time, and the flat exam spent most of it
// where there was least to get wrong.
//
// The rule: a file's budget is its summed symbol complexity — roughly one
// fault per decision point — clamped to [BudgetFloor, BudgetCeiling]. The
// ceiling is the pre-budget default exam (DefaultNMutants × DefaultMaxShards),
// so a hard file loses nothing; the floor is the least that says anything.
// Within the file, each shard is asked for its complexity share, at least
// one. The budget bounds MUTANTS, never coverage of the surface: every named
// symbol still lands in exactly one shard (see ShardSymbols).
//
// An explicit --n-mutants is honoured as the PER-SEAT budget it has always
// been documented as — an operator who typed it chose the exam by hand. And
// a file whose signatures carry no complexity (an extractor without it, no
// named symbols) cannot derive anything, so it takes the documented default
// and the record says so, rather than deriving a number from zeros.
//
// Every verdict carries its MutantBudget, because a kill rate over 8 mutants
// and one over 40 are different measurements and must never be compared as
// one.
const (
	// DefaultNMutants is the per-seat budget when --n-mutants is given as 0
	// and no complexity evidence exists, and the unit of the ceiling.
	DefaultNMutants = 5
	// BudgetFloor is the fewest mutants a complexity-derived budget asks for.
	BudgetFloor = 5
	// BudgetCeiling is the most: the pre-budget default exam, so the rule
	// only ever removes mutants from files that had no use for them.
	BudgetCeiling = DefaultNMutants * DefaultMaxShards
	// MutantsPerComplexity is the rate: one fault per decision point.
	MutantsPerComplexity = 1

	BudgetRuleComplexity = "complexity"
	BudgetRuleExplicit   = "explicit"
	BudgetRuleDefault    = "default"
	// BudgetRuleFitted is the complexity rule after the clock cut it: the
	// derived budget would not grade inside the per-file deadline at the
	// measured cost, so it was lowered to what fits. Its own rule name so a
	// row reads as what it is — a smaller exam than the file's complexity
	// asked for, sat because the operator's timeout said so.
	BudgetRuleFitted = "complexity-fitted"

	// fitShare is the fraction of the per-file deadline the DEV pass may
	// plan to spend. The rest is generation, the proof phase and the
	// critic, which a plan cannot price (survivors are unknown until the
	// dev pass runs). Half is a floor for convergence, not a target.
	fitShare = 0.5
)

// MutantBudget is the disclosed budget of one run.
type MutantBudget struct {
	// Total is how many mutants the generator seats were asked for, summed.
	Total int `json:"total"`
	// Rule is BudgetRuleComplexity, BudgetRuleExplicit or BudgetRuleDefault.
	Rule string `json:"rule"`
	// Complexity is the summed complexity of the file's named symbols — the
	// input to the complexity rule, recorded under every rule so a reader
	// can see what the exam would have been. 0 when no evidence existed.
	Complexity int `json:"complexity"`
	// Floor and Ceiling are the clamp the complexity rule applied. Zero
	// under the other rules, which clamp nothing.
	Floor   int `json:"floor,omitempty"`
	Ceiling int `json:"ceiling,omitempty"`
	// PerSeat is the explicit per-seat budget under BudgetRuleExplicit and
	// BudgetRuleDefault; 0 under the complexity rule, whose seats differ.
	PerSeat int `json:"per_seat,omitempty"`
	// Fitted is set under BudgetRuleFitted: BeforeFit is what complexity
	// asked for, DeadlineMillis the per-file timeout it had to fit, and
	// RoundCostMillis the measured cost of one round of Trees mutants the
	// fit divided by. Under every other rule all three are 0. An EXPLICIT
	// budget is never fitted — the operator chose it — but Exceeds says
	// the plan does not fit, so the header can warn before the spend.
	BeforeFit       int   `json:"before_fit,omitempty"`
	DeadlineMillis  int64 `json:"deadline_ms,omitempty"`
	RoundCostMillis int64 `json:"round_cost_ms,omitempty"`
	Trees           int   `json:"trees,omitempty"`
	Exceeds         bool  `json:"exceeds,omitempty"`
}

// fitsInDeadline is how many mutants the dev pass can grade inside
// fitShare of the deadline at the measured round cost: rounds × trees.
// 0 means "no measurement, cannot say".
func fitsInDeadline(rs RunSpec) int {
	if rs.MutantRoundCost <= 0 || rs.Deadline <= 0 {
		return 0
	}
	trees := rs.Concurrency.Trees
	if trees < 1 {
		trees = 1
	}
	rounds := int(float64(rs.Deadline) * fitShare / float64(rs.MutantRoundCost))
	if rounds < 1 {
		rounds = 1
	}
	return rounds * trees
}

// PlanShards is the ONE place a run's generator fan-out and mutant budget
// are decided: it partitions sigs with ShardSymbols and stamps each shard's
// Mutants. BuildDAG and the driver's stats seeding both call it, so the
// partition and the budget are one decision, never two sources of truth.
// nil shards means unsharded (one whole-file seat asking for budget.Total).
func PlanShards(sigs []repoindex.Signature, rs RunSpec) ([]Shard, MutantBudget) {
	complexity := 0
	named := 0
	for _, s := range sigs {
		if s.Name == "" {
			continue
		}
		named++
		complexity += s.Complexity
	}

	var b MutantBudget
	switch {
	case rs.NMutants > 0:
		b = MutantBudget{Rule: BudgetRuleExplicit, Complexity: complexity, PerSeat: rs.NMutants}
	case complexity <= 0:
		b = MutantBudget{Rule: BudgetRuleDefault, Complexity: 0, PerSeat: DefaultNMutants}
	default:
		total := complexity * MutantsPerComplexity
		if total < BudgetFloor {
			total = BudgetFloor
		}
		if total > BudgetCeiling {
			total = BudgetCeiling
		}
		b = MutantBudget{Rule: BudgetRuleComplexity, Complexity: complexity, Total: total, Floor: BudgetFloor, Ceiling: BudgetCeiling}
	}

	// The clock. A measured round cost and a deadline say how many mutants
	// can be graded in time; a derived budget above that is lowered (never
	// below the floor — five mutants that time out are still more honest
	// than a run that plants nothing), and the record says so. An explicit
	// budget is the operator's to keep; it is only marked.
	if capacity := fitsInDeadline(rs); capacity > 0 {
		trees := rs.Concurrency.Trees
		if trees < 1 {
			trees = 1
		}
		switch b.Rule {
		case BudgetRuleComplexity:
			if b.Total > capacity {
				before := b.Total
				total := capacity
				if total < BudgetFloor {
					total = BudgetFloor
				}
				b.Rule, b.BeforeFit, b.Total = BudgetRuleFitted, before, total
				b.DeadlineMillis, b.RoundCostMillis, b.Trees = rs.Deadline.Milliseconds(), rs.MutantRoundCost.Milliseconds(), trees
			}
		case BudgetRuleExplicit:
			seats := rs.MaxShards
			if named := namedCount(sigs); named < seats {
				seats = named
			}
			if seats < 1 {
				seats = 1
			}
			if b.PerSeat*seats > capacity {
				b.Exceeds = true
				b.DeadlineMillis, b.RoundCostMillis, b.Trees = rs.Deadline.Milliseconds(), rs.MutantRoundCost.Milliseconds(), trees
			}
		}
	}

	width := rs.MaxShards
	if (b.Rule == BudgetRuleComplexity || b.Rule == BudgetRuleFitted) && width > b.Total {
		// A seat asked for nothing is a wasted call; a small exam runs on
		// fewer seats, each still aimed at every symbol it holds.
		width = b.Total
	}
	shards := ShardSymbols(sigs, width)
	derived := b.Rule == BudgetRuleComplexity || b.Rule == BudgetRuleFitted
	if shards == nil {
		if !derived {
			b.Total = b.PerSeat
		}
		return nil, b
	}
	if !derived {
		for i := range shards {
			shards[i].Mutants = b.PerSeat
		}
		b.Total = b.PerSeat * len(shards)
		return shards, b
	}
	splitByComplexity(shards, b.Total)
	return shards, b
}

func namedCount(sigs []repoindex.Signature) int {
	n := 0
	for _, s := range sigs {
		if s.Name != "" {
			n++
		}
	}
	return n
}

// splitByComplexity hands total out across shards in proportion to their
// complexity, every shard at least one, by largest remainder — deterministic
// (ties by index), and exact: the shares sum to total.
func splitByComplexity(shards []Shard, total int) {
	n := len(shards)
	if total < n {
		// Cannot happen when PlanShards narrowed the width, but the
		// guarantee "at least one per seat" must not depend on the caller.
		total = n
	}
	weight := 0
	for _, sh := range shards {
		weight += sh.Complexity
	}
	spare := total - n // everything beyond the one each seat is owed
	type rem struct {
		i    int
		frac float64
	}
	rems := make([]rem, n)
	given := 0
	for i, sh := range shards {
		share := 0.0
		if weight > 0 {
			share = float64(spare) * float64(sh.Complexity) / float64(weight)
		}
		whole := int(share)
		shards[i].Mutants = 1 + whole
		given += whole
		rems[i] = rem{i: i, frac: share - float64(whole)}
	}
	sort.SliceStable(rems, func(a, c int) bool { return rems[a].frac > rems[c].frac })
	for k := 0; k < spare-given; k++ {
		shards[rems[k%n].i].Mutants++
	}
}

// String is the one-line disclosure every printer uses, so the header, the
// per-file report line and the ledger reader all say the same thing.
func (b MutantBudget) String() string {
	switch b.Rule {
	case BudgetRuleComplexity:
		return fmt.Sprintf("%d budgeted by complexity (%d decision points; floor %d, ceiling %d)", b.Total, b.Complexity, b.Floor, b.Ceiling)
	case BudgetRuleFitted:
		return fmt.Sprintf("%d — complexity asked for %d (%d decision points), FITTED to the %s per-file timeout: a round of %d mutants measured %s on this file's tests",
			b.Total, b.BeforeFit, b.Complexity, shortDuration(b.DeadlineMillis), b.Trees, shortDuration(b.RoundCostMillis))
	case BudgetRuleExplicit:
		s := fmt.Sprintf("%d — %d per seat, set by --n-mutants (complexity %d)", b.Total, b.PerSeat, b.Complexity)
		if b.Exceeds {
			s += fmt.Sprintf("; WILL NOT FIT the %s per-file timeout at the measured %s per round of %d — expect a timed-out verdict, or lower --n-mutants",
				shortDuration(b.DeadlineMillis), shortDuration(b.RoundCostMillis), b.Trees)
		}
		return s
	case BudgetRuleDefault:
		return fmt.Sprintf("%d — %d per seat, the default (no complexity evidence to derive from)", b.Total, b.PerSeat)
	}
	return ""
}

// shortDuration renders milliseconds the way the timing line does (1m32s).
func shortDuration(ms int64) string {
	return time.Duration(ms * int64(time.Millisecond)).Round(time.Second).String()
}
