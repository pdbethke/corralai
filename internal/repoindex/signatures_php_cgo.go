//go:build cgo

// SPDX-License-Identifier: Elastic-2.0

package repoindex

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// PHP had no signature extractor at all — ExtractSignatures returned
// ErrUnsupportedLang — even though its tree-sitter grammar was already
// wired in lang_cgo.go for the grammar table. Only the extractor was
// missing; this file is the sixth language's.
//
// Written against the real node names in testdata/php/sample.tree.txt (the
// iron rule of the layer: dump the parse tree first, never guess node
// names).

// extractPHPSignatures collects top-level functions plus class and trait
// methods, descending into class_declaration and trait_declaration bodies so
// a method is attributed to its enclosing type.
//
// interface_declaration is deliberately NOT descended into, and any
// method_declaration with no `body` field (an abstract method, or an
// interface member) is skipped: neither has a body to mutate, so counting
// one would hand a shard a seat with no work in it — the same reasoning the
// JS/TS extractor applies to interface_declaration's method_signature.
//
// Receiver is the enclosing class/trait name, matching every other
// extractor and keeping advpool.symbolIdentity (Receiver + "." + Name)
// unique: two classes each defining `handle` must not collapse to one
// identity, or a shard probes one twice while the other goes unprobed.
func extractPHPSignatures(text string) ([]Signature, error) {
	tree, src, err := parseTS(text, "php")
	if err != nil {
		return nil, err
	}
	defer tree.Close()
	var out []Signature
	phpWalkTop(tree.RootNode(), src, &out)
	return out, nil
}

// phpWalkTop scans the program's top-level children. A namespace_definition
// (the unbraced `namespace Foo\Bar;` form used throughout real PHP) carries
// no body field of its own in this grammar — everything that follows it is
// still a direct child of `program` — so no namespace recursion is needed.
func phpWalkTop(root *sitter.Node, src []byte, out *[]Signature) {
	if root == nil {
		return
	}
	for i := 0; i < int(root.NamedChildCount()); i++ {
		n := root.NamedChild(i)
		if n == nil {
			continue
		}
		switch n.Type() {
		case "function_definition":
			*out = append(*out, phpCallable(n, src, ""))
		case "class_declaration", "trait_declaration":
			*out = append(*out, phpSignaturesFromBody(n, src)...)
		}
	}
}

func phpSignaturesFromBody(typeDecl *sitter.Node, src []byte) []Signature {
	receiver := fieldText(typeDecl, "name", src)
	body := typeDecl.ChildByFieldName("body")
	if body == nil {
		return nil
	}
	var out []Signature
	for i := 0; i < int(body.NamedChildCount()); i++ {
		n := body.NamedChild(i)
		if n == nil || n.Type() != "method_declaration" {
			// use_declaration (trait use), property_declaration, class
			// constants, and anything else that isn't a method carries
			// nothing callable to mutate.
			continue
		}
		if n.ChildByFieldName("body") == nil {
			// Bodiless: an `abstract` method. Never extracted (iron rule).
			continue
		}
		out = append(out, phpCallable(n, src, receiver))
	}
	return out
}

func phpCallable(n *sitter.Node, src []byte, receiver string) Signature {
	kind := "func"
	if receiver != "" {
		kind = "method"
	}
	sig := Signature{
		Name:       fieldText(n, "name", src),
		Kind:       kind,
		Receiver:   receiver,
		Params:     phpParams(n.ChildByFieldName("parameters"), src),
		Line:       int(n.StartPoint().Row) + 1,
		Exported:   phpExported(n, src),
		Complexity: symbolComplexity(n, src, "php"),
		Lines:      symbolLines(n),
	}
	if rt := n.ChildByFieldName("return_type"); rt != nil {
		sig.Results = []string{rt.Content(src)}
	}
	return sig
}

// phpExported reports whether n (a method_declaration or function_definition)
// is visible outside its declaring file: PHP has no Go-style capitalization
// rule, so this reads the declared visibility_modifier instead
// (private/protected are not exported); a method with none — PHP's implicit
// visibility is public — is exported. Top-level functions have no
// visibility_modifier child at all and are always exported.
func phpExported(n *sitter.Node, src []byte) bool {
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		if c != nil && c.Type() == "visibility_modifier" {
			return c.Content(src) == "public"
		}
	}
	return true
}

// phpParams flattens a PHP formal_parameters node. simple_parameter is the
// dominant shape (`float $total`, or untyped `$msg`); the variable_name's
// content includes PHP's leading `$` sigil, matching how the parameter reads
// in source. Anything else (variadic, promoted-property, by-reference) falls
// back to its own source text rather than being dropped.
func phpParams(params *sitter.Node, src []byte) []Param {
	if params == nil {
		return nil
	}
	var out []Param
	for i := 0; i < int(params.NamedChildCount()); i++ {
		p := params.NamedChild(i)
		if p == nil {
			continue
		}
		if p.Type() == "simple_parameter" {
			out = append(out, Param{Name: fieldText(p, "name", src), Type: fieldText(p, "type", src)})
			continue
		}
		out = append(out, Param{Name: p.Content(src)})
	}
	return out
}
