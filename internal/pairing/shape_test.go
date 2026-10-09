// SPDX-License-Identifier: Elastic-2.0

package pairing

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// declaredShapes is every constant of type Shape declared in this package,
// read from the source itself, so the check below cannot be satisfied by a
// hand-kept list that a new Shape was never added to.
func declaredShapes(t *testing.T) []Shape {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var out []Shape
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f) // #nosec G304 -- this package's own source files
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range file.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "Shape" {
					continue
				}
				for i := range vs.Names {
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok {
						t.Fatalf("%s: Shape constant %s is not a string literal", f, vs.Names[i].Name)
					}
					out = append(out, Shape(strings.Trim(lit.Value, `"`)))
				}
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("found no Shape constants — the walk proves nothing")
	}
	return out
}

// TestEveryShapeHasTraits is the one-door check for Shape semantics: every
// declared Shape has an entry in shapeTable, the one place that says what a
// Shape means to Candidates, Roots and MightBeTest. Before the table, a new
// Shape had to be added to two switches and was silently dropped by the one
// it was forgotten in (Round B1 final review, I3).
func TestEveryShapeHasTraits(t *testing.T) {
	for _, s := range declaredShapes(t) {
		if _, ok := shapeTable[s]; !ok {
			t.Errorf("Shape %q has no shapeTable entry", s)
		}
	}
	if got, want := len(shapeTable), len(declaredShapes(t)); got != want {
		t.Errorf("shapeTable has %d entries for %d declared Shapes — an entry for an undeclared Shape", got, want)
	}
}

// TestUnknownShapeFailsLoudly: a Rule with a Shape nobody declared is a
// programming error, and each reader of the table must say so rather than
// skip the rule — a skipped rule is a convention silently not applied.
func TestUnknownShapeFailsLoudly(t *testing.T) {
	bogus := []Rule{{Shape: "inline", Name: "{base}_x.rs", Dir: "tests", Rank: 1}}
	for name, call := range map[string]func(){
		"Candidates":  func() { Candidates(bogus, "src/lib.rs") },
		"Roots":       func() { Roots(bogus) },
		"MightBeTest": func() { MightBeTest([][]Rule{bogus}, "src/lib.rs") },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("%s accepted an unknown Shape without complaint", name)
				}
				if msg, _ := r.(string); !strings.Contains(msg, `"inline"`) {
					t.Fatalf("%s panicked with %v; want a message naming the Shape", name, r)
				}
			}()
			call()
		})
	}
}
