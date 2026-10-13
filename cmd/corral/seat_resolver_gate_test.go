// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// resolverBypasses are the two constructors that turn a model NAME into a
// backend without knowing that a name can also be an agent seat (`codex`,
// `claude-code:claude-sonnet-5`, any CORRALAI_AGENT_<NAME>). A seat that calls
// either one accepts the flag and then dials an API — or a local daemon — with
// a string that is the name of a CLI. agentbackend.ForSeat is the one resolver
// that sees both kinds of seat.
var resolverBypasses = map[string]bool{"ForModel": true, "ForModelOrLocal": true}

// seatResolverAllowList names a bypass that must stay, with the reason. It is
// empty on purpose: every site found was resolving a seat.
var seatResolverAllowList = map[string]string{}

// agentbackendPath is the import path whose bypass constructors are banned.
const agentbackendPath = "github.com/pdbethke/corralai/internal/agentbackend"

// findResolverBypasses parses every non-test .go file directly in dir and
// returns "file:line <spelling>" for each use of a bypass. The walk is over
// the syntax tree, not text, so a comment that mentions ForModel is not a
// finding. Three spellings that a call-only, literal-qualifier check would
// miss are all caught:
//
//   - an aliased import (`ab "…/agentbackend"`; `ab.ForModel(x)`): the
//     qualifier is resolved from THIS file's import spec for the path;
//   - a method value (`f := agentbackend.ForModel`, `var x = …`): every
//     SelectorExpr is inspected, not just call targets;
//   - a dot-import of the package, which makes `ForModel(x)` unqualified and
//     unfindable by name, so the import itself is the violation.
//
// The directory is listed, not enumerated by name, so a file added tomorrow is
// walked tomorrow.
func findResolverBypasses(t *testing.T, dir string) (found []string, files int) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, n), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files++
		qualifier := ""
		for _, imp := range f.Imports {
			if p, err := strconv.Unquote(imp.Path.Value); err != nil || p != agentbackendPath {
				continue
			}
			switch {
			case imp.Name == nil:
				qualifier = "agentbackend"
			case imp.Name.Name == ".":
				found = append(found, fmt.Sprintf("%s:%d dot-import of agentbackend", n, fset.Position(imp.Pos()).Line))
			case imp.Name.Name != "_":
				qualifier = imp.Name.Name
			}
		}
		if qualifier == "" {
			continue
		}
		ast.Inspect(f, func(node ast.Node) bool {
			sel, ok := node.(*ast.SelectorExpr)
			if !ok || !resolverBypasses[sel.Sel.Name] {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == qualifier {
				found = append(found, fmt.Sprintf("%s:%d %s.%s", n, fset.Position(sel.Pos()).Line, qualifier, sel.Sel.Name))
			}
			return true
		})
	}
	sort.Strings(found)
	return found, files
}

// The negative control: a gate that has never been seen to fail proves
// nothing (AGENTS.md, "a gate that enumerates where a property holds"). Each
// spelling is its own file so one miss cannot hide behind another's hit.
func TestResolverGateReportsABypass(t *testing.T) {
	dir := t.TempDir()
	imp := "import \"" + agentbackendPath + "\"\n"
	planted := map[string]string{
		"call.go":   "package main\n\n" + imp + "\nfunc f() { _, _ = agentbackend.ForModelOrLocal(\"m\") }\n",
		"alias.go":  "package main\n\nimport ab \"" + agentbackendPath + "\"\n\nfunc f() { _, _ = ab.ForModel(\"m\") }\n",
		"value.go":  "package main\n\n" + imp + "\nvar x = agentbackend.ForModelOrLocal\n",
		"method.go": "package main\n\n" + imp + "\nfunc f() { g := agentbackend.ForModel; _, _ = g(\"m\") }\n",
		"dot.go":    "package main\n\nimport . \"" + agentbackendPath + "\"\n\nfunc f() { _, _ = ForModel(\"m\") }\n",
	}
	for name, src := range planted {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Must NOT be reported: prose, a test file, and an unrelated package that
	// happens to have a ForModel of its own.
	quiet := map[string]string{
		"quiet.go":      "package main\n\n// agentbackend.ForModel(x) in prose\n",
		"other.go":      "package main\n\nimport \"example.com/other\"\n\nvar y = other.ForModel\n",
		"call_test.go":  planted["call.go"],
		"forseat_ok.go": "package main\n\n" + imp + "\nvar z = agentbackend.ForSeat\n",
	}
	for name, src := range quiet {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	found, files := findResolverBypasses(t, dir)
	if files != len(planted)+len(quiet)-1 {
		t.Fatalf("walked %d files", files)
	}
	for name := range planted {
		hit := false
		for _, f := range found {
			hit = hit || strings.HasPrefix(f, name+":")
		}
		if !hit {
			t.Errorf("the gate missed the planted spelling in %s; found %v", name, found)
		}
	}
	if len(found) != len(planted) {
		t.Errorf("want exactly one finding per planted file, got %v", found)
	}
}

func TestNoCLISeatResolvesAroundForSeat(t *testing.T) {
	found, files := findResolverBypasses(t, ".")
	if files < 20 {
		t.Fatalf("the walk saw only %d files: the gate is not looking at cmd/corral", files)
	}
	var bad []string
	for _, s := range found {
		site := strings.Fields(s)[0]
		if _, ok := seatResolverAllowList[site]; ok {
			continue
		}
		bad = append(bad, s)
	}
	if len(bad) > 0 {
		t.Fatalf("a CLI seat resolves a model name around agentbackend.ForSeat, so an agent seat (codex, claude-code, CORRALAI_AGENT_*) would be dialled as an API name:\n  %s", strings.Join(bad, "\n  "))
	}
}
