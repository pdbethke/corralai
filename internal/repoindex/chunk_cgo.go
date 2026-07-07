//go:build cgo

package repoindex

import (
	"context"
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// chunkSymbols parses text with the tree-sitter grammar for lang, captures
// top-level definitions (+ one level of nested methods inside containers), and
// returns LineChunks with Symbol/Kind set.
func chunkSymbols(text, lang string) ([]LineChunk, error) {
	g := grammar(lang)
	if g == nil {
		return nil, fmt.Errorf("no grammar for %s", lang)
	}
	src := []byte(text)
	parser := sitter.NewParser()
	parser.SetLanguage(g)
	tree, err := parser.ParseCtx(context.Background(), nil, src)
	if err != nil {
		return nil, fmt.Errorf("tree-sitter parse: %w", err)
	}
	defer tree.Close()
	root := tree.RootNode()

	dts := defTypes(lang)

	// Collect flat, document-ordered list of spans (containers expanded inline).
	var allSpans []defSpan

	for i := 0; i < int(root.NamedChildCount()); i++ {
		node := root.NamedChild(i)
		if node == nil {
			continue
		}
		spanNode := node

		// Unwrap export_statement (TypeScript: `export function foo() {}`)
		if node.Type() == "export_statement" {
			for j := 0; j < int(node.NamedChildCount()); j++ {
				inner := node.NamedChild(j)
				if inner == nil {
					continue
				}
				if _, ok := dts[inner.Type()]; ok {
					node = inner
					spanNode = inner
					break
				}
			}
		}

		// Unwrap decorated_definition (Python: `@decorator\ndef f()`)
		if node.Type() == "decorated_definition" {
			for j := 0; j < int(node.NamedChildCount()); j++ {
				inner := node.NamedChild(j)
				if inner == nil {
					continue
				}
				if _, ok := dts[inner.Type()]; ok {
					node = inner
					break
				}
			}
		}

		kind, ok := dts[node.Type()]
		if !ok {
			continue
		}
		sym := extractName(node, src)
		cStart := int(spanNode.StartPoint().Row) + 1
		cEnd := int(spanNode.EndPoint().Row) + 1

		if isContainer(lang, node.Type()) {
			methods := findMethodsInContainer(node, lang, src)
			switch {
			case len(methods) > 0 && methods[0].start > cStart:
				allSpans = append(allSpans, defSpan{sym, kind, cStart, methods[0].start - 1})
				allSpans = append(allSpans, methods...)
			case len(methods) > 0:
				allSpans = append(allSpans, methods...)
			default:
				allSpans = append(allSpans, defSpan{sym, kind, cStart, cEnd})
			}
		} else {
			allSpans = append(allSpans, defSpan{sym, kind, cStart, cEnd})
		}
	}

	if root.HasError() && len(allSpans) == 0 {
		return nil, fmt.Errorf("parse error: no definitions captured in %s source", lang)
	}

	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	totalLines := len(lines)
	if totalLines == 0 {
		return nil, fmt.Errorf("empty source")
	}

	const winSize = 60
	const oversize = 2 * winSize
	const overlap = winSize / 4

	seq := 0
	cursor := 1
	var out []LineChunk

	emitChunks := func(sym, kind string, from, to int) {
		if from < 1 {
			from = 1
		}
		if to > totalLines {
			to = totalLines
		}
		if from > to {
			return
		}
		sub := subLines(lines, from, to)
		if to-from+1 > oversize {
			cs := chunkLines(sub, winSize, overlap)
			for _, c := range cs {
				out = append(out, LineChunk{
					Seq:       seq,
					StartLine: from + c.StartLine - 1,
					EndLine:   from + c.EndLine - 1,
					Text:      c.Text,
					Symbol:    sym,
					Kind:      kind,
					Lang:      lang,
				})
				seq++
			}
		} else {
			out = append(out, LineChunk{
				Seq:       seq,
				StartLine: from,
				EndLine:   to,
				Text:      sub,
				Symbol:    sym,
				Kind:      kind,
				Lang:      lang,
			})
			seq++
		}
	}

	for _, sp := range allSpans {
		if sp.start > cursor {
			emitChunks("", "", cursor, sp.start-1)
		}
		emitChunks(sp.symbol, sp.kind, sp.start, sp.end)
		cursor = sp.end + 1
	}
	if cursor <= totalLines {
		emitChunks("", "", cursor, totalLines)
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("no chunks produced for %s", lang)
	}
	return out, nil
}

func extractName(n *sitter.Node, src []byte) string {
	if n == nil {
		return ""
	}
	if child := n.ChildByFieldName("name"); child != nil {
		return child.Content(src)
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		ch := n.NamedChild(i)
		if ch == nil {
			continue
		}
		if ch.Type() == "type_spec" {
			if nc := ch.ChildByFieldName("name"); nc != nil {
				return nc.Content(src)
			}
		}
	}
	if child := n.ChildByFieldName("declarator"); child != nil {
		if child.Type() == "function_declarator" {
			if inner := child.ChildByFieldName("declarator"); inner != nil {
				return inner.Content(src)
			}
			for i := 0; i < int(child.NamedChildCount()); i++ {
				ch := child.NamedChild(i)
				if ch == nil {
					continue
				}
				t := ch.Type()
				if t == "identifier" || t == "field_identifier" || t == "type_identifier" {
					return ch.Content(src)
				}
			}
		}
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		ch := n.NamedChild(i)
		if ch == nil {
			continue
		}
		t := ch.Type()
		if t == "identifier" || t == "type_identifier" || t == "field_identifier" {
			return ch.Content(src)
		}
	}
	return ""
}

func findMethodsInContainer(node *sitter.Node, lang string, src []byte) []defSpan {
	mTypes := methodDefTypes(lang)
	if len(mTypes) == 0 {
		return nil
	}
	var result []defSpan
	var search func(n *sitter.Node, depth int)
	search = func(n *sitter.Node, depth int) {
		for i := 0; i < int(n.NamedChildCount()); i++ {
			ch := n.NamedChild(i)
			if ch == nil {
				continue
			}
			spanCh := ch
			defCh := ch
			if ch.Type() == "decorated_definition" {
				for j := 0; j < int(ch.NamedChildCount()); j++ {
					inner := ch.NamedChild(j)
					if inner == nil {
						continue
					}
					if _, ok := mTypes[inner.Type()]; ok {
						defCh = inner
						break
					}
				}
			}
			if kind, ok := mTypes[defCh.Type()]; ok {
				result = append(result, defSpan{
					symbol: extractName(defCh, src),
					kind:   kind,
					start:  int(spanCh.StartPoint().Row) + 1,
					end:    int(spanCh.EndPoint().Row) + 1,
				})
			} else if depth < 1 {
				search(ch, depth+1)
			}
		}
	}
	search(node, 0)
	return result
}
