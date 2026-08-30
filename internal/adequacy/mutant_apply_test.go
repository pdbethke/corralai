// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
)

// legacyApply is a VERBATIM copy of testgen.applyMutation as it stood before
// the mutant became its hunk (HEAD e413ad9). It is the oracle: Apply must
// reproduce its bytes exactly, or a replayed mutant set grades a file nobody
// generated. Never "fix" it to agree with Apply — that would turn the
// comparison into Apply comparing with itself.
func legacyApply(original, search, replace string) (mutant string, span lang.LineRange, ok bool) {
	if search == "" || search == replace {
		return "", lang.LineRange{}, false
	}
	i := strings.Index(original, search)
	if i < 0 {
		return "", lang.LineRange{}, false // anchor not found
	}
	if strings.Contains(original[i+len(search):], search) {
		return "", lang.LineRange{}, false // anchor not unique
	}
	mutant = original[:i] + replace + original[i+len(search):]
	if mutant[:i]+search+mutant[i+len(replace):] != original {
		return "", lang.LineRange{}, false
	}
	start := strings.Count(original[:i], "\n") + 1
	end := start + strings.Count(strings.TrimSuffix(search, "\n"), "\n")
	return mutant, lang.LineRange{Start: start, End: end}, true
}

// applyCases are the anchors that actually break a naive re-implementation:
// the file's very first bytes, its last bytes with no trailing newline, CRLF
// line endings, a multi-line anchor, indentation carried verbatim, and a
// replacement that is longer or shorter than what it replaces.
var applyCases = []struct {
	name, original, search, replace string
}{
	{"anchor at file start", "package p\nfunc F() int { return 1 }\n", "package p", "package q"},
	{"anchor at file end without trailing newline", "a\nb\nlast = 1", "last = 1", "last = 2"},
	{"CRLF file", "a\r\nb\r\nc\r\n", "b\r\n", "B\r\n"},
	{"multi-line search", "a\nb\nc\nd\n", "b\nc\n", "X\n"},
	{"replacement longer than search", "x = 1\ny = 2\n", "y = 2", "y = 2 + 3 + 4 + 5"},
	{"replacement shorter than search", "x = 1\nyyyyyyyy = 2\n", "yyyyyyyy = 2", "y = 2"},
	// The indentation cases. parseMutantsDiag's stripLeading is a DIAGNOSIS
	// only — a SEARCH whose leading whitespace was mangled is reported and
	// dropped, never anchored (see testgen.TestWhitespaceMangledSearch...).
	// So whatever bytes the parser anchored on are the bytes Mutant.Search
	// must store, indentation included, and an implementation that stored a
	// stripped or re-indented anchor would splice at the wrong offset.
	{"tab-indented anchor kept verbatim", "func F() int {\n\treturn 1\n}\n", "\treturn 1\n", "\treturn 99\n"},
	{"space-indented anchor kept verbatim", "def f():\n    return 1\n", "    return 1\n", "    return 99\n"},
	{"deletion", "a\nb\nc\n", "b\n", ""},
	{"whole-line anchor at start with newline", "a\nb\n", "a\n", "A\n"},
}

func TestApplyIsByteIdenticalToApplyMutation(t *testing.T) {
	for _, c := range applyCases {
		want, wantSpan, ok := legacyApply(c.original, c.search, c.replace)
		if !ok {
			t.Fatalf("%s: the legacy oracle refused this case — fix the fixture, not the oracle", c.name)
		}
		m := Mutant{ID: "m1", Search: c.search, Replace: c.replace}
		got, err := m.Apply(c.original)
		if err != nil {
			t.Errorf("%s: Apply returned %v, want the legacy bytes", c.name, err)
			continue
		}
		if got != want {
			t.Errorf("%s: Apply =\n%q\nlegacy =\n%q", c.name, got, want)
		}
		if span := HunkSpan(c.original, c.search); span != wantSpan {
			t.Errorf("%s: HunkSpan = %v, legacy span = %v", c.name, span, wantSpan)
		}
		if m.IsWholeFile() {
			t.Errorf("%s: a hunk mutant must not report itself whole-file", c.name)
		}
	}
}

func TestApplyRefusesAnAbsentOrAmbiguousAnchor(t *testing.T) {
	cases := []struct {
		name, original, search, replace string
	}{
		{"anchor not found", "a\nb\n", "zzz", "y"},
		{"anchor not unique", "aa\naa\n", "aa", "bb"},
		{"no-op replacement", "a\nb\n", "b", "b"},
	}
	for _, c := range cases {
		m := Mutant{ID: "m1", Search: c.search, Replace: c.replace}
		got, err := m.Apply(c.original)
		if err == nil {
			t.Errorf("%s: Apply returned %q with no error — a refusal must never be a silent no-op", c.name, got)
		}
		if got != "" {
			t.Errorf("%s: a refused Apply must return no source at all, got %q", c.name, got)
		}
		if _, _, ok := legacyApply(c.original, c.search, c.replace); ok {
			t.Errorf("%s: the legacy oracle ACCEPTED this — the fixture is wrong", c.name)
		}
	}
}

func TestApplyOfAWholeFileMutantIsIdentity(t *testing.T) {
	// A v1 recorded mutant carries no anchor: Replace IS the mutated file, and
	// Apply must hand it back verbatim whatever the original says. This is the
	// whole compatibility story for corral-mutants-1 documents.
	whole := "def f():\n    return 99\n"
	m := Mutant{ID: "m1", Replace: whole}
	if !m.IsWholeFile() {
		t.Fatal("Search==\"\" must report IsWholeFile")
	}
	for _, original := range []string{"def f():\n    return 1\n", "", "anything at all"} {
		got, err := m.Apply(original)
		if err != nil {
			t.Fatalf("whole-file Apply: %v", err)
		}
		if got != whole {
			t.Errorf("whole-file Apply = %q, want %q", got, whole)
		}
	}
}

// TestHunkSpanReportsTheAnchorsOriginalLines is the span algorithm's home. It
// lives beside Apply because Span and the splice are derived from the same two
// inputs (original, Search) and must never drift apart: the recorded set's
// span is what a selection rule aims a test at, and Apply is what the jail
// actually grades.
func TestHunkSpanReportsTheAnchorsOriginalLines(t *testing.T) {
	original := "a\nb\nc\nd\ne\n"
	cases := []struct {
		search, replace string
		want            lang.LineRange
	}{
		{"c\n", "C\n", lang.LineRange{Start: 3, End: 3}},         // one-line replacement
		{"b\nc\n", "X\n", lang.LineRange{Start: 2, End: 3}},      // two lines collapse to one
		{"d\n", "d\nd2\nd3\n", lang.LineRange{Start: 4, End: 4}}, // growth: span is the ORIGINAL lines
		{"b\nc\nd\n", "", lang.LineRange{Start: 2, End: 4}},      // deletion spans the removed lines
		{"a\nb", "A\nB", lang.LineRange{Start: 1, End: 2}},       // no trailing newline in the anchor
	}
	for _, c := range cases {
		_, wantSpan, ok := legacyApply(original, c.search, c.replace)
		if !ok {
			t.Fatalf("%q: the legacy oracle refused this fixture", c.search)
		}
		if wantSpan != c.want {
			t.Fatalf("%q: the oracle disagrees with the expectation (%v vs %v)", c.search, wantSpan, c.want)
		}
		if got := HunkSpan(original, c.search); got != c.want {
			t.Errorf("%q: span = %v, want %v", c.search, got, c.want)
		}
	}
}
