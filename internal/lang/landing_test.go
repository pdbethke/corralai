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

// TestInlineLandingIsAdmitted is 7b's line: a Rust file whose test module is
// a separate file the source declares lands there; one whose tests are in
// the file itself, or that declares none, lands on itself — and all three are
// admitted, because the inline block rides every run of the code
// (adequacy.WithInlineTest) and a mutant that edits the source's own tests is
// invalid (adequacy.WithEvidenceLines). What stays refused is what corral
// cannot read: a declaration that leaves the repository, a build that cannot
// read declarations, and an entry with no repository to read them from.
// OwnModule says which inline landings hold the developer's own tests.
func TestInlineLandingIsAdmitted(t *testing.T) {
	src := memSources(
		[]string{"src/foo.rs", "src/foo_tests.rs", "src/bar.rs", "src/baz.rs"},
		map[string][]pairing.TestModule{
			"src/foo.rs": {{Name: "tests", PathAttr: "foo_tests.rs", CfgTest: true}},
			"src/bar.rs": {{Name: "tests", Inline: true, CfgTest: true}},
		})
	for _, tc := range []struct {
		code, want string
		own        bool
	}{
		{"src/foo.rs", "src/foo_tests.rs", true},
		{"src/bar.rs", "src/bar.rs", true},
		{"src/baz.rs", "src/baz.rs", false},
	} {
		l, err := TestLanding(tc.code, src)
		if err != nil {
			t.Fatal(err)
		}
		if l.Path != tc.want || l.OwnModule != tc.own {
			t.Errorf("%s lands at %q (own module %v), want %q (%v)", tc.code, l.Path, l.OwnModule, tc.want, tc.own)
		}
		if err := CheckTestLanding(tc.code, l); err != nil {
			t.Errorf("%s: refused: %v", tc.code, err)
		}
	}

	out := pairing.Landing{Path: "src/qux.rs", Into: true, DeclarationsRead: true, RefusedOutsideRoot: []string{"../elsewhere_tests.rs"}}
	err := CheckTestLanding("src/qux.rs", out)
	if err == nil || !strings.Contains(err.Error(), "../elsewhere_tests.rs") || !strings.Contains(err.Error(), "outside") {
		t.Errorf("a declaration outside the root: err = %v, want it named", err)
	}
	unreadable := pairing.Landing{Path: "src/qux.rs", Into: true, DeclarationsUnreadable: true}
	if err := CheckTestLanding("src/qux.rs", unreadable); err == nil || !strings.Contains(err.Error(), "cannot read") {
		t.Errorf("declarations this build cannot read: err = %v, want refused saying so", err)
	}
	if err := CheckTestLanding("src/qux.rs", pairing.Landing{}); err == nil || !strings.Contains(err.Error(), "--repo-dir") {
		t.Errorf("no repository to read declarations from: err = %v, want refused saying so", err)
	}
}

// TestTheDevTestOfAnInlineLandingIsTheSourceOrNone: where the authored test
// lands in the source, the developer's test is decided by the landing, never
// by a language name or a guessed path: the source itself when it declares
// its own inline test module, and none when it declares no test module.
// Any other landing leaves the choice to pairing (decided false).
func TestTheDevTestOfAnInlineLandingIsTheSourceOrNone(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		l          pairing.Landing
		want       string
		decided    bool
	}{
		{"inline module", "src/bar.rs", pairing.Landing{Path: "src/bar.rs", Into: true, DeclarationsRead: true, OwnModule: true}, "src/bar.rs", true},
		{"no test module", "src/baz.rs", pairing.Landing{Path: "src/baz.rs", Into: true, DeclarationsRead: true}, "", true},
		{"declared file", "src/foo.rs", pairing.Landing{Path: "src/foo_tests.rs", Into: true, DeclarationsRead: true, OwnModule: true, Depth: 1}, "", false},
		{"path landing", "pkg/a.py", pairing.Landing{Path: "tests/test_a.py"}, "", false},
	} {
		got, decided := DevTestOf(tc.code, tc.l)
		if got != tc.want || decided != tc.decided {
			t.Errorf("%s: DevTestOf = %q, %v; want %q, %v", tc.name, got, decided, tc.want, tc.decided)
		}
	}
}

// TestInlineTestIDsAreTheLibtestPathsOfTheBlock: a block appended to a
// library source is run under that source's module path, so its tests'
// libtest ids are the module path, the block's module and the test name —
// exact names the runner rows can select by. A source the library does not
// compile (a binary's), or a block whose tests cannot be listed, has no ids.
func TestInlineTestIDsAreTheLibtestPathsOfTheBlock(t *testing.T) {
	cargo, _ := filepath.Abs("testdata/runners/src/units/cargo")
	block := rustPlugin{}.LandTest("#[test]\nfn kills() {}\n#[test]\nfn more() {}", 0, "")
	for _, tc := range []struct {
		code string
		want []string
	}{
		{"b/src/lib.rs", []string{"corral_tests::kills", "corral_tests::more"}},
		{"b/src/extra.rs", []string{"extra::corral_tests::kills", "extra::corral_tests::more"}},
		{"b/src/inner/extract.rs", []string{"inner::extract::corral_tests::kills", "inner::extract::corral_tests::more"}},
		{"b/src/inner/mod.rs", []string{"inner::corral_tests::kills", "inner::corral_tests::more"}},
	} {
		got, err := InlineTestIDs(cargo, tc.code, block)
		if err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("%s: ids %v (%v), want %v", tc.code, got, err, tc.want)
		}
	}
	if got, err := InlineTestIDs(cargo, "a/src/main.rs", block); err == nil {
		t.Errorf("a binary's source has no library module path, got %v", got)
	}
	if got, err := InlineTestIDs(cargo, "b/src/extra.rs", "#[cfg(test)]\nmod corral_tests {\n"); err == nil {
		t.Errorf("an unreadable block has no ids, got %v", got)
	}
	if got, err := InlineTestIDs(cargo, "b/src/extra.rs", rustPlugin{}.LandTest("fn helper() {}", 0, "")); err == nil {
		t.Errorf("a block with no test has no ids, got %v", got)
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
	want := "#[cfg(test)]\nmod corral_tests {\n#[allow(unused_imports)]\nuse super::super::*;\n\n" + body + "\n}\n"
	if got != want {
		t.Errorf("depth 1:\n%s\nwant:\n%s", got, want)
	}
	if got := (rustPlugin{}).LandTest(body, 0, ""); !strings.Contains(got, "\n#[allow(unused_imports)]\nuse super::*;\n") {
		t.Errorf("depth 0 must reach the source with super:\n%s", got)
	}

	wrapped := "#[cfg(test)]\nmod corral_tests {\n    use super::*;\n\n    #[test]\n    fn kills_it() {\n        assert!(true);\n    }\n}\n"
	got = rustPlugin{}.LandTest(wrapped, 1, "")
	if strings.Count(got, "mod corral_tests") != 1 || strings.Contains(got, "\nuse super::*;") || !strings.Contains(got, "\n#[test]\nfn kills_it() {") {
		t.Errorf("a writer's own wrapper must be folded, not nested:\n%s", got)
	}

	// A writer's own allow on its glob goes with the glob: left behind, it
	// would attach to the test after it. Any number of allows on the glob,
	// wrapped or not; an allow on anything else is the writer's and stays.
	for _, b := range []string{
		"#[allow(unused_imports)]\nuse super::*;\n\n#[test]\nfn kills_it() {}",
		"#[cfg(test)]\nmod corral_tests {\n    #[allow(unused_imports)]\n    #[allow(dead_code)]\n    use super::*;\n\n    #[test]\n    fn kills_it() {}\n}\n",
	} {
		got := rustPlugin{}.LandTest(b, 0, "")
		want := "#[cfg(test)]\nmod corral_tests {\n#[allow(unused_imports)]\nuse super::*;\n\n#[test]\nfn kills_it() {}\n}\n"
		if got != want {
			t.Errorf("a writer's allowed glob must be dropped with its allows:\n%s\nwant:\n%s", got, want)
		}
	}
	kept := "#[allow(dead_code)]\nfn helper() -> i32 { 1 }\n#[test]\nfn t() { assert_eq!(helper(), 1); }"
	if got := (rustPlugin{}).LandTest(kept, 0, ""); !strings.Contains(got, "\n#[allow(dead_code)]\nfn helper()") {
		t.Errorf("an allow on the writer's own item was dropped:\n%s", got)
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
	if strings.Count(merged, "mod corral_tests {") != 1 || strings.Count(merged, "#[allow(unused_imports)]\nuse super::super::*;") != 1 || strings.Count(merged, "use std::cmp::max;") != 1 {
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
