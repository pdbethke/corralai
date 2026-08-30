// SPDX-License-Identifier: Elastic-2.0

package advpool

import "sort"

// bugCatchObservations derives each seat's execution-proven contribution
// from the run state + the signed verdict. Catches = ProvenMissed only — no
// claim/self-report path may reach it.
func bugCatchObservations(run *runState, v Verdict) []BugCatchObservation {
	var out []BugCatchObservation
	// test-writer: the execution-proven catcher.
	//
	// "Graded" means the authored test actually ran against the survivors and
	// produced a verdict corral can stand behind. runState.poolScored is NOT
	// that test: tickPoolAdequacy sets it true BEFORE the soundness check and
	// on the writer-failed path too, so "poolScored" alone was true for a run
	// whose test never graded at all.
	//
	// primaryWriterMeasured is the POSITIVE flag for exactly this question —
	// see its own doc — and asking it directly replaced a proxy that broke
	// under the per-survivor fan-out. That proxy was
	// `authoredTest != "" && !poolTestUnsound && !testWriterFailed`, and
	// authoredTest is EMPTY whenever the language's concatenator refused
	// every proven part (the ordinary case on a language whose helpers
	// collide). A sound run whose proofs all rode out in AuthoredExtra then
	// scored as though corral had never let the model try: zero authored
	// tests, zero opportunities, a model penalised for corral's merge.
	//
	// It is also the same question in BOTH modes, which is the property that
	// makes the scorecard comparable across them: identical writer behaviour
	// must score identically whether it arrived as one call or as N.
	graded := run.poolScored && run.primaryWriterMeasured

	// PER FILE, not per seat, in both modes. A per-survivor run makes N calls
	// where a batched one makes 1, and counting seats here would weight one
	// file's evidence N times as heavily in a scorecard whose whole purpose is
	// to compare MODELS — the mode would move the ranking. One file, one
	// authored suite, one soundness observation.
	authored, sound := 0, 0
	if !run.testWriterMoot {
		authored = 1
		if graded {
			sound = 1
		}
	}

	// Opportunities is the RECALL DENOMINATOR, so it must count only chances
	// the writer actually got. On an ungraded run — no compiling test, or one
	// that never genuinely graded — corral's own pipeline denied it any chance
	// to catch anything, and charging that run's survivors here reports a
	// pipeline failure as a property of the MODEL. It is the same
	// false-accusation shape scanstore's NULL-never-0.0 rule exists to prevent
	// ("a stored 0.0 would later read as 'your tests caught nothing here'
	// about a file corral never graded"), aimed at a model instead of a file.
	//
	// This was not hypothetical: ProvenMissed was structurally pinned to zero
	// on real repos until 2026-07-31, so every real-repo run scored the
	// test-writer 0% recall — a number produced entirely by corral's own
	// stacked defects. The survivors are not lost, they remain on the
	// mutant-generator's own row (MutantsSurvived), and the writer's failure
	// is still penalised where it belongs: SoundTests/AuthoredTests, which is
	// what the PRECISION column measures.
	opportunities := 0
	if graded {
		opportunities = v.Survivors
	}

	out = append(out, BugCatchObservation{
		Model: v.ModelsByRole[RoleTestWriter], Role: RoleTestWriter,
		Catches: v.ProvenMissed, Opportunities: opportunities,
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
