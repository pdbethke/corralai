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
