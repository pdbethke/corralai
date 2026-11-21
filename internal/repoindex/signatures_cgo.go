//go:build cgo

// SPDX-License-Identifier: Elastic-2.0

package repoindex

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/pdbethke/corralai/internal/pairing"
)

// extractSignatures parses text once and hands the tree to lang's extractor
// in its langSpecs row. A language with no extractor refuses with
// ErrUnsupportedLang rather than returning an empty surface a caller would
// read as "no functions", and a parse that did not read the whole file
// refuses with ErrIncompleteParse (unreadSpan) for the same reason.
func extractSignatures(text, lang string) ([]Signature, error) {
	s, ok := langSpecs[lang]
	if !ok || s.extract == nil {
		return nil, ErrUnsupportedLang
	}
	tree, src, err := parseTS(text, lang)
	if err != nil {
		return nil, err
	}
	defer tree.Close()
	root := tree.RootNode()
	sigs := s.extract(root, src, lang)
	if err := unreadSpan(root, sigs); err != nil {
		return nil, err
	}
	return sigs, nil
}

// unreadSpan refuses a parse whose first ERROR or MISSING node lies outside
// every symbol the extractor read.
//
// tree-sitter does not fail on code its grammar cannot parse: it wraps what
// it could not place in an ERROR node and goes on. When that node sits
// between symbols it may have swallowed some, and a surface read around it is
// short by them with nothing to say so. That short surface is then taken as
// the file: the mutant budget is derived from part of the file's complexity,
// the generator seats are split over the symbols that were read, and each
// seat is shown only its own symbols' lines, so the swallowed code is shown
// to none of them and no fault is planted in it, and the exam coverage counts
// the symbols read as all of them. On taplin/Lariat at be0c5fd the Swift
// grammar wrapped lines 14-312 of Sources/LassoParser/LassoParser.swift, the
// whole of one private struct and its methods, in a single ERROR node at the
// top of the file. So such a parse is not a reading of the file: the caller
// gets the error and treats the file as one with no signatures, which is
// what it already does for a language with no reader (one generator seat
// over the whole file, the default budget, exam coverage not measured).
//
// An error INSIDE a symbol is kept: the grammar placed the symbol, so its
// lines are shown to its seat and counted, and only the decision points the
// error covers can be missing from its complexity. Measured 2026-11-20 with
// this repository's grammars: of 4077 Rust files in a checkout of
// warpdotdev/warp, 114 had an error and 64 an error outside every symbol; of
// 1093 Go files in this repository's internal/, none had one.
func unreadSpan(root *sitter.Node, sigs []Signature) error {
	if !root.HasError() {
		return nil
	}
	var bad *sitter.Node
	var find func(*sitter.Node)
	find = func(n *sitter.Node) {
		if bad != nil || n == nil || !n.HasError() {
			return
		}
		if n.IsError() || n.IsMissing() {
			start, end := int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1
			for _, s := range sigs {
				if s.Line <= start && end <= s.Line+s.Lines-1 {
					return
				}
			}
			bad = n
			return
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			find(n.Child(i))
		}
	}
	find(root)
	if bad == nil {
		return nil
	}
	return fmt.Errorf("%w: lines %d-%d are not inside any function it read", ErrIncompleteParse,
		int(bad.StartPoint().Row)+1, int(bad.EndPoint().Row)+1)
}

// readsTestModules: lang's row has a modules reader.
func readsTestModules(lang string) bool {
	spec, ok := langSpecs[lang]
	return ok && spec.modules != nil
}

// testModules dispatches to the modules reader in lang's langSpecs row; a
// language without one answers nil (it pairs by filename alone).
func testModules(lang string, src []byte) []pairing.TestModule {
	spec, ok := langSpecs[lang]
	if !ok || spec.modules == nil {
		return nil
	}
	tree, _, err := parseTS(string(src), lang)
	if err != nil {
		return nil
	}
	defer tree.Close()
	return spec.modules(tree.RootNode(), src)
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

// symbolComplexity walks n's subtree counting branch nodes, returning the
// cyclomatic-style complexity (minimum 1 — a straight-line symbol has exactly
// one path). Go's `&&`/`||` are binary_expression nodes distinguished by their
// operator field rather than by node type, so they are counted separately;
// Python's are their own `boolean_operator` node type and fall out of the set.
func symbolComplexity(n *sitter.Node, src []byte, lang string) int {
	return 1 + len(symbolDecisions(n, src, lang))
}

// symbolDecisions is the walk symbolComplexity counts: one Decision per
// branch node, as its subtree's inclusive line span, in source order. The
// two are ONE walk so the count and the places can never disagree.
func symbolDecisions(n *sitter.Node, src []byte, lang string) []Decision {
	spec := langSpecs[lang]
	types := spec.branches
	var out []Decision
	var walk func(*sitter.Node)
	walk = func(node *sitter.Node) {
		if node == nil {
			return
		}
		t := node.Type()
		counted := false
		switch {
		case types[t]:
			counted = spec.defaultArms[t] == "" || firstNamedOfType(node, spec.defaultArms[t]) == nil
		case spec.boolOp != "" && t == spec.boolOp:
			if op := node.ChildByFieldName("operator"); op != nil {
				switch op.Content(src) {
				case "&&", "||", "and", "or":
					counted = true
				}
			}
		}
		if counted {
			out = append(out, Decision{Start: int(node.StartPoint().Row) + 1, End: int(node.EndPoint().Row) + 1})
		}
		// Every child in source order, so a list's separating commas
		// (conditionLists) take their place among the decisions the
		// conditions themselves hold.
		for i := 0; i < int(node.ChildCount()); i++ {
			c := node.Child(i)
			if c == nil {
				continue
			}
			if c.IsNamed() {
				walk(c)
				continue
			}
			if spec.conditionLists[t] && c.Type() == "," && node.FieldNameForChild(i) == "" {
				// The joined condition's span: from the comma to the end of
				// the condition after it.
				end := c.EndPoint().Row
				if next := c.NextNamedSibling(); next != nil {
					end = next.EndPoint().Row
				}
				out = append(out, Decision{Start: int(c.StartPoint().Row) + 1, End: int(end) + 1})
			}
		}
	}
	walk(n)
	return out
}

// symbolLines is the inclusive line span of n.
func symbolLines(n *sitter.Node) int {
	return int(n.EndPoint().Row-n.StartPoint().Row) + 1
}

func extractGoSignatures(root *sitter.Node, src []byte) []Signature {
	var out []Signature
	for i := 0; i < int(root.NamedChildCount()); i++ {
		n := root.NamedChild(i)
		if n == nil {
			continue
		}
		switch n.Type() {
		case "function_declaration":
			out = append(out, goCallable(n, "func", "", src))
		case "method_declaration":
			recv := goReceiver(n.ChildByFieldName("receiver"), src)
			out = append(out, goCallable(n, "method", recv, src))
		default:
			continue
		}
	}
	return out
}

// goCallable builds a Signature shared by function_declaration and
// method_declaration nodes, which agree on name/parameters/result shape.
func goCallable(n *sitter.Node, kind, receiver string, src []byte) Signature {
	sig := Signature{
		Name:     fieldText(n, "name", src),
		Kind:     kind,
		Receiver: receiver,
		Params:   goParams(n.ChildByFieldName("parameters"), src),
		Line:     int(n.StartPoint().Row) + 1,
	}
	sig.Results = goResults(n.ChildByFieldName("result"), src)
	sig.Exported = exported(sig.Name)
	sig.Decisions = symbolDecisions(n, src, "go")
	sig.Complexity = 1 + len(sig.Decisions)
	sig.Lines = symbolLines(n)
	return sig
}

// goReceiver extracts the receiver type ("*Engine", "Store", ...) from a
// method_declaration's receiver field: a parameter_list holding exactly one
// parameter_declaration.
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
		if pd == nil {
			continue
		}
		if pd.Type() == "variadic_parameter_declaration" {
			out = append(out, Param{Name: fieldText(pd, "name", src), Type: "..." + fieldText(pd, "type", src)})
			continue
		}
		if pd.Type() != "parameter_declaration" {
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

// extractPythonSignatures walks top-level function_definition nodes (unwrapping
// decorated_definition, mirroring chunk_cgo.go's handling) and builds the
// callable surface. Python has no Go-style exported rule: a leading "_" is
// unexported, everything else is exported (the community convention).
func extractPythonSignatures(root *sitter.Node, src []byte) []Signature {
	var out []Signature
	pyWalkDefs(root, src, "", &out)
	return out
}

// pyUnwrapDecorated returns the function_definition or class_definition a
// `decorated_definition` wraps (mirroring chunk_cgo.go's handling), or the node
// itself when it is not decorated. Without this, @property, @staticmethod and
// every framework decorator would hide the symbol beneath them.
func pyUnwrapDecorated(n *sitter.Node) *sitter.Node {
	if n == nil || n.Type() != "decorated_definition" {
		return n
	}
	for j := 0; j < int(n.NamedChildCount()); j++ {
		inner := n.NamedChild(j)
		if inner == nil {
			continue
		}
		if t := inner.Type(); t == "function_definition" || t == "class_definition" {
			return inner
		}
	}
	return n
}

// pyWalkDefs collects function definitions under `parent`, DESCENDING INTO
// CLASSES and attributing each method to its class via Receiver.
//
// It previously walked only the module's top level and accepted only
// function_definition, so class_definition was skipped and every method inside
// was invisible. On psf/requests that reported ONE symbol for ~500 lines built
// around HTTPAdapter — and most real Python is class-based, so this was the
// common case rather than an edge.
//
// Three things downstream inherited that blind spot: complexity under-reported
// class-heavy files; the mutant-generator's signature surface was near-empty,
// leaving its prompt almost nothing to work from; and advpool.ShardSymbols
// bin-packs SYMBOLS, so one visible symbol produced ONE shard instead of up to
// eight — the parallel generator seats silently collapsing, taking the
// per-shard mutant budget with them.
//
// Receiver is the class name, matching the Go extractor's convention and
// keeping advpool.symbolIdentity (Receiver + "." + Name) unique: without it,
// two classes each defining `send` would collapse to one identity and a shard
// would probe one twice while the other went unprobed.
func pyWalkDefs(parent *sitter.Node, src []byte, receiver string, out *[]Signature) {
	if parent == nil {
		return
	}
	for i := 0; i < int(parent.NamedChildCount()); i++ {
		def := pyUnwrapDecorated(parent.NamedChild(i))
		if def == nil {
			continue
		}
		switch def.Type() {
		case "function_definition":
			*out = append(*out, pySignature(def, src, receiver))
		case "class_definition":
			// Nested classes recurse with the INNERMOST class as the receiver:
			// that is the name a reader (and a mutant-generator prompt) needs
			// to locate the method.
			name := fieldText(def, "name", src)
			pyWalkDefs(def.ChildByFieldName("body"), src, name, out)
		}
	}
}

// pySignature builds one Signature from a function_definition. receiver is the
// enclosing class name, or "" for a module-level function — which keeps free
// functions byte-identical to their previous shape.
func pySignature(def *sitter.Node, src []byte, receiver string) Signature {
	name := fieldText(def, "name", src)
	kind := "func"
	if receiver != "" {
		kind = "method"
	}
	sig := Signature{
		Name:     name,
		Kind:     kind,
		Receiver: receiver,
		Params:   pyParams(def.ChildByFieldName("parameters"), src),
		Line:     int(def.StartPoint().Row) + 1,
		Exported: !strings.HasPrefix(name, "_"),
	}
	if rt := def.ChildByFieldName("return_type"); rt != nil {
		sig.Results = []string{rt.Content(src)}
	}
	sig.Decisions = symbolDecisions(def, src, "python")
	sig.Complexity = 1 + len(sig.Decisions)
	sig.Lines = symbolLines(def)
	return sig
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
