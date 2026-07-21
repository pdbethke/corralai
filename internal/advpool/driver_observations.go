// SPDX-License-Identifier: Elastic-2.0

package advpool

import "sort"

// bugCatchObservations derives each seat's execution-proven contribution
// from the run state + the signed verdict. Catches = ProvenMissed only — no
// claim/self-report path may reach it.
func bugCatchObservations(run *runState, v Verdict) []BugCatchObservation {
	var out []BugCatchObservation
	// test-writer: the execution-proven catcher.
	authored, sound := 0, 0
	if !run.testWriterMoot {
		authored = 1
		if run.poolScored && run.authoredTest != "" { // compiled + scored ⇒ a valid discriminating test
			sound = 1
		}
	}
	out = append(out, BugCatchObservation{
		Model: v.ModelsByRole[RoleTestWriter], Role: RoleTestWriter,
		Catches: v.ProvenMissed, Opportunities: v.Survivors,
		AuthoredTests: authored, SoundTests: sound,
	})
	// test-critic: theater-detection (judgement, lower-confidence).
	out = append(out, BugCatchObservation{
		Model: v.ModelsByRole[RoleTestCritic], Role: RoleTestCritic,
		CriticFlags: len(v.VacuousFindings),
	})
	// mutant-generator: one row PER SHARD. Never summed — see shardStat.
	if len(run.shardStats) == 0 {
		out = append(out, BugCatchObservation{
			Model: v.ModelsByRole[RoleMutantGenerator], Role: RoleMutantGenerator,
			MutantsPlanted: v.MutantsTotal, MutantsSurvived: v.Survivors,
			TestComplexity: run.testComplexity,
		})
	} else {
		// MutantsSurvived is measured against the MERGED mutant set (Scorer.Score
		// runs once over the union of every shard's mutants — see
		// tickDevAdequacy) — there is no sound way to attribute which shard's
		// mutants specifically survived, so it CANNOT be split per shard without
		// inventing a false per-shard attribution. Record v.Survivors on exactly
		// ONE row — the lowest NON-DROPPED shard index, never just the lowest
		// index — so the run-level aggregate (SUM(mutants_survived) for this
		// role) stays exact; every other shard row carries 0. A dropped seat
		// never ran (it exhausted its retry budget before contributing any
		// mutants), so parking the run's survivor count there would produce an
		// internally incoherent row (planted=0, survived>0) AND make the
		// natural analytical filter "exclude shards that never ran" silently
		// zero the run's adversary-potency aggregate. This is always safe: a
		// run where every shard dropped produces zero mutants and errors out
		// (see the len(mutants)==0 guard in tickDevAdequacy) before ever
		// reaching a verdict, so there is always at least one non-dropped shard
		// here. Do NOT "fix" this into an even/proportional split across
		// shards — that would be a fabricated number, not a measured one.
		survivorIdx := -1
		for _, i := range sortedShardIndexes(run.shardStats) {
			if !run.shardStats[i].dropped {
				survivorIdx = i
				break
			}
		}
		for _, i := range sortedShardIndexes(run.shardStats) {
			st := run.shardStats[i]
			obs := BugCatchObservation{
				Model: v.ModelsByRole[RoleMutantGenerator], Role: RoleMutantGenerator,
				MutantsPlanted:   st.mutants,
				Shard:            i,
				Region:           st.region,
				RegionComplexity: st.complexity,
				RegionLines:      st.lines,
				TestComplexity:   run.testComplexity,
				ParseRetries:     st.parseRetries,
				Dropped:          st.dropped,
			}
			if i == survivorIdx {
				obs.MutantsSurvived = v.Survivors
			}
			out = append(out, obs)
		}
	}
	// The challenger's paired rows (Task 6): one row per shard, SAME region as
	// its primary counterpart, flagged Shadow so the scorecard can tell them
	// apart. Empty (no-op) when no shadow run was configured — shadowStats is
	// only ever seeded alongside shardStats.
	for _, i := range sortedShardIndexes(run.shadowStats) {
		st := run.shadowStats[i]
		if !st.measured {
			// The seat never produced an observation (unfinished, scoring
			// failed, or skipped by the shadow budget guard). Recording it
			// would enter mutants_planted=0 for a model that was never asked
			// the question — a fabricated comparison. See shardStat.measured.
			continue
		}
		out = append(out, BugCatchObservation{
			Model: run.rs.ShadowModel, Role: RoleMutantGeneratorShadow,
			MutantsPlanted: st.mutants, MutantsSurvived: st.survived,
			Shard: i, Region: st.region,
			RegionComplexity: st.complexity, RegionLines: st.lines,
			TestComplexity: run.testComplexity,
			ParseRetries:   st.parseRetries, Dropped: st.dropped,
			Shadow: true,
		})
	}
	return out
}

// sortedShardIndexes returns the shard indexes in ascending order, so emitted
// events and recorded rows are deterministic.
func sortedShardIndexes(m map[int]shardStat) []int {
	out := make([]int, 0, len(m))
	for i := range m {
		out = append(out, i)
	}
	sort.Ints(out)
	return out
}
