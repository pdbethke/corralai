// SPDX-License-Identifier: Elastic-2.0

package wranglerd

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

// The daemon's stores used to each carry their own default path as
// env("CORRALAI_X_DB", filepath.Join(home, ...)). Every one now comes from
// statedir; a new env(...) call whose default is a filepath.Join is a new
// store bypassing the table, and fails here.
//
// Two exemptions, neither a row in the statedir table and neither under
// another tool's directory: the TLS autocert cache (certificate material for
// the Let's Encrypt manager, under ~/.cache) and the repo workspace (a
// disposable clone area under os.TempDir). Listed by variable name so adding
// a third is a deliberate edit of this list.
var pathDefaultExempt = map[string]bool{
	"CORRALAI_TLS_AUTOCERT_CACHE": true,
	"CORRALAI_REPO_WORKSPACE":     true,
}

func TestDaemonHasNoPathDefaultOutsideStatedir(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "daemon.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 2 {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "env" {
			return true
		}
		if lit, ok := call.Args[0].(*ast.BasicLit); ok {
			if name, err := strconv.Unquote(lit.Value); err == nil && pathDefaultExempt[name] {
				return true
			}
		}
		if def, ok := call.Args[1].(*ast.CallExpr); ok {
			if sel, ok := def.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Join" {
				t.Errorf("%s: env(...) with a filepath.Join default — add a statedir row instead", fset.Position(call.Pos()))
			}
		}
		return true
	})
}
