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
// this package whose results include a Backend. The gate walks the package's
// call graph from every one of them and fails if apiVendorOf is reachable.
//
// The graph is deliberately coarse, in the direction of strictness: a
// reference to a package function's name counts as a call (so a function
// value passed along is followed), and a selector .M counts as a call to
// EVERY method named M. A false edge can only make the gate fail, never pass.
// Residual: a call through an interface value the package does not declare a
// method for, or through reflection, is not seen.
//
// hasAnyPrefix is forbidden beside apiVendorOf because it is the matcher the
// table is built from: ForModel, which this gate replaces, routed by calling
// it directly and never touched apiVendorOf, so a gate on the table's name
// alone would have passed the very code that leaked.
var prefixTable = map[string]bool{"apiVendorOf": true, "hasAnyPrefix": true}

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
	for key, fd := range decls {
		if fd.Body == nil {
			continue
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
		if returnsBackend(fd) {
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
			if prefixTable[cur] {
				var path []string
				for at := cur; at != ""; at = parent[at] {
					path = append([]string{at}, path...)
				}
				paths = append(paths, strings.Join(path, " -> "))
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

func returnsBackend(fd *ast.FuncDecl) bool {
	if fd.Type.Results == nil {
		return false
	}
	for _, r := range fd.Type.Results.List {
		if id, ok := r.Type.(*ast.Ident); ok && id.Name == "Backend" {
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

func TestNoRoutingPathReachesTheNamePrefixTable(t *testing.T) {
	paths, roots := routingReaches(t, ".")
	if roots < 5 {
		t.Fatalf("found only %d functions returning a Backend: the gate is not looking at agentbackend", roots)
	}
	if len(paths) > 0 {
		t.Fatalf("a function that builds a Backend reaches the model-name prefix table, so a seat could be routed by its spelling instead of a declaration:\n  %s", strings.Join(paths, "\n  "))
	}
}
