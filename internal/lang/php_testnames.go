// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"strings"
)

// TestNamesInSource lists what PHPUnit collects from a test file, read from
// its source: in each named class, the public methods whose name starts with
// "test", or that carry a #[Test] attribute or an @test docblock annotation,
// as Class::method in file order, each once. That is PHPUnit's own test id,
// which `--filter` accepts, so it is the selector form a PHP SingleTestCmd
// would take.
//
// It is a scanner, not a parser: phpCode first blanks every comment, string,
// heredoc and nowdoc, so a brace or a "function test" inside one is not
// structure, and the walk then reads only class bodies. A method of a trait,
// interface, enum or anonymous class is not listed, nor a closure inside a
// test.
//
// The list is a promise the critic is held to (see the Python lister), so a
// scan that ends inside a comment, string or attribute, or with unbalanced
// braces, lists NOTHING rather than the tests it found before it lost its
// place.
func (phpPlugin) TestNamesInSource(_, src string) []string {
	code, ok := phpCode(src)
	if !ok {
		return nil
	}
	toks := phpTokens(code)

	var out []string
	seen := map[string]bool{}
	// scopes holds, per open brace, the class it opens ("" for any other).
	var scopes []string
	pendingClass := ""
	// What precedes the member being declared: reset at every ; { }.
	marked, hidden := false, false
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		switch t {
		case "{":
			scopes = append(scopes, pendingClass)
			pendingClass = ""
			marked, hidden = false, false
			continue
		case "}":
			if len(scopes) == 0 {
				return nil
			}
			scopes = scopes[:len(scopes)-1]
			marked, hidden = false, false
			continue
		case ";":
			marked, hidden = false, false
			continue
		case "@":
			// phpCode leaves an @ where a docblock carried @test; at member
			// level PHP has no other use for one.
			marked = true
			continue
		case "#[":
			depth := 1
			for i++; i < len(toks) && depth > 0; i++ {
				switch toks[i] {
				case "#[", "[":
					depth++
				case "]":
					depth--
				default:
					if n := toks[i]; n == "Test" || strings.HasSuffix(n, `\Test`) {
						marked = true
					}
				}
			}
			if depth > 0 {
				return nil
			}
			i--
			continue
		}
		lower := strings.ToLower(t)
		switch lower {
		case "class":
			prev := ""
			if i > 0 {
				prev = strings.ToLower(toks[i-1])
			}
			if prev != "::" && prev != "->" && prev != "new" && i+1 < len(toks) && phpIdent(toks[i+1]) {
				pendingClass = toks[i+1]
				i++
			}
		case "private", "protected":
			hidden = true
		case "function":
			class := ""
			if n := len(scopes); n > 0 {
				class = scopes[n-1]
			}
			j := i + 1
			if j < len(toks) && toks[j] == "&" {
				j++
			}
			if class == "" || j >= len(toks) || !phpIdent(toks[j]) {
				continue
			}
			name := toks[j]
			if !hidden && (marked || strings.HasPrefix(name, "test")) {
				id := class + "::" + name
				if !seen[id] {
					seen[id] = true
					out = append(out, id)
				}
			}
			marked, hidden = false, false
			i = j
		}
	}
	if len(scopes) != 0 {
		return nil
	}
	return out
}

// phpCode returns src with everything that is not PHP code blanked to
// spaces: the inline text outside <?php ... ?>, comments, and string,
// heredoc and nowdoc bodies. Line breaks are kept. A docblock that carries
// an @test annotation leaves a single @ where it started. ok is false when
// the source ends inside a comment, string or heredoc.
func phpCode(src string) (string, bool) {
	b := []byte(src)
	out := make([]byte, len(b))
	blank := func(from, to int) {
		for k := from; k < to; k++ {
			if b[k] == '\n' {
				out[k] = '\n'
			} else {
				out[k] = ' '
			}
		}
	}
	i := 0
	// Inline text before the first open tag.
	open := func(from int) int {
		k := strings.Index(src[from:], "<?php")
		if k < 0 {
			blank(from, len(b))
			return len(b)
		}
		blank(from, from+k+len("<?php"))
		return from + k + len("<?php")
	}
	i = open(0)
	for i < len(b) {
		c := b[i]
		switch {
		case c == '?' && i+1 < len(b) && b[i+1] == '>':
			blank(i, i+2)
			i = open(i + 2)
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return "", false
			}
			end += i + 2 + 2
			blank(i, end)
			if strings.HasPrefix(src[i:], "/**") && phpDocHasTest(src[i:end]) {
				out[i] = '@'
			}
			i = end
		case (c == '/' && i+1 < len(b) && b[i+1] == '/') || (c == '#' && !(i+1 < len(b) && b[i+1] == '[')):
			end := i
			for end < len(b) && b[end] != '\n' && !(b[end] == '?' && end+1 < len(b) && b[end+1] == '>') {
				end++
			}
			blank(i, end)
			i = end
		case c == '\'' || c == '"' || c == '`':
			end := i + 1
			for end < len(b) && b[end] != c {
				if b[end] == '\\' {
					end++
				}
				end++
			}
			if end >= len(b) {
				return "", false
			}
			blank(i, end+1)
			i = end + 1
		case c == '<' && strings.HasPrefix(src[i:], "<<<"):
			end, ok := phpHeredocEnd(src, i)
			if !ok {
				return "", false
			}
			blank(i, end)
			i = end
		default:
			out[i] = c
			i++
		}
	}
	return string(out), true
}

// phpHeredocEnd returns the offset just past the closing identifier of the
// heredoc or nowdoc that opens at start (`<<<ID`, `<<<"ID"`, `<<<'ID'`). The
// closing identifier may be indented (PHP 7.3+) and must not run on into
// more identifier characters.
func phpHeredocEnd(src string, start int) (int, bool) {
	k := start + 3
	for k < len(src) && (src[k] == ' ' || src[k] == '\t') {
		k++
	}
	if k < len(src) && (src[k] == '"' || src[k] == '\'') {
		k++
	}
	idStart := k
	for k < len(src) && phpIdentByte(src[k]) {
		k++
	}
	id := src[idStart:k]
	if id == "" {
		return 0, false
	}
	nl := strings.IndexByte(src[k:], '\n')
	if nl < 0 {
		return 0, false
	}
	pos := k + nl + 1
	for pos <= len(src) {
		line := src[pos:]
		if e := strings.IndexByte(line, '\n'); e >= 0 {
			line = line[:e]
		}
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, id) && (len(trimmed) == len(id) || !phpIdentByte(trimmed[len(id)])) {
			return pos + (len(line) - len(trimmed)) + len(id), true
		}
		if len(line) == len(src[pos:]) {
			return 0, false
		}
		pos += len(line) + 1
	}
	return 0, false
}

// phpDocHasTest reports whether a docblock carries PHPUnit's @test
// annotation (not @testdox, @testWith or any other @test-prefixed tag).
func phpDocHasTest(doc string) bool {
	for k := strings.Index(doc, "@test"); k >= 0; {
		after := k + len("@test")
		if after >= len(doc) || !phpIdentByte(doc[after]) {
			return true
		}
		n := strings.Index(doc[after:], "@test")
		if n < 0 {
			return false
		}
		k = after + n
	}
	return false
}

// phpTokens splits blanked PHP code into the tokens the lister reads: names
// (with namespace separators), "#[", "::", "->", and single punctuation.
func phpTokens(code string) []string {
	var toks []string
	for i := 0; i < len(code); {
		c := code[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case phpIdentByte(c) || c == '\\':
			j := i
			for j < len(code) && (phpIdentByte(code[j]) || code[j] == '\\') {
				j++
			}
			toks = append(toks, code[i:j])
			i = j
		case c == '#' && i+1 < len(code) && code[i+1] == '[':
			toks = append(toks, "#[")
			i += 2
		case (c == ':' && strings.HasPrefix(code[i:], "::")) || (c == '-' && strings.HasPrefix(code[i:], "->")):
			toks = append(toks, code[i:i+2])
			i += 2
		default:
			toks = append(toks, string(c))
			i++
		}
	}
	return toks
}

func phpIdentByte(c byte) bool {
	return c == '_' || c >= 0x80 || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func phpIdent(t string) bool {
	return t != "" && phpIdentByte(t[0]) && !(t[0] >= '0' && t[0] <= '9')
}
