// SPDX-License-Identifier: Elastic-2.0

package agentbackend

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The name-prefix gate (founder ruling 2026-11-12). apiVendorOf, the table
// that reads a vendor off a model's spelling, may LABEL what already ran
// (RecordedVendorOf, an agent seat's pinned model) and may ROUTE nothing.
// A routing function is DERIVED, not listed (AGENTS.md: a gate that
// enumerates where a property holds is not a gate): any function or method in
// this package whose results include a Backend, a ResolvedSeat, or a concrete
// *xBackend type. The gate walks the package's call graph from every one of
// them and fails if apiVendorOf is reachable, or if any function reached
// guesses from a name INLINE: a strings.HasPrefix / HasSuffix / Contains /
// EqualFold call with an argument mentioning a variable named model, spec or
// name, or a switch on such a variable with string-literal cases. That is how
// the old ForModel was written, and it needs no table at all.
//
// The graph is deliberately coarse, in the direction of strictness: a
// reference to a package function's name counts as a call (so a function
// value passed along is followed), and a selector .M counts as a call to
// EVERY method named M. A false edge can only make the gate fail, never pass.
// Residual, stated plainly: a call through an interface value the package
// does not declare a method for, or through reflection, is not seen; nor is a
// guess made in ANOTHER package and handed in (cmd/corral is covered by
// onedoor_test.go, which forbids the string-keyed constructors there, not by
// this walk); nor an inline test on a variable with another name (a guess on
// `m` or `id` passes), a regexp, strings.Cut/Index, or a map keyed by model
// name. The variable-name rule is the narrowest that still catches the old
// ForModel body; it is a tripwire, not a proof.
//
// hasAnyPrefix is forbidden beside apiVendorOf because it is the matcher the
// table is built from: ForModel, which this gate replaces, routed by calling
// it directly and never touched apiVendorOf, so a gate on the table's name
// alone would have passed the very code that leaked.
var prefixTable = map[string]bool{"apiVendorOf": true, "hasAnyPrefix": true}

// nameVars are the variable names an inline guess is looked for on.
var nameVars = map[string]bool{"model": true, "spec": true, "name": true}

// nameTests are the strings functions that, applied to a model's name, are a
// guess about who serves it.
var nameTests = map[string]bool{"HasPrefix": true, "HasSuffix": true, "Contains": true, "EqualFold": true}

// inlineExempt are reached functions whose inline name tests are NOT vendor
// choice, each with its reason. Named, never pattern-matched, so a new one is
// a reviewed decision.
var inlineExempt = map[string]string{
	// The vendor is already declared (ForVendor's openai arm, or a gateway
	// entry); this picks the WIRE within it — the Codex models are served on
	// the Responses API only — and never moves a seat to another vendor.
	"needsResponsesAPI": "wire choice within a declared openai vendor",
	"newOpenAIBackend":  "wire choice within a declared openai vendor",
	// An agent seat's pin is refused when it starts with "-", so a model
	// value cannot smuggle a flag into the agent's argv. That is an
	// injection guard on an already-declared agent seat, not a vendor.
	"expandAgentArgv": "flag-injection guard on an agent seat's pin",
}

// mentionsNameVar reports whether e mentions an identifier in nameVars.
func mentionsNameVar(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && nameVars[id.Name] {
			found = true
		}
		return !found
	})
	return found
}

// inlineGuesses returns a description of each inline name test in body.
func inlineGuesses(body *ast.BlockStmt) []string {
	var out []string
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			sel, ok := x.Fun.(*ast.SelectorExpr)
			if !ok || !nameTests[sel.Sel.Name] {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "strings" {
				return true
			}
			for _, a := range x.Args {
				if mentionsNameVar(a) {
					out = append(out, "strings."+sel.Sel.Name+" on a name")
					break
				}
			}
		case *ast.SwitchStmt:
			if x.Tag == nil || !mentionsNameVar(x.Tag) {
				return true
			}
			for _, st := range x.Body.List {
				cc, ok := st.(*ast.CaseClause)
				if !ok {
					continue
				}
				for _, e := range cc.List {
					if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						out = append(out, "switch on a name with literal cases")
						return true
					}
				}
			}
		}
		return true
	})
	return out
}

// routingReaches returns, for each routing function that reaches the prefix
// table, the call path that does, plus the number of routing roots found.
func routingReaches(t *testing.T, dir string) (paths []string, roots int) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	decls := map[string]*ast.FuncDecl{} // key: name, or Recv.name for a method
	methods := map[string][]string{}    // method name -> keys
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, n), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			key := fd.Name.Name
			if fd.Recv != nil && len(fd.Recv.List) > 0 {
				key = recvName(fd.Recv.List[0].Type) + "." + key
				methods[fd.Name.Name] = append(methods[fd.Name.Name], key)
			}
			decls[key] = fd
		}
	}
	edges := map[string][]string{}
	inline := map[string][]string{}
	for key, fd := range decls {
		if fd.Body == nil {
			continue
		}
		if _, ok := inlineExempt[fd.Name.Name]; !ok {
			inline[key] = inlineGuesses(fd.Body)
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				edges[key] = append(edges[key], methods[x.Sel.Name]...)
			case *ast.Ident:
				if d, ok := decls[x.Name]; ok && d.Recv == nil {
					edges[key] = append(edges[key], x.Name)
				}
			}
			return true
		})
	}
	var rootKeys []string
	for key, fd := range decls {
		if returnsRoute(fd) {
			rootKeys = append(rootKeys, key)
		}
	}
	sort.Strings(rootKeys)
	for _, root := range rootKeys {
		// Breadth-first, keeping the parent so the failure names the path.
		parent := map[string]string{root: ""}
		queue := []string{root}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			if prefixTable[cur] || len(inline[cur]) > 0 {
				var path []string
				for at := cur; at != ""; at = parent[at] {
					path = append([]string{at}, path...)
				}
				p := strings.Join(path, " -> ")
				if !prefixTable[cur] {
					p += ": " + inline[cur][0]
				}
				paths = append(paths, p)
				break
			}
			for _, next := range edges[cur] {
				if _, seen := parent[next]; !seen {
					parent[next] = cur
					queue = append(queue, next)
				}
			}
		}
	}
	return paths, len(rootKeys)
}

func recvName(e ast.Expr) string {
	if s, ok := e.(*ast.StarExpr); ok {
		e = s.X
	}
	if id, ok := e.(*ast.Ident); ok {
		return id.Name
	}
	return "?"
}

// returnsRoute reports whether fd hands back a route: a Backend, a
// ResolvedSeat, or a concrete *xBackend / xBackend type.
func returnsRoute(fd *ast.FuncDecl) bool {
	if fd.Type.Results == nil {
		return false
	}
	for _, r := range fd.Type.Results.List {
		t := r.Type
		if s, ok := t.(*ast.StarExpr); ok {
			t = s.X
		}
		if id, ok := t.(*ast.Ident); ok && (id.Name == "ResolvedSeat" || strings.HasSuffix(id.Name, "Backend")) {
			return true
		}
	}
	return false
}

// Negative control, run before the gate is trusted: a routing function that
// reaches the table directly, through a helper chain, through a method, and
// through a function value must each be reported; a labelling function that
// reaches it, and a routing function that does not, must not.
func TestPrefixGateReportsEachRoutingReach(t *testing.T) {
	dir := t.TempDir()
	src := `package p

type Backend interface{}
type T struct{}

func apiVendorOf(m string) string { return m }
func label(m string) string     { return apiVendorOf(m) }
func helper(m string) string    { return label(m) }

func Direct(m string) (Backend, error)   { _ = apiVendorOf(m); return nil, nil }
func Chained(m string) (Backend, error)  { _ = helper(m); return nil, nil }
func Valued(m string) (Backend, error)   { f := label; _ = f(m); return nil, nil }
func Matcher(m string) (Backend, error)  { _ = hasAnyPrefix(m, "gpt-"); return nil, nil }
func hasAnyPrefix(s string, p ...string) bool { return false }
func (T) WithModel(m string) Backend     { _ = T{}.lbl(m); return nil }
func (T) lbl(m string) string            { return apiVendorOf(m) }

func Clean(m string) (Backend, error)    { return nil, nil }
func RecordedLabel(m string) string      { return apiVendorOf(m) }
`
	if err := os.WriteFile(filepath.Join(dir, "p.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	paths, roots := routingReaches(t, dir)
	if roots != 6 {
		t.Fatalf("found %d routing roots, want 6 (Direct, Chained, Valued, Matcher, T.WithModel, Clean)", roots)
	}
	for _, want := range []string{"Direct", "Chained", "Valued", "Matcher", "T.WithModel"} {
		hit := false
		for _, p := range paths {
			hit = hit || strings.HasPrefix(p, want+" -> ")
		}
		if !hit {
			t.Errorf("the gate missed %s reaching the prefix table; reported %v", want, paths)
		}
	}
	for _, p := range paths {
		if strings.HasPrefix(p, "Clean ") || strings.HasPrefix(p, "RecordedLabel ") {
			t.Errorf("the gate reported a function that routes nothing by name: %s", p)
		}
	}
}

// Negative control for the INLINE forms: a routing function need not call the
// table to guess a vendor — a strings.HasPrefix on the model, or a switch on
// the spec with literal cases, is the same guess written in place (it is how
// the old ForModel was written). Each form is its own root so one miss cannot
// hide behind another's hit, and a root returning a ResolvedSeat or a concrete
// *xBackend counts as routing too. A switch on the DECLARED vendor and a
// prefix test on a URL route nothing by a model's name and must stay quiet.
func TestPrefixGateReportsEachInlineGuess(t *testing.T) {
	dir := t.TempDir()
	src := `package p

import "strings"

type Backend interface{}
type ResolvedSeat struct{ Spec string }
type fooBackend struct{}

func InlinePrefix(model string) (Backend, error)   { _ = strings.HasPrefix(model, "gpt-"); return nil, nil }
func InlineSuffix(model string) (Backend, error)   { _ = strings.HasSuffix(strings.ToLower(model), "-x"); return nil, nil }
func InlineContains(spec string) (Backend, error)  { _ = strings.Contains(spec, "claude"); return nil, nil }
func InlineEqualFold(name string) (Backend, error) { _ = strings.EqualFold(name, "gemini"); return nil, nil }
func InlineSwitch(model string) (Backend, error) {
	switch model {
	case "gpt-6":
	}
	return nil, nil
}
func Helped(model string) (Backend, error) { guess(model); return nil, nil }
func guess(model string)                   { _ = strings.HasPrefix(model, "claude-") }
func SeatRoot(spec string) ResolvedSeat    { _ = strings.HasPrefix(spec, "gemini-"); return ResolvedSeat{} }
func ConcreteRoot(model string) *fooBackend { _ = strings.Contains(model, "o3"); return nil }

func VendorSwitch(vendor, model string) (Backend, error) {
	switch vendor {
	case "anthropic":
	}
	_ = strings.HasPrefix("http://x", "http")
	return nil, nil
}
func URLPrefix(url string) (Backend, error) { _ = strings.HasPrefix(url, "https://"); return nil, nil }
`
	if err := os.WriteFile(filepath.Join(dir, "p.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	paths, _ := routingReaches(t, dir)
	for _, want := range []string{"InlinePrefix", "InlineSuffix", "InlineContains", "InlineEqualFold", "InlineSwitch", "Helped", "SeatRoot", "ConcreteRoot"} {
		hit := false
		for _, p := range paths {
			hit = hit || strings.HasPrefix(p, want+" ") || p == want || strings.HasPrefix(p, want+":")
		}
		if !hit {
			t.Errorf("the gate missed the inline guess in %s; reported %v", want, paths)
		}
	}
	for _, p := range paths {
		if strings.HasPrefix(p, "VendorSwitch") || strings.HasPrefix(p, "URLPrefix") {
			t.Errorf("the gate reported a function that guesses nothing from a name: %s", p)
		}
	}
}

func TestNoRoutingPathReachesTheNamePrefixTable(t *testing.T) {
	paths, roots := routingReaches(t, ".")
	if roots < 5 {
		t.Fatalf("found only %d functions returning a Backend: the gate is not looking at agentbackend", roots)
	}
	if len(paths) > 0 {
		t.Fatalf("a function that builds a route reaches the model-name prefix table or tests a model's name inline, so a seat could be routed by its spelling instead of a declaration:\n  %s", strings.Join(paths, "\n  "))
	}
}
