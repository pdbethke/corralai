<!-- SPDX-License-Identifier: Elastic-2.0 -->
# CISO Gates — Phase 1, Plan 1: the signature-surface extractor

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deterministically extract a structured *callable surface* (functions + methods, their params, results, receiver, exported-ness, line) from a source file, so a generated CISO test can bind to the code's real shape — a parser's fact, never a model's guess.

**Architecture:** Extend `internal/repoindex` (it already owns tree-sitter: `grammar(lang)`, the `cgo`/`nocgo` build split, `extractName`, the node-walking idiom). Add a `Signature`/`Param` model + a public `ExtractSignatures(text, lang)` that dispatches by language. Go first (dogfood — the repo is Go), then Python to prove the dispatch seam generalizes; unwired languages return a sentinel `ErrUnsupportedLang` (honest, never a silent empty). No LLM anywhere — pure tree-sitter.

**Tech Stack:** Go 1.26.5; `github.com/smacker/go-tree-sitter` (already a dep); the `//go:build cgo` split (tree-sitter needs cgo; the `nocgo` build returns `ErrUnsupportedLang`).

## Global Constraints
- SPDX `// SPDX-License-Identifier: Elastic-2.0` header on every new file (the `cgo`/`nocgo` files also carry their build tag as the FIRST line, then SPDX).
- **TDD**: failing test first, watch it fail, minimal code, watch it pass.
- Per commit: `go vet ./...` + `go test ./...` + `go build ./...` + `bash scripts/check-security.sh` (run with `export PATH="$PATH:$HOME/go/bin"` so gosec is found) all green.
- **Deterministic only** — no LLM, no network, no `time.Now()`. Same input → same output.
- Reuse `repoindex`'s existing `grammar(lang)` + `extractName` — do not add a second grammar registry or a second name-extractor.
- Unwired languages return `ErrUnsupportedLang` — never a silent empty slice.
- corral metaphor (no bee/hive/swarm identifiers).

## File Structure
- `internal/repoindex/signatures.go` — build-tag-free: the `Signature`/`Param` types, `ErrUnsupportedLang`, and the exported `ExtractSignatures` dispatch (declared here, cgo/nocgo provide the impl fn). (new)
- `internal/repoindex/signatures_cgo.go` — `//go:build cgo`: the tree-sitter Go + Python extractors. (new)
- `internal/repoindex/signatures_nocgo.go` — `//go:build !cgo`: stub returning `ErrUnsupportedLang`. (new)
- `internal/repoindex/signatures_test.go` — `//go:build cgo`: table tests with exact expected `Signature`s. (new)

## Interfaces (produced — later plans consume these)
```go
type Param struct {
    Name string // "" when the source declares no name (e.g. an interface method param)
    Type string // verbatim source type text, e.g. "*Engine", "context.Context", "[]byte", "...string"
}
type Signature struct {
    Name     string   // "CheckoutPR"
    Kind     string   // "func" | "method"
    Receiver string   // method receiver type incl. pointer, e.g. "*Engine"; "" for func
    Params   []Param  // in declaration order
    Results  []string // result types in order; empty when none
    Exported bool     // Go: first rune is uppercase
    Line     int      // 1-based start line of the declaration
}
var ErrUnsupportedLang = errors.New("repoindex: no signature extractor for language")
func ExtractSignatures(text, lang string) ([]Signature, error)
```

---

## Task 1: Signature model + Go function extraction + dispatch seam

**Files:**
- Create: `internal/repoindex/signatures.go`, `internal/repoindex/signatures_cgo.go`, `internal/repoindex/signatures_nocgo.go`
- Test: `internal/repoindex/signatures_test.go`

**Interfaces:**
- Produces: `Param`, `Signature`, `ErrUnsupportedLang`, `ExtractSignatures(text, lang)` (above).
- Consumes: `repoindex.grammar(lang)` (returns `*sitter.Language`, nil if unwired) and the `sitter` node API (`RootNode`, `NamedChild`, `NamedChildCount`, `Type`, `ChildByFieldName`, `Content(src)`, `StartPoint().Row`).

- [ ] **Step 1: Write the failing test — top-level Go functions.**
```go
//go:build cgo

package repoindex

import "testing"

func TestExtractSignaturesGoFuncs(t *testing.T) {
	src := `package p

import "context"

func Add(a int, b int) int { return a + b }

func fetch(ctx context.Context, url string) ([]byte, error) { return nil, nil }

func noResult() {}
`
	got, err := ExtractSignatures(src, "go")
	if err != nil {
		t.Fatal(err)
	}
	want := []Signature{
		{Name: "Add", Kind: "func", Params: []Param{{"a", "int"}, {"b", "int"}}, Results: []string{"int"}, Exported: true, Line: 5},
		{Name: "fetch", Kind: "func", Params: []Param{{"ctx", "context.Context"}, {"url", "string"}}, Results: []string{"[]byte", "error"}, Exported: false, Line: 7},
		{Name: "noResult", Kind: "func", Params: nil, Results: nil, Exported: false, Line: 9},
	}
	assertSignatures(t, want, got)
}
```
Add a helper `assertSignatures(t, want, got []Signature)` in the test file that compares length then each field (use `reflect.DeepEqual` on `Params`/`Results`, compare scalars) and `t.Errorf`s a readable diff. (This helper is reused by Tasks 2–3.)

- [ ] **Step 2: Run it, watch it fail.** `go test ./internal/repoindex/ -run TestExtractSignaturesGoFuncs` → FAIL (`ExtractSignatures` undefined).

- [ ] **Step 3: Implement the model + dispatch + Go func extraction.**
`signatures.go`:
```go
// SPDX-License-Identifier: Elastic-2.0

package repoindex

import "errors"

type Param struct {
	Name string
	Type string
}

type Signature struct {
	Name     string
	Kind     string
	Receiver string
	Params   []Param
	Results  []string
	Exported bool
	Line     int
}

// ErrUnsupportedLang is returned for a language with no signature extractor
// wired (never a silent empty result).
var ErrUnsupportedLang = errors.New("repoindex: no signature extractor for language")

// ExtractSignatures returns the callable surface of text in lang. The concrete
// implementation is build-tagged: tree-sitter under cgo, a stub otherwise.
func ExtractSignatures(text, lang string) ([]Signature, error) {
	return extractSignatures(text, lang)
}
```
`signatures_nocgo.go`:
```go
//go:build !cgo

// SPDX-License-Identifier: Elastic-2.0

package repoindex

func extractSignatures(_, _ string) ([]Signature, error) { return nil, ErrUnsupportedLang }
```
`signatures_cgo.go` — reuse `grammar` and the `sitter` walking idiom from `chunk_cgo.go`:
```go
//go:build cgo

// SPDX-License-Identifier: Elastic-2.0

package repoindex

import (
	"context"
	"unicode"

	sitter "github.com/smacker/go-tree-sitter"
)

func extractSignatures(text, lang string) ([]Signature, error) {
	switch lang {
	case "go":
		return extractGoSignatures(text)
	// "python" wired in Task 3
	}
	return nil, ErrUnsupportedLang
}

func parseTS(text, lang string) (*sitter.Tree, []byte, error) {
	g := grammar(lang)
	if g == nil {
		return nil, nil, ErrUnsupportedLang
	}
	src := []byte(text)
	p := sitter.NewParser()
	p.SetLanguage(g)
	tree, err := p.ParseCtx(context.Background(), nil, src)
	if err != nil {
		return nil, nil, err
	}
	return tree, src, nil
}

func exported(name string) bool {
	for _, r := range name {
		return unicode.IsUpper(r)
	}
	return false
}

func extractGoSignatures(text string) ([]Signature, error) {
	tree, src, err := parseTS(text, "go")
	if err != nil {
		return nil, err
	}
	defer tree.Close()
	root := tree.RootNode()
	var out []Signature
	for i := 0; i < int(root.NamedChildCount()); i++ {
		n := root.NamedChild(i)
		if n == nil {
			continue
		}
		if n.Type() != "function_declaration" {
			continue
		}
		sig := Signature{
			Name:   fieldText(n, "name", src),
			Kind:   "func",
			Params: goParams(n.ChildByFieldName("parameters"), src),
			Line:   int(n.StartPoint().Row) + 1,
		}
		sig.Results = goResults(n.ChildByFieldName("result"), src)
		sig.Exported = exported(sig.Name)
		out = append(out, sig)
	}
	return out, nil
}

// fieldText returns the source text of n's named field, or "".
func fieldText(n *sitter.Node, field string, src []byte) string {
	if c := n.ChildByFieldName(field); c != nil {
		return c.Content(src)
	}
	return ""
}

// goParams flattens a Go parameter_list into ordered Params. A single
// parameter_declaration may bind several names to one type ("a, b int");
// each name becomes its own Param. An unnamed param (only a type) yields
// Param{Name:"", Type:...}.
func goParams(list *sitter.Node, src []byte) []Param {
	if list == nil {
		return nil
	}
	var out []Param
	for i := 0; i < int(list.NamedChildCount()); i++ {
		pd := list.NamedChild(i)
		if pd == nil || pd.Type() != "parameter_declaration" {
			continue
		}
		typ := fieldText(pd, "type", src)
		var names []string
		for j := 0; j < int(pd.NamedChildCount()); j++ {
			ch := pd.NamedChild(j)
			if ch != nil && ch.Type() == "identifier" {
				names = append(names, ch.Content(src))
			}
		}
		if len(names) == 0 {
			out = append(out, Param{Name: "", Type: typ})
			continue
		}
		for _, nm := range names {
			out = append(out, Param{Name: nm, Type: typ})
		}
	}
	return out
}

// goResults returns the ordered result types. Go's result is either absent,
// a single type node, or a parameter_list of (possibly named) results.
func goResults(res *sitter.Node, src []byte) []string {
	if res == nil {
		return nil
	}
	if res.Type() == "parameter_list" {
		var out []string
		for _, p := range goParams(res, src) {
			out = append(out, p.Type)
		}
		return out
	}
	return []string{res.Content(src)}
}
```
**Note to implementer:** the tree-sitter-go field names used above (`name`, `parameters`, `result`, `type`, node types `function_declaration`/`parameter_declaration`/`parameter_list`/`identifier`) are the expected grammar shape. If a field lookup returns `""`/nil, print `n.String()` for a failing case and adjust the field name / node type to match the actual grammar — the test's exact `want` is the oracle. This is normal tree-sitter TDD, not a placeholder.

- [ ] **Step 4: Run it, watch it pass.** `go test ./internal/repoindex/ -run TestExtractSignaturesGoFuncs` → PASS. Then `go test ./internal/repoindex/...` (don't regress `chunkSymbols`). **Commit:** `feat(repoindex): deterministic Go signature-surface extraction`.

---

## Task 2: Go methods (receiver) + parameter edge cases

**Files:**
- Modify: `internal/repoindex/signatures_cgo.go`
- Test: `internal/repoindex/signatures_test.go`

**Interfaces:**
- Consumes: Task 1's `extractGoSignatures`, `goParams`, `fieldText`, `exported`.

- [ ] **Step 1: Write the failing test — methods + edge cases.**
```go
func TestExtractSignaturesGoMethods(t *testing.T) {
	src := `package p

func (e *Engine) CheckoutPR(ctx Ctx, pr int, sha string) error { return nil }

func (s Store) get(a, b string) (Row, bool) { return Row{}, false }

func Variadic(prefix string, rest ...int) {}
`
	got, err := ExtractSignatures(src, "go")
	if err != nil {
		t.Fatal(err)
	}
	want := []Signature{
		{Name: "CheckoutPR", Kind: "method", Receiver: "*Engine", Params: []Param{{"ctx", "Ctx"}, {"pr", "int"}, {"sha", "string"}}, Results: []string{"error"}, Exported: true, Line: 3},
		{Name: "get", Kind: "method", Receiver: "Store", Params: []Param{{"a", "string"}, {"b", "string"}}, Results: []string{"Row", "bool"}, Exported: false, Line: 5},
		{Name: "Variadic", Kind: "func", Params: []Param{{"prefix", "string"}, {"rest", "...int"}}, Results: nil, Exported: true, Line: 7},
	}
	assertSignatures(t, want, got)
}
```

- [ ] **Step 2: Run it, watch it fail** (methods aren't captured yet; receiver/variadic unhandled).

- [ ] **Step 3: Implement.** In `extractGoSignatures`, also handle `method_declaration`; add receiver extraction and variadic type handling:
```go
		switch n.Type() {
		case "function_declaration":
			// (existing func path)
		case "method_declaration":
			sig := Signature{
				Name:     fieldText(n, "name", src),
				Kind:     "method",
				Receiver: goReceiver(n.ChildByFieldName("receiver"), src),
				Params:   goParams(n.ChildByFieldName("parameters"), src),
				Line:     int(n.StartPoint().Row) + 1,
			}
			sig.Results = goResults(n.ChildByFieldName("result"), src)
			sig.Exported = exported(sig.Name)
			out = append(out, sig)
		default:
			continue
		}
```
Add `goReceiver` (the receiver is a `parameter_list` with one `parameter_declaration` whose `type` is the receiver type, e.g. `*Engine`):
```go
func goReceiver(recv *sitter.Node, src []byte) string {
	if recv == nil {
		return ""
	}
	for i := 0; i < int(recv.NamedChildCount()); i++ {
		pd := recv.NamedChild(i)
		if pd != nil && pd.Type() == "parameter_declaration" {
			return fieldText(pd, "type", src)
		}
	}
	return ""
}
```
For variadic: a `variadic_parameter_declaration` node (or a `parameter_declaration` whose type is `variadic_type`) should yield `Type: "..." + elemType`. Extend `goParams` to recognize the variadic node/type and prefix `...`. Use the failing test to confirm the exact node type the grammar emits and adjust.
(Refactor the func/method bodies to share a `goCallable(n, kind, recv, src)` builder if it removes duplication — DRY, but keep it behavior-identical.)

- [ ] **Step 4: Run, watch pass.** `go test ./internal/repoindex/ -run TestExtractSignaturesGo` (both Go tests) → PASS. **Commit:** `feat(repoindex): Go method receivers + variadic/multi-name params`.

---

## Task 3: Python extractor + the unwired-language seam

**Files:**
- Modify: `internal/repoindex/signatures_cgo.go`
- Test: `internal/repoindex/signatures_test.go`

**Interfaces:**
- Consumes: Task 1's `parseTS`, `fieldText`, `exported`; `repoindex.grammar("python")` (already wired).

- [ ] **Step 1: Write the failing tests — Python + an unwired language.**
```go
func TestExtractSignaturesPython(t *testing.T) {
	src := "def add(a, b):\n    return a + b\n\n" +
		"def fetch(ctx, url: str) -> bytes:\n    return b''\n\n" +
		"async def _hidden(x):\n    return x\n"
	got, err := ExtractSignatures(src, "python")
	if err != nil {
		t.Fatal(err)
	}
	want := []Signature{
		{Name: "add", Kind: "func", Params: []Param{{"a", ""}, {"b", ""}}, Exported: true, Line: 1},
		{Name: "fetch", Kind: "func", Params: []Param{{"ctx", ""}, {"url", "str"}}, Results: []string{"bytes"}, Exported: true, Line: 4},
		{Name: "_hidden", Kind: "func", Params: []Param{{"x", ""}}, Exported: false, Line: 7},
	}
	assertSignatures(t, want, got)
}

func TestExtractSignaturesUnwiredLang(t *testing.T) {
	// cobol has no extractor wired (even though it's not a repoindex grammar);
	// the contract is an explicit ErrUnsupportedLang, never a silent empty.
	_, err := ExtractSignatures("       IDENTIFICATION DIVISION.", "cobol")
	if !errors.Is(err, ErrUnsupportedLang) {
		t.Fatalf("want ErrUnsupportedLang, got %v", err)
	}
}
```
(Python has no Go-style exported rule — treat a leading `_` as unexported and everything else exported, which is the community convention; the test encodes that.)

- [ ] **Step 2: Run, watch fail** (python returns ErrUnsupportedLang today; the unwired test passes already — that's fine, it guards the seam).

- [ ] **Step 3: Implement the Python extractor + wire dispatch.** In `extractSignatures` add `case "python": return extractPythonSignatures(text)`. Implement:
```go
func extractPythonSignatures(text string) ([]Signature, error) {
	tree, src, err := parseTS(text, "python")
	if err != nil {
		return nil, err
	}
	defer tree.Close()
	root := tree.RootNode()
	var out []Signature
	for i := 0; i < int(root.NamedChildCount()); i++ {
		n := root.NamedChild(i)
		if n == nil {
			continue
		}
		// unwrap `decorated_definition` (mirror chunk_cgo.go's handling)
		def := n
		if n.Type() == "decorated_definition" {
			for j := 0; j < int(n.NamedChildCount()); j++ {
				inner := n.NamedChild(j)
				if inner != nil && inner.Type() == "function_definition" {
					def = inner
					break
				}
			}
		}
		if def.Type() != "function_definition" {
			continue
		}
		name := fieldText(def, "name", src)
		sig := Signature{
			Name:     name,
			Kind:     "func",
			Params:   pyParams(def.ChildByFieldName("parameters"), src),
			Line:     int(def.StartPoint().Row) + 1,
			Exported: !strings.HasPrefix(name, "_"),
		}
		if rt := def.ChildByFieldName("return_type"); rt != nil {
			sig.Results = []string{rt.Content(src)}
		}
		out = append(out, sig)
	}
	return out, nil
}

// pyParams flattens a Python `parameters` node. A plain `identifier` is an
// untyped param (Type:""); a `typed_parameter` carries name + type.
func pyParams(params *sitter.Node, src []byte) []Param {
	if params == nil {
		return nil
	}
	var out []Param
	for i := 0; i < int(params.NamedChildCount()); i++ {
		p := params.NamedChild(i)
		if p == nil {
			continue
		}
		switch p.Type() {
		case "identifier":
			out = append(out, Param{Name: p.Content(src), Type: ""})
		case "typed_parameter":
			// name is the first identifier child; type is the "type" field
			nm := ""
			for j := 0; j < int(p.NamedChildCount()); j++ {
				if c := p.NamedChild(j); c != nil && c.Type() == "identifier" {
					nm = c.Content(src)
					break
				}
			}
			out = append(out, Param{Name: nm, Type: fieldText(p, "type", src)})
		}
	}
	return out
}
```
Add `"strings"` to the cgo file's imports. Confirm the tree-sitter-python field/node names (`function_definition`, `parameters`, `return_type`, `typed_parameter`) against a failing case and adjust; the test's `want` is the oracle.

- [ ] **Step 4: Run, watch pass.** `go test ./internal/repoindex/...` → PASS (Go + Python + unwired + existing chunk tests). Then the full gate: `go vet ./... && go build ./... && go test ./... && bash scripts/check-security.sh`. **Commit:** `feat(repoindex): Python signature extraction + explicit unwired-language error`.

---

## Self-Review
- **Spec coverage (4a):** deterministic callable-surface extraction reused off the existing tree-sitter layer ✓; polyglot seam proven with a 2nd language ✓; unwired → explicit error (never silent) ✓; no LLM/network/clock ✓.
- **No placeholders:** every step has complete code; the two "confirm the grammar field name against a failing case" notes are TDD discovery (the exact `want` is the oracle), not vague hand-waving.
- **Type consistency:** `Signature`/`Param` identical across all three tasks; `assertSignatures` helper defined in Task 1 and reused; `ExtractSignatures`/`ErrUnsupportedLang` stable.
- **Build split:** tree-sitter code is `//go:build cgo`; the `!cgo` build returns `ErrUnsupportedLang` (compiles + is honest). Mirrors the existing `lang.go`/`chunk.go` split.
- **Out of scope (later plans/tasks):** the remaining languages (TS/Java/…), interfaces/type declarations (v1 is callables), the control-spec store, the test-writer, the gate dimension.
