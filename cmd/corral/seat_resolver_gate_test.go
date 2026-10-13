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

// findResolverBypasses parses every non-test .go file directly in dir and
// returns "file:line agentbackend.X" for each call to a bypass. The walk is
// over the syntax tree rather than a text search so a comment that mentions
// ForModel (several do) is not a finding and a call spelled across lines is.
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
		ast.Inspect(f, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !resolverBypasses[sel.Sel.Name] {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "agentbackend" {
				found = append(found, fmt.Sprintf("%s:%d agentbackend.%s", n, fset.Position(call.Pos()).Line, sel.Sel.Name))
			}
			return true
		})
	}
	sort.Strings(found)
	return found, files
}

// The negative control: a gate that has never been seen to fail proves
// nothing (AGENTS.md, "a gate that enumerates where a property holds").
func TestResolverGateReportsABypass(t *testing.T) {
	dir := t.TempDir()
	src := "package main\n\nimport \"x/agentbackend\"\n\nfunc f() { _, _ = agentbackend.ForModelOrLocal(\"m\") }\n"
	if err := os.WriteFile(filepath.Join(dir, "seat.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	// A comment mentioning the name, and a test file, must NOT be reported.
	if err := os.WriteFile(filepath.Join(dir, "quiet.go"), []byte("package main\n\n// agentbackend.ForModel(x) in prose\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "seat_test.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	found, files := findResolverBypasses(t, dir)
	if files != 2 || len(found) != 1 || !strings.HasPrefix(found[0], "seat.go:5 agentbackend.ForModelOrLocal") {
		t.Fatalf("gate did not report exactly the planted bypass: files=%d found=%v", files, found)
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
