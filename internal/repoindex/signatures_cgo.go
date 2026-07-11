//go:build cgo

// SPDX-License-Identifier: Elastic-2.0

package repoindex

import (
	"context"
	"strings"
	"unicode"

	sitter "github.com/smacker/go-tree-sitter"
)

func extractSignatures(text, lang string) ([]Signature, error) {
	switch lang {
	case "go":
		return extractGoSignatures(text)
	case "python":
		return extractPythonSignatures(text)
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
	return out, nil
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
