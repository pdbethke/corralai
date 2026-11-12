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

// The one-door gate. Every seat a corral verb runs on is routed ONCE, by
// resolveSeats, into an agentbackend.ResolvedSeat; agentbackend.Build is the
// only thing that turns one into a Backend. Before the resolver, five doors
// each guessed a route from the model's NAME, and that is how a local gpt-oss
// name reached OpenAI.
//
// The gate states the PROPERTY, not the files that happen to hold it today
// (AGENTS.md: "a gate that enumerates where a property holds is not a gate").
// An earlier version of this task listed four allowed files; seat-resolver
// Task 7 legitimately added Build calls in two more, which is exactly the
// failure that design invites. So there is no allowlist. Two rules:
//
//  1. No string-keyed constructor — agentbackend.FromEnv, ForModel,
//     ForModelOrLocal, NewOllamaBackend, ForSeat — appears anywhere in
//     cmd/corral, by any spelling (aliased import, method value, dot-import).
//     Each turns a name or the environment into a Backend without the
//     resolver's decision.
//  2. A ResolvedSeat composite literal that SETS A FIELD appears only in the
//     file that defines resolveSeats (found by its FuncDecl, not by name). The
//     zero literal `agentbackend.ResolvedSeat{}` is allowed anywhere, because
//     it asserts nothing about routing. agentbackend.Build is allowed anywhere:
//     once rule 2 holds, the only routes it can be handed are the resolver's.
//
// Residual this gate cannot see, stated plainly: a seat assembled by FIELD
// ASSIGNMENT on a zero value (`var rs agentbackend.ResolvedSeat; rs.Endpoint =
// "…"`), a ResolvedSeat received as a parameter and mutated, or one built
// through a type alias, is not a composite literal and is not reported; nor
// is a field-set seat built OUTSIDE cmd/corral (another package, another
// binary) and returned into it, since the gate parses cmd/corral only. The
// gate raises the cost of a second door; it does not make one impossible.
// Review catches what the syntax tree cannot.
const agentbackendPath = "github.com/pdbethke/corralai/internal/agentbackend"

// stringKeyedConstructors are the entry points that build a Backend from a
// name or from the process environment. None may appear in cmd/corral.
var stringKeyedConstructors = map[string]bool{
	"FromEnv": true, "ForModel": true, "ForModelOrLocal": true, "NewOllamaBackend": true, "ForSeat": true,
}

// doorViolations parses every non-test .go file directly in dir and returns
// "file:line what" for each breach of the two rules, plus the number of files
// walked and the files that define resolveSeats.
func doorViolations(t *testing.T, dir string) (found []string, files int, resolverFiles []string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var parsed []*ast.File
	var names []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, n), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		parsed = append(parsed, f)
		names = append(names, n)
	}
	for i, f := range parsed {
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.Name == "resolveSeats" {
				resolverFiles = append(resolverFiles, names[i])
			}
		}
	}
	for i, f := range parsed {
		n := names[i]
		files++
		isResolver := false
		for _, rf := range resolverFiles {
			isResolver = isResolver || rf == n
		}
		report := func(pos token.Pos, what string) {
			found = append(found, fmt.Sprintf("%s:%d %s", n, fset.Position(pos).Line, what))
		}
		// The qualifier is resolved from THIS file's import spec, so an
		// aliased import is followed and an unrelated package that happens to
		// export a ForModel is not a finding. A dot-import makes every use
		// unqualified and unfindable by name, so the import itself is the breach.
		qualifier := ""
		for _, imp := range f.Imports {
			if p, err := strconv.Unquote(imp.Path.Value); err != nil || p != agentbackendPath {
				continue
			}
			switch {
			case imp.Name == nil:
				qualifier = "agentbackend"
			case imp.Name.Name == ".":
				report(imp.Pos(), "dot-import of agentbackend")
			case imp.Name.Name != "_":
				qualifier = imp.Name.Name
			}
		}
		if qualifier == "" {
			continue
		}
		isSeatType := func(e ast.Expr) bool {
			if star, ok := e.(*ast.StarExpr); ok {
				e = star.X
			}
			sel, ok := e.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "ResolvedSeat" {
				return false
			}
			id, ok := sel.X.(*ast.Ident)
			return ok && id.Name == qualifier
		}
		// elided: the element type of a slice/array/map literal, whose inner
		// literals (`{Spec: "x"}`) carry no type of their own.
		elided := func(typ ast.Expr) bool {
			switch tt := typ.(type) {
			case *ast.ArrayType:
				return isSeatType(tt.Elt)
			case *ast.MapType:
				return isSeatType(tt.Value)
			}
			return false
		}
		ast.Inspect(f, func(node ast.Node) bool {
			switch x := node.(type) {
			case *ast.SelectorExpr:
				// Every SelectorExpr, not just call targets, so a method value
				// (`g := agentbackend.ForSeat`) is found too.
				if id, ok := x.X.(*ast.Ident); ok && id.Name == qualifier && stringKeyedConstructors[x.Sel.Name] {
					report(x.Pos(), qualifier+"."+x.Sel.Name)
				}
			case *ast.CompositeLit:
				if isResolver {
					return true
				}
				if x.Type != nil && isSeatType(x.Type) && len(x.Elts) > 0 {
					report(x.Pos(), "ResolvedSeat literal with fields set outside the resolver's file")
				}
				if x.Type != nil && elided(x.Type) {
					for _, el := range x.Elts {
						if kv, ok := el.(*ast.KeyValueExpr); ok {
							el = kv.Value
						}
						if in, ok := el.(*ast.CompositeLit); ok && in.Type == nil && len(in.Elts) > 0 {
							report(in.Pos(), "ResolvedSeat literal with fields set outside the resolver's file")
						}
					}
				}
			}
			return true
		})
	}
	sort.Strings(found)
	return found, files, resolverFiles
}

func plant(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const doorImport = "import \"" + agentbackendPath + "\"\n"

// Negative control, written and run BEFORE the gate is trusted: a gate that has
// never been seen to fail proves nothing. Each spelling is its own file so one
// miss cannot hide behind another's hit.
func TestOneDoorGateReportsEachBreach(t *testing.T) {
	planted := map[string]string{
		"fromenv.go":  "package main\n\n" + doorImport + "\nfunc f() { _, _ = agentbackend.FromEnv() }\n",
		"formodel.go": "package main\n\n" + doorImport + "\nfunc f() { _, _ = agentbackend.ForModel(\"m\") }\n",
		"local.go":    "package main\n\n" + doorImport + "\nfunc f() { _, _ = agentbackend.ForModelOrLocal(\"m\") }\n",
		"ollama.go":   "package main\n\n" + doorImport + "\nfunc f() { _ = agentbackend.NewOllamaBackend(\"u\", \"m\") }\n",
		"forseat.go":  "package main\n\n" + doorImport + "\nfunc f() { _, _ = agentbackend.ForSeat(\"m\", agentbackend.SeatOptions{}) }\n",
		"alias.go":    "package main\n\nimport ab \"" + agentbackendPath + "\"\n\nfunc f() { _, _ = ab.ForModel(\"m\") }\n",
		"value.go":    "package main\n\n" + doorImport + "\nvar x = agentbackend.ForSeat\n",
		"dot.go":      "package main\n\nimport . \"" + agentbackendPath + "\"\n\nfunc f() { _, _ = ForModel(\"m\") }\n",
		"literal.go":  "package main\n\n" + doorImport + "\nvar x = agentbackend.ResolvedSeat{Spec: \"x\"}\n",
		"pointer.go":  "package main\n\n" + doorImport + "\nvar x = &agentbackend.ResolvedSeat{Endpoint: \"http://e\"}\n",
		"slice.go":    "package main\n\n" + doorImport + "\nvar x = []agentbackend.ResolvedSeat{{Spec: \"x\"}}\n",
		"mapped.go":   "package main\n\n" + doorImport + "\nvar x = map[string]agentbackend.ResolvedSeat{\"k\": {Kind: \"api\"}}\n",
		"aliaslit.go": "package main\n\nimport ab \"" + agentbackendPath + "\"\n\nvar x = ab.ResolvedSeat{Spec: \"x\"}\n",
	}
	quiet := map[string]string{
		"prose.go":        "package main\n\n// agentbackend.FromEnv() and ResolvedSeat{Spec: \"x\"} in prose\n",
		"other.go":        "package main\n\nimport \"example.com/other\"\n\nvar y = other.ForModel\nvar z = other.ResolvedSeat{Spec: \"x\"}\n",
		"zero.go":         "package main\n\n" + doorImport + "\nvar x = agentbackend.ResolvedSeat{}\nvar y = []agentbackend.ResolvedSeat{{}}\n",
		"build.go":        "package main\n\n" + doorImport + "\nfunc f(rs agentbackend.ResolvedSeat) { _, _ = agentbackend.Build(rs, agentbackend.SeatOptions{}) }\n",
		"in_test_test.go": "package main\n\n" + doorImport + "\nvar x = agentbackend.ForSeat\n",
	}
	all := map[string]string{}
	for k, v := range planted {
		all[k] = v
	}
	for k, v := range quiet {
		all[k] = v
	}
	found, files, _ := doorViolations(t, plant(t, all))
	if files != len(all)-1 { // the _test.go file is not walked
		t.Fatalf("walked %d files, want %d", files, len(all)-1)
	}
	for name := range planted {
		hit := false
		for _, f := range found {
			hit = hit || strings.HasPrefix(f, name+":")
		}
		if !hit {
			t.Errorf("the gate missed the planted breach in %s; found %v", name, found)
		}
	}
	for _, f := range found {
		name := strings.SplitN(f, ":", 2)[0]
		if _, ok := planted[name]; !ok {
			t.Errorf("the gate reported a file that breaches nothing: %s", f)
		}
	}
}

// The file that defines resolveSeats is the one place a field-set literal
// belongs; it is found by the FuncDecl, so renaming the file cannot orphan the
// gate, and a method that merely shares the name does not qualify.
func TestOneDoorGateAllowsLiteralsInTheResolversFile(t *testing.T) {
	dir := plant(t, map[string]string{
		"anything_at_all.go": "package main\n\n" + doorImport + "\nfunc resolveSeats() { _ = agentbackend.ResolvedSeat{Spec: \"x\", Kind: \"api\"} }\n",
		"elsewhere.go":       "package main\n\n" + doorImport + "\nvar x = agentbackend.ResolvedSeat{Spec: \"x\"}\n",
		"method.go":          "package main\n\ntype t struct{}\n\nfunc (t) resolveSeats() {}\n",
	})
	found, _, resolverFiles := doorViolations(t, dir)
	if len(resolverFiles) != 1 || resolverFiles[0] != "anything_at_all.go" {
		t.Fatalf("resolver files = %v, want exactly anything_at_all.go (a method named resolveSeats is not the function)", resolverFiles)
	}
	if len(found) != 1 || !strings.HasPrefix(found[0], "elsewhere.go:") {
		t.Fatalf("want exactly the literal in elsewhere.go reported, got %v", found)
	}
}

func TestOnlyTheResolverBuildsBackends(t *testing.T) {
	found, files, resolverFiles := doorViolations(t, ".")
	if files < 20 {
		t.Fatalf("the walk saw only %d files: the gate is not looking at cmd/corral", files)
	}
	if len(resolverFiles) != 1 {
		t.Fatalf("want exactly one file defining resolveSeats (the gate's anchor), found %v", resolverFiles)
	}
	sort.Strings(found)
	if len(found) > 0 {
		t.Fatalf("a second door builds backends around the seat resolver, so a name could be routed by a guess instead of a declaration:\n  %s", strings.Join(found, "\n  "))
	}
}
