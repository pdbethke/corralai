// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// failMethods are the testing.T methods that fail a test.
var failMethods = map[string]bool{"Error": true, "Errorf": true, "Fatal": true, "Fatalf": true, "Fail": true, "FailNow": true}

// plantVacuous loads a real Go source file with its test file and makes k of
// its tests vacuous by deleting every call through which they can fail. The
// answer key is the set it planted, and it is checked rather than asserted:
//
//   - only a test whose failures all go through its own *testing.T is
//     eligible. One that hands t to a helper, or calls panic, os.Exit or
//     log.Fatal itself, is left alone, since deleting its own calls would
//     not make it vacuous;
//   - after stripping, the planted test is walked again and must contain no
//     failure call, so it can fail only by panicking;
//   - the seeded file is compiled and the planted tests run with
//     `go test -overlay`, which never touches the file on disk, and they
//     must pass. A candidate that no longer compiles (a variable used only
//     in a deleted message) is dropped and the next one is tried.
//
// The file's other tests are NOT keyed. Nobody has checked them, so a flag
// on one of them is reported on its own, never as right or false.
func plantVacuous(src string, k int) (fixture, error) {
	f, err := realFixture(src)
	if err != nil {
		return fixture{}, err
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, f.testPath, f.tests, parser.ParseComments)
	if err != nil {
		return fixture{}, err
	}
	var cands []*ast.FuncDecl
	for _, d := range file.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test") && plantable(fn) {
			cands = append(cands, fn)
		}
	}
	var planted []string
	var cuts [][2]int
	seeded := f.tests
	for _, fn := range spread(cands, k) {
		if len(planted) == k {
			break
		}
		try := append(append([][2]int{}, cuts...), failLines(fset, f.tests, fn)...)
		out, err := cutLines(f.tests, try)
		if err == nil {
			err = stillFails(out, append(append([]string{}, planted...), fn.Name.Name))
		}
		if err == nil {
			err = runPlanted(f.testPath, out, append(append([]string{}, planted...), fn.Name.Name))
		}
		if err != nil {
			continue
		}
		cuts, seeded = try, out
		planted = append(planted, fn.Name.Name)
	}
	if len(planted) < k {
		return fixture{}, fmt.Errorf("%s: only %d test(s) could be planted, %d asked for", f.testPath, len(planted), k)
	}
	sort.Strings(planted)
	f.tests, f.vacuous, f.unkeyed, f.planted = seeded, planted, false, true
	f.name += " (planted)"
	return f, nil
}

// failLines returns the byte range of every whole line holding a failure
// call statement in fn, closures included. Cutting source text, rather than
// re-printing an edited tree, leaves every other byte of the file as its
// author wrote it, so nothing but the missing checks marks a planted test.
func failLines(fset *token.FileSet, src string, fn *ast.FuncDecl) [][2]int {
	ts := tNames(fn)
	var out [][2]int
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		es, ok := n.(*ast.ExprStmt)
		if !ok || !isFailCall(es.X, ts) {
			return true
		}
		a, b := fset.Position(es.Pos()).Offset, fset.Position(es.End()).Offset
		for a > 0 && (src[a-1] == ' ' || src[a-1] == '\t') {
			a--
		}
		for b < len(src) && src[b] != '\n' {
			b++
		}
		if b < len(src) {
			b++
		}
		out = append(out, [2]int{a, b})
		return false
	})
	return out
}

// cutLines deletes the ranges and gofmts the result.
func cutLines(src string, cuts [][2]int) (string, error) {
	sorted := append([][2]int{}, cuts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i][0] > sorted[j][0] })
	out := src
	for _, c := range sorted {
		out = out[:c[0]] + out[c[1]:]
	}
	b, err := format.Source([]byte(out))
	return string(b), err
}

// stillFails re-parses the seeded file and refuses it if any named test can
// still call a failure method.
func stillFails(seeded string, tests []string) error {
	file, err := parser.ParseFile(token.NewFileSet(), "seeded_test.go", seeded, 0)
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, t := range tests {
		want[t] = true
	}
	for _, d := range file.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && want[fn.Name.Name] && hasFailureCall(fn) {
			return fmt.Errorf("%s can still fail after planting", fn.Name.Name)
		}
	}
	return nil
}

// spread orders the candidates so the first k are evenly spaced through the
// file, then the rest in file order as replacements.
func spread(cands []*ast.FuncDecl, k int) []*ast.FuncDecl {
	if k <= 0 || len(cands) <= k {
		return cands
	}
	var out []*ast.FuncDecl
	used := map[int]bool{}
	for i := 0; i < k; i++ {
		j := i * len(cands) / k
		used[j] = true
		out = append(out, cands[j])
	}
	for j, c := range cands {
		if !used[j] {
			out = append(out, c)
		}
	}
	return out
}

// tNames is every *testing.T parameter in fn, its own and its closures'.
func tNames(fn *ast.FuncDecl) map[string]bool {
	names := map[string]bool{}
	add := func(ft *ast.FuncType) {
		for _, p := range ft.Params.List {
			if isTestingT(p.Type) {
				for _, n := range p.Names {
					names[n.Name] = true
				}
			}
		}
	}
	add(fn.Type)
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if fl, ok := n.(*ast.FuncLit); ok {
			add(fl.Type)
		}
		return true
	})
	return names
}

func isTestingT(e ast.Expr) bool {
	st, ok := e.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := st.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "testing" && sel.Sel.Name == "T"
}

// isFailCall reports a call of a failure method on one of the test's T names.
func isFailCall(n ast.Node, ts map[string]bool) bool {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !failMethods[sel.Sel.Name] {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && ts[id.Name]
}

// plantable: the test fails somewhere through its own T, hands T to nothing,
// and does not exit or panic by itself.
func plantable(fn *ast.FuncDecl) bool {
	ts := tNames(fn)
	if len(ts) == 0 {
		return false
	}
	fails, ok := false, true
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, isCall := n.(*ast.CallExpr)
		if !isCall {
			return true
		}
		if isFailCall(call, ts) {
			fails = true
		}
		for _, a := range call.Args {
			if id, isID := a.(*ast.Ident); isID && ts[id.Name] {
				ok = false
			}
		}
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			if fun.Name == "panic" {
				ok = false
			}
		case *ast.SelectorExpr:
			if pkg, isPkg := fun.X.(*ast.Ident); isPkg && ((pkg.Name == "os" && fun.Sel.Name == "Exit") || (pkg.Name == "log" && strings.HasPrefix(fun.Sel.Name, "Fatal")) || (pkg.Name == "log" && strings.HasPrefix(fun.Sel.Name, "Panic"))) {
				ok = false
			}
		}
		return true
	})
	return fails && ok
}

func hasFailureCall(fn *ast.FuncDecl) bool {
	ts := tNames(fn)
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if isFailCall(n, ts) {
			found = true
		}
		return !found
	})
	return found
}

// runPlanted compiles the seeded test file in place of the real one, through
// an overlay, and runs the named tests; they must pass.
func runPlanted(testPath, seeded string, tests []string) error {
	dir, err := os.MkdirTemp("", "criticbench-plant-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	abs, err := filepath.Abs(testPath)
	if err != nil {
		return err
	}
	seededPath := filepath.Join(dir, filepath.Base(testPath))
	if err := os.WriteFile(seededPath, []byte(seeded), 0o600); err != nil {
		return err
	}
	ov, _ := json.Marshal(map[string]map[string]string{"Replace": {abs: seededPath}})
	ovPath := filepath.Join(dir, "overlay.json")
	if err := os.WriteFile(ovPath, ov, 0o600); err != nil {
		return err
	}
	cmd := exec.Command("go", "test", "-overlay="+ovPath, "-count=1", "-run", "^("+strings.Join(tests, "|")+")$", ".") // #nosec G204 -- dev-only bench; test names come from the parsed file
	cmd.Dir = filepath.Dir(abs)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("planted tests did not compile and pass: %v\n%s", err, out)
	}
	return nil
}
