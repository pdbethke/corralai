// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/pairing"
)

// memSources is a pairing.Sources over an in-memory tree: files exist, mods
// is what each source declares.
func memSources(files []string, mods map[string][]pairing.TestModule) pairing.Sources {
	return pairing.Sources{
		Exists:      func(rel string) bool { return slices.Contains(files, rel) },
		Universe:    func() ([]string, error) { return files, nil },
		TestModules: func(rel string) ([]pairing.TestModule, error) { return mods[rel], nil },
	}
}

// TestCheckTestLandingRefusesOnlyWhereTheTestWouldOverwriteTheSource derives
// both halves from the registry: with no declarations to read (the zero
// Landing), every corpus source of every registered plugin is refused
// exactly when Primary names the source itself, so a language that has a
// real test file (every pre-Rust one) is untouched and Rust, whose Primary
// is its Inline candidate, is refused — with a message that says why, not
// merely that.
func TestCheckTestLandingRefusesOnlyWhereTheTestWouldOverwriteTheSource(t *testing.T) {
	refused := map[string]int{}
	for _, name := range Names() {
		p := registry[name]
		ext, ok := sourceExt(p.TestRules())
		if !ok {
			t.Fatalf("%s: no source extension", name)
		}
		for _, dir := range corpusDirs {
			for _, base := range corpusBases {
				src := filepath.ToSlash(filepath.Join(dir, base+ext))
				err := CheckTestLanding(src, pairing.Landing{})
				primary, _ := pairing.Primary(p.TestRules(), src)
				if (err != nil) != (primary.Path == src) {
					t.Errorf("%s %s: refused=%v but Primary=%q", name, src, err != nil, primary.Path)
				}
				if err != nil {
					refused[name]++
					if !strings.Contains(err.Error(), src) || !strings.Contains(err.Error(), "--repo-dir") {
						t.Errorf("%s: refusal %q does not say why (no declarations read)", src, err)
					}
				}
			}
		}
	}
	if refused["rust"] == 0 {
		t.Error("no Rust source was refused — the property never fired")
	}
	if err := CheckTestLanding("src/flask/cli.py", pairing.Landing{}); err != nil {
		t.Errorf("a Python source is refused: %v", err)
	}
	if err := CheckTestLanding("README", pairing.Landing{}); err != nil {
		t.Errorf("an unclaimed file is refused: %v", err)
	}
}

// TestADeclaredRustSourceIsAdmittedAndAnInlineOneIsNot is 7a's line: a Rust
// file whose test module is a separate file the source declares lands there
// and is admitted; one whose tests are in the file itself, or that declares
// none, still lands on itself and is refused as "not built yet"; one whose
// declaration leaves the repository is refused naming that path.
func TestADeclaredRustSourceIsAdmittedAndAnInlineOneIsNot(t *testing.T) {
	src := memSources(
		[]string{"src/foo.rs", "src/foo_tests.rs", "src/bar.rs", "src/baz.rs"},
		map[string][]pairing.TestModule{
			"src/foo.rs": {{Name: "tests", PathAttr: "foo_tests.rs", CfgTest: true}},
			"src/bar.rs": {{Name: "tests", Inline: true, CfgTest: true}},
		})
	for _, tc := range []struct {
		code, want string
		admit      bool
		says       string
	}{
		{"src/foo.rs", "src/foo_tests.rs", true, ""},
		{"src/bar.rs", "src/bar.rs", false, "not built yet"},
		{"src/baz.rs", "src/baz.rs", false, "not built yet"},
	} {
		l, err := TestLanding(tc.code, src)
		if err != nil {
			t.Fatal(err)
		}
		if l.Path != tc.want {
			t.Errorf("%s lands at %q, want %q", tc.code, l.Path, tc.want)
		}
		err = CheckTestLanding(tc.code, l)
		if (err == nil) != tc.admit {
			t.Errorf("%s: admitted=%v, want %v (%v)", tc.code, err == nil, tc.admit, err)
		}
		if err != nil && (!strings.Contains(err.Error(), tc.says) || !strings.Contains(err.Error(), "test landing is not built")) {
			t.Errorf("%s: refusal %q does not say %q", tc.code, err, tc.says)
		}
	}

	out := pairing.Landing{Path: "src/qux.rs", Into: true, DeclarationsRead: true, RefusedOutsideRoot: []string{"../elsewhere_tests.rs"}}
	err := CheckTestLanding("src/qux.rs", out)
	if err == nil || !strings.Contains(err.Error(), "../elsewhere_tests.rs") || !strings.Contains(err.Error(), "outside") {
		t.Errorf("a declaration outside the root: err = %v, want it named", err)
	}
}

// TestEveryIntoLandingLanguageCanCompose: a landing that appends INTO an
// existing file is admitted only for a language that implements TestLander
// — the plugin that can write the appended block. Derived from the registry:
// a language with a content rule must implement it, and the check refuses an
// Into landing whose file's language cannot compose.
func TestEveryIntoLandingLanguageCanCompose(t *testing.T) {
	for _, name := range Names() {
		p := registry[name]
		content := false
		for _, r := range p.TestRules() {
			content = content || r.Shape.Content()
		}
		if _, ok := p.(TestLander); content && !ok {
			t.Errorf("%s declares a content rule but does not implement TestLander", name)
		}
	}
	// A declared landing in a file no TestLander claims (a made-up .txt
	// "test module") is refused rather than written over.
	l := pairing.Landing{Path: "src/foo_tests.txt", Into: true, DeclarationsRead: true, Depth: 1}
	if err := CheckTestLanding("src/foo.rs", l); err == nil {
		t.Error("an Into landing no TestLander can compose was admitted")
	}
}

// TestRustLandsUnderTheDeclaredModuleWithItsUseLine is the use line as data:
// depth 1 (a declared file) reaches the audited module with super::super,
// depth 0 (the source) with super; a writer's own wrapper and stale use line
// are folded away rather than nested; a corral_tests module the target
// already has is never redeclared; the project's own `mod tests` is never
// touched, because the target is never edited — only appended to.
func TestRustLandsUnderTheDeclaredModuleWithItsUseLine(t *testing.T) {
	body := "#[test]\nfn kills_it() {\n    assert_eq!(private_double(2), 4);\n}"
	got := rustPlugin{}.LandTest(body, 1, "use super::*;\n\n#[test]\nfn mine() {}\n")
	want := "#[cfg(test)]\nmod corral_tests {\nuse super::super::*;\n\n" + body + "\n}\n"
	if got != want {
		t.Errorf("depth 1:\n%s\nwant:\n%s", got, want)
	}
	if got := (rustPlugin{}).LandTest(body, 0, ""); !strings.Contains(got, "\nuse super::*;\n") {
		t.Errorf("depth 0 must reach the source with super:\n%s", got)
	}

	wrapped := "#[cfg(test)]\nmod corral_tests {\n    use super::*;\n\n    #[test]\n    fn kills_it() {\n        assert!(true);\n    }\n}\n"
	got = rustPlugin{}.LandTest(wrapped, 1, "")
	if strings.Count(got, "mod corral_tests") != 1 || strings.Contains(got, "\nuse super::*;") || !strings.Contains(got, "\n#[test]\nfn kills_it() {") {
		t.Errorf("a writer's own wrapper must be folded, not nested:\n%s", got)
	}

	// A wrapper that is not the whole body (a helper module beside a test)
	// is the writer's own code and is kept.
	helper := "mod helpers {\n    pub fn two() -> i32 { 2 }\n}\n#[test]\nfn t() { assert_eq!(helpers::two(), 2); }"
	if got := (rustPlugin{}).LandTest(helper, 1, ""); !strings.Contains(got, "mod helpers {") {
		t.Errorf("a helper module was unwrapped:\n%s", got)
	}

	taken := "use super::*;\n\n#[cfg(test)]\nmod corral_tests {\n}\n"
	if got := (rustPlugin{}).LandTest(body, 1, taken); !strings.Contains(got, "mod corral_tests_2 {") {
		t.Errorf("the target already declares corral_tests; want corral_tests_2:\n%s", got)
	}
}

// TestRustConcatMergesLandedModulesUnderOneHeader: the per-survivor parts are
// each a landed module; the operator gets ONE module with one use line, the
// header lines de-duplicated, a colliding test renamed with its mutant id and
// a colliding helper refused (carried out separately by ConcatAuthored).
func TestRustConcatMergesLandedModulesUnderOneHeader(t *testing.T) {
	r := rustPlugin{}
	a := r.LandTest("use std::cmp::max;\n\n#[test]\nfn test_kills() { assert_eq!(max(1, 2), 2); }", 1, "")
	b := r.LandTest("use std::cmp::max;\n\n#[test]\nfn test_kills() { assert_eq!(max(3, 2), 3); }", 1, "")
	c := r.LandTest("fn helper() -> i32 { 1 }\n#[test]\nfn test_other() { assert_eq!(helper(), 1); }", 1, "")
	d := r.LandTest("fn helper() -> i32 { 2 }\n#[test]\nfn test_more() { assert_eq!(helper(), 2); }", 1, "")
	merged, extra := ConcatAuthored(r, []AuthoredPart{{MutantID: "s0/m1", Source: a}, {MutantID: "s0/m2", Source: b}, {MutantID: "s0/m3", Source: c}, {MutantID: "s0/m4", Source: d}})
	if strings.Count(merged, "mod corral_tests {") != 1 || strings.Count(merged, "use super::super::*;") != 1 || strings.Count(merged, "use std::cmp::max;") != 1 {
		t.Errorf("want one module, one use line, one hoisted import:\n%s", merged)
	}
	if !strings.Contains(merged, "fn test_kills_s0_m1()") || !strings.Contains(merged, "fn test_kills_s0_m2()") {
		t.Errorf("colliding tests must be suffixed with their mutant ids:\n%s", merged)
	}
	if len(extra) != 1 || extra[0].MutantID != "s0/m4" || !strings.Contains(extra[0].Reason, "helper") {
		t.Errorf("the colliding helper's part must be carried out separately, got %+v", extra)
	}
	if !strings.HasSuffix(merged, "}\n") {
		t.Errorf("merged module must close:\n%s", merged)
	}

	// A part that is not a landed module is refused, never re-wrapped by guess.
	if _, extra := ConcatAuthored(r, []AuthoredPart{{MutantID: "x", Source: "#[test]\nfn t() {}"}}); len(extra) != 1 {
		t.Errorf("an unlanded part was merged: %+v", extra)
	}
	// Two different headers (two landings) are not one module.
	other := r.LandTest("#[test]\nfn test_z() {}", 0, "")
	if _, extra := ConcatAuthored(r, []AuthoredPart{{MutantID: "a", Source: a}, {MutantID: "z", Source: other}}); len(extra) != 1 || extra[0].MutantID != "z" {
		t.Errorf("parts landed under different headers were merged: %+v", extra)
	}
}

// TestComposeLandingAppendsAndNeverReplaces: an Into landing is the target's
// own bytes, unchanged, followed by the landed block; a path landing is the
// test as its own file.
func TestComposeLandingAppendsAndNeverReplaces(t *testing.T) {
	target := "use super::*;\n#[test]\nfn mine() {}"
	block := "#[cfg(test)]\nmod corral_tests {\n}\n"
	got := ComposeLanding(pairing.Landing{Into: true}, target, block)
	if !strings.HasPrefix(got, target) || !strings.HasSuffix(got, block) {
		t.Errorf("compose = %q", got)
	}
	if got := ComposeLanding(pairing.Landing{}, target, "def test_x(): pass\n"); got != "def test_x(): pass\n" {
		t.Errorf("a path landing must be the test alone, got %q", got)
	}
}

// TestRustUnwrapLeavesLinesAloneWithNoCommonIndent: a writer's wrapper whose
// inside shares no indentation (a raw string continuing at column 0) is
// unwrapped with every line exactly as written — the string's text and the
// nesting of the rest are not flattened.
func TestRustUnwrapLeavesLinesAloneWithNoCommonIndent(t *testing.T) {
	wrapped := "mod corral_tests {\n    #[test]\n    fn t() {\n        let s = \"a\nb\";\n        assert_eq!(s.len(), 3);\n    }\n}\n"
	got := rustPlugin{}.LandTest(wrapped, 1, "")
	for _, want := range []string{"\n    #[test]\n", "\n        let s = \"a\nb\";\n", "\n        assert_eq!(s.len(), 3);\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("line changed: want %q in\n%s", want, got)
		}
	}
	// With shared indentation the common part, and only it, goes.
	got = rustPlugin{}.LandTest("mod x {\n    #[test]\n    fn t() {\n        f();\n    }\n}", 1, "")
	if !strings.Contains(got, "\n#[test]\nfn t() {\n    f();\n}\n") {
		t.Errorf("common indentation not removed:\n%s", got)
	}
}

// TestARefusalSaysWhenTheBuildCannotReadDeclarations: a no-cgo build's
// refusal of a Rust file says the build cannot read the declaration — never
// the false "its tests live in the file itself".
func TestARefusalSaysWhenTheBuildCannotReadDeclarations(t *testing.T) {
	err := CheckTestLanding("src/foo.rs", pairing.Landing{Path: "src/foo.rs", Into: true, DeclarationsUnreadable: true})
	if err == nil || !strings.Contains(err.Error(), "cannot read") || strings.Contains(err.Error(), "live in the file itself") {
		t.Errorf("err = %v", err)
	}
}
