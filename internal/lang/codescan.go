// SPDX-License-Identifier: Elastic-2.0

package lang

import "strings"

// The pieces of a source scanner the static test listers share, for the
// languages whose lexical shape is C's: an identifier is letters, digits and
// "_" (and any non-ASCII byte), a line comment is "//", and blanking a
// comment or string keeps its line breaks so offsets and lines survive.
// Each lister keeps what is its language's own (Rust's raw strings and char
// literals, PHP's heredocs, Swift's interpolation).

// identByte reports whether c can continue an identifier.
func identByte(c byte) bool {
	return c == '_' || c >= 0x80 || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// isIdent reports whether t is an identifier: identifier bytes, not starting
// with a digit.
func isIdent(t string) bool {
	return t != "" && identByte(t[0]) && !(t[0] >= '0' && t[0] <= '9')
}

// blankSpan writes spaces over out[from:to], keeping b's line breaks.
func blankSpan(out, b []byte, from, to int) {
	for k := from; k < to; k++ {
		if b[k] == '\n' {
			out[k] = '\n'
		} else {
			out[k] = ' '
		}
	}
}

// lineCommentEnd is the offset of the line break that ends the line comment
// starting at i, or len(b).
func lineCommentEnd(b []byte, i int) int {
	for i < len(b) && b[i] != '\n' {
		i++
	}
	return i
}

// nestedCommentEnd is the offset just past the block comment that opens at i
// ("/*"), counting each inner "/*" as Rust and Swift do; ok is false when the
// source ends inside it.
func nestedCommentEnd(b []byte, i int) (int, bool) {
	depth, k := 1, i+2
	for k < len(b) && depth > 0 {
		switch {
		case b[k] == '/' && k+1 < len(b) && b[k+1] == '*':
			depth++
			k += 2
		case b[k] == '*' && k+1 < len(b) && b[k+1] == '/':
			depth--
			k += 2
		default:
			k++
		}
	}
	return k, depth == 0
}

// identTokens splits blanked code into identifiers, "::" and single
// punctuation. rawPrefix, when not empty, introduces a raw identifier that
// is read as the name after it (Rust's r#name); "" for a language without.
func identTokens(code, rawPrefix string) []string {
	var toks []string
	for i := 0; i < len(code); {
		c := code[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case rawPrefix != "" && strings.HasPrefix(code[i:], rawPrefix) && i+len(rawPrefix) < len(code) && identByte(code[i+len(rawPrefix)]):
			j := i + len(rawPrefix)
			for j < len(code) && identByte(code[j]) {
				j++
			}
			toks = append(toks, code[i+len(rawPrefix):j])
			i = j
		case identByte(c):
			j := i
			for j < len(code) && identByte(code[j]) {
				j++
			}
			toks = append(toks, code[i:j])
			i = j
		case c == ':' && strings.HasPrefix(code[i:], "::"):
			toks = append(toks, "::")
			i += 2
		default:
			toks = append(toks, string(c))
			i++
		}
	}
	return toks
}
