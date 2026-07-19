// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"sort"

	"github.com/pdbethke/corralai/internal/repoindex"
)

// Shard is one mutant-generator's assigned region: a GROUP of top-level
// symbols, not a slice of the file. Every shard's prompt carries the WHOLE
// file as context — patch-based mutants are SEARCH/REPLACE hunks anchored
// against the original (unique anchor + round-trip + Mutant.ParentSHA256), so
// handing a generator a fragment would break anchor uniqueness and the
// tamper-evidence chain. Sharding changes AIM, not CONTEXT.
type Shard struct {
	Index      int
	Symbols    []string
	Complexity int // summed symbol complexity — the packing weight and the difficulty control
	Lines      int // summed symbol line span
}

// ShardSymbols bin-packs sigs into at most maxShards balanced groups, or
// returns nil meaning "do not shard" (the caller falls back to the single
// whole-file generator, whose prompt stays byte-identical to the pre-slice-2
// behavior).
//
// EVERY symbol lands in exactly one shard. maxShards bounds PARALLELISM, never
// COVERAGE: a top-N-by-size selection would silently make the "every function
// gets probed" claim false while the readout still said "sharded", and
// "we probed 8 of your 30 functions" is exactly what surfaces embarrassingly
// in a real audit.
//
// Balancing is by COMPLEXITY rather than line span so a shard of gnarly
// branch-heavy functions is not paired against a shard of one-line getters
// merely because their line counts matched — which would poison the very
// per-shard comparison the metrics exist for.
//
// Packing is greedy hardest-first into the lightest bin. Deterministic: the
// sort breaks ties by name, so the same input always yields the same shards
// (a run must be reproducible, and shard index is a recorded metrics key).
func ShardSymbols(sigs []repoindex.Signature, maxShards int) []Shard {
	if maxShards <= 1 {
		return nil
	}
	named := make([]repoindex.Signature, 0, len(sigs))
	for _, s := range sigs {
		if s.Name != "" {
			named = append(named, s)
		}
	}
	if len(named) < 2 {
		return nil
	}

	sort.SliceStable(named, func(i, j int) bool {
		if named[i].Complexity != named[j].Complexity {
			return named[i].Complexity > named[j].Complexity // hardest first
		}
		return named[i].Name < named[j].Name // deterministic tie-break
	})

	n := maxShards
	if len(named) < n {
		n = len(named)
	}
	shards := make([]Shard, n)
	for i := range shards {
		shards[i].Index = i
	}
	for _, s := range named {
		lightest := 0
		for i := 1; i < len(shards); i++ {
			if shards[i].Complexity < shards[lightest].Complexity {
				lightest = i
			}
		}
		shards[lightest].Symbols = append(shards[lightest].Symbols, s.Name)
		shards[lightest].Complexity += s.Complexity
		shards[lightest].Lines += s.Lines
	}
	return shards
}
