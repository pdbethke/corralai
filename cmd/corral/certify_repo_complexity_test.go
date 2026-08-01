// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"encoding/json"
	"testing"
)

// Complexity is cyclomatic-style: 1 + the branch/loop/case/catch/boolean-operator
// nodes in a symbol's tree-sitter subtree. That is the same decision-point
// approximation gocyclo, radon and eslint's `complexity` rule all use in
// practice (true McCabe walks a control-flow graph; essentially no production
// tool does). So the METHOD is standard — but the numbers are comparable within
// corral, not against a specific tool's report, because tools disagree about
// whether `else`, `default`, ternaries and boolean operators count.
//
// It is only computed where a signature extractor exists. Go, Python, Ruby,
// JavaScript and TypeScript have one; the remaining grammars repoindex can
// PARSE (rust, java, c, cpp, csharp, php, bash) do not, so ExtractSignatures
// returns "no signature extractor for language" for them — verified
// empirically, not assumed.
//
// So complexity MUST be nullable. Emitting 0, or the floor of 1, for an
// unmeasured language would render "this code is trivial" where the truth is
// "corral never measured it" — the same not-measured-as-measurement shape as a
// NULL kill rate printed as 0.00, and as the `evidence=paired` label applied to
// files that were never paired.
func TestFileComplexity_AbsentWhereNotMeasured(t *testing.T) {
	// Languages repoindex can parse but has no signature extractor for. The
	// original examples here were ruby/javascript/typescript; those now HAVE
	// extractors, so the property is re-pinned against languages where it is
	// still true rather than deleted.
	for _, lang := range []string{"rust", "java", "php"} {
		got := fileComplexity("whatever.src", []byte("fn f(a: i32) -> i32 { if a > 0 { 1 } else { 0 } }"), lang)
		if got != nil {
			t.Errorf("%s complexity = %+v, want nil — no signature extractor exists, so any number would be a fabricated measurement", lang, got)
		}
	}
}

// TestFileComplexity_PresentForPython pins that a measured language really is
// measured, and that a branch-heavy function does NOT score the floor of 1 —
// the assertion that would catch a silently-disabled node set.
func TestFileComplexity_PresentForPython(t *testing.T) {
	src := []byte("def f(a):\n    if a:\n        for x in a:\n            if x:\n                return 1\n    return 0\n")
	got := fileComplexity("a.py", src, "python")
	if got == nil {
		t.Fatal("python complexity = nil, want a measurement")
	}
	if got.Symbols != 1 {
		t.Errorf("Symbols = %d, want 1", got.Symbols)
	}
	if got.Max <= 1 {
		t.Errorf("Max = %d, want > 1 — a function with two ifs and a loop is not straight-line; a floor of 1 means the node set is not matching", got.Max)
	}
	if got.Total < got.Max {
		t.Errorf("Total(%d) < Max(%d), which is impossible", got.Total, got.Max)
	}
}

// TestFileComplexity_ReportsTermsNotAScore pins the same discipline the funnel
// has: Max and Total are BOTH carried rather than collapsed. Summing tracks file
// length (a long file of getters outranks a short file with one hairy branch);
// max names the worst symbol. Which one should drive a UI's ranking is a
// judgement, so the API supplies both and lets the consumer choose instead of
// baking one in.
func TestFileComplexity_ReportsTermsNotAScore(t *testing.T) {
	src := []byte("def a(x):\n    if x:\n        return 1\n    return 0\n\ndef b(y):\n    return y\n")
	got := fileComplexity("a.py", src, "python")
	if got == nil {
		t.Fatal("expected a measurement")
	}
	if got.Symbols != 2 {
		t.Fatalf("Symbols = %d, want 2", got.Symbols)
	}
	if got.Max >= got.Total {
		t.Errorf("Max(%d) should be < Total(%d) across two symbols where one branches", got.Max, got.Total)
	}

	// And it must serialise as absent, not as zeroes, when unmeasured.
	blob, err := json.Marshal(auditableJSON{Path: "x.rb", Lang: "ruby"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if s := string(blob); contains(s, "complexity") {
		t.Errorf("unmeasured file serialised a complexity key: %s", s)
	}
}

func contains(hay, needle string) bool {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
