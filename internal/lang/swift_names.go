// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"strings"
)

// TestNamesInSource lists a Swift test file's tests as `swift test list`
// prints their ids (testdata/runners/swift-list.txt), in file order, each
// once:
//
//   - a swift-testing `@Test func name()` at the top of the file as
//     `<Target>.name()`, and one declared in a type as `<Target>.<Type>/name()`
//     (with or without a display name, which the id does not carry);
//   - an XCTest `func testName()` in a class whose superclass is XCTestCase as
//     `<Target>.<Class>/testName`.
//
// <Target> is the test target the file is in (Tests/<Target>/...), which is
// the module the ids begin with when its name is an identifier. Each shape
// was listed by Swift 6.3.2: swift-list.txt (top level, an @Suite with a
// display-named test, an XCTest class) and swift-shapes-list.txt (a type with
// no @Suite, an @Suite with a display name, static, async throws and doubly
// attributed tests).
//
// It lists NOTHING (nil) for a file it cannot name completely, since the
// list is a promise the critic is held to: a file outside Tests/<Target>/; a
// test whose function takes parameters (swift-testing's parameterised tests,
// whose ids carry argument labels this reader does not derive); a test in a
// nested type, an extension, a generic type or a class whose superclass is
// not XCTestCase itself (ids or inheritance not measured or not knowable
// from one file); a private, fileprivate or static test-shaped method in an
// XCTest class; a test named with backticks; any `#if` (a test it compiles
// out would be listed); and a scan that ends inside a comment or string, or
// with unbalanced braces.
//
// It is a scanner, not a parser: swiftCode first blanks every comment
// (block comments nest), string (interpolations included) and extended
// regex literal, so a brace or an "@Test" inside one is not structure. A
// bare-slash regex literal (/.../) is not recognised; a brace inside one can
// only unbalance the scan, which then lists nothing.
func (swiftPlugin) TestNamesInSource(testPath, src string) []string {
	target, ok := swiftTestTarget(testPath)
	if !ok || !swiftModuleName.MatchString(target) {
		return nil
	}
	code, ok := swiftCode(src)
	if !ok {
		return nil
	}
	return swiftTestIDs(target, identTokens(code, ""))
}

// swiftTypeKeywords are the declarations whose braces open a type.
var swiftTypeKeywords = map[string]bool{"struct": true, "class": true, "enum": true, "actor": true, "extension": true, "protocol": true}

// swiftDeclKeywords end whatever a pending modifier or attribute applied to.
var swiftDeclKeywords = map[string]bool{"var": true, "let": true, "init": true, "deinit": true, "subscript": true, "typealias": true, "case": true, "import": true}

// swiftScope is one open brace: a type's body, or anything else.
type swiftScope struct {
	typ      bool
	kind     string // struct, class, enum, actor, extension, protocol
	name     string
	generic  bool
	super    string // a class's first inherited name
	xctest   bool   // a class whose first inherited name is XCTestCase
	topLevel bool   // declared at the top of the file
}

// swiftTestIDs walks the tokens of blanked code; see TestNamesInSource.
func swiftTestIDs(target string, toks []string) []string {
	var scopes []swiftScope
	var out []string
	seen := map[string]bool{}
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	var pending *swiftScope
	marked, hidden := false, false
	reset := func() { marked, hidden = false, false }
	classes := map[string]bool{} // every class this file declares
	var subclasses []string      // superclasses named by a class that is not XCTestCase's direct subclass

	// skipGroup returns the index of the token closing the group that
	// opens at i, or -1 when it does not close.
	skipGroup := func(i int) int {
		depth := 0
		for k := i; k < len(toks); k++ {
			switch toks[k] {
			case "(", "[":
				depth++
			case ")", "]":
				depth--
				if depth == 0 {
					return k
				}
			}
		}
		return -1
	}

	for i := 0; i < len(toks); i++ {
		t := toks[i]
		switch t {
		case "{":
			s := swiftScope{}
			if pending != nil {
				s = *pending
				s.topLevel = len(scopes) == 0
				pending = nil
			}
			scopes = append(scopes, s)
			reset()
			continue
		case "}":
			if len(scopes) == 0 {
				return nil
			}
			scopes = scopes[:len(scopes)-1]
			reset()
			continue
		case ";":
			reset()
			continue
		case "#":
			if i+1 < len(toks) && toks[i+1] == "if" {
				return nil
			}
			continue
		case "@":
			if i+1 >= len(toks) {
				continue
			}
			if toks[i+1] == "Test" {
				marked = true
			}
			i++
			// An attribute's arguments: @Test("name"), @available(...).
			if i+1 < len(toks) && toks[i+1] == "(" {
				end := skipGroup(i + 1)
				if end < 0 {
					return nil
				}
				i = end
			}
			continue
		}
		switch {
		case swiftTypeKeywords[t] && i+1 < len(toks) && isIdent(toks[i+1]) && toks[i+1] != "func" && toks[i+1] != "var" && toks[i+1] != "let":
			s := swiftScope{typ: true, kind: t, name: toks[i+1]}
			i++
			// A dotted extension name (extension A.B) or generic parameters.
			for i+2 < len(toks) && toks[i+1] == "." && isIdent(toks[i+2]) {
				s.name += "." + toks[i+2]
				i += 2
			}
			if i+1 < len(toks) && toks[i+1] == "<" {
				s.generic = true
			}
			// The first inherited name, the last segment of a dotted one
			// (XCTest.XCTestCase).
			for k := i + 1; k < len(toks) && toks[k] != "{" && toks[k] != ";"; k++ {
				if toks[k] == ":" && k+1 < len(toks) && isIdent(toks[k+1]) {
					sup := toks[k+1]
					for k+3 < len(toks) && toks[k+2] == "." && isIdent(toks[k+3]) {
						sup = toks[k+3]
						k += 2
					}
					s.super = sup
					break
				}
			}
			if t == "class" {
				classes[s.name] = true
				s.xctest = s.super == "XCTestCase"
				if s.super != "" && !s.xctest {
					subclasses = append(subclasses, s.super)
				}
			}
			pending = &s
			reset()
		case t == "private" || t == "fileprivate" || t == "static" || t == "class":
			// `class func` and `class var` declare type members, as static
			// does: not what XCTest runs.
			hidden = true
		case swiftDeclKeywords[t]:
			reset()
		case t == "func":
			if i+1 >= len(toks) {
				return nil
			}
			name := toks[i+1]
			in := swiftScope{}
			depth := len(scopes)
			if depth > 0 {
				in = scopes[depth-1]
			}
			// The test-shaped declarations this reader must name or refuse.
			xcShaped := strings.HasPrefix(name, "test") && depth > 0 && in.typ && (in.kind == "class" || in.kind == "extension")
			if !marked && !xcShaped {
				reset()
				continue
			}
			if name == "`" || !isIdent(name) {
				return nil
			}
			i++
			noParams := i+2 < len(toks) && toks[i+1] == "(" && toks[i+2] == ")"
			switch {
			case marked && depth == 0:
				if !noParams {
					return nil
				}
				add(target + "." + name + "()")
			case marked:
				if depth != 1 || !in.typ || !in.topLevel || in.generic || in.kind == "extension" || in.kind == "protocol" || in.xctest {
					return nil
				}
				if !noParams {
					return nil
				}
				add(target + "." + in.name + "/" + name + "()")
			default: // xcShaped
				if !in.xctest || depth != 1 || !in.topLevel || in.generic || hidden || !noParams {
					// An XCTest-shaped method this reader cannot vouch for.
					return nil
				}
				add(target + "." + in.name + "/" + name)
			}
			reset()
		}
	}
	if len(scopes) != 0 || pending != nil {
		return nil
	}
	// A class that inherits from a class declared here inherits its tests,
	// which `swift test list` lists under the subclass too.
	for _, sup := range subclasses {
		if classes[sup] {
			return nil
		}
	}
	return out
}

// swiftCode returns src with every comment, string literal (single-line,
// multi-line and raw, with their interpolations) and extended regex literal
// (#/.../#) blanked to spaces, line breaks kept. Block comments nest, as
// Swift's do. ok is false when the source ends inside one.
func swiftCode(src string) (string, bool) {
	b := []byte(src)
	out := make([]byte, len(b))
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c == '/' && i+1 < len(b) && b[i+1] == '/':
			end := lineCommentEnd(b, i)
			blankSpan(out, b, i, end)
			i = end
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
			end, ok := nestedCommentEnd(b, i)
			if !ok {
				return "", false
			}
			blankSpan(out, b, i, end)
			i = end
		case c == '"' || c == '#':
			end, lit := swiftLiteralEnd(b, i)
			if end < 0 {
				return "", false
			}
			if !lit {
				out[i] = c
				i++
				continue
			}
			blankSpan(out, b, i, end)
			i = end
		default:
			out[i] = c
			i++
		}
	}
	return string(out), true
}

// swiftLiteralEnd reads the literal that may open at i: a string ("..",
// """..""", and #".."# raw forms with any number of #) or an extended regex
// (#/../#). It returns the offset just past it and lit=true; lit=false when
// no literal opens at i (a # that starts #if, #expect, ...), and end=-1
// when one opens and the source ends inside it.
func swiftLiteralEnd(b []byte, i int) (end int, lit bool) {
	hashes := 0
	k := i
	for k < len(b) && b[k] == '#' {
		hashes++
		k++
	}
	if k >= len(b) {
		return 0, false
	}
	closeHashes := strings.Repeat("#", hashes)
	if b[k] == '/' && hashes > 0 {
		// An extended regex literal: #/ ... /#, no escapes that matter here.
		j := strings.Index(string(b[k+1:]), "/"+closeHashes)
		if j < 0 {
			return -1, true
		}
		return k + 1 + j + 1 + hashes, true
	}
	if b[k] != '"' {
		return 0, false
	}
	multi := k+2 < len(b) && b[k+1] == '"' && b[k+2] == '"'
	quote := `"`
	if multi {
		quote = `"""`
	}
	j := k + len(quote)
	escape := `\` + closeHashes
	for j < len(b) {
		switch {
		case strings.HasPrefix(string(b[j:min(len(b), j+len(quote)+hashes)]), quote+closeHashes):
			return j + len(quote) + hashes, true
		case !multi && b[j] == '\n':
			return -1, true
		case strings.HasPrefix(string(b[j:min(len(b), j+len(escape))]), escape):
			j += len(escape)
			if j < len(b) && b[j] == '(' {
				// An interpolation: code up to its closing parenthesis,
				// which may hold strings of its own.
				e, ok := swiftInterpolationEnd(b, j)
				if !ok {
					return -1, true
				}
				j = e
				continue
			}
			j++ // the escaped character
		default:
			j++
		}
	}
	return -1, true
}

// swiftInterpolationEnd is the offset just past the ")" that closes the
// interpolation whose "(" is at open, skipping the strings and comments
// inside it.
func swiftInterpolationEnd(b []byte, open int) (int, bool) {
	depth := 0
	for j := open; j < len(b); {
		c := b[j]
		switch {
		case c == '(':
			depth++
			j++
		case c == ')':
			depth--
			j++
			if depth == 0 {
				return j, true
			}
		case c == '/' && j+1 < len(b) && b[j+1] == '*':
			end, ok := nestedCommentEnd(b, j)
			if !ok {
				return 0, false
			}
			j = end
		case c == '"' || c == '#':
			end, lit := swiftLiteralEnd(b, j)
			if end < 0 {
				return 0, false
			}
			if !lit {
				j++
				continue
			}
			j = end
		default:
			j++
		}
	}
	return 0, false
}
