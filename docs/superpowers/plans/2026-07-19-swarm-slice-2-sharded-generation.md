# Swarm Slice 2 — Sharded Generation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fan the single `mutant-generator` task out into N generators — one per complexity-balanced group of top-level symbols — so every function in a file gets probed, and record per-shard effectiveness conditioned on complexity.

**Architecture:** `repoindex` learns to measure per-symbol complexity. A pure `ShardSymbols` bin-packer groups symbols into ≤`MaxShards` balanced shards. `BuildDAG` emits one `mutant-generator` task per shard (each prompt carries the whole file, but aims at only its own symbols). The driver collects all shards, unions their mutants under shard-prefixed IDs, tolerates per-shard failure by dropping, and records coverage in the signed statement. A shadow challenger model attacks every shard in parallel for a region-controlled head-to-head, structurally excluded from the gate by its own role key.

**Tech Stack:** Go 1.26.5, tree-sitter (`github.com/smacker/go-tree-sitter`, cgo-gated), SQLite (`internal/queue`), DuckDB (`internal/bugcatch`), Ed25519 DSSE signing (`internal/certify`).

**Spec:** `docs/superpowers/specs/2026-07-19-swarm-slice-2-sharded-generation-design.md`

## Global Constraints

- Every new `.go` file starts with `// SPDX-License-Identifier: Elastic-2.0`.
- `gofmt -l .` must print nothing before any commit. **A gofmt miss fails the deploy gate.**
- `bash scripts/check-security.sh` (gosec + govulncheck) must pass before pushing.
- Tests touching concurrency run under `-race`.
- Files under `internal/repoindex/*_cgo.go` require `//go:build cgo`; their tests too.
- **Correctness invariant:** kill-rate stays execution-proven in the jail. Sharding changes how many generators run and what each aims at — never what counts as killed.
- **Exam invariant:** only PRIMARY-model mutants reach dev-adequacy. Shadow mutants never influence `DevKillRate`, `Survivors`, or `Status`.
- **Coverage invariant:** `--max-shards` bounds parallelism only. Symbols are never dropped from probing by the packer; only a *failed* shard drops, and that shortfall goes in the signed record.

---

### Task 0: Per-symbol complexity in repoindex

**Files:**
- Modify: `internal/repoindex/signatures.go` (add fields to `Signature`)
- Modify: `internal/repoindex/signatures_cgo.go` (compute them)
- Test: `internal/repoindex/signatures_test.go` (append)

**Interfaces:**
- Consumes: nothing (leaf change)
- Produces: `repoindex.Signature.Complexity int`, `repoindex.Signature.Lines int`. `Complexity` is cyclomatic-style (1 + branch/loop/case/catch/boolean-operator count in the symbol subtree), minimum 1. `Lines` is the symbol's inclusive line span. Both are 0 on the nocgo path (which returns no signatures at all).

- [ ] **Step 1: Write the failing test**

Append to `internal/repoindex/signatures_test.go`:

```go
func TestGoSignatureComplexity(t *testing.T) {
	src := `package p

func Simple(a int) int { return a }

func Branchy(a int) int {
	if a > 0 {
		for i := 0; i < a; i++ {
			if i%2 == 0 && i > 2 {
				return i
			}
		}
	}
	switch a {
	case 1:
		return 1
	case 2:
		return 2
	}
	return 0
}
`
	got, err := ExtractSignatures(src, "go")
	if err != nil {
		t.Fatalf("ExtractSignatures: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 signatures, got %d: %+v", len(got), got)
	}
	if got[0].Complexity != 1 {
		t.Errorf("Simple.Complexity: want 1, got %d", got[0].Complexity)
	}
	if got[0].Lines != 1 {
		t.Errorf("Simple.Lines: want 1, got %d", got[0].Lines)
	}
	// 1 base + 2 if + 1 for + 1 "&&" + 2 case = 7
	if got[1].Complexity != 7 {
		t.Errorf("Branchy.Complexity: want 7, got %d", got[1].Complexity)
	}
	if got[1].Lines != 16 {
		t.Errorf("Branchy.Lines: want 16, got %d", got[1].Lines)
	}
}

func TestPythonSignatureComplexity(t *testing.T) {
	src := `def simple(a):
    return a


def branchy(a):
    if a > 0:
        for i in range(a):
            if i % 2 == 0 and i > 2:
                return i
    elif a < 0:
        while a < 0:
            a += 1
    return 0
`
	got, err := ExtractSignatures(src, "python")
	if err != nil {
		t.Fatalf("ExtractSignatures: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 signatures, got %d: %+v", len(got), got)
	}
	if got[0].Complexity != 1 {
		t.Errorf("simple.Complexity: want 1, got %d", got[0].Complexity)
	}
	// 1 base + 2 if + 1 for + 1 "and" + 1 elif + 1 while = 7
	if got[1].Complexity != 7 {
		t.Errorf("branchy.Complexity: want 7, got %d", got[1].Complexity)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/repoindex/ -run 'TestGoSignatureComplexity|TestPythonSignatureComplexity' -v`
Expected: FAIL — `got[0].Complexity undefined (type Signature has no field or method Complexity)` (compile error).

- [ ] **Step 3: Add the fields**

In `internal/repoindex/signatures.go`, extend `Signature`:

```go
type Signature struct {
	Name     string
	Kind     string
	Receiver string
	Params   []Param
	Results  []string
	Exported bool
	Line     int
	// Complexity is a cyclomatic-style difficulty measure for this symbol:
	// 1 + the number of branch, loop, case, catch, and boolean-operator nodes
	// in its subtree. It is the difficulty CONTROL for model-effectiveness
	// comparison — a model that is fine on getters and collapses on
	// branch-heavy code reads as merely "average" when yield is pooled across
	// difficulty. Also the bin-packing weight for shard balancing.
	//
	// The branch-node set is per-language, so complexity numbers are NOT
	// strictly comparable ACROSS languages; band within a corpus instead.
	// 0 when unavailable (the nocgo path returns no signatures at all).
	Complexity int
	// Lines is the symbol's inclusive line span (end row - start row + 1).
	Lines int
}
```

- [ ] **Step 4: Compute them in the cgo extractor**

In `internal/repoindex/signatures_cgo.go`, add near the top (after `exported`):

```go
// branchNodeTypes are the per-language tree-sitter node types that introduce an
// independent execution path through a symbol. Counting them (plus the boolean
// operators handled below) yields a cyclomatic-style complexity.
//
// Deliberately per-language: a "case_clause" in Python and an "expression_case"
// in Go are the same concept under different grammar names, so a shared set
// would silently under-count one language and make its symbols look easy.
var branchNodeTypes = map[string]map[string]bool{
	"go": {
		"if_statement":        true,
		"for_statement":       true,
		"expression_case":     true,
		"type_case":           true,
		"communication_case":  true,
		"select_statement":    true,
	},
	"python": {
		"if_statement":           true,
		"elif_clause":            true,
		"for_statement":          true,
		"while_statement":        true,
		"except_clause":          true,
		"case_clause":            true,
		"conditional_expression": true,
		"boolean_operator":       true, // `and` / `or`
	},
}

// symbolComplexity walks n's subtree counting branch nodes, returning the
// cyclomatic-style complexity (minimum 1 — a straight-line symbol has exactly
// one path). Go's `&&`/`||` are binary_expression nodes distinguished by their
// operator field rather than by node type, so they are counted separately;
// Python's are their own `boolean_operator` node type and fall out of the set.
func symbolComplexity(n *sitter.Node, src []byte, lang string) int {
	types := branchNodeTypes[lang]
	c := 1
	var walk func(*sitter.Node)
	walk = func(node *sitter.Node) {
		if node == nil {
			return
		}
		t := node.Type()
		switch {
		case types[t]:
			c++
		case lang == "go" && t == "binary_expression":
			if op := node.ChildByFieldName("operator"); op != nil {
				if s := op.Content(src); s == "&&" || s == "||" {
					c++
				}
			}
		}
		for i := 0; i < int(node.NamedChildCount()); i++ {
			walk(node.NamedChild(i))
		}
	}
	walk(n)
	return c
}

// symbolLines is the inclusive line span of n.
func symbolLines(n *sitter.Node) int {
	return int(n.EndPoint().Row-n.StartPoint().Row) + 1
}
```

In `goCallable`, before `return sig`:

```go
	sig.Complexity = symbolComplexity(n, src, "go")
	sig.Lines = symbolLines(n)
```

In `extractPythonSignatures`, after the `if rt := def.ChildByFieldName("return_type"); rt != nil { ... }` block and before `out = append(out, sig)`:

```go
		sig.Complexity = symbolComplexity(def, src, "python")
		sig.Lines = symbolLines(def)
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/repoindex/ -run 'TestGoSignatureComplexity|TestPythonSignatureComplexity' -v`
Expected: PASS (both).

Then the full package, to confirm the existing `assertSignatures` helper (which does not compare the new fields) still passes:

Run: `go test ./internal/repoindex/`
Expected: `ok  github.com/pdbethke/corralai/internal/repoindex`

- [ ] **Step 6: Commit**

```bash
gofmt -l internal/repoindex/
git add internal/repoindex/signatures.go internal/repoindex/signatures_cgo.go internal/repoindex/signatures_test.go
git commit -m "feat(repoindex): per-symbol Complexity + Lines on Signature

Cyclomatic-style branch count per symbol, computed in the existing
tree-sitter walk (go + python). This is the difficulty CONTROL for
model-effectiveness comparison: pooling yield across difficulty hides a
model that is fine on getters and collapses on branch-heavy code. Also the
bin-packing weight for swarm slice 2's shard balancing.

Per-language branch-node sets, so cross-language numbers are not strictly
comparable — band within a corpus. Additive; nothing consumes it yet."
```

---

### Task 1: ShardSymbols bin-packer

**Files:**
- Create: `internal/advpool/shard.go`
- Test: `internal/advpool/shard_test.go`

**Interfaces:**
- Consumes: `repoindex.Signature` (`.Name`, `.Complexity`, `.Lines`) from Task 0.
- Produces:
  - `advpool.Shard struct { Index int; Symbols []string; Complexity int; Lines int }`
  - `advpool.ShardSymbols(sigs []repoindex.Signature, maxShards int) []Shard` — returns `nil` to mean **do not shard** (caller falls back to today's single whole-file generator).

- [ ] **Step 1: Write the failing test**

Create `internal/advpool/shard_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/advpool/ -run TestShardSymbols -v`
Expected: FAIL — `undefined: ShardSymbols` and `undefined: Shard` (compile error).

- [ ] **Step 3: Implement the packer**

Create `internal/advpool/shard.go`:

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/advpool/ -run TestShardSymbols -v`
Expected: PASS (all six).

- [ ] **Step 5: Commit**

```bash
gofmt -l internal/advpool/
git add internal/advpool/shard.go internal/advpool/shard_test.go
git commit -m "feat(advpool): ShardSymbols — complexity-balanced symbol bin-packer

Groups top-level symbols into <=maxShards balanced bins, or returns nil
meaning do-not-shard. EVERY symbol lands in exactly one shard: maxShards
bounds parallelism, never coverage (a top-N selection would silently falsify
the 'every function gets probed' claim). Balanced by complexity, not line
span, so a gnarly shard is not paired against a getters shard merely because
line counts matched. Deterministic — shard index is a recorded metrics key."
```

---

### Task 2: RunSpec.MaxShards, sharded BuildDAG, and the CLI dial

**Files:**
- Modify: `internal/advpool/run.go` (add `MaxShards`)
- Modify: `internal/advpool/roles.go` (`BuildDAG` fan-out, shard prompt, shard key helpers)
- Modify: `cmd/corral/certify_local.go` (`--max-shards` flag + readout)
- Test: `internal/advpool/roles_test.go` (append)

**Interfaces:**
- Consumes: `ShardSymbols`, `Shard` (Task 1).
- Produces:
  - `RunSpec.MaxShards int` — 0 or 1 means unsharded.
  - `advpool.DefaultMaxShards = 8`
  - `advpool.ShardTaskKey(index int) string` → `"mutant-generator/<index>"`
  - `advpool.ShardIndexFromKey(key string) (int, bool)` — `("mutant-generator", …)` → `(0, false)`; `("mutant-generator/3", …)` → `(3, true)`.
  - `BuildDAG` emits N mutant-generator specs when sharded, 1 otherwise.

- [ ] **Step 1: Write the failing test**

Append to `internal/advpool/roles_test.go`:

```go
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
```

Ensure the test file imports `strings`, `testing`, `github.com/pdbethke/corralai/internal/queue`, and `github.com/pdbethke/corralai/internal/repoindex`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/advpool/ -run 'TestBuildDAG|TestShardTaskKey' -v`
Expected: FAIL — `undefined: ShardTaskKey`, `undefined: ShardIndexFromKey`, and `rs.MaxShards undefined`.

- [ ] **Step 3: Add `MaxShards` to RunSpec**

In `internal/advpool/run.go`, add to `RunSpec` after `NMutants`:

```go
	// MaxShards bounds how many mutant-generator seats fan out across the
	// file's top-level symbols. 0 or 1 means unsharded (one generator, whole
	// file — the pre-slice-2 behavior, byte-identical prompt). It bounds
	// PARALLELISM only: every symbol is probed regardless (see ShardSymbols).
	// NMutants is the PER-SHARD budget, so total mutants scale with width.
	MaxShards int
```

- [ ] **Step 4: Implement the fan-out**

In `internal/advpool/roles.go`, add imports `strconv` (keep existing ones) and append:

```go
// DefaultMaxShards is the stock generator width. It matches
// cmd/corral.localSwarmAutoCap so a default run's shard count and its
// concurrent-worker bound agree rather than one throttling the other.
const DefaultMaxShards = 8

// ShardTaskKey is the queue key for shard i of the mutant-generator role.
// Sharded keys are distinct from the bare role name so an unsharded run's
// task key is unchanged.
func ShardTaskKey(index int) string {
	return RoleMutantGenerator + "/" + strconv.Itoa(index)
}

// ShardIndexFromKey returns the shard index encoded in a mutant-generator task
// key, and whether the key was a sharded one. The bare role key (an unsharded
// run) and any malformed suffix report (0, false).
func ShardIndexFromKey(key string) (int, bool) {
	rest, ok := strings.CutPrefix(key, RoleMutantGenerator+"/")
	if !ok {
		return 0, false
	}
	i, err := strconv.Atoi(rest)
	if err != nil || i < 0 {
		return 0, false
	}
	return i, true
}

// renderMutantGeneratorShard renders one shard's prompt: the SAME testgen
// prompt and the SAME whole-file context as the unsharded path, with the goal
// augmented by an aiming directive and the signature list filtered to this
// shard's symbols. The file is never fragmented — patch-based mutants anchor
// against the whole original.
func renderMutantGeneratorShard(rs RunSpec, sigs []repoindex.Signature, sh Shard) string {
	aimed := rs
	aimed.Goal = fmt.Sprintf(
		"%s\n\nATTACK ONLY THESE FUNCTIONS: %s. Every mutation you produce MUST edit code inside one of them. Other functions in the file are being attacked by other seats — do not mutate them, and do not report that you skipped them.",
		rs.Goal, strings.Join(sh.Symbols, ", "))
	return renderMutantGenerator(aimed, filterSignatures(sigs, sh.Symbols), nil)
}

// filterSignatures keeps only the signatures naming one of want, preserving
// input order, so a shard's prompt lists exactly the surface it is aimed at.
func filterSignatures(sigs []repoindex.Signature, want []string) []repoindex.Signature {
	keep := make(map[string]bool, len(want))
	for _, w := range want {
		keep[w] = true
	}
	var out []repoindex.Signature
	for _, s := range sigs {
		if keep[s.Name] {
			out = append(out, s)
		}
	}
	return out
}

// shardTitle labels a shard task with the region it attacks, so the queue and
// the cockpit show WHICH functions each seat is on.
func shardTitle(sh Shard) string {
	return "Generate mutants for " + strings.Join(sh.Symbols, ", ")
}
```

Then replace the body of `BuildDAG`:

```go
func BuildDAG(rs RunSpec, assign RoleAssignment, sigs []repoindex.Signature) []queue.TaskSpec {
	roles := Roles()
	shards := ShardSymbols(sigs, rs.MaxShards)
	specs := make([]queue.TaskSpec, 0, len(roles)+len(shards))
	for _, role := range roles {
		// The mutant-generator fans out into one seat per shard when the file
		// has an extractable symbol surface; otherwise it stays exactly one
		// whole-file seat with an unchanged key and a byte-identical prompt.
		if role.Name == RoleMutantGenerator && len(shards) > 0 {
			for _, sh := range shards {
				specs = append(specs, queue.TaskSpec{
					Key:         ShardTaskKey(sh.Index),
					Role:        RoleMutantGenerator,
					Title:       shardTitle(sh),
					Instruction: renderMutantGeneratorShard(rs, sigs, sh),
					Model:       assign[RoleMutantGenerator],
				})
			}
			continue
		}
		specs = append(specs, queue.TaskSpec{
			Key:         role.Name,
			Role:        role.Name,
			Title:       roleTitle(role.Name),
			Instruction: role.Render(rs, sigs, nil),
			DependsOn:   role.Deps,
			Model:       assign[role.Name],
		})
	}
	return specs
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/advpool/ -run 'TestBuildDAG|TestShardTaskKey|TestShardSymbols' -v`
Expected: PASS.

Run: `go test ./internal/advpool/`
Expected: `ok` — existing driver tests still pass (they build runs with `MaxShards` unset, so they stay on the unsharded path).

- [ ] **Step 6: Wire the CLI dial**

In `cmd/corral/certify_local.go`, add the flag next to `swarmFlag`:

```go
	maxShardsFlag := fs.Int("max-shards", 0, "max mutant-generator seats fanned out across the file's functions (0 = "+fmt.Sprint(advpool.DefaultMaxShards)+"). Bounds PARALLELISM only — every function is probed regardless; --n-mutants is the PER-SHARD budget")
```

Set it on the `RunSpec` (in the `rs := advpool.RunSpec{...}` literal, after `NMutants: n,`):

```go
		MaxShards: resolveMaxShards(*maxShardsFlag),
```

Add near `resolveSwarm`:

```go
// resolveMaxShards resolves the generator fan-out width: the operator's
// --max-shards budget, else the stock default.
func resolveMaxShards(flag int) int {
	if flag > 0 {
		return flag
	}
	return advpool.DefaultMaxShards
}
```

And announce the actual width alongside the existing `swarm:` line — after the `d.StartRun(...)` call succeeds (so `sigs` is known), replace the existing `auditing %s ...` print block's trailing area by adding, immediately after the `swarm:` readout:

```go
	if shards := advpool.ShardSymbols(sigs, rs.MaxShards); len(shards) > 0 {
		fmt.Fprintf(stdout, "regions: %d generator seats over %d functions\n", len(shards), len(sigs))
	} else {
		fmt.Fprintf(stdout, "regions: 1 generator seat (whole file — no symbol surface extracted)\n")
	}
```

- [ ] **Step 7: Verify the CLI builds and prints the readout**

Run: `go build ./... && go vet ./cmd/corral/`
Expected: no output.

Run: `go test ./cmd/corral/`
Expected: `ok`.

- [ ] **Step 8: Commit**

```bash
gofmt -l internal/advpool/ cmd/corral/
git add internal/advpool/run.go internal/advpool/roles.go internal/advpool/roles_test.go cmd/corral/certify_local.go
git commit -m "feat(advpool): fan the mutant-generator out into one seat per symbol shard

BuildDAG emits N mutant-generator tasks keyed mutant-generator/<i>, each
titled with the region it attacks and each carrying the WHOLE file as context
with the goal augmented by an aiming directive — patch-based mutants anchor
against the original, so sharding changes aim, not context.

RunSpec.MaxShards (default 8, matching localSwarmAutoCap) bounds parallelism;
--n-mutants keeps its per-generator meaning. No symbol surface (nocgo builds,
or a language without an extractor — today only go+python) degrades to one
whole-file seat with a byte-identical prompt, asserted by test.

--max-shards dials the width and the run announces it out loud."
```

---

### Task 3: Driver collects all shards

**Files:**
- Modify: `internal/advpool/driver.go` (`tasksByRole`, `tickDevAdequacy`)
- Test: `internal/advpool/driver_test.go` (append)

**Interfaces:**
- Consumes: `ShardIndexFromKey` (Task 2).
- Produces: `(*Driver).tasksByRole(missionID int64, role string) ([]queue.Task, error)` — returns every task with that role, sorted by key for determinism. `tickDevAdequacy` now gates on ALL mutant-generator shards being `StatusDone` and unions their mutants under shard-prefixed IDs (`s<idx>/<id>`); an unsharded run's mutant IDs are unchanged.

**This is the careful one.** The gate's correctness depends on never scoring a partial mutant set as if it were complete.

- [ ] **Step 1: Write the failing test**

Append to `internal/advpool/driver_test.go`. Match the existing helpers in that file for constructing a driver and queue — if the existing tests use a helper such as `newTestDriver(t)`, reuse it verbatim rather than inventing a new one.

```go
// TestTickDevAdequacyWaitsForEveryShard proves the gate never scores a PARTIAL
// mutant set: with 3 shards and only 2 done, dev-adequacy must not run.
func TestTickDevAdequacyWaitsForEveryShard(t *testing.T) {
	const missionID = int64(200)
	scorer := &fakeScorer{devKillRate: 0.9}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m1", Code: "c1"}}}
	d := newShardedRun(t, missionID, 3, scorer, validator)

	mgs, err := d.tasksByRole(missionID, RoleMutantGenerator)
	if err != nil {
		t.Fatalf("tasksByRole: %v", err)
	}
	if len(mgs) != 3 {
		t.Fatalf("want 3 shard tasks, got %d", len(mgs))
	}

	// Complete only two of the three shards.
	ready := claimAllReady(t, d.Q)
	done := 0
	for key, task := range ready {
		if _, sharded := ShardIndexFromKey(key); sharded && done < 2 {
			mustComplete(t, d.Q, task.ID, "raw")
			done++
		}
	}
	if _, err := d.Tick(context.Background(), missionID); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if d.runs[missionID].devScored {
		t.Fatal("dev-adequacy scored a PARTIAL mutant set — the gate must wait for every shard")
	}
	if len(scorer.calls) != 0 {
		t.Fatalf("Scorer ran on a partial mutant set (%d calls)", len(scorer.calls))
	}

	// Complete the last shard; now dev-adequacy may run.
	for key, task := range claimAllReady(t, d.Q) {
		if _, sharded := ShardIndexFromKey(key); sharded {
			mustComplete(t, d.Q, task.ID, "raw")
		}
	}
	if _, err := d.Tick(context.Background(), missionID); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if !d.runs[missionID].devScored {
		t.Fatal("dev-adequacy did not run once every shard was done")
	}
}

// TestShardedMutantIDsArePrefixed proves mutants from different shards cannot
// collide. Every shard's fake returns a mutant named "m1", so ONLY prefixing
// keeps them distinct. Asserted on the mutants handed TO the Scorer, because
// devSurvivors comes from the fake scorer's canned list, not from the union.
func TestShardedMutantIDsArePrefixed(t *testing.T) {
	const missionID = int64(201)
	scorer := &fakeScorer{devKillRate: 1.0}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m1", Code: "c1"}}}
	d := newShardedRun(t, missionID, 2, scorer, validator)

	completeAllReady(t, d, "raw")
	if _, err := d.Tick(context.Background(), missionID); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(scorer.calls) == 0 {
		t.Fatal("Scorer never ran")
	}
	got := scorer.calls[0].mutants
	if len(got) != 2 {
		t.Fatalf("want 2 unioned mutants, got %d — identical shard IDs collided", len(got))
	}
	seen := map[string]bool{}
	for _, m := range got {
		if seen[m.ID] {
			t.Errorf("duplicate mutant ID %q across shards", m.ID)
		}
		seen[m.ID] = true
		if !strings.HasPrefix(m.ID, "s") || !strings.Contains(m.ID, "/") {
			t.Errorf("sharded mutant ID %q must carry its shard prefix (s<idx>/…)", m.ID)
		}
	}
}

// TestUnshardedMutantIDsUnchanged pins the back-compat guarantee.
func TestUnshardedMutantIDsUnchanged(t *testing.T) {
	const missionID = int64(202)
	scorer := &fakeScorer{devKillRate: 1.0}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m1", Code: "c1"}}}
	d := newShardedRun(t, missionID, 0, scorer, validator) // MaxShards 0 => unsharded

	mgs, _ := d.tasksByRole(missionID, RoleMutantGenerator)
	if len(mgs) != 1 {
		t.Fatalf("want 1 unsharded task, got %d", len(mgs))
	}
	if mgs[0].Key != RoleMutantGenerator {
		t.Errorf("unsharded key: want %q, got %q", RoleMutantGenerator, mgs[0].Key)
	}
	completeAllReady(t, d, "raw")
	if _, err := d.Tick(context.Background(), missionID); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	for _, m := range scorer.calls[0].mutants {
		if strings.Contains(m.ID, "/") {
			t.Errorf("unsharded mutant ID %q must not be prefixed", m.ID)
		}
	}
}
```

Add the shared helpers this task and Tasks 4–6 need. These are written against the helpers `driver_test.go` **actually has** — `newTestQueue`, `claimAllReady`, `claimByKey`, `mustComplete`, `decorrelatedAssign`, `fakeScorer`, `fakeValidator`, `fakeBugCatch`. Do not introduce parallel machinery.

```go
// shardedRunSpec is the fixture every sharded test starts from: three symbols,
// so a maxShards of 2 or 3 produces a real fan-out.
func shardedRunSpec(maxShards int) (RunSpec, []repoindex.Signature) {
	rs := RunSpec{
		Repo: "r", Commit: "c", Goal: "g",
		CodePath: "a.go", Code: "package p\nfunc A() {}\nfunc B() {}\nfunc C() {}\n",
		DevTestPath: "a_test.go", DevTestCode: "package p\n",
		NMutants: 1, Lang: "go", MaxShards: maxShards,
	}
	sigs := []repoindex.Signature{
		{Name: "A", Complexity: 5, Lines: 10},
		{Name: "B", Complexity: 3, Lines: 6},
		{Name: "C", Complexity: 1, Lines: 2},
	}
	return rs, sigs
}

// newShardedRun mirrors newTestDriver but starts the run WITH signatures and a
// MaxShards budget, so BuildDAG actually fans out. maxShards 0 yields the
// unsharded single-seat run. validator is an interface param so a test can
// supply a shard-aware fake (see shardValidator in Task 4).
func newShardedRun(t *testing.T, missionID int64, maxShards int, scorer *fakeScorer, validator Validator) *Driver {
	t.Helper()
	q := newTestQueue(t)
	d, err := NewDriver(q, scorer, validator, decorrelatedAssign(), 0.5)
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}
	rs, sigs := shardedRunSpec(maxShards)
	if err := d.StartRun(missionID, rs, sigs); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if _, err := q.PromoteReady(missionID); err != nil {
		t.Fatalf("PromoteReady: %v", err)
	}
	return d
}

// completeAllReady claims every currently-ready task and completes it with
// result — the sharded analogue of the file's claimByKey/mustComplete pair.
func completeAllReady(t *testing.T, d *Driver, result string) int {
	t.Helper()
	ready := claimAllReady(t, d.Q)
	for _, task := range ready {
		mustComplete(t, d.Q, task.ID, result)
	}
	return len(ready)
}

// driveShardedToVerdict ticks to convergence, completing whatever becomes
// claimable, and returns the terminal verdict.
func driveShardedToVerdict(t *testing.T, d *Driver, missionID int64, result string) Verdict {
	t.Helper()
	for i := 0; i < 50; i++ {
		v, err := d.Tick(context.Background(), missionID)
		if err != nil {
			t.Fatalf("Tick: %v", err)
		}
		if v != nil {
			return *v
		}
		completeAllReady(t, d, result)
	}
	t.Fatal("run did not converge in 50 ticks")
	return Verdict{}
}
```

**Note on `fakeValidator`:** its `ParseMutants` ignores the raw string and returns the same canned `f.mutants` for every call — which is exactly what the ID-collision test wants (every shard returns an identically-named mutant, so only prefixing can keep them distinct). Because the survivors in `runState.devSurvivors` come from `fakeScorer`'s canned list rather than the union, **assert prefixing on the mutants handed TO the scorer** (`scorer.calls[0].mutants`), not on `devSurvivors`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/advpool/ -run 'TestTickDevAdequacyWaitsForEveryShard|TestShardedMutantIDs|TestUnshardedMutantIDs' -v`
Expected: FAIL — `d.tasksByRole undefined`, and once that compiles, the partial-set assertion fails because `tickDevAdequacy` scores on the first shard alone.

- [ ] **Step 3: Add `tasksByRole`**

In `internal/advpool/driver.go`, next to `taskByKey`:

```go
// tasksByRole returns every task for a role, sorted by key so shard order is
// deterministic (shard index is a recorded metrics key, and a run must be
// reproducible). Used for the mutant-generator, which fans out into one task
// per symbol shard; taskByKey remains correct for the single-task roles.
func (d *Driver) tasksByRole(missionID int64, role string) ([]queue.Task, error) {
	tasks, err := d.Q.List(missionID)
	if err != nil {
		return nil, err
	}
	var out []queue.Task
	for i := range tasks {
		if tasks[i].Role == role {
			out = append(out, tasks[i])
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}
```

Add `"sort"` to the file's imports if absent.

- [ ] **Step 4: Gate `tickDevAdequacy` on every shard**

Replace the opening of `tickDevAdequacy` (the `mg, err := d.taskByKey(...)` lookup through the `ParseMutants` block) with:

```go
	mgs, err := d.tasksByRole(missionID, RoleMutantGenerator)
	if err != nil {
		return err
	}
	if len(mgs) == 0 {
		return nil
	}
	// EVERY shard must be terminal before the dev's tests are scored. Scoring a
	// partial mutant set would grade the suite against a smaller exam than the
	// run claims to have set — the kill-rate would be real but would not mean
	// what the verdict says it means.
	for i := range mgs {
		if mgs[i].Status != queue.StatusDone {
			return nil
		}
	}

	// Union every shard's mutants. IDs are prefixed with the shard index so two
	// shards returning "m1" cannot collide, and so each survivor names the
	// region it came from (including in the test-writer's prompt). An UNSHARDED
	// run keeps its original, unprefixed IDs.
	var mutants []adequacy.Mutant
	for i := range mgs {
		shardIdx, sharded := ShardIndexFromKey(mgs[i].Key)
		parsed, perr := d.Validator.ParseMutants(mgs[i].Result, run.rs.Code)
		if perr != nil {
			// Malformed artifact: refuse it. The pure driver has no live hook
			// into the completion call — reopen the task so a worker can retry,
			// and surface the failure to the caller.
			if _, rerr := d.Q.ReopenTask(mgs[i].ID); rerr != nil {
				return fmt.Errorf("advpool: reopen %s after parse failure: %w", mgs[i].Key, rerr)
			}
			return fmt.Errorf("advpool: %s result unparseable, reissued for retry: %w", mgs[i].Key, perr)
		}
		for _, m := range parsed {
			if sharded {
				m.ID = fmt.Sprintf("s%d/%s", shardIdx, m.ID)
			}
			mutants = append(mutants, m)
		}
	}
```

Leave the rest of the function (the `d.Scorer.Score(...)` call onward) unchanged — it already operates on `mutants`.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/advpool/ -run 'TestTickDevAdequacy|TestShardedMutantIDs|TestUnshardedMutantIDs' -v`
Expected: PASS.

Run: `go test ./internal/advpool/ -race`
Expected: `ok` — the whole existing driver suite still passes.

- [ ] **Step 6: Commit**

```bash
gofmt -l internal/advpool/
git add internal/advpool/driver.go internal/advpool/driver_test.go
git commit -m "feat(advpool): collect every mutant-generator shard before dev-adequacy

tasksByRole replaces the single taskByKey lookup for the fanned-out
mutant-generator, and tickDevAdequacy now gates on EVERY shard being done
before scoring: grading the dev suite against a partial mutant set would
produce a real kill-rate that does not mean what the verdict says it means.

Shard mutants union under shard-prefixed IDs (s<idx>/<id>) so two shards
returning 'm1' cannot collide and every survivor names its region — including
in the test-writer's prompt. Unsharded runs keep their original IDs, pinned
by test."
```

---

### Task 4: Per-shard retry, drop, and coverage in the signed record

**Files:**
- Modify: `internal/advpool/driver.go` (`runState`, `tickDevAdequacy`, `Verdict`, `tickAggregate`)
- Modify: `internal/advpool/gate.go` (`CertSigner` statement detail)
- Modify: `cmd/corral/certify_local.go` (readout)
- Test: `internal/advpool/driver_test.go` (append)

**Interfaces:**
- Consumes: Task 3's `tasksByRole` + shard collection.
- Produces:
  - `Verdict.RegionsProbed int`, `Verdict.RegionsTotal int`, `Verdict.DroppedRegions []string`
  - `advpool.MaxShardRetries = 2`
  - `runState.shardRetries map[string]int` keyed by task KEY.

- [ ] **Step 1: Write the failing test**

Append to `internal/advpool/driver_test.go`:

First add a shard-aware validator. The file's `fakeValidator` has a single `parseErr` applied to every call, so it cannot make *one* shard fail while others succeed — which is exactly the case under test:

```go
// shardValidator fails to parse any result equal to failRaw, and returns
// canned mutants otherwise — so a test can make ONE shard persistently bad
// while its siblings succeed. (fakeValidator's single parseErr applies to
// every call and cannot express this.)
type shardValidator struct {
	failRaw string
	mutants []adequacy.Mutant
}

func (v *shardValidator) ParseMutants(raw, _ string) ([]adequacy.Mutant, error) {
	if raw == v.failRaw {
		return nil, fmt.Errorf("shardValidator: unparseable")
	}
	return v.mutants, nil
}

func (v *shardValidator) ParseTest(raw string) string { return raw }

func (v *shardValidator) CompileTest(_ context.Context, _, _, _ string) error { return nil }
```

Then the tests:

```go
// TestShardDroppedAfterRetriesAndRecorded proves a persistently unparseable
// shard is dropped (not retried forever, not an aborted run) and that the
// shortfall lands in the verdict.
func TestShardDroppedAfterRetriesAndRecorded(t *testing.T) {
	const missionID = int64(210)
	const bad = "UNPARSEABLE"
	scorer := &fakeScorer{devKillRate: 1.0}
	validator := &shardValidator{failRaw: bad, mutants: []adequacy.Mutant{{ID: "m1", Code: "c1"}}}
	d := newShardedRun(t, missionID, 3, scorer, validator)

	// One chosen shard always returns junk; the others return parseable output.
	badKey := ShardTaskKey(0)
	completeShards := func() {
		for key, task := range claimAllReady(t, d.Q) {
			result := "raw"
			if key == badKey {
				result = bad
			}
			mustComplete(t, d.Q, task.ID, result)
		}
	}
	completeShards()

	// Each Tick reopens the bad shard and errors, up to the retry budget.
	for i := 0; i < MaxShardRetries; i++ {
		if _, err := d.Tick(context.Background(), missionID); err == nil {
			t.Fatalf("Tick %d: want a retry error while the shard has budget left", i)
		}
		completeShards()
	}

	// Budget exhausted: the shard DROPS and the run proceeds.
	if _, err := d.Tick(context.Background(), missionID); err != nil {
		t.Fatalf("Tick after drop: %v", err)
	}
	run := d.runs[missionID]
	if !run.devScored {
		t.Fatal("run did not proceed after dropping a persistently bad shard")
	}
	if got := len(run.droppedRegions); got != 1 {
		t.Fatalf("want 1 dropped region, got %d: %v", got, run.droppedRegions)
	}
	if run.regionsTotal != 3 {
		t.Errorf("regionsTotal: want 3, got %d", run.regionsTotal)
	}
}

// TestVerdictCarriesRegionCoverage proves a clean run reports full coverage —
// the counterpart to the drop case, and what makes PARTIAL meaningful.
func TestVerdictCarriesRegionCoverage(t *testing.T) {
	const missionID = int64(211)
	scorer := &fakeScorer{devKillRate: 1.0}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m1", Code: "c1"}}}
	d := newShardedRun(t, missionID, 3, scorer, validator)

	v := driveShardedToVerdict(t, d, missionID, "raw")
	if v.RegionsTotal != 3 {
		t.Errorf("RegionsTotal: want 3, got %d", v.RegionsTotal)
	}
	if v.RegionsProbed != 3 {
		t.Errorf("RegionsProbed: want 3, got %d", v.RegionsProbed)
	}
	if len(v.DroppedRegions) != 0 {
		t.Errorf("DroppedRegions: want none, got %v", v.DroppedRegions)
	}
}
```

Note: a `devKillRate` of 1.0 leaves zero survivors, so the run takes the perfect-suite path straight to aggregate — which is what makes `driveShardedToVerdict` converge quickly here.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/advpool/ -run 'TestShardDropped|TestVerdictCarriesRegionCoverage' -v`
Expected: FAIL — `undefined: MaxShardRetries`, `run.droppedRegions undefined`, `v.RegionsTotal undefined`.

- [ ] **Step 3: Add retry state and verdict fields**

In `internal/advpool/driver.go`, add to `runState` after `devSurvivors`:

```go
	// shardRetries counts parse failures per mutant-generator task KEY (never
	// its id). Keying by key is deliberate: a lease-expiry re-claim and a
	// parse-failure reopen must draw on the SAME budget, or a shard could
	// retry forever by alternating failure modes.
	shardRetries map[string]int
	// droppedRegions names the shards abandoned after exhausting their retry
	// budget — the coverage shortfall, carried into the signed verdict so a
	// partial audit is provably partial rather than silently partial.
	droppedRegions []string
	regionsTotal   int
```

Initialize `shardRetries` where `runState` is constructed in `StartRun`:

```go
		shardRetries: map[string]int{},
```

Add the constant next to the role constants in `roles.go`:

```go
// MaxShardRetries is how many times a mutant-generator shard whose result will
// not parse is reopened before it is DROPPED and the run proceeds without it.
//
// Straight-lining the pre-shard "retry until the run dies" semantics would
// make sharding actively worse: with 8 seats the odds that at least one
// misbehaves rise ~8x, and one flaky shard would waste the other seven seats'
// spend. Dropping converges; the shortfall is recorded, never swallowed.
const MaxShardRetries = 2
```

Add to `Verdict` after `ProvenMissed`:

```go
	RegionsTotal   int      // mutant-generator seats the run dispatched
	RegionsProbed  int      // seats that returned usable mutants
	DroppedRegions []string // seats abandoned after MaxShardRetries — the coverage shortfall
```

- [ ] **Step 4: Implement retry-then-drop**

In `tickDevAdequacy`, replace the parse-failure branch written in Task 3 with:

```go
		parsed, perr := d.Validator.ParseMutants(mgs[i].Result, run.rs.Code)
		if perr != nil {
			key := mgs[i].Key
			run.shardRetries[key]++
			if run.shardRetries[key] <= MaxShardRetries {
				if _, rerr := d.Q.ReopenTask(mgs[i].ID); rerr != nil {
					return fmt.Errorf("advpool: reopen %s after parse failure: %w", key, rerr)
				}
				return fmt.Errorf("advpool: %s result unparseable, reissued for retry (%d/%d): %w",
					key, run.shardRetries[key], MaxShardRetries, perr)
			}
			// Budget exhausted: DROP this region and proceed on the shards that
			// parsed. Recorded, never swallowed.
			log.Printf("advpool: run %d: dropping region %s after %d unparseable results — its functions go unprobed",
				missionID, key, run.shardRetries[key])
			run.droppedRegions = append(run.droppedRegions, mgs[i].Title)
			continue
		}
```

And set `regionsTotal` just after the all-terminal loop:

```go
	run.regionsTotal = len(mgs)
```

Guard against a run where EVERY shard dropped — there is nothing to grade, so it must not certify. After the union loop:

```go
	if len(mutants) == 0 && len(run.droppedRegions) > 0 {
		return fmt.Errorf("advpool: every mutant-generator region failed (%d dropped) — nothing to grade the dev suite against", len(run.droppedRegions))
	}
```

- [ ] **Step 5: Carry coverage into the Verdict and the signed statement**

In `tickAggregate`, where the `Verdict` is built, add:

```go
		RegionsTotal:   run.regionsTotal,
		RegionsProbed:  run.regionsTotal - len(run.droppedRegions),
		DroppedRegions: run.droppedRegions,
```

In `internal/advpool/gate.go`, add coverage to the signed `execution` step's `Detail` map so it lands inside the signed ledger:

```go
			Detail: map[string]any{
				"exit_code":       exitCode,
				"ok":              exitCode == 0,
				"duration_s":      0.0,
				"output_digest":   digest,
				"regions_total":   v.RegionsTotal,
				"regions_probed":  v.RegionsProbed,
				"dropped_regions": v.DroppedRegions,
			},
```

- [ ] **Step 6: Surface it at the CLI**

In `cmd/corral/certify_local.go`, extend `advVerdict` and `advVerdictFromPool` with the three fields, then print the shortfall in `renderAdvVerdict` when it is non-zero. Add to `advVerdictFromPool`'s `out` literal:

```go
		RegionsTotal: v.RegionsTotal, RegionsProbed: v.RegionsProbed,
		DroppedRegions: v.DroppedRegions,
```

Add matching fields to the `advVerdict` struct (with json tags matching the brain's wire shape: `regions_total`, `regions_probed`, `dropped_regions`), and in `renderAdvVerdict`, after the kill-rate line:

```go
	if v.RegionsTotal > 0 && v.RegionsProbed < v.RegionsTotal {
		fmt.Fprintf(w, "  PARTIAL AUDIT: %d of %d regions probed — these went unprobed: %s\n",
			v.RegionsProbed, v.RegionsTotal, strings.Join(v.DroppedRegions, "; "))
	}
```

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./internal/advpool/ -run 'TestShardDropped|TestVerdictCarriesRegionCoverage' -v`
Expected: PASS.

Run: `go test ./... -race`
Expected: all packages `ok`.

- [ ] **Step 8: Commit**

```bash
gofmt -l internal/advpool/ cmd/corral/
bash scripts/check-security.sh
git add internal/advpool/ cmd/corral/certify_local.go
git commit -m "feat(advpool): per-shard retry, drop, and coverage in the SIGNED record

A shard whose result will not parse is reopened MaxShardRetries times, then
DROPPED so the run proceeds on the shards that parsed. Straight-lining the
old abort-the-run semantics would make sharding actively worse: with 8 seats
the odds one misbehaves rise ~8x, and one flaky shard would waste the other
seven seats' spend.

Retries are keyed by task KEY, not id, so a lease-expiry re-claim and a parse
failure draw on the SAME budget — a shard cannot retry forever by alternating
failure modes.

RegionsTotal/RegionsProbed/DroppedRegions ride in the signed execution step,
and the CLI prints PARTIAL AUDIT when they disagree: a partial audit is
provably partial, never silently partial. An all-regions-failed run refuses
to grade rather than certifying on an empty exam."
```

---

### Task 5: Per-shard metrics, conditioned on complexity

**Files:**
- Modify: `internal/bugcatch/store.go` (three columns)
- Modify: `internal/advpool/driver.go` (`BugCatchObservation`, `bugCatchObservations`, per-shard emit)
- Test: `internal/bugcatch/store_test.go`, `internal/advpool/driver_test.go` (append)

**Interfaces:**
- Consumes: Task 4's shard state; `Shard.Complexity`/`.Lines` (Task 1).
- Produces: `BugCatchObservation` gains `Shard int`, `Region string`, `RegionComplexity int`, `RegionLines int`, `TestComplexity int`, `ParseRetries int`, `Dropped bool`. `bugcatch_observations` gains `shard`, `region`, `region_complexity`, `region_lines`, `test_complexity`.

- [ ] **Step 1: Write the failing test**

Append to `internal/advpool/driver_test.go`:

Reuse the file's existing `fakeBugCatch` — do not add a second sink type.

```go
// TestBugCatchRowsArePerShard proves the metrics keep every seat visible.
// Summing shards back into one generator row would collapse N seats into 1 and
// make an underperforming seat invisible BY CONSTRUCTION.
func TestBugCatchRowsArePerShard(t *testing.T) {
	const missionID = int64(220)
	scorer := &fakeScorer{devKillRate: 1.0}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m1", Code: "c1"}}}
	d := newShardedRun(t, missionID, 3, scorer, validator)
	sink := &fakeBugCatch{}
	d.BugCatch = sink
	d.Signer = &fakeSigner{} // BugCatch is fed on a terminal, signed verdict

	driveShardedToVerdict(t, d, missionID, "raw")

	var gen []BugCatchObservation
	for _, o := range sink.obs {
		if o.Role == RoleMutantGenerator {
			gen = append(gen, o)
		}
	}
	if len(gen) != 3 {
		t.Fatalf("want one generator row per shard (3), got %d — rows were summed", len(gen))
	}
	seenShards := map[int]bool{}
	for _, o := range gen {
		if seenShards[o.Shard] {
			t.Errorf("duplicate row for shard %d", o.Shard)
		}
		seenShards[o.Shard] = true
		if o.Region == "" {
			t.Errorf("shard %d: Region must name the symbols attacked", o.Shard)
		}
		if o.RegionComplexity <= 0 {
			t.Errorf("shard %d: RegionComplexity must be recorded as the difficulty control, got %d", o.Shard, o.RegionComplexity)
		}
	}
}
```

Check how the file's existing convergence tests wire `fakeSigner` (it may already be set by a helper) and match that — `BugCatch` is fed only on a terminal signed verdict, so a run with no signer records nothing and this test would fail for the wrong reason.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/advpool/ -run TestBugCatchRowsArePerShard -v`
Expected: FAIL — `o.Region undefined`, and once compiling, "want one generator row per shard (3), got 1".

- [ ] **Step 3: Record per-shard state in runState**

In `internal/advpool/driver.go`, add to `runState`:

```go
	// shardStats is per-shard generation outcome, keyed by shard index — the
	// metrics substrate. Recorded per shard and NEVER summed: summing collapses
	// N seats into one row and makes an underperforming seat invisible by
	// construction.
	shardStats map[int]shardStat
```

And the type near `runState`:

```go
// shardStat is one generator seat's recorded outcome. Region complexity is the
// DIFFICULTY CONTROL: raw yield cannot distinguish a weak model from an easy
// region, so effectiveness is read CONDITIONED on complexity, never pooled
// across it.
type shardStat struct {
	region       string
	complexity   int
	lines        int
	mutants      int
	parseRetries int
	dropped      bool
}
```

Initialize in `StartRun` alongside `shardRetries`, populating region/complexity/lines from the shards the DAG was built with — compute them once in `StartRun`:

```go
	shards := ShardSymbols(sigs, rs.MaxShards)
	stats := make(map[int]shardStat, len(shards))
	for _, sh := range shards {
		stats[sh.Index] = shardStat{
			region: strings.Join(sh.Symbols, ", "), complexity: sh.Complexity, lines: sh.Lines,
		}
	}
```

Assign `shardStats: stats` in the `runState` literal. Add `"strings"` to imports if absent.

In `tickDevAdequacy`, update the stat as each shard resolves — in the drop branch:

```go
			st := run.shardStats[shardIdx]
			st.parseRetries = run.shardRetries[key]
			st.dropped = true
			run.shardStats[shardIdx] = st
```

and after a successful parse:

```go
		st := run.shardStats[shardIdx]
		st.mutants = len(parsed)
		st.parseRetries = run.shardRetries[mgs[i].Key]
		run.shardStats[shardIdx] = st
```

- [ ] **Step 4: Emit one observation per shard**

Extend `BugCatchObservation`:

```go
type BugCatchObservation struct {
	Model, Role                                  string
	Catches, Opportunities                       int
	SoundTests, AuthoredTests                    int
	CriticFlags, MutantsPlanted, MutantsSurvived int
	// Per-shard generator dimensions (zero for the single-seat roles).
	Shard            int
	Region           string
	RegionComplexity int
	RegionLines      int
	TestComplexity   int
	ParseRetries     int
	Dropped          bool
	Shadow           bool // set by Task 6; a shadow seat NEVER gates
}
```

In `bugCatchObservations`, replace the single mutant-generator row with one per shard (keeping the single-row behavior when unsharded):

```go
	// mutant-generator: one row PER SHARD. Never summed — see shardStat.
	if len(run.shardStats) == 0 {
		out = append(out, BugCatchObservation{
			Model: v.ModelsByRole[RoleMutantGenerator], Role: RoleMutantGenerator,
			MutantsPlanted: v.MutantsTotal, MutantsSurvived: v.Survivors,
			TestComplexity: run.testComplexity,
		})
	} else {
		idxs := make([]int, 0, len(run.shardStats))
		for i := range run.shardStats {
			idxs = append(idxs, i)
		}
		sort.Ints(idxs)
		for _, i := range idxs {
			st := run.shardStats[i]
			out = append(out, BugCatchObservation{
				Model: v.ModelsByRole[RoleMutantGenerator], Role: RoleMutantGenerator,
				MutantsPlanted: st.mutants,
				Shard:          i,
				Region:         st.region,
				RegionComplexity: st.complexity,
				RegionLines:      st.lines,
				TestComplexity:   run.testComplexity,
				ParseRetries:     st.parseRetries,
				Dropped:          st.dropped,
			})
		}
	}
```

Note `MutantsSurvived` is deliberately NOT split per shard here: survival is measured against the unioned set by the Scorer, and attributing a survivor back to its shard is available from the `s<idx>/` ID prefix but is not needed for this slice's aggregate.

Add `testComplexity` to `runState`, computed once in `StartRun` from the dev test file:

```go
	// testComplexity is the dev suite's complexity — the SECOND conditioning
	// axis (a model that only wins against naive suites is a different
	// proposition from one that wins against rigorous ones).
	//
	// FILE-granular by necessity: attributing a specific test to a specific
	// region requires knowing which tests exercise which code, which is exactly
	// what the slice-5 tests-x-mutants matrix establishes by execution. Any
	// per-region test-complexity claim would be unproven until then.
	testComplexity int
```

Populate it in `StartRun`:

```go
	testComplexity := 0
	if testSigs, terr := repoindex.ExtractSignatures(rs.DevTestCode, rs.Lang); terr == nil {
		for _, s := range testSigs {
			testComplexity += s.Complexity
		}
	}
```

- [ ] **Step 5: Add the DuckDB columns**

In `internal/bugcatch/store.go`, extend the `CREATE TABLE` and the `INSERT`:

```go
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS bugcatch_observations (
		ts TIMESTAMP, record_id BIGINT, record_head VARCHAR, mission_id BIGINT,
		repo VARCHAR, commit VARCHAR, model VARCHAR, role VARCHAR, source VARCHAR,
		catches INTEGER, opportunities INTEGER, sound_tests INTEGER, authored_tests INTEGER,
		critic_flags INTEGER, mutants_planted INTEGER, mutants_survived INTEGER,
		shard INTEGER, region VARCHAR, region_complexity INTEGER, region_lines INTEGER,
		test_complexity INTEGER, parse_retries INTEGER, dropped BOOLEAN, shadow BOOLEAN
	)`); err != nil {
```

For an existing database, add idempotent migrations right after:

```go
	// Additive migration for ledgers created before swarm slice 2. Each column
	// is added independently and errors are ignored: DuckDB has no
	// ADD COLUMN IF NOT EXISTS, so a re-run on a migrated table errors
	// harmlessly rather than needing a schema probe.
	for _, col := range []string{
		"shard INTEGER", "region VARCHAR", "region_complexity INTEGER",
		"region_lines INTEGER", "test_complexity INTEGER", "parse_retries INTEGER",
		"dropped BOOLEAN", "shadow BOOLEAN",
	} {
		_, _ = db.Exec("ALTER TABLE bugcatch_observations ADD COLUMN " + col)
	}
```

Extend `Observation` with the matching fields and widen the `INSERT` placeholder list from 16 to 24, passing the new values in the same order as the column list.

- [ ] **Step 6: Emit the per-shard telemetry event**

In `tickDevAdequacy`, after the union loop completes and `run.shardStats` is final, emit one event per shard so the `--record` tape, the cockpit, and telemetry all get it from one write:

```go
	for _, i := range sortedShardIndexes(run.shardStats) {
		st := run.shardStats[i]
		d.emit(missionID, "pool_shard", st.region, map[string]any{
			"shard": i, "region": st.region,
			"region_complexity": st.complexity, "region_lines": st.lines,
			"mutants": st.mutants, "parse_retries": st.parseRetries, "dropped": st.dropped,
		})
	}
```

with the helper:

```go
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
```

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./internal/advpool/ -run TestBugCatchRowsArePerShard -v`
Expected: PASS.

Run: `go test ./internal/bugcatch/ ./internal/advpool/ -race`
Expected: both `ok`.

- [ ] **Step 8: Commit**

```bash
gofmt -l internal/advpool/ internal/bugcatch/
git add internal/advpool/ internal/bugcatch/
git commit -m "feat(bugcatch): per-shard rows conditioned on complexity

One bugcatch row PER SHARD, never summed: summing collapses N seats into one
row and makes an underperforming seat invisible by construction.

Rows carry the difficulty CONTROL (region_complexity, region_lines) plus
test_complexity, because raw per-shard yield cannot distinguish a weak model
from an easy region — effectiveness must be read CONDITIONED on complexity,
not pooled across it. Also the directly-attributable signals the region cannot
cause: parse_retries and dropped.

test_complexity is FILE-granular by necessity; per-region test attribution
needs the slice-5 tests-x-mutants matrix and would be unproven before it.

Additive columns + idempotent ALTERs; existing queries unaffected."
```

---

### Task 6: The shadow challenger

**Files:**
- Modify: `internal/advpool/roles.go` (shadow role key + specs)
- Modify: `internal/advpool/driver.go` (shadow scoring, paired rows)
- Modify: `cmd/corral/certify_local.go` (`--shadow-model`, chatter routing, readout)
- Test: `internal/advpool/driver_test.go` (append)

**Interfaces:**
- Consumes: everything above.
- Produces: `advpool.RoleMutantGeneratorShadow = "mutant-generator-shadow"`, `RunSpec.ShadowModel string`, `ShadowShardTaskKey(index int) string`.

**The load-bearing invariant:** shadow work has its own ROLE KEY, so `tasksByRole(missionID, RoleMutantGenerator)` structurally cannot return a shadow task. This is deliberately not a boolean on the task — this is the gate, and a flag is a thing someone forgets to check at one of four call sites.

- [ ] **Step 1: Write the failing test**

Append to `internal/advpool/driver_test.go`:

```go
// TestShadowMutantsNeverReachTheGate is THE invariant test. A shadow seat's
// mutants must never influence DevKillRate, Survivors, MutantsTotal, or Status.
func TestShadowMutantsNeverReachTheGate(t *testing.T) {
	const missionID = int64(230)
	scorer := &fakeScorer{devKillRate: 1.0}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m1", Code: "c1"}}}
	d := newShadowedRun(t, missionID, 2, "challenger-model", scorer, validator)

	primary, err := d.tasksByRole(missionID, RoleMutantGenerator)
	if err != nil {
		t.Fatalf("tasksByRole: %v", err)
	}
	if len(primary) != 2 {
		t.Fatalf("want 2 primary seats, got %d", len(primary))
	}
	for _, mg := range primary {
		if strings.Contains(mg.Key, "shadow") {
			t.Fatalf("a shadow task leaked into the PRIMARY role lookup: %q", mg.Key)
		}
	}

	shadow, err := d.tasksByRole(missionID, RoleMutantGeneratorShadow)
	if err != nil {
		t.Fatalf("tasksByRole(shadow): %v", err)
	}
	if len(shadow) != 2 {
		t.Fatalf("want 2 shadow seats, got %d", len(shadow))
	}
	for _, sh := range shadow {
		if sh.Model != "challenger-model" {
			t.Errorf("shadow seat model: want %q, got %q", "challenger-model", sh.Model)
		}
	}

	// Every seat — primary and shadow — returns the SAME single canned mutant
	// via fakeValidator. There are 2 primary seats and 2 shadow seats, so if
	// shadow mutants reached the gate MutantsTotal would be 4, not 2.
	driveShardedToVerdict(t, d, missionID, "raw")
	st, ok := d.RunStatus(missionID)
	if !ok || st.Verdict == nil {
		t.Fatal("run did not converge to a verdict")
	}
	v := *st.Verdict
	if v.MutantsTotal != 2 {
		t.Fatalf("MutantsTotal: want 2 (primary seats only), got %d — SHADOW MUTANTS REACHED THE GATE", v.MutantsTotal)
	}
	if v.RegionsTotal != 2 {
		t.Errorf("RegionsTotal: want 2 (primary seats), got %d", v.RegionsTotal)
	}
}

// TestShadowRowsArePairedAndFlagged proves the comparison data lands, marked.
func TestShadowRowsArePairedAndFlagged(t *testing.T) {
	const missionID = int64(231)
	scorer := &fakeScorer{devKillRate: 1.0}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m1", Code: "c1"}}}
	d := newShadowedRun(t, missionID, 2, "challenger-model", scorer, validator)
	sink := &fakeBugCatch{}
	d.BugCatch = sink
	d.Signer = &fakeSigner{}

	driveShardedToVerdict(t, d, missionID, "raw")

	byShard := map[int][]BugCatchObservation{}
	for _, o := range sink.obs {
		if o.Role == RoleMutantGenerator || o.Role == RoleMutantGeneratorShadow {
			byShard[o.Shard] = append(byShard[o.Shard], o)
		}
	}
	for idx, rows := range byShard {
		if len(rows) != 2 {
			t.Errorf("shard %d: want a PAIR (primary + shadow), got %d rows", idx, len(rows))
		}
		var regions []string
		shadows := 0
		for _, r := range rows {
			regions = append(regions, r.Region)
			if r.Shadow {
				shadows++
			}
		}
		if shadows != 1 {
			t.Errorf("shard %d: want exactly 1 shadow row, got %d", idx, shadows)
		}
		// The whole point: SAME region, so the comparison is not confounded.
		if regions[0] != regions[1] {
			t.Errorf("shard %d: paired rows must name the SAME region, got %q vs %q", idx, regions[0], regions[1])
		}
	}
}
```

Add the run constructor:

```go
// newShadowedRun mirrors newShardedRun but sets a challenger model, so
// BuildDAG also emits the shadow seats.
func newShadowedRun(t *testing.T, missionID int64, maxShards int, shadowModel string, scorer *fakeScorer, validator Validator) *Driver {
	t.Helper()
	q := newTestQueue(t)
	d, err := NewDriver(q, scorer, validator, decorrelatedAssign(), 0.5)
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}
	rs, sigs := shardedRunSpec(maxShards)
	rs.ShadowModel = shadowModel
	if err := d.StartRun(missionID, rs, sigs); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if _, err := q.PromoteReady(missionID); err != nil {
		t.Fatalf("PromoteReady: %v", err)
	}
	return d
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/advpool/ -run TestShadow -v`
Expected: FAIL — `undefined: RoleMutantGeneratorShadow`, `rs.ShadowModel undefined`.

- [ ] **Step 3: Add the shadow role and specs**

In `internal/advpool/roles.go`:

```go
// RoleMutantGeneratorShadow is the CHALLENGER generator seat: a second model
// attacking the SAME region as its primary, for a region-controlled head-to-head.
//
// It is a DISTINCT role key on purpose. tasksByRole(RoleMutantGenerator)
// therefore CANNOT return a shadow task — the exclusion is structural, not a
// boolean someone must remember to check at each of four call sites. This is
// the gate; a flag would be the wrong mechanism.
//
// Assigning different models to different SHARDS instead would be no comparison
// at all: it is confounded by region exactly as raw per-shard yield is, and it
// would blend the exam's difficulty (the generator SETS the difficulty, so a
// weaker model on one shard plants easier mutants, the dev suite kills them,
// and the kill-rate rises) under a fixed certification threshold.
const RoleMutantGeneratorShadow = "mutant-generator-shadow"

// ShadowShardTaskKey is the queue key for the challenger seat on shard i.
func ShadowShardTaskKey(index int) string {
	return RoleMutantGeneratorShadow + "/" + strconv.Itoa(index)
}

// ShadowShardIndexFromKey mirrors ShardIndexFromKey for challenger seats.
func ShadowShardIndexFromKey(key string) (int, bool) {
	rest, ok := strings.CutPrefix(key, RoleMutantGeneratorShadow+"/")
	if !ok {
		return 0, false
	}
	i, err := strconv.Atoi(rest)
	if err != nil || i < 0 {
		return 0, false
	}
	return i, true
}
```

Add `RunSpec.ShadowModel string` in `run.go`:

```go
	// ShadowModel is the CHALLENGER generator model. When set, every shard is
	// attacked a second time by this model for a region-controlled head-to-head.
	// Shadow mutants are parsed, scored, and recorded, but NEVER feed the
	// verdict — the exam's difficulty stays set by the primary model alone, so
	// certification means exactly what it meant before. "" disables.
	ShadowModel string
```

In `BuildDAG`, inside the sharded branch, after appending each primary spec:

```go
			if strings.TrimSpace(rs.ShadowModel) != "" {
				for _, sh := range shards {
					specs = append(specs, queue.TaskSpec{
						Key:         ShadowShardTaskKey(sh.Index),
						Role:        RoleMutantGeneratorShadow,
						Title:       "Challenger: " + shardTitle(sh),
						Instruction: renderMutantGeneratorShard(rs, sigs, sh),
						Model:       rs.ShadowModel,
					})
				}
			}
```

- [ ] **Step 4: Score shadow mutants without gating**

In `tickDevAdequacy`, AFTER the primary scoring completes (after `run.devSurvivors = survivors`), add a separate shadow pass. It uses the same Scorer but writes only to `run.shadowStats`:

```go
	// The challenger pass: score the shadow seats' mutants against the SAME dev
	// suite so the comparison measures POTENCY (mutants that survive a good
	// suite), not merely output volume. Results are recorded and never
	// aggregated into the verdict — the exam stays the primary model's.
	//
	// A shadow failure is NEVER fatal: it is measurement, not the gate. Errors
	// are logged and the seat is skipped.
	if strings.TrimSpace(run.rs.ShadowModel) != "" {
		shadows, serr := d.tasksByRole(missionID, RoleMutantGeneratorShadow)
		if serr != nil {
			log.Printf("advpool: run %d: shadow seats unavailable (measurement only): %v", missionID, serr)
		}
		for i := range shadows {
			if shadows[i].Status != queue.StatusDone {
				continue
			}
			idx, _ := ShadowShardIndexFromKey(shadows[i].Key)
			parsed, perr := d.Validator.ParseMutants(shadows[i].Result, run.rs.Code)
			if perr != nil {
				st := run.shadowStats[idx]
				st.parseRetries++
				st.dropped = true
				run.shadowStats[idx] = st
				continue
			}
			_, shadowSurvivors, sserr := d.Scorer.Score(ctx, run.rs.CodePath, run.rs.Code, run.rs.DevTestCode, parsed, run.rs.TestCmd)
			if sserr != nil {
				log.Printf("advpool: run %d: shadow shard %d scoring failed (measurement only): %v", missionID, idx, sserr)
				continue
			}
			st := run.shadowStats[idx]
			st.mutants = len(parsed)
			st.survived = len(shadowSurvivors)
			run.shadowStats[idx] = st
		}
	}
```

Add `survived int` to `shardStat`, and `shadowStats map[int]shardStat` to `runState`, initialized in `StartRun` with the same region/complexity/lines as `shardStats` (the SAME regions — that is the point).

Gate the all-terminal wait so it never waits on shadow seats: `tasksByRole(missionID, RoleMutantGenerator)` already excludes them structurally, so no change is needed — verify this by the invariant test rather than by inspection.

- [ ] **Step 5: Emit paired rows**

In `bugCatchObservations`, after the primary shard rows, append the shadow rows:

```go
	for _, i := range sortedShardIndexes(run.shadowStats) {
		st := run.shadowStats[i]
		out = append(out, BugCatchObservation{
			Model: run.rs.ShadowModel, Role: RoleMutantGeneratorShadow,
			MutantsPlanted: st.mutants, MutantsSurvived: st.survived,
			Shard: i, Region: st.region,
			RegionComplexity: st.complexity, RegionLines: st.lines,
			TestComplexity:   run.testComplexity,
			ParseRetries:     st.parseRetries, Dropped: st.dropped,
			Shadow:           true,
		})
	}
```

Pass `Shadow` through to the `shadow` column in `internal/bugcatch/store.go`'s INSERT.

- [ ] **Step 6: Wire the CLI**

In `cmd/corral/certify_local.go`:

```go
	shadowModelFlag := fs.String("shadow-model", "", "challenger model that attacks every region a SECOND time for a region-controlled head-to-head (default "+defaultLocalShadowModel+"; \"off\" disables). Recorded for comparison — NEVER gates the verdict")
```

Add the default constant next to the other role models:

```go
// defaultLocalShadowModel is the challenger seat's model. Cheap and already the
// critic's model, so it needs no additional provider credential.
const defaultLocalShadowModel = "claude-haiku-4-5"
```

Resolve it, set `rs.ShadowModel`, route it in `localChatterFor` (add `RoleMutantGeneratorShadow` to the `assign` map so the switcher picks the challenger model), and announce it after the `regions:` line:

```go
	if shadow := resolveShadowModel(*shadowModelFlag); shadow != "" {
		fmt.Fprintf(stdout, "shadow: %d challenger seats (%s) — recorded, never gating\n", len(shards), shadow)
	}
```

with:

```go
// resolveShadowModel resolves the challenger model: the operator's
// --shadow-model, "off" to disable, else the stock default.
func resolveShadowModel(flag string) string {
	f := strings.TrimSpace(flag)
	switch f {
	case "off", "none":
		return ""
	case "":
		return defaultLocalShadowModel
	}
	return f
}
```

Add `RoleMutantGeneratorShadow: shadow` to the `assign` map built before `CheckDecorrelation`. Confirm `CheckDecorrelation` still passes — it only compares critic vs writer, so an additive role is safe, but the shadow model equalling the critic model is expected and must NOT error.

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./internal/advpool/ -run TestShadow -v`
Expected: PASS (both).

Run: `go test ./... -race`
Expected: all `ok`.

- [ ] **Step 8: Full gate + commit**

```bash
gofmt -l .
bash scripts/check-security.sh
go build ./...
git add internal/advpool/ internal/bugcatch/ cmd/corral/certify_local.go
git commit -m "feat(advpool): shadow challenger — region-controlled model head-to-head

Every shard is attacked a SECOND time by a challenger model on the SAME
region, so the comparison is not confounded by region difficulty the way
'a different model per shard' would be — and the exam's difficulty stays set
by the primary model alone, so certification means exactly what it did.

Shadow mutants are parsed AND scored (potency: mutants that survive a good
suite is the real measure of an adversary), recorded as paired rows flagged
shadow=true, and never aggregated into the verdict.

Excluded STRUCTURALLY via its own role key: tasksByRole(RoleMutantGenerator)
cannot return a shadow task. This is the gate; a boolean would be a thing
someone forgets to check at one of four call sites. Pinned by an invariant
test that would catch shadow mutants inflating MutantsTotal.

Default challenger claude-haiku-4-5 (already the critic's model, so no new
credential); --shadow-model off disables. A shadow failure is never fatal."
```

---

### Task 7: End-to-end verification on a real repository

**Files:** none modified — this is the proof gate before deploy.

- [ ] **Step 1: Confirm the full suite is green under race**

Run: `go test ./... -race`
Expected: every package `ok`.

- [ ] **Step 2: Confirm the deploy gate passes locally**

Run: `gofmt -l . && bash scripts/check-security.sh`
Expected: `gofmt -l` prints nothing; the security script exits 0.

Both of these have failed deploys before — do not skip them.

- [ ] **Step 3: Run a real sharded audit**

Use the `more-itertools` recipe from the prior session (a Python repo with a large, many-function file). Export the key and run under a FRESH `HOME` so the DuckDB ledger does not contend with a running brain:

```bash
export ANTHROPIC_API_KEY="$(ssh hetzner 'sudo -n systemd-creds decrypt /etc/credstore.encrypted/anthropic-api-key -')"
HOME=$(mktemp -d) go run ./cmd/corral certify --local \
  --repo-dir /path/to/more-itertools \
  --code more_itertools/recipes.py \
  --test tests/test_recipes.py \
  --goal "the recipes behave as documented" \
  --max-shards 6 \
  --record /tmp/sharded-run.json \
  -- python3 -m pytest tests/test_recipes.py
```

Expected in the output:
- `swarm: N concurrent workers …`
- `regions: 6 generator seats over M functions`
- `shadow: 6 challenger seats (claude-haiku-4-5) — recorded, never gating`
- A dev kill-rate, and either a clean certify or `needs-review` — **not** a `PARTIAL AUDIT` line unless a shard genuinely failed.

- [ ] **Step 4: Verify the metrics landed per shard**

```bash
HOME=$(mktemp -d) go run ./cmd/corral scorecard
```

Expected: generator rows per shard with distinct regions, and paired shadow rows on the same regions.

- [ ] **Step 5: Verify the tape renders**

Confirm `/tmp/sharded-run.json` contains `pool_shard` events with `region_complexity`, and that the parallel seats appear in the cockpit replay.

- [ ] **Step 6: Deploy and watch**

**This changes prod behavior on the landing commit** — the hosted brain pool now shards too. Push, then watch the run to completion and confirm the conclusion explicitly:

```bash
gh run watch <id>
gh run view <id> --json conclusion
```

`gh run watch` can print exit 0 on a FAILED run — the `--json conclusion` check is the real gate, not the watch.

- [ ] **Step 7: Update the docs**

Per the standing directive, update `ROADMAP.md` (move swarm slice 2 to shipped) and re-review `README.md` for anything now inaccurate — including the `certify --local` flag list, which gained `--max-shards` and `--shadow-model`. Stay honest: do not advertise the slice-4 optimizer or the slice-5 matrix as built.

```bash
git add ROADMAP.md README.md
git commit -m "docs: swarm slice 2 shipped — sharded generation + shadow challenger"
```

---

## Notes for the implementer

**On the existing test file's helpers:** `internal/advpool/driver_test.go` is 1,331 lines and already provides everything the new tests build on — `newTestQueue`, `claimAllReady`, `claimByKey`, `mustComplete`, `decorrelatedAssign`, `fakeScorer`, `fakeValidator`, `fakeSigner`, `fakeBugCatch`, `obsFor`. The test code in this plan is written against those exact names. **Do not add parallel machinery**; the only new fixtures are `shardedRunSpec`, `newShardedRun`, `newShadowedRun`, `completeAllReady`, `driveShardedToVerdict` (Task 3) and `shardValidator` (Task 4).

Note that the file's own `newTestDriver(t, missionID, scorer, validator, threshold)` starts its run with **nil signatures**, so it can never shard — which is why the sharded tests need their own constructor rather than wrapping it.

**On the fake Validator:** `fakeValidator.ParseMutants` ignores the raw string and returns the same canned `f.mutants` on every call. That is *useful* for Task 3 (every shard returns an identically-named mutant, so only prefixing keeps them distinct), but it cannot express "one shard fails, the others succeed" — hence `shardValidator` in Task 4.

**On asserting mutant IDs:** `runState.devSurvivors` comes from `fakeScorer`'s canned survivor list, **not** from the unioned mutants. Assert prefixing on `scorer.calls[0].mutants` — the set actually handed to the Scorer.

**On `d.runs[missionID]`:** direct access is the established pattern in this file (see the existing `d.runs[2].testWriterTaskID` usages), so the new tests follow it. It is safe here because `Tick` is called synchronously from the test goroutine.

**Why the order matters:** Tasks 0–4 are the gate-critical path and are shippable on their own as slice 2 proper. Tasks 5–6 are the metrics/comparison layer the parent spec had deferred to slice 4, pulled forward because runs made before it exists produce data that cannot answer "did this agent underperform." If the work needs to be cut short, **stop cleanly after Task 4** — that is a coherent, deployable slice.
