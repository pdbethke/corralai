// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"strings"
)

// jsTestNames lists the tests a JavaScript or TypeScript test file declares,
// read from its source: every it/test/specify call (and its x/f prefixed and
// .skip/.only forms), named by the titles of the describe/context/suite calls
// around it and its own, joined with "::", outermost first. That is the id
// jest, vitest and node list a test under (joinTitles); mocha's space-joined
// form resolves onto it through ResolveListedTest's full-name rule. A
// .todo test has no body and is not listed; a test declared twice is one
// test.
//
// It is a scanner, not a parser: jsTokens lexes comments, strings, template
// literals and regex literals so nothing inside one is read as structure,
// and the walk reads only calls. TypeScript needs nothing more, since its
// types appear inside the bodies the walk does not interpret.
//
// The list is a promise the critic is held to (see the Python lister), so
// any test it cannot name exactly lists NOTHING rather than a short list:
// a title that is not a plain string literal (a variable, a concatenation, a
// template with a substitution), a generated or conditional test (.each, or
// a call inside a loop, a hook, a function, an arrow that is not a
// describe's callback, any block that is not one, or a braceless if/else or
// loop body), a test function passed around by value (ok ? it : xit) or
// bound under another name (import { test as check }, or node:test's default
// export imported or required as anything but a test function's own name),
// a node subtest (t.test('name', fn), which the runner lists under its parent), an
// unknown modifier, or a scan that ends inside a literal or with unbalanced
// brackets. Other calls preceded by "." are other objects' methods
// (re.test(s)).
func jsTestNames(src string) []string {
	toks, ok := jsTokens(src)
	if !ok || jsBindsNodeTestElsewhere(toks) {
		return nil
	}
	type frame struct {
		open     string
		group    bool   // a '{' opened directly inside a describe call: its callback body
		describe bool   // a '(' of a describe call
		cond     bool   // a '(' after if/for/while/with: a control-flow head
		title    string // the describe's title
	}
	var stack []frame
	allowed := func() bool {
		if len(stack) == 0 {
			return true
		}
		top := stack[len(stack)-1]
		return top.group || top.describe
	}
	path := func(leaf string) string {
		var titles []string
		for _, f := range stack {
			if f.describe {
				titles = append(titles, f.title)
			}
		}
		return strings.Join(append(titles, leaf), "::")
	}
	var out []string
	seen := map[string]bool{}
	// afterHead: the token before this one closed a control-flow head, so a
	// statement here is that if's or loop's body, braces or not.
	afterHead := false
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		wasAfterHead := afterHead
		afterHead = false
		if t.kind == jsPunct {
			switch t.text {
			case "(":
				head := i > 0 && toks[i-1].kind == jsIdent && jsControlHeads[toks[i-1].text]
				stack = append(stack, frame{open: "(", cond: head})
			case "[":
				stack = append(stack, frame{open: t.text})
			case "{":
				top := len(stack) > 0 && stack[len(stack)-1].describe
				stack = append(stack, frame{open: "{", group: top})
			case ")", "]", "}":
				want := map[string]string{")": "(", "]": "[", "}": "{"}[t.text]
				if len(stack) == 0 || stack[len(stack)-1].open != want {
					return nil
				}
				afterHead = stack[len(stack)-1].cond
				stack = stack[:len(stack)-1]
			}
			continue
		}
		if t.kind != jsIdent {
			continue
		}
		isTest, isDescribe := jsTestCallees[t.text], jsDescribeCallees[t.text]
		if !isTest && !isDescribe {
			continue
		}
		next := jsTok{}
		if i+1 < len(toks) {
			next = toks[i+1]
		}
		if i > 0 && toks[i-1].kind == jsPunct && toks[i-1].text == "." {
			// t.test('sub', fn) is a node subtest, listed by the runner under
			// its parent. re.test('abc') is RegExp's, and takes one argument.
			if next.kind == jsPunct && next.text == "(" && i+3 < len(toks) && toks[i+2].kind == jsString &&
				toks[i+3].kind == jsPunct && toks[i+3].text == "," {
				return nil
			}
			continue // a property or another method: require('node:test').it, re.test(s)
		}
		j, todo, modified := i+1, false, false
		for j+1 < len(toks) && toks[j].kind == jsPunct && toks[j].text == "." {
			switch m := toks[j+1].text; {
			case m == "todo":
				todo = true
			case jsTestModifiers[m]:
			default:
				return nil
			}
			modified = true
			j += 2
		}
		if j >= len(toks) || toks[j].kind != jsPunct || toks[j].text != "(" {
			if modified || !jsNamesOnly(toks, i) {
				return nil // passed around (ok ? it : xit): calls of it cannot be seen
			}
			if next.kind == jsIdent && next.text == "as" && (i+2 >= len(toks) || toks[i+2].text != t.text) {
				return nil // renamed (test as check): calls through the new name cannot be seen
			}
			continue // an import or declaration of the name
		}
		if !allowed() || wasAfterHead {
			return nil
		}
		if i > 0 {
			prev := toks[i-1]
			if prev.kind == jsIdent && (prev.text == "else" || prev.text == "do") {
				return nil
			}
			// An arrow's body is a describe's callback only directly inside
			// the describe call; anywhere else it runs when called, if ever.
			arrow := i > 1 && prev.kind == jsPunct && prev.text == ">" && toks[i-2].kind == jsPunct && toks[i-2].text == "="
			if arrow && (len(stack) == 0 || !stack[len(stack)-1].describe) {
				return nil
			}
		}
		if j+2 >= len(toks) || toks[j+1].kind != jsString || toks[j+2].kind != jsPunct || (toks[j+2].text != "," && toks[j+2].text != ")") {
			return nil
		}
		title := toks[j+1].text
		stack = append(stack, frame{open: "(", describe: isDescribe, title: title})
		i = j
		if isTest && !todo {
			if id := path(title); !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	if len(stack) != 0 {
		return nil
	}
	return out
}

// jsNamesOnly reports whether the bare test function name at i is only
// being named: in an import or destructuring list ({ describe, it }), renamed
// (it as i), or declared (const test = require('node:test')).
func jsNamesOnly(toks []jsTok, i int) bool {
	if i > 0 && toks[i-1].kind == jsIdent {
		switch toks[i-1].text {
		case "import", "const", "let", "var", "as":
			return true
		}
	}
	if i+1 >= len(toks) {
		return false
	}
	n := toks[i+1]
	return n.kind == jsPunct && (n.text == "," || n.text == "}") || n.kind == jsIdent && n.text == "as"
}

// jsBindsNodeTestElsewhere reports whether node's test module is bound, whole,
// to a name that is not a test function's own: import nt from 'node:test',
// import nt, { it } from 'node:test', const nt = require('node:test'). Its
// default export IS test, so nt('x', fn) declares a test the walk, which
// knows test functions by name, would not see. A named or destructured
// import binds names the walk checks itself.
func jsBindsNodeTestElsewhere(toks []jsTok) bool {
	nodeTest := func(t jsTok) bool { return t.kind == jsString && (t.text == "node:test" || t.text == "test") }
	unknown := func(t jsTok) bool {
		return t.kind == jsIdent && !jsTestCallees[t.text] && !jsDescribeCallees[t.text]
	}
	for i, t := range toks {
		if t.kind != jsIdent {
			continue
		}
		switch {
		case t.text == "import" && i+1 < len(toks) && unknown(toks[i+1]) && toks[i+1].text != "type":
			// The default binding; find the module it comes from.
			for j := i + 2; j+1 < len(toks); j++ {
				if toks[j].kind == jsIdent && toks[j].text == "from" {
					if nodeTest(toks[j+1]) {
						return true
					}
					break
				}
			}
		case t.text == "require" && i >= 2 && i+3 < len(toks) &&
			toks[i+1].text == "(" && nodeTest(toks[i+2]) && toks[i+3].text == ")" &&
			toks[i-1].kind == jsPunct && toks[i-1].text == "=" && unknown(toks[i-2]):
			return true
		}
	}
	return false
}

var (
	// jsControlHeads take a parenthesized head whose statement may be a
	// braceless body.
	jsControlHeads    = map[string]bool{"if": true, "for": true, "while": true, "with": true}
	jsTestCallees     = map[string]bool{"it": true, "test": true, "specify": true, "xit": true, "fit": true, "xtest": true}
	jsDescribeCallees = map[string]bool{"describe": true, "context": true, "suite": true, "xdescribe": true, "fdescribe": true, "xcontext": true}
	// jsTestModifiers are the chained forms that still declare one test or
	// group under its literal title. .todo is handled apart (no body, not a
	// test); any other modifier (.each above all) makes the list unknown.
	jsTestModifiers = map[string]bool{"skip": true, "only": true, "concurrent": true, "failing": true}
)

const (
	jsIdent  = 'i' // identifier, keyword or number
	jsPunct  = 'p'
	jsString = 's' // a string literal, or a template without substitutions; text is its value
	jsOther  = 'o' // a template with substitutions, or a regex literal
)

type jsTok struct {
	kind byte
	text string
}

// jsTokens lexes JavaScript (and TypeScript) source into the tokens
// jsTestNames walks. ok is false when the source ends inside a comment,
// string, template or regex literal, or a string runs into a line break.
func jsTokens(src string) ([]jsTok, bool) {
	start := 0
	if strings.HasPrefix(src, "#!") {
		if start = strings.IndexByte(src, '\n'); start < 0 {
			return nil, true
		}
	}
	toks, _, ok := jsLex(src, start, false)
	return toks, ok
}

// jsLex lexes from i. Inside a template substitution (nested) it stops at
// the "}" that closes it and returns the offset just past it.
func jsLex(src string, i int, nested bool) ([]jsTok, int, bool) {
	var toks []jsTok
	depth := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case strings.HasPrefix(src[i:], "//"):
			if e := strings.IndexByte(src[i:], '\n'); e >= 0 {
				i += e
			} else {
				i = len(src)
			}
		case strings.HasPrefix(src[i:], "/*"):
			e := strings.Index(src[i+2:], "*/")
			if e < 0 {
				return nil, i, false
			}
			i += 2 + e + 2
		case c == '/' && jsRegexAllowed(toks):
			end, ok := jsRegexEnd(src, i)
			if !ok {
				return nil, i, false
			}
			toks = append(toks, jsTok{jsOther, ""})
			i = end
		case c == '\'' || c == '"':
			val, end, ok := jsString1(src, i)
			if !ok {
				return nil, i, false
			}
			toks = append(toks, jsTok{jsString, val})
			i = end
		case c == '`':
			val, subst, end, ok := jsTemplate(src, i)
			if !ok {
				return nil, i, false
			}
			if subst {
				toks = append(toks, jsTok{jsOther, ""})
			} else {
				toks = append(toks, jsTok{jsString, val})
			}
			i = end
		case jsIdentByte(c):
			j := i
			for j < len(src) && jsIdentByte(src[j]) {
				j++
			}
			toks = append(toks, jsTok{jsIdent, src[i:j]})
			i = j
		default:
			if nested {
				switch c {
				case '{':
					depth++
				case '}':
					if depth == 0 {
						return toks, i + 1, true
					}
					depth--
				}
			}
			toks = append(toks, jsTok{jsPunct, string(c)})
			i++
		}
	}
	return toks, i, !nested
}

// jsRegexAllowed reports whether a "/" here starts a regex literal rather
// than dividing: after an operator, an opening bracket, a "}" (a block's end
// is a statement start far more often than an object literal is divided), a
// keyword that takes an expression, or nothing. After a name, a number, a
// literal, ")" or "]" it divides.
func jsRegexAllowed(toks []jsTok) bool {
	if len(toks) == 0 {
		return true
	}
	t := toks[len(toks)-1]
	switch t.kind {
	case jsString, jsOther:
		return false
	case jsIdent:
		return jsExprKeywords[t.text]
	}
	return t.text != ")" && t.text != "]"
}

var jsExprKeywords = map[string]bool{"return": true, "typeof": true, "instanceof": true, "in": true, "of": true, "new": true,
	"delete": true, "void": true, "throw": true, "case": true, "do": true, "else": true, "yield": true, "await": true}

// jsRegexEnd returns the offset past the regex literal at i and its flags.
// A "/" inside a character class does not end it; a line break is an error.
func jsRegexEnd(src string, i int) (int, bool) {
	inClass := false
	for j := i + 1; j < len(src); j++ {
		switch src[j] {
		case '\\':
			j++
		case '\n':
			return 0, false
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '/':
			if !inClass {
				j++
				for j < len(src) && jsIdentByte(src[j]) {
					j++
				}
				return j, true
			}
		}
	}
	return 0, false
}

// jsString1 reads the quoted string at i and returns its value. An escape
// other than a quote, a backslash, n or t gives up: the title it spells is
// not one this scanner can promise.
func jsString1(src string, i int) (string, int, bool) {
	q := src[i]
	var b strings.Builder
	for j := i + 1; j < len(src); j++ {
		c := src[j]
		switch {
		case c == q:
			return b.String(), j + 1, true
		case c == '\n':
			return "", 0, false
		case c == '\\':
			r, ok := jsUnescape(src, j)
			if !ok {
				return "", 0, false
			}
			b.WriteString(r)
			j++
		default:
			b.WriteByte(c)
		}
	}
	return "", 0, false
}

// jsTemplate reads the template literal at i. subst reports a ${...}; its
// expression is lexed (so a brace or backtick inside it is not the
// template's) and the value is then meaningless.
func jsTemplate(src string, i int) (val string, subst bool, end int, ok bool) {
	var b strings.Builder
	for j := i + 1; j < len(src); j++ {
		c := src[j]
		switch {
		case c == '`':
			return b.String(), subst, j + 1, true
		case c == '\\':
			r, ok := jsUnescape(src, j)
			if !ok {
				subst = true // an escape this scanner cannot read: not a promised title
				j++
				continue
			}
			b.WriteString(r)
			j++
		case c == '$' && j+1 < len(src) && src[j+1] == '{':
			_, after, ok := jsLex(src, j+2, true)
			if !ok {
				return "", false, 0, false
			}
			subst = true
			j = after - 1
		default:
			b.WriteByte(c)
		}
	}
	return "", false, 0, false
}

func jsUnescape(src string, j int) (string, bool) {
	if j+1 >= len(src) {
		return "", false
	}
	switch c := src[j+1]; c {
	case '\'', '"', '`', '\\', '$':
		return string(c), true
	case 'n':
		return "\n", true
	case 't':
		return "\t", true
	}
	return "", false
}

func jsIdentByte(c byte) bool {
	return c == '_' || c == '$' || c >= 0x80 || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
