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
)

// fixtureLang maps each langspec fixture to the language it exercises.
// (go.go.txt is named so the Go toolchain never compiles it.)
var fixtureLang = map[string]string{
	"go.go.txt": "go", "python.py": "python", "ruby.rb": "ruby",
	"javascript.js": "javascript", "typescript.ts": "typescript", "php.php": "php",
	"rust.rs": "rust", "java.java": "java", "c.c": "c", "cpp.cpp": "cpp",
	"csharp.cs": "csharp", "bash.sh": "bash",
}

// TestLangSpecGolden pins, per language, what the chunker and the signature
// extractor make of one fixture each: every symbol chunk (symbol, kind, line
// span) and every signature (name, kind, receiver, exported, line,
// complexity, decision spans), or the extractor's error. It is the
// characterization net for folding seven per-language switches into one
// table. Generated deliberately with UPDATE_GOLDEN=1.
func TestLangSpecGolden(t *testing.T) {
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
