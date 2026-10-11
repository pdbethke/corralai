//go:build cgo

// SPDX-License-Identifier: Elastic-2.0

package repoindex

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/pdbethke/corralai/internal/pairing"
)

// Rust is the seventh language's extractor and the first whose tests can live
// in the file under audit. Written against the real node names dumped from
// testdata/langspec/rust.rs (a byteorder file, a Warp file, and one
// constructed section — the iron rule of the layer: dump the tree first).

// extractRustSignatures collects free functions and the methods of impl and
// trait blocks, attributing each method to its enclosing type.
//
// Receiver is the impl's TYPE (`impl Shape for Square` → "Square") or, for a
// trait's default method, the trait's name. advpool.symbolIdentity is
// Receiver + "." + Name, so two impl blocks that each define `area` stay two
// symbols (the rider from the PHP review). Known limit: two impls of
// different generic traits for ONE type (`impl From<A> for X` and
// `impl From<B> for X`, each with `from`) still share an identity.
//
// A function_signature_item — a trait method declaration with no body — is
// skipped: there is nothing to mutate, and counting one would hand a shard a
// seat with no work in it. Functions nested inside a function body are not
// symbols of their own (they are part of the enclosing fn's span), and
// neither is anything under `#[cfg(test)]`: a test module is the audit's
// evidence, never its target.
func extractRustSignatures(text string) ([]Signature, error) {
	tree, src, err := parseTS(text, "rust")
	if err != nil {
		return nil, err
	}
	defer tree.Close()
	var out []Signature
	rustWalkItems(tree.RootNode(), src, &out)
	return out, nil
}

// rustWalkItems scans one item list (the file, or an inline `mod x { }`
// body). An inline module is descended into — real crates keep production
// code in them — except a #[cfg(test)] one.
func rustWalkItems(list *sitter.Node, src []byte, out *[]Signature) {
	if list == nil {
		return
	}
	for i := 0; i < int(list.NamedChildCount()); i++ {
		n := list.NamedChild(i)
		if n == nil {
			continue
		}
		switch n.Type() {
		case "function_item":
			*out = append(*out, rustCallable(n, src, ""))
		case "impl_item":
			rustMethods(n, "type", src, out)
		case "trait_item":
			rustMethods(n, "name", src, out)
		case "mod_item":
			if rustAttrs(n, src).cfgTest {
				continue
			}
			rustWalkItems(n.ChildByFieldName("body"), src, out)
		}
	}
}

// rustMethods appends the bodied functions of an impl or trait. recvField is
// the field naming the receiver: an impl's `type`, a trait's `name`.
func rustMethods(container *sitter.Node, recvField string, src []byte, out *[]Signature) {
	receiver := fieldText(container, recvField, src)
	body := container.ChildByFieldName("body")
	if body == nil {
		return
	}
	for i := 0; i < int(body.NamedChildCount()); i++ {
		n := body.NamedChild(i)
		if n == nil || n.Type() != "function_item" {
			// function_signature_item (bodiless), consts, associated types.
			continue
		}
		*out = append(*out, rustCallable(n, src, receiver))
	}
}

func rustCallable(n *sitter.Node, src []byte, receiver string) Signature {
	kind := "func"
	if receiver != "" {
		kind = "method"
	}
	decisions := symbolDecisions(n, src, "rust")
	sig := Signature{
		Name:      fieldText(n, "name", src),
		Kind:      kind,
		Receiver:  receiver,
		Params:    rustParams(n.ChildByFieldName("parameters"), src),
		Line:      int(n.StartPoint().Row) + 1,
		Exported:  rustExported(n, receiver),
		Decisions: decisions,
		// The same walk as symbolComplexity, taken once so the count and the
		// places can never disagree.
		Complexity: 1 + len(decisions),
		Lines:      symbolLines(n),
	}
	if rt := n.ChildByFieldName("return_type"); rt != nil {
		sig.Results = []string{rt.Content(src)}
	}
	return sig
}

// rustExported reports whether n is visible outside its declaring file. Rust
// has no capitalization rule: any visibility_modifier (`pub`, `pub(crate)`,
// `pub(super)`) widens it past the file, so any is exported. A method inside
// an impl or trait often carries none and is still reachable — a trait
// impl's methods take the trait's visibility, which has no syntax — so a
// receiver'd fn with no modifier is exported too; only a bare private free
// fn is not. (Inherent private methods are not told apart: Exported is a
// weighting hint, not a gate.)
func rustExported(n *sitter.Node, receiver string) bool {
	for i := 0; i < int(n.NamedChildCount()); i++ {
		if c := n.NamedChild(i); c != nil && c.Type() == "visibility_modifier" {
			return true
		}
	}
	return receiver != ""
}

// rustParams flattens a parameters node. A `parameter` is pattern + type; a
// self_parameter (`&mut self`) has no type and keeps its own text as the
// name, as PHP keeps `$msg`; anything else is kept verbatim, never dropped.
func rustParams(params *sitter.Node, src []byte) []Param {
	if params == nil {
		return nil
	}
	var out []Param
	for i := 0; i < int(params.NamedChildCount()); i++ {
		p := params.NamedChild(i)
		if p == nil || strings.HasSuffix(p.Type(), "comment") {
			continue
		}
		if p.Type() == "parameter" {
			out = append(out, Param{Name: fieldText(p, "pattern", src), Type: fieldText(p, "type", src)})
			continue
		}
		out = append(out, Param{Name: p.Content(src)})
	}
	return out
}

// rustModAttrs is what the attribute_items directly above an item say.
type rustModAttrs struct {
	cfgTest  bool
	pathAttr string
}

// rustAttrs reads the run of attribute_items (comments between them are
// skipped) that immediately precedes n. In this grammar an attribute is a
// SIBLING of the item it decorates, not a child, so it is found by walking
// backwards.
func rustAttrs(n *sitter.Node, src []byte) rustModAttrs {
	var a rustModAttrs
	for p := n.PrevNamedSibling(); p != nil; p = p.PrevNamedSibling() {
		switch p.Type() {
		case "attribute_item":
		case "line_comment", "block_comment":
			continue
		default:
			return a
		}
		attr := firstNamedOfType(p, "attribute")
		if attr == nil {
			continue
		}
		name := ""
		if id := firstNamedOfType(attr, "identifier"); id != nil {
			name = id.Content(src)
		}
		switch name {
		case "cfg":
			if tt := attr.ChildByFieldName("arguments"); tt != nil && strings.Join(strings.Fields(tt.Content(src)), "") == "(test)" {
				a.cfgTest = true
			}
		case "path":
			if v := attr.ChildByFieldName("value"); v != nil {
				a.pathAttr = strings.Trim(v.Content(src), `"`)
			}
		}
	}
	return a
}

func firstNamedOfType(n *sitter.Node, typ string) *sitter.Node {
	for i := 0; i < int(n.NamedChildCount()); i++ {
		if c := n.NamedChild(i); c != nil && c.Type() == typ {
			return c
		}
	}
	return nil
}

// rustTestModules reads the file's TOP-LEVEL test modules. A module is a test
// module when it is #[cfg(test)] or is named tests/test; every other `mod
// x;` is ordinary structure and reporting it would make every lib.rs look
// like it holds tests. Top level only: a module nested in an inline `mod`
// resolves its files relative to that module, not to this file, and a
// pairing that guessed otherwise would name the wrong file.
func rustTestModules(root *sitter.Node, src []byte) []pairing.TestModule {
	var out []pairing.TestModule
	for i := 0; i < int(root.NamedChildCount()); i++ {
		n := root.NamedChild(i)
		if n == nil || n.Type() != "mod_item" {
			continue
		}
		attrs := rustAttrs(n, src)
		name := fieldText(n, "name", src)
		if !attrs.cfgTest && name != "tests" && name != "test" {
			continue
		}
		out = append(out, pairing.TestModule{
			Name:     name,
			Inline:   n.ChildByFieldName("body") != nil,
			PathAttr: attrs.pathAttr,
			CfgTest:  attrs.cfgTest,
		})
	}
	return out
}
