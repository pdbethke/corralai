//go:build cgo

// SPDX-License-Identifier: Elastic-2.0

package repoindex

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// Ruby, JavaScript and TypeScript had no signature extractor at all —
// ExtractSignatures returned ErrUnsupportedLang — even though their
// tree-sitter grammars were already wired for chunking. Only the extractors
// were missing.
//
// The cost was not cosmetic. advpool.ShardSymbols bin-packs SYMBOLS, so with
// none of them the mutant-generator collapses to a single whole-file seat and
// its prompt's SIGNATURE SURFACE is empty — for exactly the languages that
// already pair worst (expressjs/express is pinned at ZERO candidates in the CI
// sweep). Complexity was unavailable for the same reason, so those files could
// not be ranked or balanced either.

// extractRubySignatures collects Ruby methods, descending into classes and
// modules so a method is attributed to its enclosing constant.
//
// Receiver is that constant's name, matching the Go and Python extractors and
// keeping advpool.symbolIdentity (Receiver + "." + Name) unique: two classes
// each defining `call` must not collapse to one identity, or a shard probes one
// twice while the other goes unprobed.
func extractRubySignatures(text string) ([]Signature, error) {
	tree, src, err := parseTS(text, "ruby")
	if err != nil {
		return nil, err
	}
	defer tree.Close()
	var out []Signature
	rubyWalk(tree.RootNode(), src, "", &out)
	return out, nil
}

func rubyWalk(parent *sitter.Node, src []byte, receiver string, out *[]Signature) {
	if parent == nil {
		return
	}
	for i := 0; i < int(parent.NamedChildCount()); i++ {
		n := parent.NamedChild(i)
		if n == nil {
			continue
		}
		switch n.Type() {
		case "method", "singleton_method":
			// `def self.build` is a singleton_method: still a callable unit
			// with a body to mutate, so it is collected the same way.
			name := fieldText(n, "name", src)
			if name == "" {
				continue
			}
			kind := "func"
			if receiver != "" {
				kind = "method"
			}
			sig := Signature{
				Name:     name,
				Kind:     kind,
				Receiver: receiver,
				Params:   rubyParams(n.ChildByFieldName("parameters"), src),
				Line:     int(n.StartPoint().Row) + 1,
				// Ruby has no export marker; a leading underscore is the
				// closest widely-used convention for "internal".
				Exported:   !strings.HasPrefix(name, "_"),
				Decisions:  symbolDecisions(n, src, "ruby"),
				Complexity: symbolComplexity(n, src, "ruby"),
				Lines:      symbolLines(n),
			}
			*out = append(*out, sig)
		case "class", "module":
			// Both introduce a namespace a method belongs to. Recursing with
			// the INNERMOST name is what a reader — and a mutant-generator
			// prompt — needs in order to locate the method.
			name := fieldText(n, "name", src)
			rubyWalk(n.ChildByFieldName("body"), src, name, out)
		}
	}
}

func rubyParams(params *sitter.Node, src []byte) []Param {
	if params == nil {
		return nil
	}
	var out []Param
	for i := 0; i < int(params.NamedChildCount()); i++ {
		p := params.NamedChild(i)
		if p == nil {
			continue
		}
		// Ruby params are untyped; the node's own text is the most faithful
		// rendering (it preserves splats, defaults and keyword forms).
		out = append(out, Param{Name: p.Content(src)})
	}
	return out
}

// extractCurlySignatures handles javascript and typescript, whose grammars name
// these nodes identically — TS being a superset of JS. One extractor rather than
// two so the pair cannot drift; lang is threaded only so complexity uses the
// right branch table and the caller's language is echoed faithfully.
func extractCurlySignatures(text, lang string) ([]Signature, error) {
	tree, src, err := parseTS(text, lang)
	if err != nil {
		return nil, err
	}
	defer tree.Close()
	var out []Signature
	curlyWalk(tree.RootNode(), src, lang, "", &out)
	return out, nil
}

func curlyWalk(parent *sitter.Node, src []byte, lang, receiver string, out *[]Signature) {
	if parent == nil {
		return
	}
	for i := 0; i < int(parent.NamedChildCount()); i++ {
		n := parent.NamedChild(i)
		if n == nil {
			continue
		}
		switch n.Type() {
		case "function_declaration", "generator_function_declaration", "method_definition":
			name := fieldText(n, "name", src)
			if name == "" {
				continue
			}
			*out = append(*out, curlySignature(n, src, lang, receiver, name))

		case "class_declaration":
			// The class name is `identifier` in JS and `type_identifier` in TS;
			// fieldText resolves both via the `name` field.
			curlyWalk(n.ChildByFieldName("body"), src, lang, fieldText(n, "name", src), out)

		case "lexical_declaration", "variable_declaration":
			// `const f = (a) => ...` and `const f = function () {}`. A named
			// arrow is a first-class unit of JS code — whole modules export
			// nothing else — so missing it would leave real files with no
			// symbols at all.
			for j := 0; j < int(n.NamedChildCount()); j++ {
				d := n.NamedChild(j)
				if d == nil || d.Type() != "variable_declarator" {
					continue
				}
				val := d.ChildByFieldName("value")
				if val == nil {
					continue
				}
				if t := val.Type(); t != "arrow_function" && t != "function_expression" && t != "function" {
					continue
				}
				name := fieldText(d, "name", src)
				if name == "" {
					continue
				}
				// Measured over the FUNCTION body, not the declaration, so the
				// complexity and line span describe the code that runs.
				*out = append(*out, curlySignature(val, src, lang, receiver, name))
			}

		case "expression_statement":
			// `res.send = function send(body) {}` — a property assignment, and
			// the DOMINANT shape in CommonJS-era JavaScript. express/lib
			// yielded 2 symbols for an entire response module before this,
			// because none of its functions are declarations, class methods or
			// const arrows. Missing it leaves real files effectively unindexed,
			// which then collapses sharding to a single seat.
			for j := 0; j < int(n.NamedChildCount()); j++ {
				as := n.NamedChild(j)
				if as == nil || as.Type() != "assignment_expression" {
					continue
				}
				val := as.ChildByFieldName("right")
				if val == nil {
					continue
				}
				if t := val.Type(); t != "arrow_function" && t != "function_expression" && t != "function" {
					continue
				}
				// The assignment target names the symbol: `res.send` gives
				// receiver "res", name "send" — matching the Receiver + "." +
				// Name identity every other extractor produces. An unqualified
				// target (`f = function(){}`) has no receiver.
				name, recv := "", receiver
				switch left := as.ChildByFieldName("left"); {
				case left == nil:
					continue
				case left.Type() == "member_expression":
					if prop := left.ChildByFieldName("property"); prop != nil {
						name = prop.Content(src)
					}
					if obj := left.ChildByFieldName("object"); obj != nil {
						// `Thing.prototype.send` reads better as Thing.send
						// than as "Thing.prototype".send.
						recv = strings.TrimSuffix(obj.Content(src), ".prototype")
					}
				default:
					name = left.Content(src)
				}
				if name == "" {
					continue
				}
				*out = append(*out, curlySignature(val, src, lang, recv, name))
			}

		// interface_declaration is deliberately NOT descended into: a
		// method_signature has no body, so there is nothing to mutate and
		// nothing a test could catch. Counting one would inflate the symbol
		// count with units the audit can never grade, and hand a shard a seat
		// with no work in it.
		default:
			// export statements wrap the real declaration; descend so
			// `export function f()` and `export class C {}` are not lost.
			if n.Type() == "export_statement" {
				curlyWalk(n, src, lang, receiver, out)
			}
		}
	}
}

func curlySignature(n *sitter.Node, src []byte, lang, receiver, name string) Signature {
	kind := "func"
	if receiver != "" {
		kind = "method"
	}
	sig := Signature{
		Name:     name,
		Kind:     kind,
		Receiver: receiver,
		Params:   curlyParams(n.ChildByFieldName("parameters"), src),
		Line:     int(n.StartPoint().Row) + 1,
		// JS/TS have no export marker on the symbol itself; a leading
		// underscore is the closest widely-used "internal" convention.
		Exported:   !strings.HasPrefix(name, "_"),
		Decisions:  symbolDecisions(n, src, lang),
		Complexity: symbolComplexity(n, src, lang),
		Lines:      symbolLines(n),
	}
	if rt := n.ChildByFieldName("return_type"); rt != nil {
		sig.Results = []string{strings.TrimPrefix(rt.Content(src), ":")}
	}
	return sig
}

func curlyParams(params *sitter.Node, src []byte) []Param {
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
		case "identifier", "shorthand_property_identifier_pattern":
			out = append(out, Param{Name: p.Content(src)})
		case "required_parameter", "optional_parameter":
			// TypeScript: the name is the `pattern` field, the type an
			// annotation child.
			name := ""
			if pat := p.ChildByFieldName("pattern"); pat != nil {
				name = pat.Content(src)
			}
			typ := ""
			if ta := p.ChildByFieldName("type"); ta != nil {
				typ = strings.TrimPrefix(strings.TrimSpace(ta.Content(src)), ":")
				typ = strings.TrimSpace(typ)
			}
			out = append(out, Param{Name: name, Type: typ})
		default:
			out = append(out, Param{Name: p.Content(src)})
		}
	}
	return out
}
