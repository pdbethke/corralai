//go:build cgo

// SPDX-License-Identifier: Elastic-2.0

package repoindex

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// Swift's reader. Written against the node names the grammar gives
// testdata/langspec/swift.swift (dump the tree first, never guess node
// names). Its decision points are the swift row's branches, defaultArms and
// conditionLists, counted by the shared walk (symbolDecisions) as every
// language's are.

// extractSwiftSignatures collects the units of Swift code that have a body to
// mutate: functions, initializers, deinitializers, subscripts, computed
// properties, properties with willSet/didSet observers, and properties whose
// value is a closure (the counterpart of the javascript row's `const f = () =>`),
// descending into struct, class, enum, actor and extension bodies so each is
// attributed to its type.
//
// A protocol's requirements have no body and are skipped, as the javascript
// and php readers skip an interface's members; a protocol extension's methods
// do have one and are read under the protocol's name. A function inside a
// function body, or a closure inside one, is part of the enclosing unit (its
// lines and decision points count there), as in the rust reader. Stored
// properties without observers, enum cases and top-level statements have no
// body of their own and are not symbols.
//
// Name is Swift's own name for the unit: a function's base name with its
// argument labels, `parse(_:)`, `init(w:h:)`, `subscript(_:)`, `+(_:_:)`; a
// property's name; `deinit`. Swift overloads by argument label, and a type
// commonly has several initializers, so the base name alone would give
// `Box.init` for each and advpool.symbolIdentity (Receiver + "." + Name) would
// collapse them into one symbol. Overloads that differ only in parameter
// types still share a name; Swift's documentation tells those apart by type,
// and this reader does not.
//
// Receiver is the enclosing type, qualified through the types it is nested in
// (`Outer.Inner`), which is how an extension names a nested type
// (`extension Outer.Inner`), so a method declared in the type and one added
// by an extension share a receiver. An extension's receiver is the type it
// names as written.
//
// Exported is false for a unit declared private or fileprivate, or inside a
// type or extension declared so; true otherwise. Swift's default, internal,
// is visible to the module's tests through `@testable import`. (A weighting
// hint, as in the other readers, not a gate.)
func extractSwiftSignatures(root *sitter.Node, src []byte) []Signature {
	var out []Signature
	swiftWalk(root, src, "", false, &out)
	return out
}

func swiftWalk(parent *sitter.Node, src []byte, receiver string, private bool, out *[]Signature) {
	if parent == nil {
		return
	}
	for i := 0; i < int(parent.NamedChildCount()); i++ {
		n := parent.NamedChild(i)
		if n == nil {
			continue
		}
		switch n.Type() {
		case "function_declaration", "init_declaration", "deinit_declaration", "subscript_declaration":
			*out = append(*out, swiftCallable(n, src, receiver, private))
		case "property_declaration":
			if swiftPropertyHasBody(n) {
				*out = append(*out, swiftUnit(n, src, swiftPropertyName(n, src), receiver, private))
			}
		case "class_declaration":
			name := strings.Join(strings.Fields(fieldText(n, "name", src)), "")
			if fieldText(n, "declaration_kind", src) != "extension" && receiver != "" {
				name = receiver + "." + name
			}
			swiftWalk(n.ChildByFieldName("body"), src, name, private || swiftPrivate(n, src), out)
		}
	}
}

// swiftCallable is a function, initializer, deinitializer or subscript.
func swiftCallable(n *sitter.Node, src []byte, receiver string, private bool) Signature {
	base := ""
	switch n.Type() {
	case "init_declaration":
		base = "init"
	case "deinit_declaration":
		base = "deinit"
	case "subscript_declaration":
		base = "subscript"
	default:
		// The first name field is the function's name: a simple_identifier,
		// or the operator token itself for an operator function. The grammar
		// gives the return type the name field too, after the parameters.
		if c := n.ChildByFieldName("name"); c != nil {
			base = c.Content(src)
		}
	}
	sig := swiftUnit(n, src, base, receiver, private)
	if base == "deinit" {
		return sig
	}
	// An operator function's and a subscript's parameters take no argument
	// label unless they name one; a function's and an initializer's take
	// their parameter name.
	unlabelled := base == "subscript" || (base != "" && !swiftIdentStart(base))
	var labels strings.Builder
	afterArrow := false
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		switch {
		case c.Type() == "parameter":
			name, typ := swiftParam(c, src)
			sig.Params = append(sig.Params, Param{Name: name, Type: typ})
			label := fieldText(c, "external_name", src)
			if label == "" {
				label = name
				if unlabelled {
					label = "_"
				}
			}
			labels.WriteString(label + ":")
		case !c.IsNamed() && c.Type() == "->":
			afterArrow = true
		case afterArrow && c.IsNamed():
			sig.Results = []string{c.Content(src)}
			afterArrow = false
		}
	}
	sig.Name = base + "(" + labels.String() + ")"
	return sig
}

// swiftUnit is the Signature every unit shares: its span, its decision
// points and its visibility.
func swiftUnit(n *sitter.Node, src []byte, name, receiver string, private bool) Signature {
	kind := "func"
	if receiver != "" {
		kind = "method"
	}
	decisions := symbolDecisions(n, src, "swift")
	return Signature{
		Name:       name,
		Kind:       kind,
		Receiver:   receiver,
		Line:       int(n.StartPoint().Row) + 1,
		Exported:   !private && !swiftPrivate(n, src),
		Decisions:  decisions,
		Complexity: 1 + len(decisions),
		Lines:      symbolLines(n),
	}
}

// swiftParam is a parameter's name and type. The grammar gives both the name
// field (the first is the name, the second the type) and leaves `inout`, an
// attribute such as `@escaping` and a variadic's `...` outside the type node,
// so the type is the parameter's text after its colon, which keeps them.
func swiftParam(p *sitter.Node, src []byte) (name, typ string) {
	if c := p.ChildByFieldName("name"); c != nil {
		name = c.Content(src)
	}
	if _, after, ok := strings.Cut(p.Content(src), ":"); ok {
		typ = strings.Join(strings.Fields(after), " ")
	}
	return name, typ
}

// swiftPropertyHasBody: a computed property, a property with observers, or a
// property whose value is a closure. A stored property's initial value is an
// expression evaluated once, not a body.
func swiftPropertyHasBody(n *sitter.Node) bool {
	if n.ChildByFieldName("computed_value") != nil || firstNamedOfType(n, "willset_didset_block") != nil {
		return true
	}
	v := n.ChildByFieldName("value")
	return v != nil && v.Type() == "lambda_literal"
}

// swiftPropertyName is the identifier a property's pattern binds.
func swiftPropertyName(n *sitter.Node, src []byte) string {
	if pat := n.ChildByFieldName("name"); pat != nil {
		if id := pat.ChildByFieldName("bound_identifier"); id != nil {
			return id.Content(src)
		}
		return pat.Content(src)
	}
	return ""
}

// swiftPrivate reports a private or fileprivate access modifier on n itself.
// `private(set)` restricts only the setter, so it does not.
func swiftPrivate(n *sitter.Node, src []byte) bool {
	mods := firstNamedOfType(n, "modifiers")
	if mods == nil {
		return false
	}
	for i := 0; i < int(mods.NamedChildCount()); i++ {
		if c := mods.NamedChild(i); c != nil && c.Type() == "visibility_modifier" {
			if v := c.Content(src); v == "private" || v == "fileprivate" {
				return true
			}
		}
	}
	return false
}

// swiftIdentStart reports whether name starts as an identifier does, which an
// operator function's name does not.
func swiftIdentStart(name string) bool {
	r := name[0]
	return r == '_' || r == '`' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= 0x80
}
