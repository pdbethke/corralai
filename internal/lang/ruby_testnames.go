// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"path/filepath"
	"regexp"
	"strings"
)

// rubyTestNames lists the tests a Ruby test file declares, read from its
// source, in the form its runner names them. Which runner is read from the
// path convention the Ruby plugin pairs on:
//
//   - *_spec.rb or spec_*.rb is RSpec: every example (it, specify, example,
//     scenario, their x/f forms, and pending or skip with a description) by
//     its full description in idFromFull's form — the groups' descriptions
//     joined as rspec-core joins them (rspecGroupJoin), then "::" and the
//     example's own — the id rspec lists it under.
//   - *_test.rb or test_*.rb is minitest: in each class whose superclass
//     ends in Test or TestCase, the public test_* methods and ActiveSupport
//     `test "name"` blocks, as Class#test_name (modules and outer classes
//     joined by "::"), minitest's own label for a test.
//
// It is a scanner, not a parser: rbTokens lexes comments, strings, symbols,
// heredocs, percent literals and regexes, and the walk tracks only the
// blocks that end with "end" or "}". The list is a promise the critic is
// held to (see the Python lister), so any test it cannot name exactly lists
// NOTHING: a description that is not a plain literal, an example with no
// description (its name comes from the matcher), shared examples, a test
// declared inside a loop, hook, helper or any block that is not a group, a
// minitest spec, a test class whose base is not a framework test class
// (its tests would run under this class's name), a test method made or
// renamed at run time (define_method, alias, alias_method) or hidden by
// name (private :test_x), or a scan that loses its place. An include is
// allowed: refusing it would refuse most Rails tests, and a mixin that
// defines test_ methods is the residual this list can miss.
func rubyTestNames(testPath, src string) []string {
	base := filepath.Base(testPath)
	spec := strings.HasSuffix(base, "_spec.rb") || strings.HasPrefix(base, "spec_") && strings.HasSuffix(base, ".rb")
	unit := strings.HasSuffix(base, "_test.rb") || strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".rb")
	if !spec && !unit {
		return nil
	}
	toks, ok := rbTokens(src)
	if !ok {
		return nil
	}
	w := rbWalk{toks: toks, spec: spec, seen: map[string]bool{}, classes: map[string]bool{}, assigned: rbAssignedConstants(toks)}
	if !w.run() || len(w.out) == 0 {
		return nil
	}
	return w.out
}

// rbFrame is one open block or bracket.
type rbFrame struct {
	open    string // "kw" (ends with end), "do", "{", "(", "["
	role    byte   // rbGroup, rbExample, rbTestClass, rbModule, or 0
	full    string // a group's full description; a class or module's full name
	private bool   // a test class's default visibility after a bare private
	// constLast: a group whose last description argument is a constant
	// (a Module, at run time), which decides how a child group joins it.
	constLast bool
}

const (
	rbGroup     = 'g'
	rbExample   = 'e'
	rbTestClass = 't'
	rbModule    = 'm' // a module or a class that is not a test class
)

type rbWalk struct {
	toks []rbTok
	spec bool
	// assigned holds the constants this file assigns (Alias = MyApp::Calc):
	// described, they print the name of what they hold, not their own.
	assigned map[string]bool
	stack    []rbFrame
	out      []string
	seen     map[string]bool
	classes  map[string]bool // test classes defined in this file
	// pending is the role the next do/{ opened at depth pendDepth takes: the
	// block of the call just read. A statement end at that depth drops it.
	pending      *rbFrame
	pendDepth    int
	loopOpenLine bool // a while/until/for opened on this line; its "do" opens nothing
}

var (
	// rbMinitestBases are the framework test classes, which declare no tests
	// of their own. ApplicationSystemTestCase is the app's own (Rails
	// generates it) and is taken on trust: it is a base for system tests and
	// does not hold them.
	rbMinitestBases = map[string]bool{"Minitest::Test": true, "MiniTest::Test": true, "Minitest::Unit::TestCase": true,
		"ActiveSupport::TestCase": true, "ActionDispatch::IntegrationTest": true, "ActionDispatch::SystemTestCase": true,
		"ActionController::TestCase": true, "ActionMailer::TestCase": true, "ActionView::TestCase": true, "ActiveJob::TestCase": true,
		"ActionCable::TestCase": true, "ActionCable::Channel::TestCase": true, "ActionCable::Connection::TestCase": true,
		"ActionMailbox::TestCase": true, "Rails::Generators::TestCase": true, "ApplicationSystemTestCase": true}
	rbGroupCallees = map[string]bool{"describe": true, "context": true, "feature": true, "example_group": true,
		"xdescribe": true, "xcontext": true, "xfeature": true, "fdescribe": true, "fcontext": true, "ffeature": true}
	rbExampleCallees = map[string]bool{"it": true, "specify": true, "example": true, "scenario": true,
		"xit": true, "xspecify": true, "xexample": true, "xscenario": true, "fit": true, "fspecify": true, "fexample": true, "fscenario": true,
		"focus": true, "pending": true, "skip": true}
	// rbGeneratedCallees declare examples whose names are not in this file
	// as literals (shared examples, rspec-its).
	rbGeneratedCallees = map[string]bool{"it_behaves_like": true, "it_should_behave_like": true, "include_examples": true,
		"include_context": true, "shared_examples": true, "shared_examples_for": true, "shared_context": true, "its": true}
	// rbStatusOnly may also be called inside a hook or an example to mark it
	// skipped; only with a description, at group level, do they declare one.
	rbStatusOnly = map[string]bool{"pending": true, "skip": true, "focus": true}
	rbTestBlock  = regexp.MustCompile(`^[A-Za-z0-9_ ]+$`)
	rbSpace      = regexp.MustCompile(`\s+`)
)

func (w *rbWalk) add(id string) {
	if !w.seen[id] {
		w.seen[id] = true
		w.out = append(w.out, id)
	}
}

func (w *rbWalk) top() *rbFrame {
	if len(w.stack) == 0 {
		return nil
	}
	return &w.stack[len(w.stack)-1]
}

// inExample reports whether the walk is anywhere inside an example's body,
// where nothing is a declaration.
func (w *rbWalk) inExample() bool {
	for _, f := range w.stack {
		if f.role == rbExample {
			return true
		}
	}
	return false
}

// group is the innermost enclosing group, or nil.
func (w *rbWalk) group() *rbFrame {
	for i := len(w.stack) - 1; i >= 0; i-- {
		if w.stack[i].role == rbGroup {
			return &w.stack[i]
		}
	}
	return nil
}

// inModule reports whether the walk is inside a module or class body, where
// a constant resolves through the nesting and describes as its full name.
func (w *rbWalk) inModule() bool {
	for _, f := range w.stack {
		if f.role == rbModule || f.role == rbTestClass {
			return true
		}
	}
	return false
}

// scopeName is the full name of the innermost enclosing class or module.
func (w *rbWalk) scopeName() string {
	for i := len(w.stack) - 1; i >= 0; i-- {
		if r := w.stack[i].role; r == rbTestClass || r == rbModule {
			return w.stack[i].full
		}
	}
	return ""
}

// rspecGroupJoin joins a description onto what precedes it as rspec-core
// 3.13 does (Metadata#description_separator): with no space when the part
// before is a Module (a constant here) and the description starts "#", "."
// or "::" (`describe Calc do describe '#add'` is "Calc#add"), and with a
// space otherwise (`describe 'Str' do describe '#add'` is "Str #add"). It
// joins a group's two arguments and a group onto its parent group. An
// example always joins its group with a space (build_description_from is
// handed the parent's full description, a String): `it '#mul'` under Calc is
// "Calc #mul".
func rspecGroupJoin(before string, beforeIsConst bool, desc string) string {
	if before == "" {
		return desc
	}
	if beforeIsConst && (strings.HasPrefix(desc, "#") || strings.HasPrefix(desc, ".") || strings.HasPrefix(desc, "::")) {
		return before + desc
	}
	return before + " " + desc
}

// rbAssignedConstants is every constant a statement in toks assigns.
func rbAssignedConstants(toks []rbTok) map[string]bool {
	out := map[string]bool{}
	for i := 0; i+2 < len(toks); i++ {
		t := toks[i]
		if t.kind == rbIdent && t.text[0] >= 'A' && t.text[0] <= 'Z' && (i == 0 || toks[i-1].kind == rbNL || toks[i-1].kind == rbPunct && toks[i-1].text == ";") &&
			toks[i+1].kind == rbPunct && toks[i+1].text == "=" && !(toks[i+2].kind == rbPunct && (toks[i+2].text == "=" || toks[i+2].text == "~")) {
			out[t.text] = true
		}
	}
	return out
}

func (w *rbWalk) push(f rbFrame) {
	if (f.open == "do" || f.open == "{") && w.pending != nil && len(w.stack) == w.pendDepth {
		f.role, f.full, f.constLast = w.pending.role, w.pending.full, w.pending.constLast
		w.pending = nil
	}
	w.stack = append(w.stack, f)
}

func (w *rbWalk) pop(open string) bool {
	t := w.top()
	if t == nil {
		return false
	}
	if open == "end" {
		if t.open != "kw" && t.open != "do" {
			return false
		}
	} else if t.open != open {
		return false
	}
	w.stack = w.stack[:len(w.stack)-1]
	return true
}

func (w *rbWalk) run() bool {
	toks := w.toks
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		prev := rbTok{kind: rbNL}
		if i > 0 {
			prev = toks[i-1]
		}
		switch t.kind {
		case rbNL:
			w.loopOpenLine = false
			if w.pending != nil && len(w.stack) == w.pendDepth {
				w.pending = nil
			}
			continue
		case rbPunct:
			switch t.text {
			case ";":
				w.loopOpenLine = false
				if w.pending != nil && len(w.stack) == w.pendDepth {
					w.pending = nil
				}
			case "{":
				f := rbFrame{open: "{"}
				if prev.kind == rbLabel || prev.kind == rbPunct && (prev.text == "," || prev.text == "=>" || prev.text == "(") {
					w.stack = append(w.stack, f) // a hash, never a block
				} else {
					w.push(f)
				}
			case "(", "[":
				w.stack = append(w.stack, rbFrame{open: t.text})
			case "}":
				if !w.pop("{") {
					return false
				}
			case ")":
				if !w.pop("(") {
					return false
				}
			case "]":
				if !w.pop("[") {
					return false
				}
			}
			continue
		case rbIdent:
		default:
			continue
		}
		if prev.kind == rbPunct && (prev.text == "." || prev.text == "&.") {
			// A method name: obj.end, obj.class, but RSpec.describe declares.
			if !(t.text == "describe" && i >= 2 && toks[i-2].kind == rbIdent && toks[i-2].text == "RSpec") {
				continue
			}
		}
		switch t.text {
		case "end":
			if !w.pop("end") {
				return false
			}
			continue
		case "do":
			if w.loopOpenLine {
				w.loopOpenLine = false
				continue
			}
			w.push(rbFrame{open: "do"})
			continue
		case "begin", "case":
			w.stack = append(w.stack, rbFrame{open: "kw"})
			continue
		case "if", "unless", "while", "until":
			if rbStatementStart(prev) {
				w.stack = append(w.stack, rbFrame{open: "kw"})
				w.loopOpenLine = t.text == "while" || t.text == "until"
			}
			continue
		case "for":
			w.stack = append(w.stack, rbFrame{open: "kw"})
			w.loopOpenLine = true
			continue
		case "module":
			name, _ := rbConstPath(toks, i+1)
			if name == "" {
				return false
			}
			w.stack = append(w.stack, rbFrame{open: "kw", role: rbModule, full: rbJoinScope(w.scopeName(), name)})
			continue
		case "class":
			if !w.class(i) {
				return false
			}
			continue
		case "def":
			if !w.def(i) {
				return false
			}
			continue
		}
		if !w.declaration(i) {
			return false
		}
	}
	return len(w.stack) == 0
}

// rbStatementStart reports whether a keyword after prev begins a statement
// (and so opens a block ending in "end") rather than modifying one.
func rbStatementStart(prev rbTok) bool {
	switch prev.kind {
	case rbNL:
		return true
	case rbIdent:
		switch prev.text {
		case "then", "do", "else", "begin", "and", "or", "not", "when", "in", "elsif", "ensure", "rescue":
			return true
		}
		return false
	case rbPunct:
		return prev.text != ")" && prev.text != "]" && prev.text != "}"
	}
	return false
}

func rbJoinScope(outer, name string) string {
	if outer == "" || strings.HasPrefix(name, "::") {
		return strings.TrimPrefix(name, "::")
	}
	return outer + "::" + name
}

// class opens a class body; in a minitest file one whose superclass ends in
// Test or TestCase is a test class.
func (w *rbWalk) class(i int) bool {
	toks := w.toks
	if i+1 < len(toks) && toks[i+1].kind == rbPunct && toks[i+1].text == "<" {
		w.stack = append(w.stack, rbFrame{open: "kw"}) // class << self
		return true
	}
	name, k := rbConstPath(toks, i+1)
	if name == "" {
		return false
	}
	full := rbJoinScope(w.scopeName(), name)
	f := rbFrame{open: "kw", role: rbModule, full: full}
	if k < len(toks) && toks[k].kind == rbPunct && toks[k].text == "<" {
		super, _ := rbConstPath(toks, k+1)
		if !w.spec && (strings.HasSuffix(super, "Test") || strings.HasSuffix(super, "TestCase")) {
			if !rbMinitestBases[strings.TrimPrefix(super, "::")] {
				// A base from this file or another may define tests of its
				// own, which minitest runs under this class's name.
				return false
			}
			f.role = rbTestClass
			w.classes[full] = true
		}
	} else if w.classes[full] {
		f.role = rbTestClass // a test class reopened
	}
	w.stack = append(w.stack, f)
	return true
}

// def opens a method body (an endless def opens nothing) and, directly in a
// test class, lists a public test_ method.
func (w *rbWalk) def(i int) bool {
	toks := w.toks
	k := i + 1
	if k >= len(toks) {
		return false
	}
	singleton := false
	if k+1 < len(toks) && toks[k].kind == rbIdent && toks[k+1].kind == rbPunct && toks[k+1].text == "." {
		singleton, k = true, k+2
	}
	if k >= len(toks) {
		return false
	}
	nameTok := toks[k]
	k++
	endless := false
	if nameTok.kind == rbIdent && k < len(toks) {
		if toks[k].kind == rbPunct && toks[k].text == "=" && toks[k].sp {
			endless = true
		} else if toks[k].kind == rbPunct && toks[k].text == "(" && !toks[k].sp {
			depth := 0
			for ; k < len(toks); k++ {
				if toks[k].kind == rbPunct && toks[k].text == "(" {
					depth++
				} else if toks[k].kind == rbPunct && toks[k].text == ")" {
					if depth--; depth == 0 {
						k++
						break
					}
				}
			}
			endless = k < len(toks) && toks[k].kind == rbPunct && toks[k].text == "="
		}
	}
	if !w.spec && nameTok.kind == rbIdent && strings.HasPrefix(nameTok.text, "test_") && !singleton {
		if t := w.top(); t != nil && t.role == rbTestClass {
			hidden := t.private || (i > 0 && toks[i-1].kind == rbIdent && (toks[i-1].text == "private" || toks[i-1].text == "protected"))
			if !hidden {
				w.add(t.full + "#" + nameTok.text)
			}
		} else if w.inTestClass() {
			return false // a test method defined inside a block: generated
		}
	}
	if !endless {
		w.stack = append(w.stack, rbFrame{open: "kw"})
	}
	return true
}

func (w *rbWalk) inTestClass() bool {
	for _, f := range w.stack {
		if f.role == rbTestClass {
			return true
		}
	}
	return false
}

// declaration reads an identifier that may declare a test or group.
func (w *rbWalk) declaration(i int) bool {
	toks := w.toks
	name := toks[i].text
	next := rbTok{kind: rbNL}
	if i+1 < len(toks) {
		next = toks[i+1]
	}
	if !w.spec {
		if rbGroupCallees[name] || name == "it" {
			if next.kind == rbStr || next.kind == rbIStr || next.kind == rbIdent || next.kind == rbPunct && next.text == "(" {
				return false // a minitest spec: test names are generated
			}
			return true
		}
		if w.inTestClass() {
			switch {
			case name == "define_method" || name == "alias" || name == "alias_method":
				return false // a test method made at run time, or under a second name
			case (name == "private" || name == "protected") && next.kind == rbSym && strings.HasPrefix(next.text, "test_"):
				return false // hides a test by name, after the fact
			}
		}
		if t := w.top(); t != nil && t.role == rbTestClass {
			switch {
			case (name == "private" || name == "protected") && (next.kind == rbNL || next.kind == rbPunct && next.text == ";"):
				t.private = true
			case name == "public" && (next.kind == rbNL || next.kind == rbPunct && next.text == ";"):
				t.private = false
			case name == "test" && (next.kind == rbStr || next.kind == rbIStr):
				if next.kind != rbStr || !rbTestBlock.MatchString(next.text) {
					return false
				}
				w.add(t.full + "#test_" + rbSpace.ReplaceAllString(next.text, "_"))
			}
		}
		return true
	}
	isGroup, isExample, generated := rbGroupCallees[name], rbExampleCallees[name], rbGeneratedCallees[name]
	if !isGroup && !isExample && !generated {
		return true
	}
	if w.inExample() {
		return true
	}
	atGroup := true
	if t := w.top(); t != nil && t.role != rbGroup && t.role != rbModule {
		atGroup = false
	}
	if !atGroup {
		if rbStatusOnly[name] {
			return true // skip/pending inside a hook marks, it does not declare
		}
		return false // a declaration inside a loop, hook or helper: generated
	}
	if generated {
		return false
	}
	args := rbArgs(toks, i+1)
	if isExample {
		if len(args) == 0 || len(args[0]) != 1 || args[0][0].kind != rbStr {
			return false
		}
		leaf := args[0][0].text
		g := w.group()
		if g == nil {
			return false // rspec has no it outside a group: the file fails to load
		}
		w.add(listedName(idFromFull(g.full+" "+leaf, leaf)))
		w.pending, w.pendDepth = &rbFrame{role: rbExample}, len(w.stack)
		return true
	}
	if len(args) == 0 {
		return false
	}
	desc, isConst, ok := rbDescription(args[0])
	if !ok {
		return false
	}
	if isConst && (w.inModule() || w.assigned[strings.SplitN(desc, "::", 2)[0]]) {
		return false // the constant describes as a name this file does not spell
	}
	constLast := isConst
	if len(args) > 1 && len(args[1]) == 1 && args[1][0].kind == rbStr {
		desc, constLast = rspecGroupJoin(desc, isConst, args[1][0].text), false
	}
	full := desc
	if g := w.group(); g != nil {
		full = rspecGroupJoin(g.full, g.constLast, desc)
	}
	w.pending, w.pendDepth = &rbFrame{role: rbGroup, full: full, constLast: constLast}, len(w.stack)
	return true
}

// rbDescription is a group's description from its first argument: a plain
// string, a symbol, or a constant path (isConst), which rspec describes by
// the Module's name.
func rbDescription(arg []rbTok) (desc string, isConst, ok bool) {
	if len(arg) == 1 && (arg[0].kind == rbStr || arg[0].kind == rbSym) {
		return arg[0].text, false, true
	}
	var b strings.Builder
	for k, t := range arg {
		if k%2 == 0 && t.kind == rbIdent && t.text != "" && t.text[0] >= 'A' && t.text[0] <= 'Z' {
			b.WriteString(t.text)
		} else if k%2 == 1 && t.kind == rbPunct && t.text == "::" {
			b.WriteString("::")
		} else {
			return "", false, false
		}
	}
	if len(arg)%2 == 0 {
		return "", false, false
	}
	return b.String(), true, true
}

// rbArgs splits a call's arguments, starting at k, at its top-level commas:
// inside parentheses written against the name, or up to the block or the
// end of the statement.
func rbArgs(toks []rbTok, k int) [][]rbTok {
	paren := k < len(toks) && toks[k].kind == rbPunct && toks[k].text == "(" && !toks[k].sp
	if paren {
		k++
	}
	var args [][]rbTok
	var cur []rbTok
	depth := 0
	for ; k < len(toks); k++ {
		t := toks[k]
		if depth == 0 {
			if paren && t.kind == rbPunct && t.text == ")" {
				break
			}
			if !paren && (t.kind == rbNL || t.kind == rbPunct && t.text == ";" || t.kind == rbIdent && t.text == "do") {
				break
			}
			if !paren && t.kind == rbPunct && t.text == "{" && len(cur) > 0 {
				break
			}
			if t.kind == rbPunct && t.text == "," {
				args = append(args, cur)
				cur = nil
				continue
			}
		}
		if t.kind == rbPunct {
			switch t.text {
			case "(", "[", "{":
				depth++
			case ")", "]", "}":
				depth--
			}
		}
		cur = append(cur, t)
	}
	if len(cur) > 0 {
		args = append(args, cur)
	}
	return args
}

// rbConstPath reads A::B::C from k and returns it and the offset after it.
func rbConstPath(toks []rbTok, k int) (string, int) {
	var b strings.Builder
	if k < len(toks) && toks[k].kind == rbPunct && toks[k].text == "::" {
		b.WriteString("::")
		k++
	}
	for k < len(toks) && toks[k].kind == rbIdent {
		b.WriteString(toks[k].text)
		k++
		if k+1 < len(toks) && toks[k].kind == rbPunct && toks[k].text == "::" && toks[k+1].kind == rbIdent {
			b.WriteString("::")
			k++
			continue
		}
		break
	}
	return b.String(), k
}

const (
	rbIdent = 'i' // identifier, keyword, constant or number
	rbLabel = 'l' // a hash label (key:)
	rbPunct = 'p'
	rbStr   = 's' // a string with no interpolation; text is its value
	rbIStr  = 'x' // an interpolated string, or one with an escape not read
	rbSym   = 'y' // :name; text is the name
	rbNL    = 'n'
	rbOther = 'o' // regex, percent literal, heredoc
)

type rbTok struct {
	kind byte
	text string
	sp   bool // whitespace before it
}

// rbTokens lexes Ruby source into the tokens rubyTestNames walks. ok is
// false when the source ends inside a comment block, string, heredoc,
// regex or percent literal.
func rbTokens(src string) ([]rbTok, bool) {
	toks, _, ok := rbLex(src, 0, false)
	return toks, ok
}

func rbLex(src string, i int, nested bool) ([]rbTok, int, bool) {
	var toks []rbTok
	var heredocs []string // terminators (with their squiggly/dash flag) waiting for the next line
	depth := 0
	sp := false
	emit := func(kind byte, text string) {
		toks = append(toks, rbTok{kind: kind, text: text, sp: sp})
		sp = false
	}
	lineStart := true
	for i < len(src) {
		c := src[i]
		if lineStart && !nested {
			if strings.HasPrefix(src[i:], "=begin") {
				e := strings.Index(src[i:], "\n=end")
				if e < 0 {
					return nil, i, false
				}
				i += e + len("\n=end")
				for i < len(src) && src[i] != '\n' {
					i++
				}
				continue
			}
			if strings.HasPrefix(src[i:], "__END__") && (i+7 == len(src) || src[i+7] == '\n' || src[i+7] == '\r') {
				return toks, len(src), true
			}
		}
		lineStart = false
		switch {
		case c == '\n':
			emit(rbNL, "")
			i++
			lineStart = true
			for _, term := range heredocs {
				end, ok := rbHeredocBody(src, i, term)
				if !ok {
					return nil, i, false
				}
				i = end
			}
			heredocs = nil
		case c == ' ' || c == '\t' || c == '\r':
			sp = true
			i++
		case c == '\\' && i+1 < len(src) && src[i+1] == '\n':
			sp = true
			i += 2
		case c == '#':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '\'':
			val, end, ok := rbSingle(src, i)
			if !ok {
				return nil, i, false
			}
			emit(rbStr, val)
			i = end
		case c == '"' || c == '`':
			val, interp, end, ok := rbDouble(src, i, c)
			if !ok {
				return nil, i, false
			}
			if interp {
				emit(rbIStr, "")
			} else {
				emit(rbStr, val)
			}
			i = end
		case c == ':' && i+1 < len(src) && src[i+1] == ':':
			emit(rbPunct, "::")
			i += 2
		case c == ':' && i+1 < len(src) && (src[i+1] == '"' || src[i+1] == '\''):
			var val string
			var interp bool
			var end int
			var ok bool
			if src[i+1] == '\'' {
				val, end, ok = rbSingle(src, i+1)
			} else {
				val, interp, end, ok = rbDouble(src, i+1, '"')
			}
			if !ok {
				return nil, i, false
			}
			if interp {
				emit(rbIStr, "")
			} else {
				emit(rbSym, val)
			}
			i = end
		case c == ':' && i+1 < len(src) && rbIdentStart(src[i+1]) && (len(toks) == 0 || sp || toks[len(toks)-1].kind == rbPunct):
			j := i + 1
			for j < len(src) && rbIdentByte(src[j]) {
				j++
			}
			if j < len(src) && (src[j] == '?' || src[j] == '!' || src[j] == '=') && !(j+1 < len(src) && src[j+1] == '=') {
				j++
			}
			emit(rbSym, src[i+1:j])
			i = j
		case c == '<' && strings.HasPrefix(src[i:], "<<") && rbHeredocStart(src, i+2) > 0:
			term, end := rbHeredocTerm(src, i+2)
			heredocs = append(heredocs, term)
			emit(rbOther, "")
			i = end
		case c == '%' && rbValueExpected(toks, sp) && i+1 < len(src):
			end, ok := rbPercent(src, i)
			if !ok {
				emit(rbPunct, "%")
				i++
				continue
			}
			emit(rbOther, "")
			i = end
		case c == '/' && rbValueExpected(toks, sp):
			end, ok := rbRegex(src, i)
			if !ok {
				return nil, i, false
			}
			emit(rbOther, "")
			i = end
		case rbIdentStart(c) || (c >= '0' && c <= '9'):
			j := i
			for j < len(src) && rbIdentByte(src[j]) {
				j++
			}
			if j < len(src) && (src[j] == '?' || src[j] == '!') && !(j+1 < len(src) && src[j+1] == '=') {
				j++
			}
			if j < len(src) && src[j] == ':' && !(j+1 < len(src) && src[j+1] == ':') && (j+1 == len(src) || src[j+1] == ' ' || src[j+1] == '\n') {
				emit(rbLabel, src[i:j])
				i = j + 1
				continue
			}
			emit(rbIdent, src[i:j])
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
			if strings.HasPrefix(src[i:], "&.") || strings.HasPrefix(src[i:], "=>") {
				emit(rbPunct, src[i:i+2])
				i += 2
				continue
			}
			emit(rbPunct, string(c))
			i++
		}
	}
	if len(heredocs) > 0 {
		return nil, i, false
	}
	return toks, i, !nested
}

// rbValueExpected reports whether a "/" or "%" here starts a literal rather
// than an operator: after an operator, an opening bracket, a comma, a
// keyword, a line start, or a method name followed by a space (`it %w[a]`).
func rbValueExpected(toks []rbTok, sp bool) bool {
	if len(toks) == 0 {
		return true
	}
	t := toks[len(toks)-1]
	switch t.kind {
	case rbNL, rbLabel:
		return true
	case rbPunct:
		return t.text != ")" && t.text != "]" && t.text != "}"
	case rbIdent:
		switch t.text {
		case "if", "unless", "while", "until", "and", "or", "not", "return", "when", "then", "do", "else", "elsif", "in", "case":
			return true
		}
		return false
	}
	return false
}

func rbIdentStart(c byte) bool {
	return c == '_' || c == '@' || c == '$' || c >= 0x80 || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func rbIdentByte(c byte) bool { return rbIdentStart(c) || (c >= '0' && c <= '9') }

// rbSingle reads a single-quoted string: only \' and \\ are escapes.
func rbSingle(src string, i int) (string, int, bool) {
	var b strings.Builder
	for j := i + 1; j < len(src); j++ {
		switch c := src[j]; {
		case c == '\'':
			return b.String(), j + 1, true
		case c == '\\' && j+1 < len(src) && (src[j+1] == '\'' || src[j+1] == '\\'):
			b.WriteByte(src[j+1])
			j++
		default:
			b.WriteByte(c)
		}
	}
	return "", 0, false
}

// rbDouble reads a double-quoted (or backtick) string. interp reports an
// interpolation, or an escape this scanner does not read; its expression is
// lexed so a quote or brace inside it is not the string's.
func rbDouble(src string, i int, q byte) (val string, interp bool, end int, ok bool) {
	var b strings.Builder
	for j := i + 1; j < len(src); j++ {
		c := src[j]
		switch {
		case c == q:
			return b.String(), interp, j + 1, true
		case c == '\\':
			if j+1 >= len(src) {
				return "", false, 0, false
			}
			switch e := src[j+1]; e {
			case '"', '\\', '`', '#':
				b.WriteByte(e)
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				interp = true
			}
			j++
		case c == '#' && j+1 < len(src) && src[j+1] == '{':
			_, after, ok := rbLex(src, j+2, true)
			if !ok {
				return "", false, 0, false
			}
			interp = true
			j = after - 1
		case c == '#' && j+1 < len(src) && (src[j+1] == '@' || src[j+1] == '$'):
			interp = true
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return "", false, 0, false
}

// rbHeredocStart returns the length of a heredoc opener's identifier part at
// k (after "<<"), or 0 when "<<" is the operator.
func rbHeredocStart(src string, k int) int {
	j := k
	if j < len(src) && (src[j] == '~' || src[j] == '-') {
		j++
	}
	if j < len(src) && (src[j] == '\'' || src[j] == '"' || src[j] == '`') {
		j++
	}
	if j >= len(src) || !(src[j] == '_' || (src[j] >= 'A' && src[j] <= 'Z') || (src[j] >= 'a' && src[j] <= 'z')) {
		return 0
	}
	return j - k + 1
}

// rbHeredocTerm reads the opener at k and returns its terminator, prefixed
// "~" when the terminator may be indented, and the offset after the opener.
func rbHeredocTerm(src string, k int) (string, int) {
	indent := false
	if src[k] == '~' || src[k] == '-' {
		indent = true
		k++
	}
	quote := byte(0)
	if src[k] == '\'' || src[k] == '"' || src[k] == '`' {
		quote = src[k]
		k++
	}
	s := k
	for k < len(src) && rbIdentByte(src[k]) {
		k++
	}
	id := src[s:k]
	if quote != 0 && k < len(src) && src[k] == quote {
		k++
	}
	if indent {
		return "~" + id, k
	}
	return id, k
}

// rbHeredocBody skips a heredoc body starting at the line at i and returns
// the offset after its terminator line.
func rbHeredocBody(src string, i int, term string) (int, bool) {
	indent := strings.HasPrefix(term, "~")
	id := strings.TrimPrefix(term, "~")
	for i < len(src) {
		e := strings.IndexByte(src[i:], '\n')
		line := src[i:]
		next := len(src)
		if e >= 0 {
			line, next = src[i:i+e], i+e+1
		}
		check := strings.TrimRight(line, "\r")
		if indent {
			check = strings.TrimLeft(check, " \t")
		}
		if check == id {
			return next, true
		}
		i = next
	}
	return 0, false
}

// rbPercent reads a percent literal (%w[], %i(), %q{}, %Q<>, %r||, %()) at
// i and returns the offset after it; ok is false when % is the operator.
func rbPercent(src string, i int) (int, bool) {
	j := i + 1
	if j < len(src) && strings.IndexByte("wWiIqQrsx", src[j]) >= 0 {
		j++
	}
	if j >= len(src) {
		return 0, false
	}
	open := src[j]
	closeBy := map[byte]byte{'(': ')', '[': ']', '{': '}', '<': '>'}
	cl, paired := closeBy[open]
	if !paired {
		if rbIdentByte(open) || open == ' ' || open == '\n' || open == '=' {
			return 0, false
		}
		cl = open
	}
	depth := 1
	for k := j + 1; k < len(src); k++ {
		switch c := src[k]; {
		case c == '\\':
			k++
		case paired && c == open:
			depth++
		case c == cl:
			if depth--; depth == 0 {
				k++
				for k < len(src) && rbIdentByte(src[k]) && src[j-1] == 'r' {
					k++
				}
				return k, true
			}
		}
	}
	return 0, false
}

// rbRegex returns the offset past the regex literal at i and its flags.
func rbRegex(src string, i int) (int, bool) {
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
				for j < len(src) && rbIdentByte(src[j]) {
					j++
				}
				return j, true
			}
		}
	}
	return 0, false
}
