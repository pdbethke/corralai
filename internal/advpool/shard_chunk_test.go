// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/repoindex"
	"github.com/pdbethke/corralai/internal/testgen"
)

// twoFuncPythonFile is the fixture every test in this file starts from: a
// real import preamble followed by two functions whose own bodies are
// mutually exclusive substrings, so a shard's prompt either contains one
// function's body or it does not — there is no ambiguous overlap.
//
// Line numbers (1-indexed), matched by the Signature fixtures below:
//
//	1: import os
//	2: import sys
//	3: (blank)
//	4: (blank)
//	5: def alpha(x):
//	6:     y = x + 1
//	7:     return y
//	8: (blank)
//	9: (blank)
//
// 10: def beta(y):
// 11:     z = y * 2
// 12:     return z
const twoFuncPythonFile = "import os\nimport sys\n\n\ndef alpha(x):\n    y = x + 1\n    return y\n\n\ndef beta(y):\n    z = y * 2\n    return z\n"

func twoFuncPythonRunSpec() (RunSpec, []repoindex.Signature) {
	rs := RunSpec{
		Repo: "r", Commit: "c", Goal: "g",
		CodePath: "a.py", Code: twoFuncPythonFile,
		DevTestPath: "a_test.py", DevTestCode: "def test_ok(): pass\n",
		NMutants: 1, Lang: "python", MaxShards: 2,
	}
	sigs := []repoindex.Signature{
		{Name: "alpha", Complexity: 1, Line: 5, Lines: 3},
		{Name: "beta", Complexity: 1, Line: 10, Lines: 3},
	}
	return rs, sigs
}

// newShardedRunWithSpec mirrors newShardedRun (driver_test.go) but takes the
// RunSpec/signatures directly, for a fixture (twoFuncPythonRunSpec) that
// carries real Line/Lines values rather than shardedRunSpec's synthetic ones.
func newShardedRunWithSpec(t *testing.T, missionID int64, scorer *fakeScorer, validator Validator, rs RunSpec, sigs []repoindex.Signature) *Driver {
	t.Helper()
	q := newTestQueue(t)
	d, err := NewDriver(q, scorer, validator, decorrelatedAssign(), 0.5)
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}
	if err := d.StartRun(missionID, rs, sigs); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if _, err := q.PromoteReady(missionID); err != nil {
		t.Fatalf("PromoteReady: %v", err)
	}
	return d
}

// shardByFirstSymbol returns the Shard from ShardSymbols(sigs, maxShards)
// whose Symbols contains want — the two-symbol fixture always splits one
// symbol per shard at maxShards 2, but never by a guaranteed index order.
func shardByFirstSymbol(t *testing.T, sigs []repoindex.Signature, maxShards int, want string) Shard {
	t.Helper()
	for _, sh := range ShardSymbols(sigs, maxShards) {
		for _, s := range sh.Symbols {
			if s == want {
				return sh
			}
		}
	}
	t.Fatalf("no shard aimed at %q", want)
	return Shard{}
}

// TestShardPromptContainsOnlyItsOwnSymbolBody is the core hunk-native claim:
// a generator shard aimed at `alpha` sees alpha's body and NOT beta's — the
// prompt shows the shard's own symbols, not the whole file.
func TestShardPromptContainsOnlyItsOwnSymbolBody(t *testing.T) {
	rs, sigs := twoFuncPythonRunSpec()
	shA := shardByFirstSymbol(t, sigs, rs.MaxShards, "alpha")
	shB := shardByFirstSymbol(t, sigs, rs.MaxShards, "beta")

	promptA, chunkedA := renderMutantGeneratorShard(rs, sigs, shA)
	promptB, chunkedB := renderMutantGeneratorShard(rs, sigs, shB)

	if !chunkedA || !chunkedB {
		t.Fatalf("both shards have Lines set on their only signature and must be chunked: chunkedA=%v chunkedB=%v", chunkedA, chunkedB)
	}
	if !strings.Contains(promptA, "y = x + 1") {
		t.Errorf("shard A's prompt must contain alpha's own body:\n%s", promptA)
	}
	if strings.Contains(promptA, "z = y * 2") {
		t.Errorf("shard A's prompt must NOT contain beta's body — the whole point of chunking is that a shard sees only its own symbols:\n%s", promptA)
	}
	if !strings.Contains(promptB, "z = y * 2") {
		t.Errorf("shard B's prompt must contain beta's own body:\n%s", promptB)
	}
	if strings.Contains(promptB, "y = x + 1") {
		t.Errorf("shard B's prompt must NOT contain alpha's body:\n%s", promptB)
	}
}

// TestShardPromptCarriesThePreambleInBoth proves the file's import header
// rides onto EVERY shard's slice, not just the one that happens to be first
// in the file — a shard sees the preamble plus its own symbols, never the
// preamble alone or the whole file.
func TestShardPromptCarriesThePreambleInBoth(t *testing.T) {
	rs, sigs := twoFuncPythonRunSpec()
	shA := shardByFirstSymbol(t, sigs, rs.MaxShards, "alpha")
	shB := shardByFirstSymbol(t, sigs, rs.MaxShards, "beta")

	promptA, _ := renderMutantGeneratorShard(rs, sigs, shA)
	promptB, _ := renderMutantGeneratorShard(rs, sigs, shB)

	for _, want := range []string{"import os", "import sys"} {
		if !strings.Contains(promptA, want) {
			t.Errorf("shard A's prompt must carry the file's preamble (%q):\n%s", want, promptA)
		}
		if !strings.Contains(promptB, want) {
			t.Errorf("shard B's prompt must carry the file's preamble (%q):\n%s", want, promptB)
		}
	}
}

// TestShardWithUnpopulatedLinesFallsBackToWholeFile proves a shard whose
// signature never got a Lines count (an extractor gap, or hand-built
// Signature that never ran through repoindex) falls back to showing the
// WHOLE file rather than silently hiding code from the model with no way for
// a reader to know it was cut.
func TestShardWithUnpopulatedLinesFallsBackToWholeFile(t *testing.T) {
	rs, sigs := twoFuncPythonRunSpec()
	sigs[0].Lines = 0 // alpha's extractor never populated Lines
	shA := shardByFirstSymbol(t, sigs, rs.MaxShards, "alpha")

	prompt, chunked := renderMutantGeneratorShard(rs, sigs, shA)
	if chunked {
		t.Fatal("a shard with Lines==0 on its signature must fall back to the whole file, not chunk")
	}
	if !strings.Contains(prompt, "z = y * 2") {
		t.Errorf("the fallback shard prompt must carry the WHOLE file (including beta's body), got:\n%s", prompt)
	}
}

// TestPromptShapeFileWhenAnyShardFellBack drives a real run to its terminal
// Verdict and checks PromptShape at the disclosure boundary: "file" whenever
// even one shard fell back to the whole file, per the binding rule "chunk
// only when EVERY shard of the file was sliced".
func TestPromptShapeFileWhenAnyShardFellBack(t *testing.T) {
	const missionID = int64(1)
	rs, sigs := twoFuncPythonRunSpec()
	sigs[1].Lines = 0 // beta's extractor never populated Lines
	scorer := &fakeScorer{devKillRate: 1.0}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m1", Replace: "c1"}}}
	d := newShardedRunWithSpec(t, missionID, scorer, validator, rs, sigs)

	v := driveShardedToVerdict(t, d, missionID, "raw mutant-generator output")
	if v.PromptShape != "file" {
		t.Fatalf("PromptShape = %q, want %q (beta's shard fell back)", v.PromptShape, "file")
	}
}

// TestPromptShapeChunkWhenEveryShardSliced is the positive case: every
// signature carries a real Lines span, so every shard actually chunked, and
// the run's disclosed PromptShape says so.
func TestPromptShapeChunkWhenEveryShardSliced(t *testing.T) {
	const missionID = int64(2)
	rs, sigs := twoFuncPythonRunSpec()
	scorer := &fakeScorer{devKillRate: 1.0}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m1", Replace: "c1"}}}
	d := newShardedRunWithSpec(t, missionID, scorer, validator, rs, sigs)

	v := driveShardedToVerdict(t, d, missionID, "raw mutant-generator output")
	if v.PromptShape != "chunk" {
		t.Fatalf("PromptShape = %q, want %q (every shard had a real Lines span)", v.PromptShape, "chunk")
	}
}

// TestAnchorNotUniqueStillRunsAgainstTheWholeFile is the binding constraint:
// chunking a shard's PROMPT must never touch the SEARCH-anchor uniqueness
// check, which validates a generator's output against the whole original
// file regardless of what the model was shown. A SEARCH string that would be
// unique inside one function's own body but repeats across the file must
// still be rejected as AnchorNotUnique.
func TestAnchorNotUniqueStillRunsAgainstTheWholeFile(t *testing.T) {
	original := "def alpha(x):\n    return x\n\n\ndef beta(y):\n    return x\n"
	raw := "===MUTATION_1===\n<<<<<<< SEARCH\n    return x\n=======\n    return -x\n>>>>>>> REPLACE\n===END_1===\n"

	_, err := testgen.ParseMutantsOutput(raw, original)
	if err == nil {
		t.Fatal("expected an error: \"    return x\" occurs twice in the whole file and is not a valid anchor")
	}
	if !strings.Contains(err.Error(), "anchor-not-unique") {
		t.Fatalf("error = %v, want it to name anchor-not-unique", err)
	}
}

// TestShardCodeDoesNotDuplicateAPreambleOverlap: the preamble is emitted
// first and `shown` is set to its line count, so a symbol whose span STARTS
// inside it was written out a second time — the model saw the same lines
// twice and the elision arithmetic (sp.from-1-shown) went negative, hiding
// that anything had happened. A doc comment the language's preamble scanner
// consumes and the extractor counts as part of the declaration is exactly
// that shape.
func TestShardCodeDoesNotDuplicateAPreambleOverlap(t *testing.T) {
	// 1: package p
	// 2: (blank)
	// 3: // Alpha does a thing      <- consumed by goPlugin.Preamble
	// 4: func Alpha() int {
	// 5:     return 1
	// 6: }
	code := "package p\n\n// Alpha does a thing\nfunc Alpha() int {\n\treturn 1\n}\n"
	sigs := []repoindex.Signature{{Name: "Alpha", Line: 3, Lines: 4}} // span 3-6, overlapping the preamble

	got, chunked := shardCode(langFor(RunSpec{Lang: "go"}), code, sigs)
	if !chunked {
		t.Fatalf("the signature has a real Lines span and must chunk, got the whole-file fallback:\n%s", got)
	}
	if n := strings.Count(got, "// Alpha does a thing"); n != 1 {
		t.Errorf("the doc comment appears %d times, want 1 — the preamble already emitted it:\n%s", n, got)
	}
	if !strings.Contains(got, "return 1") {
		t.Errorf("the symbol's own body must still be shown:\n%s", got)
	}
	if strings.Contains(got, "lines elided") {
		t.Errorf("nothing was cut between the preamble and a span that overlaps it, so no elision note belongs:\n%s", got)
	}
}

// TestShardCodeWithAnEmptyPreambleHasNoLeadingBlankLine: a file that begins
// straight at a definition has no preamble at all, and the unconditional
// newline after it opened every such shard's view with a blank line — a
// cosmetic lie about the file's first line that the elision arithmetic then
// counted as shown.
func TestShardCodeWithAnEmptyPreambleHasNoLeadingBlankLine(t *testing.T) {
	code := "def alpha(x):\n    return x\n\n\ndef beta(y):\n    return y\n"
	sigs := []repoindex.Signature{{Name: "alpha", Line: 1, Lines: 2}}

	got, chunked := shardCode(langFor(RunSpec{Lang: "python"}), code, sigs)
	if !chunked {
		t.Fatalf("want a chunked slice, got the whole-file fallback:\n%s", got)
	}
	if strings.HasPrefix(got, "\n") {
		t.Errorf("the slice opens with a blank line although the file has no preamble:\n%q", got)
	}
	if !strings.HasPrefix(got, "def alpha(x):") {
		t.Errorf("the slice must open at the file's own first line, got:\n%q", got)
	}
}

// longTwoFuncPythonFile is a realistic two-function file — bodies long enough
// that showing one function costs far less than showing both, which is the
// entire economic claim chunking makes.
func longTwoFuncPythonFile() (string, []repoindex.Signature) {
	var b strings.Builder
	b.WriteString("import os\nimport sys\n\n\n")
	sigs := []repoindex.Signature{}
	line := 5
	for _, name := range []string{"alpha", "beta"} {
		fmt.Fprintf(&b, "def %s(x):\n", name)
		for i := 0; i < 40; i++ {
			fmt.Fprintf(&b, "    x = x + %d  # %s step %d\n", i, name, i)
		}
		b.WriteString("    return x\n\n\n")
		sigs = append(sigs, repoindex.Signature{Name: name, Complexity: 1, Line: line, Lines: 42})
		line += 44
	}
	return b.String(), sigs
}

// TestShardPromptIsStrictlySmallerThanTheWholeFile is the cost claim, stated
// as an inequality rather than left to inference from "it contains one body
// and not the other". A slice that showed one function and then elided
// nothing would satisfy every containment assertion in this file and still
// send the whole file's worth of tokens.
func TestShardPromptIsStrictlySmallerThanTheWholeFile(t *testing.T) {
	code, sigs := longTwoFuncPythonFile()
	rs := RunSpec{
		Repo: "r", Commit: "c", Goal: "g",
		CodePath: "a.py", Code: code,
		DevTestPath: "a_test.py", DevTestCode: "def test_ok(): pass\n",
		NMutants: 1, Lang: "python", MaxShards: 2,
	}
	sh := shardByFirstSymbol(t, sigs, rs.MaxShards, "alpha")

	shardPrompt, chunked := renderMutantGeneratorShard(rs, sigs, sh)
	if !chunked {
		t.Fatal("both signatures carry a real Lines span, so the shard must chunk")
	}
	// The SAME shard rendered with no chunkable span — the whole-file prompt
	// this shard would otherwise have sent. Rendered through the same
	// function so the comparison is prompt to prompt, not prompt to source.
	blind := append([]repoindex.Signature(nil), sigs...)
	for i := range blind {
		blind[i].Lines = 0
	}
	wholeFilePrompt, stillChunked := renderMutantGeneratorShard(rs, blind, sh)
	if stillChunked {
		t.Fatal("with Lines zeroed the render must fall back to the whole file")
	}
	if len(shardPrompt) >= len(wholeFilePrompt) {
		t.Errorf("the chunked prompt is %d bytes and the whole-file prompt is %d — chunking must send STRICTLY less, or the fan-out's cost argument does not hold",
			len(shardPrompt), len(wholeFilePrompt))
	}
}
