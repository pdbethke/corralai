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
	Symbols    []string // qualified identities from symbolIdentity, e.g. "*Engine.String" — never bare names, see symbolIdentity
	Complexity int      // summed symbol complexity — the packing weight and the difficulty control
	Lines      int      // summed symbol line span
}

// symbolIdentity is the ONE qualified identifier used both to record a
// symbol into a Shard and to match it back out of a signature list
// (filterSignatures in roles.go). A bare Signature.Name is not unique: Go
// lets two methods on different receivers share a name — (*Engine).String
// and (*Store).String both have Name == "String" — so packing and matching
// by name alone can silently pack them into different shards while each
// shard's aiming directive and filtered signature list still reference BOTH
// of them ambiguously. Qualifying with the receiver when present closes
// that gap and, as a side effect, makes the "ATTACK ONLY THESE FUNCTIONS"
// prompt line unambiguous instead of just "String".
//
// The Python extractor only walks top-level function definitions and never
// populates Receiver, so this is a no-op there — plain names pass through
// unchanged.
func symbolIdentity(s repoindex.Signature) string {
	if s.Receiver != "" {
		return s.Receiver + "." + s.Name
	}
	return s.Name
}

// ShardSymbols bin-packs sigs into at most maxShards balanced groups, or
// returns nil meaning "do not shard" (the caller falls back to the single
// whole-file generator, whose prompt stays byte-identical to the pre-slice-2
// behavior).
//
// EVERY named symbol lands in exactly one shard. maxShards bounds PARALLELISM,
// never COVERAGE: a top-N-by-size selection would silently make the "every
// function gets probed" claim false while the readout still said "sharded",
// and "we probed 8 of your 30 functions" is exactly what surfaces
// embarrassingly in a real audit. Unnamed symbols are excluded from that
// guarantee — see the filter below — but they are the only exclusion.
//
// Balancing is by COMPLEXITY rather than line span so a shard of gnarly
// branch-heavy functions is not paired against a shard of one-line getters
// merely because their line counts matched — which would poison the very
// per-shard comparison the metrics exist for.
//
// Packing is greedy hardest-first into the lightest bin. Deterministic: the
// sort breaks ties by qualified identity (symbolIdentity), so the same input
// always yields the same shards (a run must be reproducible, and shard index
// is a recorded metrics key).
func ShardSymbols(sigs []repoindex.Signature, maxShards int) []Shard {
	if maxShards <= 1 {
		return nil
	}
	// An unnamed symbol has no stable identifier a shard's aiming directive
	// could reference, so there is no way to tell a seat what to attack — it
	// is dropped here rather than counted against the coverage guarantee.
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
		return symbolIdentity(named[i]) < symbolIdentity(named[j]) // deterministic tie-break
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
			// Weight ties (e.g. all-zero-complexity input from a future
			// extractor) fall through to fewest-symbols-so-far, so equal
			// weight still spreads across shards instead of everything
			// piling into shard 0. Both comparisons are deterministic.
			if shards[i].Complexity < shards[lightest].Complexity ||
				(shards[i].Complexity == shards[lightest].Complexity && len(shards[i].Symbols) < len(shards[lightest].Symbols)) {
				lightest = i
			}
		}
		shards[lightest].Symbols = append(shards[lightest].Symbols, symbolIdentity(s))
		shards[lightest].Complexity += s.Complexity
		shards[lightest].Lines += s.Lines
	}
	return shards
}
