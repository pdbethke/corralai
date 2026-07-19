// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"reflect"
	"testing"

	"github.com/pdbethke/corralai/internal/repoindex"
)

func sig(name string, complexity, lines int) repoindex.Signature {
	return repoindex.Signature{Name: name, Complexity: complexity, Lines: lines}
}

func TestShardSymbolsDegenerateCasesReturnNil(t *testing.T) {
	three := []repoindex.Signature{sig("a", 1, 1), sig("b", 1, 1), sig("c", 1, 1)}
	cases := []struct {
		name      string
		sigs      []repoindex.Signature
		maxShards int
	}{
		{"no signatures", nil, 8},
		{"one symbol", []repoindex.Signature{sig("a", 1, 1)}, 8},
		{"maxShards zero", three, 0},
		{"maxShards one", three, 1},
		{"maxShards negative", three, -3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShardSymbols(tc.sigs, tc.maxShards); got != nil {
				t.Errorf("want nil (unsharded), got %+v", got)
			}
		})
	}
}

func TestShardSymbolsCoversEverySymbol(t *testing.T) {
	sigs := []repoindex.Signature{
		sig("a", 10, 40), sig("b", 1, 2), sig("c", 7, 20),
		sig("d", 3, 9), sig("e", 1, 1), sig("f", 5, 15),
	}
	shards := ShardSymbols(sigs, 3)
	if len(shards) != 3 {
		t.Fatalf("want 3 shards, got %d: %+v", len(shards), shards)
	}
	seen := map[string]int{}
	for _, s := range shards {
		for _, name := range s.Symbols {
			seen[name]++
		}
	}
	if len(seen) != len(sigs) {
		t.Errorf("want all %d symbols covered, got %d: %v", len(sigs), len(seen), seen)
	}
	for name, n := range seen {
		if n != 1 {
			t.Errorf("symbol %q appears %d times, want exactly 1", name, n)
		}
	}
}

func TestShardSymbolsBalancesByComplexity(t *testing.T) {
	// One monster + five trivial. A line-span or round-robin packer would put
	// the monster with others; complexity packing must isolate it.
	sigs := []repoindex.Signature{
		sig("monster", 40, 200),
		sig("a", 1, 3), sig("b", 1, 3), sig("c", 1, 3), sig("d", 1, 3), sig("e", 1, 3),
	}
	shards := ShardSymbols(sigs, 2)
	if len(shards) != 2 {
		t.Fatalf("want 2 shards, got %d", len(shards))
	}
	var monsterShard Shard
	for _, s := range shards {
		for _, name := range s.Symbols {
			if name == "monster" {
				monsterShard = s
			}
		}
	}
	if len(monsterShard.Symbols) != 1 {
		t.Errorf("monster should sit alone in its shard, got %v", monsterShard.Symbols)
	}
	if monsterShard.Complexity != 40 {
		t.Errorf("monster shard Complexity: want 40, got %d", monsterShard.Complexity)
	}
	if monsterShard.Lines != 200 {
		t.Errorf("monster shard Lines: want 200, got %d", monsterShard.Lines)
	}
}

func TestShardSymbolsFewerSymbolsThanMaxShards(t *testing.T) {
	sigs := []repoindex.Signature{sig("a", 3, 9), sig("b", 2, 5), sig("c", 1, 1)}
	shards := ShardSymbols(sigs, 8)
	if len(shards) != 3 {
		t.Fatalf("want 3 shards (one per symbol), got %d: %+v", len(shards), shards)
	}
	for i, s := range shards {
		if s.Index != i {
			t.Errorf("shard[%d].Index: want %d, got %d", i, i, s.Index)
		}
		if len(s.Symbols) != 1 {
			t.Errorf("shard[%d]: want 1 symbol, got %v", i, s.Symbols)
		}
	}
}

func TestShardSymbolsIsDeterministic(t *testing.T) {
	sigs := []repoindex.Signature{
		sig("a", 5, 10), sig("b", 5, 10), sig("c", 5, 10),
		sig("d", 5, 10), sig("e", 5, 10), sig("f", 5, 10),
	}
	first := ShardSymbols(sigs, 3)
	for i := 0; i < 20; i++ {
		if got := ShardSymbols(sigs, 3); !reflect.DeepEqual(first, got) {
			t.Fatalf("non-deterministic packing on iteration %d:\nfirst=%+v\ngot=%+v", i, first, got)
		}
	}
}

func TestShardSymbolsZeroComplexityDistributesAcrossShards(t *testing.T) {
	// A future language extractor could report Complexity 0 for every symbol
	// (today's Go/Python extractors floor at 1). A strict-less-than lightest
	// search never moves off index 0 when weights never change, collapsing
	// everything into shard 0 and leaving the rest empty. The tie-break must
	// spread these instead.
	sigs := []repoindex.Signature{
		sig("a", 0, 1), sig("b", 0, 1), sig("c", 0, 1),
		sig("d", 0, 1), sig("e", 0, 1), sig("f", 0, 1),
	}
	shards := ShardSymbols(sigs, 3)
	if len(shards) != 3 {
		t.Fatalf("want 3 shards, got %d: %+v", len(shards), shards)
	}
	for _, s := range shards {
		if len(s.Symbols) == 0 {
			t.Errorf("shard %d got no symbols, want distribution across all shards: %+v", s.Index, shards)
		}
	}
	seen := map[string]int{}
	for _, s := range shards {
		for _, name := range s.Symbols {
			seen[name]++
		}
	}
	if len(seen) != len(sigs) {
		t.Errorf("want all %d symbols covered, got %d: %v", len(sigs), len(seen), seen)
	}
}

func TestShardSymbolsZeroComplexityIsDeterministic(t *testing.T) {
	sigs := []repoindex.Signature{
		sig("a", 0, 1), sig("b", 0, 1), sig("c", 0, 1),
		sig("d", 0, 1), sig("e", 0, 1), sig("f", 0, 1),
	}
	first := ShardSymbols(sigs, 3)
	for i := 0; i < 20; i++ {
		if got := ShardSymbols(sigs, 3); !reflect.DeepEqual(first, got) {
			t.Fatalf("non-deterministic packing under zero-weight tie-break on iteration %d:\nfirst=%+v\ngot=%+v", i, first, got)
		}
	}
}

func TestShardSymbolsQualifiesMethodsByReceiver(t *testing.T) {
	// (*Engine).String and (*Store).String share a bare Name; ShardSymbols must
	// record a qualified identity for each so they remain distinguishable once
	// packed into shards (a bare "String" would be ambiguous between them).
	sigs := []repoindex.Signature{
		{Name: "String", Receiver: "*Engine", Complexity: 1, Lines: 1},
		{Name: "String", Receiver: "*Store", Complexity: 1, Lines: 1},
		{Name: "Plain", Complexity: 1, Lines: 1}, // no receiver — a top-level function
	}
	shards := ShardSymbols(sigs, 3)
	seen := map[string]bool{}
	for _, s := range shards {
		for _, name := range s.Symbols {
			seen[name] = true
		}
	}
	for _, want := range []string{"*Engine.String", "*Store.String", "Plain"} {
		if !seen[want] {
			t.Errorf("want symbol identity %q recorded in some shard, got %v", want, seen)
		}
	}
	if seen["String"] {
		t.Errorf("bare unqualified %q must not appear as a recorded symbol identity: %v", "String", seen)
	}
}

func TestShardSymbolsSkipsUnnamedSymbols(t *testing.T) {
	sigs := []repoindex.Signature{sig("a", 2, 4), sig("", 9, 30), sig("b", 2, 4)}
	shards := ShardSymbols(sigs, 4)
	for _, s := range shards {
		for _, name := range s.Symbols {
			if name == "" {
				t.Errorf("unnamed symbol leaked into shard %d", s.Index)
			}
		}
	}
}
