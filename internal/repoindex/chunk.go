// SPDX-License-Identifier: Elastic-2.0

package repoindex

import (
	"context"
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// LineChunk holds a contiguous window of source lines with optional symbol metadata.
type LineChunk struct {
	Seq       int
	StartLine int
	EndLine   int
	Text      string
	Symbol    string // populated by language-aware chunker; empty in fallback / preamble / gap
	Kind      string // e.g. "function", "type", "method"; empty in fallback / gap
	Lang      string // e.g. "go", "python"; tagged from file extension
}

// defSpan records a definition's line span before building LineChunks.
type defSpan struct {
	symbol string
	kind   string
	start  int // 1-indexed
	end    int // 1-indexed
}

// chunkFile dispatches by language: uses chunkSymbols for supported languages,
// falling back to chunkLines on parse error, empty capture, or unsupported lang.
// ALWAYS returns chunks — indexing never fails.
func chunkFile(path, text string) []LineChunk {
	lang := langForExt(path)
	if supported(lang) {
		if cs, err := chunkSymbols(text, lang); err == nil && len(cs) > 0 {
			return cs
		}
	}
	cs := chunkLines(text, 60, 15)
	for i := range cs {
		cs[i].Lang = lang
	}
	return cs
}

// chunkSymbols parses text with the tree-sitter grammar for lang, captures
// top-level definitions (+ one level of nested methods inside containers), and
// returns LineChunks with Symbol/Kind set.
//
// Invariants:
//   - Every line 1..N belongs to exactly one chunk (whole-file coverage).
//   - Oversized definitions (> 2*window lines) are sub-windowed via chunkLines,
//     each sub-chunk tagged with the same Symbol.
//   - Container types (class/struct with methods) emit a header chunk for the
//     declaration preamble, then one chunk per method — no double-indexing.
//   - Returns an error (triggering chunkFile fallback) when: no grammar registered,
//     parse fails, or HasError && zero definitions captured.
//
// A new parser is created per call — tree-sitter parsers are not goroutine-safe.
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
		// spanNode tracks which node's line range we use for the chunk (may differ from
		// node after unwrapping so that decorator/export lines are included).
		spanNode := node

		// Unwrap export_statement (TypeScript: `export function foo() {}`)
		// so the inner declaration is matched against dts.
		// spanNode follows node here because export keywords are on the same line.
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

		// Unwrap decorated_definition (Python: `@decorator\ndef f()` or `@decorator\nclass C`).
		// spanNode intentionally stays as the decorated_definition so that decorator
		// lines are included in the chunk span; node is set to the inner def for
		// kind/name extraction and container expansion.
		if node.Type() == "decorated_definition" {
			for j := 0; j < int(node.NamedChildCount()); j++ {
				inner := node.NamedChild(j)
				if inner == nil {
					continue
				}
				if _, ok := dts[inner.Type()]; ok {
					node = inner // kind/name from inner; spanNode retains decorated_definition
					break
				}
			}
		}

		kind, ok := dts[node.Type()]
		if !ok {
			continue
		}
		sym := extractName(node, src)
		cStart := int(spanNode.StartPoint().Row) + 1 // tree-sitter rows are 0-indexed
		cEnd := int(spanNode.EndPoint().Row) + 1

		if isContainer(lang, node.Type()) {
			methods := findMethodsInContainer(node, lang, src)
			switch {
			case len(methods) > 0 && methods[0].start > cStart:
				// Emit class header (cStart..firstMethod.start-1) then methods.
				allSpans = append(allSpans, defSpan{sym, kind, cStart, methods[0].start - 1})
				allSpans = append(allSpans, methods...)
			case len(methods) > 0:
				// Class declaration and first method share the same line (e.g. single-line class).
				// Skip the zero-width header; emit methods only.
				allSpans = append(allSpans, methods...)
			default:
				// No methods found — emit the container as a single span.
				allSpans = append(allSpans, defSpan{sym, kind, cStart, cEnd})
			}
		} else {
			allSpans = append(allSpans, defSpan{sym, kind, cStart, cEnd})
		}
	}

	// If the tree has errors AND we captured nothing, signal caller to fall back
	// to the line-window chunker rather than returning empty or garbage chunks.
	if root.HasError() && len(allSpans) == 0 {
		return nil, fmt.Errorf("parse error: no definitions captured in %s source", lang)
	}

	// Build LineChunks from spans, filling any gaps (preamble, inter-def, trailing)
	// with line-window chunks that carry an empty Symbol.
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	totalLines := len(lines)
	if totalLines == 0 {
		return nil, fmt.Errorf("empty source")
	}

	const winSize = 60
	const oversize = 2 * winSize // 120 lines
	const overlap = winSize / 4  // 15 lines

	seq := 0
	cursor := 1 // next unprocessed line (1-indexed)
	var out []LineChunk

	// emitChunks emits one or more LineChunks for lines [from, to] (1-indexed, inclusive).
	// If the range exceeds oversize, it is sub-windowed via chunkLines; otherwise one chunk.
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
			// Sub-window oversized range; tag each sub-chunk with the same symbol.
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
		// Fill any gap before this span (preamble or inter-def region).
		if sp.start > cursor {
			emitChunks("", "", cursor, sp.start-1)
		}
		// Emit the definition span itself.
		emitChunks(sp.symbol, sp.kind, sp.start, sp.end)
		cursor = sp.end + 1
	}
	// Fill any trailing gap after the last span.
	if cursor <= totalLines {
		emitChunks("", "", cursor, totalLines)
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("no chunks produced for %s", lang)
	}
	return out, nil
}

// extractName returns the identifier name of a tree-sitter definition node by:
//  1. Checking the "name" field directly (works for function_declaration, method_declaration,
//     function_definition, class_definition, method_definition, function_item, struct_item,
//     class_declaration, method, php function_definition, bash function_definition, etc.)
//  2. Scanning named children for a type_spec node (Go type_declaration).
//  3. C/C++ function_definition: traversing the "declarator" field chain
//     (function_definition → function_declarator → identifier / field_identifier).
//  4. Falling back to the first identifier, type_identifier, or field_identifier child.
func extractName(n *sitter.Node, src []byte) string {
	if n == nil {
		return ""
	}
	if child := n.ChildByFieldName("name"); child != nil {
		return child.Content(src)
	}
	// Go type_declaration: name lives on the type_spec named child, not the declaration itself.
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
	// C/C++ function_definition: name is nested inside the "declarator" field chain.
	// function_definition.declarator → function_declarator
	// function_declarator.declarator → identifier | field_identifier | pointer_declarator ...
	if child := n.ChildByFieldName("declarator"); child != nil {
		if child.Type() == "function_declarator" {
			if inner := child.ChildByFieldName("declarator"); inner != nil {
				return inner.Content(src)
			}
			// Fallback: first identifier / field_identifier inside function_declarator.
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
	// Last resort: first identifier, type_identifier, or field_identifier child.
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

// findMethodsInContainer searches one level of nesting inside a container node
// (class body) for method-like definition nodes per methodDefTypes(lang).
// It descends at most one intermediate level (e.g., class_definition → block → function_definition).
//
// decorated_definition nodes (Python @decorator + def/class inside a class body) are unwrapped:
// the inner function_definition is used for kind/name extraction, but the span includes the
// decorated_definition so that decorator lines appear in the chunk.
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
			// Unwrap decorated_definition (Python @decorator\ndef method inside a class body).
			// spanCh retains the decorated_definition's line range so the decorator is in the chunk.
			spanCh := ch
			defCh := ch
			if ch.Type() == "decorated_definition" {
				for j := 0; j < int(ch.NamedChildCount()); j++ {
					inner := ch.NamedChild(j)
					if inner == nil {
						continue
					}
					if _, ok := mTypes[inner.Type()]; ok {
						defCh = inner // kind/name from inner def
						break         // spanCh stays as decorated_definition
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
				// Recurse into intermediate body nodes (block, class_body, etc.) once.
				search(ch, depth+1)
			}
		}
	}
	search(node, 0)
	return result
}

// subLines joins lines[from-1 : to] (1-indexed, inclusive) from a pre-split slice.
func subLines(lines []string, from, to int) string {
	if from < 1 {
		from = 1
	}
	if to > len(lines) {
		to = len(lines)
	}
	if from > to {
		return ""
	}
	return strings.Join(lines[from-1:to], "\n")
}

// chunkLines splits text into overlapping windows of `window` lines stepping by
// (window-overlap). Lines are 1-based; the final short window is included.
func chunkLines(text string, window, overlap int) []LineChunk {
	if window <= 0 {
		window = 60
	}
	if overlap < 0 || overlap >= window {
		overlap = window / 4
	}
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1] // drop the empty tail from a trailing newline
	}
	if len(lines) == 0 {
		return nil
	}
	step := window - overlap
	var out []LineChunk
	seq := 0
	for start := 0; start < len(lines); start += step {
		end := start + window
		if end > len(lines) {
			end = len(lines)
		}
		out = append(out, LineChunk{Seq: seq, StartLine: start + 1, EndLine: end, Text: strings.Join(lines[start:end], "\n")})
		seq++
		if end == len(lines) {
			break
		}
	}
	return out
}
