# Symbol-Aware Code Chunking Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Chunk source files along symbol boundaries (functions/methods/types) via tree-sitter, tag each chunk with symbol/kind/lang, surface it in `repo_search` — with graceful fallback to the existing line-window chunker so indexing never fails.

**Architecture:** T1 builds the metadata plumbing + a `chunkFile` dispatcher that (for now) always falls back to `chunkLines` — proving schema/Hit/retrieval end-to-end with NO tree-sitter. T2 adds tree-sitter + `chunkSymbols` validated on Go/Python/TypeScript. T3 wires the remaining 9 grammars.

**Tech Stack:** Go 1.26; `github.com/smacker/go-tree-sitter` (CGO); DuckDB (`internal/repoindex`).

## Global Constraints (bind every task)

- **Indexing must NEVER fail.** Unsupported extension OR any parse error/empty capture → `chunkLines` fallback. `chunkFile` returns chunks unconditionally.
- **Graceful degradation is the safety invariant** — a weak/misbehaving grammar just means that language falls back; it can't break the index.
- **tree-sitter is confined to `corral`** (chunking runs brain-side at `create_mission` + per-commit reindex). Do not import it into `corral-agent`/bee code.
- **Schema migration is additive + safe** — `ALTER TABLE chunks ADD COLUMN IF NOT EXISTS ...` (mirror the #21 `vetted` migration); new columns nullable; existing rows read back with empty symbol/kind.
- **Line convention:** match `chunkLines`' existing `StartLine`/`EndLine` convention (check whether it's 1- or 0-indexed and convert tree-sitter's 0-indexed `Point().Row` to match).
- `go build ./...` + `go test ./...` stay green each task. `internal/repoindex` is CGO.

---

## File Structure

- `internal/repoindex/chunk.go` (modify) — extend `LineChunk`; add `chunkFile`, `chunkSymbols`.
- `internal/repoindex/lang.go` (create) — `langForExt`, grammar registry, per-language definition node-types.
- `internal/repoindex/store.go` (modify) — schema migration + `IndexFiles` writes symbol/kind/lang.
- `internal/repoindex/search.go` (modify) — `Hit` gains symbol/kind/lang; `Search` selects them.
- `internal/brain/reposearch.go` (modify) — surface symbol/kind/lang in `repo_search` output.

---

## Task 1: metadata plumbing + `chunkFile` dispatcher + fallback (NO tree-sitter)

**Goal:** every chunk carries `Symbol`/`Kind`/`Lang`; the schema, `IndexFiles`, `Hit`, `Search`, and `repo_search` all thread them; `chunkFile` tags `Lang` (via extension) and — for now — always uses `chunkLines`. Proves the whole data path before parsing exists.

**Files:** Modify `chunk.go`, `store.go`, `search.go`, `brain/reposearch.go`; Create `lang.go` (langForExt only for now).

**Interfaces produced:**
- `LineChunk{Seq, StartLine, EndLine int; Text, Symbol, Kind, Lang string}`
- `func langForExt(path string) string`
- `func chunkFile(path, text string) []LineChunk`
- `Hit` gains `Symbol, Kind, Lang string`

- [ ] **Step 1: Write failing tests**

```go
// internal/repoindex/chunk_test.go (add)
func TestLangForExt(t *testing.T) {
	cases := map[string]string{
		"a/b/main.go": "go", "x.py": "python", "y.ts": "typescript", "y.tsx": "typescript",
		"z.js": "javascript", "z.jsx": "javascript", "r.rs": "rust", "README.md": "", "noext": "",
	}
	for path, want := range cases {
		if got := langForExt(path); got != want {
			t.Errorf("langForExt(%q)=%q want %q", path, got, want)
		}
	}
}

func TestChunkFileFallbackTagsLang(t *testing.T) {
	// with no tree-sitter yet, chunkFile falls back to chunkLines but tags Lang from the ext
	chunks := chunkFile("main.go", "package main\n\nfunc F() {}\n")
	if len(chunks) == 0 {
		t.Fatal("no chunks")
	}
	for _, c := range chunks {
		if c.Lang != "go" {
			t.Errorf("chunk Lang=%q want go", c.Lang)
		}
		if c.Symbol != "" || c.Kind != "" {
			t.Errorf("fallback chunk should have empty Symbol/Kind, got %q/%q", c.Symbol, c.Kind)
		}
	}
}
```
Add a store/search test: index a file, `Search` returns a `Hit` whose `Lang` is set and `Symbol`/`Kind` are empty (fallback). (Use the existing repoindex test harness + a nil/echo embedder as current tests do.)

- [ ] **Step 2: Run red** — `go test ./internal/repoindex/` → FAIL (undefined).

- [ ] **Step 3: Implement**
  - `chunk.go`: add `Symbol, Kind, Lang string` to `LineChunk`. Add:
    ```go
    // chunkFile dispatches by language; for now it always falls back to the line-window
    // chunker (tree-sitter arrives in Task 2). It ALWAYS returns chunks — indexing never fails.
    func chunkFile(path, text string) []LineChunk {
        lang := langForExt(path)
        cs := chunkLines(text, 60, 15)
        for i := range cs {
            cs[i].Lang = lang
        }
        return cs
    }
    ```
  - `lang.go` (create): `langForExt` — a `map[string]string` of extension→language for the ~12 languages (`.go`→go, `.py`→python, `.ts`/`.tsx`→typescript, `.js`/`.jsx`/`.mjs`→javascript, `.rs`→rust, `.java`→java, `.c`/`.h`→c, `.cc`/`.cpp`/`.cxx`/`.hpp`→cpp, `.cs`→csharp, `.rb`→ruby, `.php`→php, `.sh`/`.bash`→bash). Unknown → `""`.
  - `store.go`: migration — after the `CREATE TABLE`, run `ALTER TABLE chunks ADD COLUMN IF NOT EXISTS symbol VARCHAR`, `... kind VARCHAR`, `... lang VARCHAR` (mirror the #21 vetted migration idempotency: tolerate the "already exists" error). Change `IndexFiles` to call `chunkFile(f.Path, f.Text)` instead of `chunkLines(f.Text, 60, 15)`; extend the INSERT column list + values with `symbol, kind, lang` from each chunk.
  - `search.go`: add `Symbol, Kind, Lang string` to `Hit`; the `SELECT` in `searchKeyword`/`searchSemantic` adds `symbol, kind, lang`; scan them into the `Hit`.
  - `brain/reposearch.go`: include `symbol`/`kind`/`lang` in the returned hit structure (surface them to the caller).

- [ ] **Step 4: Run green + build** — `go test ./internal/repoindex/ ./internal/brain/ ./...`; `go build ./...`.
- [ ] **Step 5: Commit** — `git commit -m "feat(repoindex): chunk metadata (symbol/kind/lang) plumbing + chunkFile dispatcher (fallback-only)"`

---

## Task 2: tree-sitter + `chunkSymbols` (Go/Python/TypeScript — the mechanism proof)

**Goal:** add tree-sitter + 3 grammars; implement `chunkSymbols` (parse → capture definitions → symbol chunks with oversized-windowing + preamble); wire it into `chunkFile` for supported languages. This is the load-bearing task — the mechanism validated across 3 diverse grammars.

**Files:** Modify `chunk.go`, `lang.go`, `go.mod`.

**Interfaces produced:**
- `func chunkSymbols(text, lang string) ([]LineChunk, error)`
- `lang.go`: `grammar(lang string) *sitter.Language`, `supported(lang string) bool`, `defTypes(lang string) map[string]string`

- [ ] **Step 1: Add the dependency**
Run: `go get github.com/smacker/go-tree-sitter@latest` (this pulls the module; grammar sub-packages `.../golang`, `.../python`, `.../typescript/typescript` are imported directly). Verify `go build` compiles the CGO grammars.

- [ ] **Step 2: Write failing tests**

```go
// internal/repoindex/chunk_test.go (add)
func TestChunkSymbolsGo(t *testing.T) {
	src := "package main\n\nimport \"fmt\"\n\nfunc Foo() { fmt.Println(\"x\") }\n\ntype Bar struct{ N int }\n\nfunc (b Bar) M() int { return b.N }\n"
	cs, err := chunkSymbols(src, "go")
	if err != nil { t.Fatal(err) }
	got := map[string]string{} // symbol -> kind
	for _, c := range cs {
		if c.Symbol != "" { got[c.Symbol] = c.Kind }
	}
	// expect Foo (function), Bar (type/struct), M (method)
	for _, sym := range []string{"Foo", "Bar", "M"} {
		if _, ok := got[sym]; !ok {
			t.Errorf("missing symbol %q; got %v", sym, got)
		}
	}
	// preamble (package/import) chunk exists, symbol empty, lang go
	// (assert at least one chunk covers line 1 with empty Symbol)
}

func TestChunkSymbolsPythonAndTS(t *testing.T) {
	py := "import os\n\ndef greet(name):\n    return f\"hi {name}\"\n\nclass Dog:\n    def bark(self):\n        return \"woof\"\n"
	cs, _ := chunkSymbols(py, "python")
	assertHasSymbol(t, cs, "greet")
	assertHasSymbol(t, cs, "bark") // method inside class
	ts := "export function add(a: number, b: number) { return a + b }\nclass Svc { run() { return 1 } }\n"
	cs2, _ := chunkSymbols(ts, "typescript")
	assertHasSymbol(t, cs2, "add")
	assertHasSymbol(t, cs2, "run")
}

func TestChunkSymbolsOversizedWindowed(t *testing.T) {
	// a function whose body exceeds the oversize threshold (2*window=120 lines) is split into
	// multiple sub-chunks, all tagged with the same Symbol; union of ranges covers the function.
	var b strings.Builder
	b.WriteString("package main\nfunc Big() {\n")
	for i := 0; i < 200; i++ { b.WriteString("\t_ = " + strconv.Itoa(i) + "\n") }
	b.WriteString("}\n")
	cs, _ := chunkSymbols(b.String(), "go")
	n := 0
	for _, c := range cs { if c.Symbol == "Big" { n++ } }
	if n < 2 { t.Fatalf("oversized function should window into >=2 sub-chunks, got %d", n) }
}

func TestChunkFileBrokenSyntaxFallsBack(t *testing.T) {
	// unterminated func → parse produces an error/erroneous tree → chunkFile returns line-window
	// chunks (no panic, file still chunked)
	cs := chunkFile("broken.go", "package main\nfunc Oops( {\n")
	if len(cs) == 0 { t.Fatal("broken file must still produce fallback chunks") }
}
```
(Add the small `assertHasSymbol` test helper.)

- [ ] **Step 3: Run red.**

- [ ] **Step 4: Implement**
  - `lang.go`: register the 3 grammars:
    ```go
    import (
        sitter "github.com/smacker/go-tree-sitter"
        "github.com/smacker/go-tree-sitter/golang"
        "github.com/smacker/go-tree-sitter/python"
        "github.com/smacker/go-tree-sitter/typescript/typescript"
    )
    func grammar(lang string) *sitter.Language {
        switch lang {
        case "go": return golang.GetLanguage()
        case "python": return python.GetLanguage()
        case "typescript", "javascript": return typescript.GetLanguage() // JS via the TS grammar for now (superset); refine in T3 if needed
        }
        return nil
    }
    func supported(lang string) bool { return grammar(lang) != nil }
    // defTypes maps a tree-sitter node type -> a human kind label, per language.
    func defTypes(lang string) map[string]string {
        switch lang {
        case "go":
            return map[string]string{"function_declaration": "function", "method_declaration": "method", "type_declaration": "type"}
        case "python":
            return map[string]string{"function_definition": "function", "class_definition": "class"}
        case "typescript", "javascript":
            return map[string]string{"function_declaration": "function", "method_definition": "method", "class_declaration": "class", "interface_declaration": "interface"}
        }
        return nil
    }
    ```
  - `chunk.go`: implement `chunkSymbols`:
    ```go
    func chunkSymbols(text, lang string) ([]LineChunk, error) {
        g := grammar(lang)
        if g == nil { return nil, fmt.Errorf("no grammar for %s", lang) }
        parser := sitter.NewParser()
        parser.SetLanguage(g)
        src := []byte(text)
        tree, err := parser.ParseCtx(context.Background(), nil, src)
        if err != nil { return nil, err }
        defer tree.Close()
        root := tree.RootNode()
        dts := defTypes(lang)
        // 1. collect definition spans (top-level + one level of nesting for methods in a class):
        //    walk named children of root; if type in dts -> record {kind,name,startRow,endRow};
        //    additionally, if a node is a class/impl container, walk ITS named children for
        //    method defs and record those too (leaf-preferred: a class contributes a header
        //    chunk [class.start .. firstMethod.start-1], methods are their own chunks).
        // 2. name = node.ChildByFieldName("name").Content(src); if nil, scan named children
        //    for the first "identifier"/"type_identifier" node; else symbol="".
        // 3. build []LineChunk over the file: a preamble chunk [1 .. firstDefStart-1] via
        //    chunkLines on that slice (Symbol=""); one chunk per def (Symbol/Kind set), and if a
        //    def spans > 2*60 lines, sub-window it with chunkLines within its range, each
        //    sub-chunk Symbol=<name> (optionally "<name>#<i>"). Any inter-def gap folds into a
        //    line-window chunk (Symbol="").  Set Lang=lang on every chunk.
        // Convert tree-sitter 0-indexed StartPoint().Row/EndPoint().Row to the chunkLines line
        // convention. If root.HasError() AND zero defs were captured, return an error so
        // chunkFile falls back.
        ...
    }
    ```
    > IMPLEMENTER: keep the nesting to ONE level (top-level defs + methods inside a top-level class/impl/module). Emit a class **header** chunk (declaration through the line before its first method) so method bodies aren't double-indexed. Ensure the **whole-file-coverage invariant**: every line 1..N belongs to exactly one chunk lineage. Use `node.StartPoint().Row`, `node.EndPoint().Row`, `node.ChildByFieldName("name")`, `node.Content(src)`, `node.NamedChildCount()`, `node.NamedChild(i)`, `node.Type()`, `root.HasError()`.
  - `chunk.go` `chunkFile`: now dispatch —
    ```go
    func chunkFile(path, text string) []LineChunk {
        lang := langForExt(path)
        if supported(lang) {
            if cs, err := chunkSymbols(text, lang); err == nil && len(cs) > 0 {
                return cs
            }
        }
        cs := chunkLines(text, 60, 15)
        for i := range cs { cs[i].Lang = lang }
        return cs
    }
    ```

- [ ] **Step 5: Run green + build** — `go test ./internal/repoindex/ -run 'ChunkSymbols|ChunkFile' -v`; `go build ./...`; full `go test ./...`.
- [ ] **Step 6: Commit** — `git commit -m "feat(repoindex): tree-sitter symbol chunking for Go/Python/TypeScript (mechanism + oversized-windowing + graceful fallback)"`

---

## Task 3: wire the remaining 9 grammars (breadth)

**Goal:** register Rust, Java, C, C++, C#, Ruby, PHP, Bash (+ real JavaScript grammar if T2 aliased it to TS) in `grammar()` + `defTypes()`. Mechanical — same mechanism; any grammar that misbehaves falls back.

**Files:** Modify `lang.go` (+ `go.mod` for the grammar sub-packages).

- [ ] **Step 1: Write a table test across languages**

```go
// internal/repoindex/lang_test.go (add)
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
	}
	for _, c := range cases {
		t.Run(c.lang, func(t *testing.T) {
			cs, err := chunkSymbols(c.src, c.lang)
			if err != nil { t.Skipf("grammar %s not capturing (falls back in prod): %v", c.lang, err) }
			assertHasSymbol(t, cs, c.sym)
		})
	}
}
```
> Note: if a specific grammar's node types differ and a case can't capture the symbol, that language simply falls back in production (graceful) — but prefer to get the `defTypes` right. Use `t.Skipf` only as a last resort and LOG which languages fell back (don't silently claim coverage).

- [ ] **Step 2: Run red.**

- [ ] **Step 3: Implement** — add the grammar imports + `grammar()` cases + `defTypes()` entries for each language. Reference node types (verify against the smacker grammar for each): rust `function_item`/`struct_item`/`impl_item`/`enum_item`; java `method_declaration`/`class_declaration`/`interface_declaration`; c `function_definition`/`struct_specifier`; cpp adds `class_specifier`; csharp `method_declaration`/`class_declaration`; ruby `method`/`class`/`module`; php `function_definition`/`method_declaration`/`class_declaration`; bash `function_definition`. If JS was aliased to the TS grammar in T2, wire the dedicated `javascript` grammar here if it improves capture (else keep the alias and note it).

- [ ] **Step 4: Run green + build** — `go test ./internal/repoindex/ -v`; `go build ./...`; full `go test ./...`. Log which languages (if any) fall back.
- [ ] **Step 5: Commit** — `git commit -m "feat(repoindex): wire remaining 9 tree-sitter grammars (rust/java/c/cpp/csharp/ruby/php/bash)"`

---

## Final verification

- [ ] `go build ./...` clean; `go test ./...` all PASS.
- [ ] **Graceful fallback (the safety invariant):** a broken-syntax file and an unsupported extension both still produce chunks (line-window); no panic. Confirm the broken-file test.
- [ ] Go/Python/TypeScript symbol chunks carry correct `Symbol`/`Kind`/line ranges; oversized function windows into multiple same-symbol sub-chunks; preamble captured.
- [ ] Schema migration adds symbol/kind/lang to an existing `chunks` DB without error; a `repo_search` hit surfaces symbol/kind/lang.
- [ ] tree-sitter is imported ONLY under `internal/repoindex` (grep: not in `cmd/corral-agent` or bee code).
- [ ] Report which of the 12 languages capture symbols vs fall back (honest coverage — no silent "12 languages!" claim if 2 fall back).
