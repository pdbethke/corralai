// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"strings"
)

// TestNamesInSource lists what libtest collects from a Rust file, read from
// its source: every function at module level (the file's top level, or inside
// a `mod name { ... }`) carrying a test attribute, as its module path within
// this file joined with "::" (`tests::name`, or `name` at the top of a
// `#[path]` test file, whose top level IS the test module's body), in file
// order, each once. That is a suffix of libtest's own test path, and
// `cargo test <filter>` matches by substring, so it is a usable selector
// without knowing where the crate mounts the file.
//
// A test attribute is one whose path ends in `test` (#[test],
// #[tokio::test], #[test_log::test]) or names a case-generating framework
// (#[rstest], #[test_case], #[wasm_bindgen_test]); one riding beside it
// (#[serial_test::serial], #[should_panic]) is not. A function inside a
// function body or an impl block is not collected by libtest and is not
// listed. A macro invocation's braces (`proptest! { #[test] fn ... }`) are
// read as the module they sit in; a `macro_rules!` body is not, since what it
// declares exists only where it is invoked.
//
// It is a scanner, not a parser: rustCode first blanks every comment (block
// comments nest), string, raw string and char literal, so a brace or a
// "#[test]" inside one is not structure. The list is a promise the critic is
// held to, so a scan that ends inside a comment, string or attribute, or with
// unbalanced braces, lists NOTHING rather than the tests before it lost its
// place.
func (rustPlugin) TestNamesInSource(_, src string) []string {
	code, ok := rustCode(src)
	if !ok {
		return nil
	}
	toks := rustTokens(code)

	type scope struct {
		module bool   // a module body, where libtest collects
		path   string // the module path within this file, for a module
	}
	scopes := []scope{{module: true}}
	var out []string
	seen := map[string]bool{}
	marked := false
	pendingMod, pendingMacro := "", ""
	reset := func() { marked, pendingMod, pendingMacro = false, "", "" }
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		cur := scopes[len(scopes)-1]
		switch t {
		case "{":
			next := scope{}
			switch {
			case pendingMod != "" && cur.module:
				next = scope{module: true, path: rustJoin(cur.path, pendingMod)}
			case pendingMacro != "" && pendingMacro != "macro_rules":
				next = cur
			}
			scopes = append(scopes, next)
			reset()
			continue
		case "}":
			if len(scopes) == 1 {
				return nil
			}
			scopes = scopes[:len(scopes)-1]
			reset()
			continue
		case ";":
			reset()
			continue
		case "#":
			j := i + 1
			if j < len(toks) && toks[j] == "!" {
				j++ // an inner attribute, #![...]: never a test marker
			}
			if j >= len(toks) || toks[j] != "[" {
				continue
			}
			depth, k := 1, j+1
			var attrPath []string
			inPath := true
			for ; k < len(toks) && depth > 0; k++ {
				switch x := toks[k]; {
				case x == "[" || x == "(" || x == "{":
					depth++
					inPath = false
				case x == "]" || x == ")" || x == "}":
					depth--
				case x == "::":
				case inPath && rustIdent(x):
					attrPath = append(attrPath, x)
				default:
					inPath = false
				}
			}
			if depth > 0 {
				return nil
			}
			if j == i+1 && rustTestAttr(attrPath) {
				marked = true
			}
			i = k - 1
			continue
		case "!":
			if i > 0 && rustIdent(toks[i-1]) {
				pendingMacro = toks[i-1]
			}
			continue
		}
		switch t {
		case "mod":
			if i+1 < len(toks) && rustIdent(toks[i+1]) {
				pendingMod = toks[i+1]
				i++
			}
		case "fn":
			if i+1 >= len(toks) || !rustIdent(toks[i+1]) {
				continue
			}
			name := toks[i+1]
			i++
			if marked && cur.module {
				id := rustJoin(cur.path, name)
				if !seen[id] {
					seen[id] = true
					out = append(out, id)
				}
			}
			marked = false
		}
	}
	if len(scopes) != 1 {
		return nil
	}
	return out
}

// rustTestAttr reports whether an attribute path marks a test function.
func rustTestAttr(path []string) bool {
	if len(path) == 0 {
		return false
	}
	switch path[len(path)-1] {
	case "test", "rstest", "test_case", "wasm_bindgen_test":
		return true
	}
	return false
}

func rustJoin(path, name string) string {
	if path == "" {
		return name
	}
	return path + "::" + name
}

// rustCode returns src with every comment, string and char literal blanked
// to spaces, line breaks kept. Block comments nest, as Rust's do. A quote
// that does not close as a char literal is a lifetime or a label and stays
// code. ok is false when the source ends inside a comment or string.
func rustCode(src string) (string, bool) {
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
	// startsToken: the byte before i cannot continue an identifier, so a
	// prefix letter at i (r, b, c) begins a literal rather than ends a name.
	startsToken := func(i int) bool { return i == 0 || !rustIdentByte(b[i-1]) }
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c == '/' && i+1 < len(b) && b[i+1] == '/':
			end := i
			for end < len(b) && b[end] != '\n' {
				end++
			}
			blank(i, end)
			i = end
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
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
			if depth > 0 {
				return "", false
			}
			blank(i, k)
			i = k
		case startsToken(i) && rustRawStart(b, i) >= 0:
			// r"..", r#".."#, br".., cr#"..: no escapes; closed by a quote
			// followed by as many hashes as opened it.
			q := rustRawStart(b, i)
			hashes := q - i - 1
			if b[i] != 'r' {
				hashes--
			}
			closing := "\"" + strings.Repeat("#", hashes)
			end := strings.Index(src[q+1:], closing)
			if end < 0 {
				return "", false
			}
			end = q + 1 + end + len(closing)
			blank(i, end)
			i = end
		case c == '"' || (startsToken(i) && (c == 'b' || c == 'c') && i+1 < len(b) && b[i+1] == '"'):
			k := i + 1
			if c != '"' {
				k++
			}
			for k < len(b) && b[k] != '"' {
				if b[k] == '\\' {
					k++
				}
				k++
			}
			if k >= len(b) {
				return "", false
			}
			blank(i, k+1)
			i = k + 1
		case c == '\'' || (startsToken(i) && c == 'b' && i+1 < len(b) && b[i+1] == '\''):
			q := i
			if c == 'b' {
				q++
			}
			if end := rustCharEnd(b, q); end > 0 {
				blank(i, end)
				i = end
				continue
			}
			out[i] = c
			i++
		default:
			out[i] = c
			i++
		}
	}
	return string(out), true
}

// rustRawStart returns the offset of the opening quote of a raw string that
// starts at i (r", r#", br", cr##"), or -1.
func rustRawStart(b []byte, i int) int {
	k := i
	if b[k] == 'b' || b[k] == 'c' {
		k++
	}
	if k >= len(b) || b[k] != 'r' {
		return -1
	}
	k++
	for k < len(b) && b[k] == '#' {
		k++
	}
	if k < len(b) && b[k] == '"' {
		return k
	}
	return -1
}

// rustCharEnd returns the offset just past a char literal whose opening
// quote is at q ('x', '\n', '\”, '\u{1F600}', a multi-byte 'é'), or 0 when
// the quote opens a lifetime or a label instead.
func rustCharEnd(b []byte, q int) int {
	k := q + 1
	if k >= len(b) {
		return 0
	}
	if b[k] == '\\' {
		k++
		for k < len(b) && b[k] != '\'' && b[k] != '\n' {
			k++
		}
		if k < len(b) && b[k] == '\'' {
			return k + 1
		}
		return 0
	}
	// One character, which may be several bytes of UTF-8.
	k++
	for k < len(b) && b[k]&0xC0 == 0x80 {
		k++
	}
	if k < len(b) && b[k] == '\'' {
		return k + 1
	}
	return 0
}

// rustTokens splits blanked Rust code into the tokens the lister reads:
// identifiers (a raw identifier r#name as its name), "::", and single
// punctuation.
func rustTokens(code string) []string {
	var toks []string
	for i := 0; i < len(code); {
		c := code[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == 'r' && strings.HasPrefix(code[i:], "r#") && i+2 < len(code) && rustIdentByte(code[i+2]):
			j := i + 2
			for j < len(code) && rustIdentByte(code[j]) {
				j++
			}
			toks = append(toks, code[i+2:j])
			i = j
		case rustIdentByte(c):
			j := i
			for j < len(code) && rustIdentByte(code[j]) {
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

func rustIdentByte(c byte) bool {
	return c == '_' || c >= 0x80 || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func rustIdent(t string) bool {
	return t != "" && rustIdentByte(t[0]) && !(t[0] >= '0' && t[0] <= '9')
}
