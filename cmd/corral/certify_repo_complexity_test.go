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
// JavaScript, TypeScript, PHP and Rust have one; the remaining grammars repoindex can
// PARSE (java, c, cpp, csharp, bash) do not, so ExtractSignatures
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
	// still true rather than deleted. (php and rust have since gained
	// theirs; the sample is C-family source, which java/c/cpp all parse.)
	for _, lang := range []string{"java", "c", "cpp"} {
		got, _ := fileComplexity("whatever.src", []byte("int f(int a) { if (a > 0) { return 1; } else { return 0; } }"), lang)
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
	got, _ := fileComplexity("a.py", src, "python")
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
	got, _ := fileComplexity("a.py", src, "python")
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

// TestFileComplexity_SaysWhyAnIncompleteParseIsUnmeasured: a file whose
// grammar set code aside between functions is not measured (its complexity
// would be part of the file's), and the inventory says so, naming the lines,
// rather than leaving the key absent as it is for a language with no reader.
func TestFileComplexity_SaysWhyAnIncompleteParseIsUnmeasured(t *testing.T) {
	src := []byte("package a\n\nfunc F() int { return 1 }\n\ntype struct {\n\nfunc H() int { return 2 }\n")
	got, unread := fileComplexity("a.go", src, "go")
	if got != nil {
		t.Errorf("complexity = %+v, want nil for a file the grammar did not read whole", got)
	}
	if !contains(unread, "lines ") {
		t.Errorf("unread = %q, want the reason naming the unparsed lines", unread)
	}
	if _, unread := fileComplexity("x.java", []byte("int f() { return 1; }"), "java"); unread != "" {
		t.Errorf("a language with no reader gave unread %q; it is unmeasured, not unread", unread)
	}
	blob, err := json.Marshal(auditableJSON{Path: "a.go", Lang: "go", ComplexityUnread: unread})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(blob), `"complexity_unread"`) || contains(string(blob), `"complexity":`) {
		t.Errorf("serialised %s; want complexity_unread and no complexity", blob)
	}
}
