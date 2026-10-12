// SPDX-License-Identifier: Elastic-2.0

package pairing

import (
	"errors"
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
	bogus := []Rule{{Shape: "no-such-shape", Name: "{base}_x.rs", Dir: "tests", Rank: 1}}
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
				if msg, _ := r.(string); !strings.Contains(msg, `"no-such-shape"`) {
					t.Fatalf("%s panicked with %v; want a message naming the Shape", name, r)
				}
			}()
			call()
		})
	}
}

// TestOnlySiblingIsSameDir: Dedupe's "a sibling always wins the merge" reads
// the sameDir trait, and the trait must mean exactly one thing — a candidate
// in the source's own directory. Two Shapes claiming it would hand a second
// kind of candidate sibling strength, the vacuous-evidence bug Dedupe's essay
// records.
func TestOnlySiblingIsSameDir(t *testing.T) {
	var got []Shape
	for s, tr := range shapeTable {
		if tr.sameDir {
			got = append(got, s)
		}
	}
	if len(got) != 1 || got[0] != Sibling {
		t.Errorf("Shapes with sameDir = %v, want exactly [%s]", got, Sibling)
	}
}

// rustRules is Rust's rule list restated for this package's internal tests
// (pairing cannot import lang). declared_test.go reads the REAL plugin's
// rules through lang and checks them against fixture trees; this copy only
// has to exercise the Shapes.
var rustRules = []Rule{
	{Shape: Declared, Rank: 0},
	{Shape: Inline, Rank: 0},
	{Shape: Recognize, Name: "{base}_tests.rs"},
	{Shape: Recognize, Name: "tests.rs"},
	{Shape: TestDir, Dir: "tests"},
}

// modules is a Sources.TestModules over a fixed answer per path.
func modules(by map[string][]TestModule) func(string) ([]TestModule, error) {
	return func(rel string) ([]TestModule, error) { return by[rel], nil }
}

// Fail closed: a content rule asks what the source declares, and a caller
// that cannot say must get an error — never a silent skip of the rule, which
// would pair (or not pair) every Rust file by the other rules alone with
// nothing to show the content rules never ran (the rule-at-one-door shape).
func TestResolveContentRuleWithoutTestModulesFailsClosed(t *testing.T) {
	for _, s := range []Shape{Inline, Declared} {
		touched := false
		_, err := Resolve([]Rule{{Shape: s}}, "src/lib.rs", Sources{
			Exists:   func(string) bool { touched = true; return true },
			Universe: func() ([]string, error) { touched = true; return nil, nil },
		})
		if err == nil || !strings.Contains(err.Error(), "TestModules") {
			t.Errorf("%s: err = %v, want a fail-closed error naming TestModules", s, err)
		}
		if touched {
			t.Errorf("%s: Resolve consulted the disk before refusing", s)
		}
	}
}

// A content rule cannot read the source: that is the caller's error to
// surface, never "this file declares no test module".
func TestResolveSurfacesTestModulesError(t *testing.T) {
	_, err := Resolve(rustRules, "src/lib.rs", Sources{
		Exists:      func(string) bool { return true },
		Universe:    func() ([]string, error) { return nil, nil },
		TestModules: func(string) ([]TestModule, error) { return nil, errors.New("permission denied") },
	})
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("err = %v, want the read error", err)
	}
}

// A source with no test module does not pair to itself, and since Rust
// derives no named candidate there is no basename to search for: nothing
// is listed as tried, and no search is claimed (Review Focus 1).
func TestResolveRustSourceWithNoModuleIsAMiss(t *testing.T) {
	listed := false
	res, err := Resolve(rustRules, "src/plain.rs", Sources{
		Exists:      func(string) bool { return true },
		Universe:    func() ([]string, error) { listed = true; return []string{"tests/plain.rs", "src/plain.rs"}, nil },
		TestModules: modules(nil),
	})
	if err != nil || res.Found || res.Path != "" || res.Label != "" || res.Shape != "" {
		t.Fatalf("res=%+v err=%v, want a miss", res, err)
	}
	if listed || len(res.Roots) != 0 {
		t.Fatalf("a search with no basename to look for ran or was reported: listed=%v roots=%v", listed, res.Roots)
	}
}

func TestResolveInlineLabelsTheSourceItself(t *testing.T) {
	res, err := Resolve(rustRules, "src/lib.rs", Sources{
		Exists:      func(rel string) bool { return rel == "src/lib.rs" },
		Universe:    func() ([]string, error) { return nil, nil },
		TestModules: modules(map[string][]TestModule{"src/lib.rs": {{Name: "tests", Inline: true, CfgTest: true}}}),
	})
	if err != nil || !res.Found || res.Path != "src/lib.rs" || res.Shape != Inline || res.Label != "inline" || res.Rank != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

// Declared's paths, by rustc's rule. Each row is one module declaration in
// the source; the path is tried with Exists, and both the hit and the label
// are asserted.
func TestResolveDeclaredFollowsRustc(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		mod        TestModule
		want       string // "" = a miss
		label      string
		tried      []string
	}{
		{"path attr, non-mod.rs file: beside the declaring file", "crates/warp_core/src/app_id.rs",
			TestModule{Name: "tests", PathAttr: "app_id_tests.rs", CfgTest: true},
			"crates/warp_core/src/app_id_tests.rs", "path-attr", nil},
		{"path attr with ../ is cleaned", "crates/warp_cli/src/local_control/mod.rs",
			TestModule{Name: "tests", PathAttr: "../local_control_tests.rs", CfgTest: true},
			"crates/warp_cli/src/local_control_tests.rs", "path-attr", nil},
		{"default module of a non-mod.rs file: <stem>/<name>.rs", "src/bar.rs",
			TestModule{Name: "tests", CfgTest: true}, "src/bar/tests.rs", "default-module", nil},
		{"default module of mod.rs: beside it", "src/foo/mod.rs",
			TestModule{Name: "tests", CfgTest: true}, "src/foo/tests.rs", "default-module", nil},
		{"default module of lib.rs: beside it", "src/lib.rs",
			TestModule{Name: "tests"}, "src/tests.rs", "default-module", nil},
		{"default module of main.rs: beside it", "main.rs",
			TestModule{Name: "test"}, "test.rs", "default-module", nil},
		{"default module may be <name>/mod.rs", "src/baz.rs",
			TestModule{Name: "tests"}, "src/baz/tests/mod.rs", "default-module", nil},
		{"missing #[path] target is a miss, listed in Tried", "src/missing.rs",
			TestModule{Name: "tests", PathAttr: "missing_tests.rs"}, "", "", []string{"src/missing_tests.rs"}},
		{"#[path] escaping the root is never a pairing", "src/escape.rs",
			TestModule{Name: "tests", PathAttr: "../../outside_tests.rs"}, "", "", nil},
		{"an inline module is not Declared's", "src/inl.rs",
			TestModule{Name: "tests", Inline: true, PathAttr: "ignored.rs"}, "src/inl.rs", "inline", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exists := map[string]bool{tc.code: true}
			if tc.want != "" {
				exists[tc.want] = true
			}
			res, err := Resolve(rustRules, tc.code, Sources{
				Exists:      func(rel string) bool { return exists[rel] },
				Universe:    func() ([]string, error) { return nil, nil },
				TestModules: modules(map[string][]TestModule{tc.code: {tc.mod}}),
			})
			if err != nil {
				t.Fatal(err)
			}
			if res.Path != tc.want || res.Found != (tc.want != "") || res.Label != tc.label {
				t.Fatalf("got path=%q found=%v label=%q, want path=%q label=%q (res=%+v)", res.Path, res.Found, res.Label, tc.want, tc.label, res)
			}
			if tc.tried != nil && strings.Join(res.Tried, ",") != strings.Join(tc.tried, ",") {
				t.Fatalf("Tried = %v, want %v", res.Tried, tc.tried)
			}
			for _, p := range res.Tried {
				if strings.HasPrefix(p, "../") {
					t.Fatalf("Tried lists a path outside the root: %v", res.Tried)
				}
			}
		})
	}
}

// Primary's answer for Rust is the source itself: a new test is written into
// the file's own test module, never into a guessed file name.
func TestPrimaryForRustIsTheSource(t *testing.T) {
	c, ok := Primary(rustRules, "src/net/conn.rs")
	if !ok || c.Path != "src/net/conn.rs" || c.Shape != Inline {
		t.Fatalf("Primary = %+v, %v", c, ok)
	}
}

// TestDir: a directory alone makes a file a test, at any depth (a workspace
// member's tests/), while a src/ file is never one by name (Review Focus 2).
func TestTestDirMarksTestsAndOnlyThem(t *testing.T) {
	for rel, want := range map[string]bool{
		"tests/integration.rs":                    true,
		"crates/integration/tests/integration.rs": true,
		"crates/warp_core/src/app_id_tests.rs":    true,
		"src/foo/tests.rs":                        true,
		"src/lib.rs":                              false,
		"src/main.rs":                             false,
		"src/testing_utils.rs":                    false,
		"crates/warp_core/src/app_id.rs":          false,
		"src/foo/mod.rs":                          false,
	} {
		if got := IsTest(rustRules, rel); got != want {
			t.Errorf("IsTest(%q) = %v, want %v", rel, got, want)
		}
		if want && !MightBeTest([][]Rule{rustRules}, rel) {
			t.Errorf("MightBeTest(%q) = false for a test", rel)
		}
	}
	if got := Roots(rustRules); strings.Join(got, ",") != "tests" {
		t.Errorf("Roots = %v, want [tests]", got)
	}
}

// Content and dirMarksTest are each the property of exactly the Shapes that
// need them, so no older Shape changes meaning by gaining one.
func TestNewTraitsBelongToTheNewShapes(t *testing.T) {
	for s, tr := range shapeTable {
		if tr.content != (s == Inline || s == Declared) {
			t.Errorf("%s: content = %v", s, tr.content)
		}
		if tr.dirMarksTest != (s == TestDir) {
			t.Errorf("%s: dirMarksTest = %v", s, tr.dirMarksTest)
		}
	}
}
