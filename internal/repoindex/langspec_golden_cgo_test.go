//go:build cgo

// SPDX-License-Identifier: Elastic-2.0

package repoindex

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	plugins "github.com/pdbethke/corralai/internal/lang"
)

// langSpecFixtures finds each langSpecs row's fixture by name —
// testdata/langspec/<lang>.* (go.go.txt is named so the Go toolchain never
// compiles it, and still matches go.*) — and returns fixture file name →
// language. It is derived from the table, never kept by hand, so a new row
// with no fixture fails here instead of going unpinned. It also fails on a
// fixture that maps to no row (a renamed or deleted language would otherwise
// leave a file nobody reads) and floors the walk, so an emptied table cannot
// pass by pinning nothing.
func langSpecFixtures(t *testing.T) map[string]string {
	t.Helper()
	const dir = "testdata/langspec"
	const minRows = 12
	if len(langSpecs) < minRows {
		t.Fatalf("langSpecs has %d rows, want at least %d — the golden would pin less than it did", len(langSpecs), minRows)
	}
	fixtures := map[string]string{}
	for lang := range langSpecs {
		matches, err := filepath.Glob(filepath.Join(dir, lang+".*"))
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) != 1 {
			t.Errorf("langSpecs row %q has %d fixtures matching %s/%s.* (%v), want exactly 1 — add one and regenerate the golden deliberately", lang, len(matches), dir, lang, matches)
			continue
		}
		fixtures[filepath.Base(matches[0])] = lang
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == "langspec.golden.tsv" {
			continue
		}
		if _, ok := fixtures[e.Name()]; !ok {
			t.Errorf("fixture %s/%s maps to no langSpecs row — delete it, or name it <lang>.<ext> for the row it exercises", dir, e.Name())
		}
	}
	return fixtures
}

// TestLangSpecGolden pins, per language, what the chunker and the signature
// extractor make of one fixture each: every symbol chunk (symbol, kind, line
// span) and every signature (name, kind, receiver, exported, line,
// complexity, decision spans), or the extractor's error. It is the
// characterization net for folding seven per-language switches into one
// table. Generated deliberately with UPDATE_GOLDEN=1. Fixtures are visited in
// fixture-file-name order, which is the order the golden was generated in.
func TestLangSpecGolden(t *testing.T) {
	fixtureLang := langSpecFixtures(t)
	names := make([]string, 0, len(fixtureLang))
	for n := range fixtureLang {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		lang := fixtureLang[n]
		src, err := os.ReadFile(filepath.Join("testdata", "langspec", n))
		if err != nil {
			t.Fatal(err)
		}
		chunks, cerr := chunkSymbols(string(src), lang)
		fmt.Fprintf(&b, "== %s (%s) chunk err=%v\n", n, lang, cerr)
		for _, c := range chunks {
			fmt.Fprintf(&b, "chunk\t%s\t%s\t%d-%d\n", c.Symbol, c.Kind, c.StartLine, c.EndLine)
		}
		sigs, serr := ExtractSignatures(string(src), lang)
		fmt.Fprintf(&b, "sig err=%v\n", serr)
		for _, s := range sigs {
			fmt.Fprintf(&b, "sig\t%s\t%s\t%s\t%v\t%d\t%d\t%v\n", s.Name, s.Kind, s.Receiver, s.Exported, s.Line, s.Complexity, s.Decisions)
		}
	}
	got := b.String()
	const path = "testdata/langspec/langspec.golden.tsv"
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden missing (generate deliberately with UPDATE_GOLDEN=1): %v", err)
	}
	if got != string(want) {
		t.Fatalf("parsing output changed; regenerate to a scratch path and diff against %s", path)
	}
}

// TestNoExtractorIsAnErrorNotEmpty: a language with a grammar row but no
// signature extractor must refuse, never return an empty surface a caller
// would read as "this file has no functions".
func TestNoExtractorIsAnErrorNotEmpty(t *testing.T) {
	for name, s := range langSpecs {
		if s.extract != nil {
			continue
		}
		if sigs, err := ExtractSignatures("x", name); err != ErrUnsupportedLang || sigs != nil {
			t.Errorf("ExtractSignatures(_, %q) = %v, %v; want nil, ErrUnsupportedLang", name, sigs, err)
		}
	}
	if _, err := ExtractSignatures("x", "cobol"); err != ErrUnsupportedLang {
		t.Errorf("unknown language: err = %v, want ErrUnsupportedLang", err)
	}
}

// TestEveryLanguageHasAParsingRow ties the three places a language name lives
// to langSpecs: a registered plugin with no row is audited but indexed as
// nothing (the .cjs disagreement, one level up); an ownExtLang value with no
// row maps a file to a language nothing can parse; and a row that extracts
// signatures with no branch set scores every symbol complexity 1 — "trivial"
// where the truth is "never measured" (see TestNewLanguagesMeasureComplexity).
func TestEveryLanguageHasAParsingRow(t *testing.T) {
	names := plugins.Names()
	if len(names) < 6 {
		t.Fatalf("lang.Names() returned %d plugins, want at least 6 — the check would cover nothing", len(names))
	}
	for _, name := range names {
		if _, ok := langSpecs[name]; !ok {
			t.Errorf("plugin %q is registered but langSpecs has no row for it — repoindex cannot parse a language corral audits", name)
		}
	}
	for ext, name := range ownExtLang {
		if _, ok := langSpecs[name]; !ok {
			t.Errorf("ownExtLang maps %q to %q, which has no langSpecs row", ext, name)
		}
	}
	for name, s := range langSpecs {
		if s.extract != nil && len(s.branches) == 0 {
			t.Errorf("langSpecs row %q extracts signatures but has no branches — every symbol would score complexity 1", name)
		}
	}
}
