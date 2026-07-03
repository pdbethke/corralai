// SPDX-License-Identifier: Elastic-2.0

package repoindex

import (
	"strconv"
	"strings"
	"testing"
)

func TestLangForExt(t *testing.T) {
	cases := map[string]string{
		"a/b/main.go": "go", "x.py": "python", "y.ts": "typescript", "y.tsx": "typescript",
		"z.js": "javascript", "z.jsx": "javascript", "r.rs": "rust", "README.md": "", "noext": "",
		// case-insensitive (Task-2 carry-over)
		"foo.GO": "go", "bar.PY": "python", "baz.TS": "typescript",
	}
	for path, want := range cases {
		if got := langForExt(path); got != want {
			t.Errorf("langForExt(%q)=%q want %q", path, got, want)
		}
	}
}

func TestChunkFileFallbackTagsLang(t *testing.T) {
	// After tree-sitter wired in, chunkFile dispatches to chunkSymbols for .go;
	// all returned chunks must still carry Lang=go.
	chunks := chunkFile("main.go", "package main\n\nfunc F() {}\n")
	if len(chunks) == 0 {
		t.Fatal("no chunks")
	}
	for _, c := range chunks {
		if c.Lang != "go" {
			t.Errorf("chunk Lang=%q want go", c.Lang)
		}
	}
}

func TestChunkLines(t *testing.T) {
	// 100 numbered lines
	var sb strings.Builder
	for i := 1; i <= 100; i++ {
		sb.WriteString("line")
		sb.WriteByte('\n')
	}
	cs := chunkLines(sb.String(), 60, 15) // step = 45
	if len(cs) != 2 {
		t.Fatalf("want 2 chunks, got %d", len(cs))
	}
	if cs[0].StartLine != 1 || cs[0].EndLine != 60 {
		t.Fatalf("chunk0 lines = %d-%d", cs[0].StartLine, cs[0].EndLine)
	}
	if cs[1].StartLine != 46 || cs[1].EndLine != 100 {
		t.Fatalf("chunk1 lines = %d-%d", cs[1].StartLine, cs[1].EndLine)
	}
	if cs[1].Seq != 1 {
		t.Fatalf("chunk1 seq = %d", cs[1].Seq)
	}
}

func TestChunkLinesShortAndEmpty(t *testing.T) {
	cs := chunkLines("a\nb\nc\n", 60, 15)
	if len(cs) != 1 || cs[0].StartLine != 1 || cs[0].EndLine != 3 || cs[0].Text != "a\nb\nc" {
		t.Fatalf("short file: %+v", cs)
	}
	if len(chunkLines("", 60, 15)) != 0 {
		t.Fatal("empty text should produce no chunks")
	}
}

// assertHasSymbol fails if no chunk in cs has the given symbol.
func assertHasSymbol(t *testing.T, cs []LineChunk, sym string) {
	t.Helper()
	for _, c := range cs {
		if c.Symbol == sym {
			return
		}
	}
	var got []string
	for _, c := range cs {
		if c.Symbol != "" {
			got = append(got, c.Symbol)
		}
	}
	t.Fatalf("no chunk with Symbol=%q; symbols found: %v", sym, got)
}

func TestChunkSymbolsGo(t *testing.T) {
	src := "package main\n\nimport \"fmt\"\n\nfunc Foo() { fmt.Println(\"x\") }\n\ntype Bar struct{ N int }\n\nfunc (b Bar) M() int { return b.N }\n"
	cs, err := chunkSymbols(src, "go")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{} // symbol -> kind
	for _, c := range cs {
		if c.Symbol != "" {
			got[c.Symbol] = c.Kind
		}
	}
	// expect Foo (function), Bar (type), M (method)
	for _, sym := range []string{"Foo", "Bar", "M"} {
		if _, ok := got[sym]; !ok {
			t.Errorf("missing symbol %q; got %v", sym, got)
		}
	}
	// preamble (package/import) chunk exists: at least one chunk with empty Symbol covering line 1
	hasPreamble := false
	for _, c := range cs {
		if c.Symbol == "" && c.StartLine == 1 {
			hasPreamble = true
			break
		}
	}
	if !hasPreamble {
		t.Errorf("expected a preamble chunk (Symbol='', StartLine=1); chunks: %+v", cs)
	}
	// every chunk must carry Lang=go
	for _, c := range cs {
		if c.Lang != "go" {
			t.Errorf("chunk %+v has Lang=%q want go", c, c.Lang)
		}
	}
}

func TestChunkSymbolsPythonAndTS(t *testing.T) {
	py := "import os\n\ndef greet(name):\n    return f\"hi {name}\"\n\nclass Dog:\n    def bark(self):\n        return \"woof\"\n"
	cs, err := chunkSymbols(py, "python")
	if err != nil {
		t.Fatalf("python chunkSymbols error: %v", err)
	}
	assertHasSymbol(t, cs, "greet")
	assertHasSymbol(t, cs, "bark") // method inside class

	ts := "export function add(a: number, b: number) { return a + b }\nclass Svc { run() { return 1 } }\n"
	cs2, err := chunkSymbols(ts, "typescript")
	if err != nil {
		t.Fatalf("typescript chunkSymbols error: %v", err)
	}
	assertHasSymbol(t, cs2, "add")
	assertHasSymbol(t, cs2, "run")
}

func TestChunkSymbolsOversizedWindowed(t *testing.T) {
	// A function whose body exceeds the oversize threshold (2*window=120 lines) is split into
	// multiple sub-chunks, all tagged with the same Symbol; union of ranges covers the function.
	var b strings.Builder
	b.WriteString("package main\nfunc Big() {\n")
	for i := 0; i < 200; i++ {
		b.WriteString("\t_ = " + strconv.Itoa(i) + "\n")
	}
	b.WriteString("}\n")
	cs, err := chunkSymbols(b.String(), "go")
	if err != nil {
		t.Fatalf("chunkSymbols error: %v", err)
	}
	n := 0
	for _, c := range cs {
		if c.Symbol == "Big" {
			n++
		}
	}
	if n < 2 {
		t.Fatalf("oversized function should window into >=2 sub-chunks, got %d", n)
	}
}

// TestSymbolChunkingAcrossLanguages is the T3 table test covering the 9 new grammars.
// All 12 languages capture symbols today; this test FAILS (t.Fatalf), not skips, if any
// grammar ever stops capturing — so the "12 languages" coverage claim can't silently rot in CI.
func TestSymbolChunkingAcrossLanguages(t *testing.T) {
	cases := []struct{ lang, src, sym string }{
		{"rust", "fn add(a: i32) -> i32 { a }\n", "add"},
		{"java", "class A { void run() {} }\n", "run"},
		{"c", "int add(int a){return a;}\n", "add"},
		{"cpp", "int add(int a){return a;}\n", "add"},
		{"csharp", "class A { void Run() {} }\n", "Run"},
		{"ruby", "def greet(n)\n  n\nend\n", "greet"},
		{"php", "<?php function greet($n){return $n;}\n", "greet"},
		{"bash", "greet() { echo hi; }\n", "greet"},
		// JavaScript now uses the dedicated grammar (not aliased to TypeScript).
		{"javascript", "function greet(n) { return n; }\n", "greet"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.lang, func(t *testing.T) {
			cs, err := chunkSymbols(c.src, c.lang)
			if err != nil {
				// FAIL (not skip) on regression: all 12 grammars capture today, so this
				// guards the "12 languages" coverage claim from silently rotting if a
				// future grammar bump breaks capture. Production still falls back
				// gracefully at runtime — this is a dev-facing test guard only.
				t.Fatalf("grammar %s failed to capture symbol %q (regression — prod would fall back): %v", c.lang, c.sym, err)
			}
			assertHasSymbol(t, cs, c.sym)
		})
	}
}

// TestDecoratedPythonCapture verifies that @decorator + def and decorated class methods
// are captured as symbol chunks (T3 carry-over from T2 review).
func TestDecoratedPythonCapture(t *testing.T) {
	// Top-level decorated functions: @property + def and @staticmethod + def.
	// Decorated class method: class Cls with @classmethod def cm.
	src := "@property\ndef foo(self):\n    pass\n\n@staticmethod\ndef bar():\n    pass\n\nclass Cls:\n    @classmethod\n    def cm(cls):\n        pass\n"

	cs, err := chunkSymbols(src, "python")
	if err != nil {
		t.Fatalf("python chunkSymbols error: %v", err)
	}
	assertHasSymbol(t, cs, "foo") // top-level decorated function
	assertHasSymbol(t, cs, "bar") // top-level decorated function
	assertHasSymbol(t, cs, "cm")  // decorated method inside class

	// Confirm decorator lines are within the chunk for "foo".
	for _, c := range cs {
		if c.Symbol == "foo" {
			if c.StartLine > 1 {
				t.Errorf("decorated foo chunk should start at line 1 (decorator); got StartLine=%d", c.StartLine)
			}
			break
		}
	}
}

func TestChunkFileBrokenSyntaxFallsBack(t *testing.T) {
	// Unterminated func → parse produces an error/erroneous tree → chunkFile returns line-window
	// chunks (no panic, file still chunked).
	cs := chunkFile("broken.go", "package main\nfunc Oops( {\n")
	if len(cs) == 0 {
		t.Fatal("broken file must still produce fallback chunks")
	}
}
