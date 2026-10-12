//go:build cgo

// SPDX-License-Identifier: Elastic-2.0

package repoindex

import (
	"os"
	"reflect"
	"testing"

	"github.com/pdbethke/corralai/internal/pairing"
)

func rustFixture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/langspec/rust.rs")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestRustModFormsAreAllRead pins the three `mod` forms against the fixture:
// the warp file's #[cfg(test)] #[path] declaration (a real file), then the
// constructed inline and bare forms. Order is source order.
func TestRustModFormsAreAllRead(t *testing.T) {
	got := TestModules("rust", []byte(rustFixture(t)))
	want := []pairing.TestModule{
		{Name: "tests", Inline: false, PathAttr: "focus_env_tests.rs", CfgTest: true},
		{Name: "tests", Inline: true, CfgTest: true},
		{Name: "tests", Inline: false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TestModules = %+v\nwant %+v", got, want)
	}
}

func TestRustSourceWithNoTestModuleReportsNone(t *testing.T) {
	src := "mod helpers;\npub mod util { pub fn f() {} }\npub fn g() {}\n"
	if got := TestModules("rust", []byte(src)); len(got) != 0 {
		t.Fatalf("a source with no test module reported %+v; it would pair to itself", got)
	}
}

// A language whose row has no modules reader answers nil, never a guess.
func TestModulesForALanguageWithoutAReaderIsNil(t *testing.T) {
	if got := TestModules("python", []byte("mod tests;")); got != nil {
		t.Fatalf("python TestModules = %+v, want nil", got)
	}
	if got := TestModules("cobol", []byte("x")); got != nil {
		t.Fatalf("unknown language TestModules = %+v, want nil", got)
	}
}

func TestRustCfgTestWithoutPathIsRead(t *testing.T) {
	got := TestModules("rust", []byte("#[cfg(test)]\nmod tests;\n"))
	want := []pairing.TestModule{{Name: "tests", CfgTest: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func sigByIdentity(sigs []Signature) map[string]Signature {
	m := map[string]Signature{}
	for _, s := range sigs {
		m[s.Receiver+"."+s.Name] = s
	}
	return m
}

// TestRustSameNamedFnInTwoImplsAreTwoSymbols is the house rider from the PHP
// review: advpool.symbolIdentity is Receiver + "." + Name, so two impl blocks
// each defining `area` must not collapse into one.
func TestRustSameNamedFnInTwoImplsAreTwoSymbols(t *testing.T) {
	sigs, err := ExtractSignatures(rustFixture(t), "rust")
	if err != nil {
		t.Fatal(err)
	}
	by := sigByIdentity(sigs)
	sq, c := by["<Square as Shape>.area"], by["<Circle as Shape>.area"]
	if sq.Name == "" || c.Name == "" {
		t.Fatalf("trait-impl area symbols missing from %v", by)
	}
	if sq.Line == c.Line {
		t.Fatalf("both impls' area resolved to one symbol at line %d", sq.Line)
	}
	n := 0
	for _, s := range sigs {
		if s.Name == "area" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("%d symbols named area, want 2 (the bodiless trait declaration must be skipped)", n)
	}
}

// TestRustSameTypeImplsAreDistinctIdentities: Display/Debug fmt, From<A>/From<B>
// from, and an inherent new beside a trait new all share one TYPE and one fn
// name; symbolIdentity (Receiver + "." + Name) must still tell them apart.
func TestRustSameTypeImplsAreDistinctIdentities(t *testing.T) {
	sigs, err := ExtractSignatures(rustFixture(t), "rust")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, s := range sigs {
		id := s.Receiver + "." + s.Name
		if seen[id] {
			t.Errorf("two symbols share identity %q", id)
		}
		seen[id] = true
	}
	for _, id := range []string{
		"<Square as fmt::Display>.fmt", "<Square as fmt::Debug>.fmt",
		"<Circle as From<i32>>.from", "<Circle as From<f64>>.from",
		"Square.new", "<Square as Make>.new",
	} {
		if !seen[id] {
			t.Errorf("missing identity %q; have %v", id, seen)
		}
	}
}

func TestRustExtractsTheCallableSurface(t *testing.T) {
	sigs, err := ExtractSignatures(rustFixture(t), "rust")
	if err != nil {
		t.Fatal(err)
	}
	by := sigByIdentity(sigs)
	// free fn, pub(crate) free fn, trait default method (Receiver = the trait).
	for _, id := range []string{".session_focus_url", ".add_session_focus_env_vars", "ReadBytesExt.read_u8", "ReadBytesExt.read_u16_into", "Shape.name", ".classify", ".scale"} {
		if _, ok := by[id]; !ok {
			t.Errorf("missing symbol %q", id)
		}
	}
	// the inline test module's #[test] fn is not production code.
	if _, ok := by[".zero"]; ok {
		t.Error("a fn inside #[cfg(test)] mod tests was extracted")
	}
	if k := by["<Square as Shape>.area"].Kind; k != "method" {
		t.Errorf("Square.area kind = %q, want method", k)
	}
	if k := by[".classify"].Kind; k != "func" {
		t.Errorf("classify kind = %q, want func", k)
	}
	if !by[".session_focus_url"].Exported || by["<Square as Shape>.area"].Exported != true {
		t.Errorf("exported: pub(crate) free fn and a trait-impl method are both visible outside the file")
	}
	if got := by[".classify"].Results; len(got) != 1 || got[0] != "&'static str" {
		t.Errorf("classify results = %v", got)
	}
}

// TestRustMatchIsArmCountedAndClosuresBelongToTheirFn: classify has three
// arms → complexity 4 (as PHP's switch counts one per case); scale's nested
// closures are part of scale and its `if` inside a closure is counted there.
func TestRustMatchIsArmCountedAndClosuresBelongToTheirFn(t *testing.T) {
	sigs, err := ExtractSignatures(rustFixture(t), "rust")
	if err != nil {
		t.Fatal(err)
	}
	by := sigByIdentity(sigs)
	if c := by[".classify"].Complexity; c != 4 {
		t.Errorf("classify complexity = %d, want 4 (1 + 3 arms)", c)
	}
	if c := by[".scale"].Complexity; c != 2 {
		t.Errorf("scale complexity = %d, want 2 (1 + the if inside the inner closure)", c)
	}
	for _, s := range sigs {
		if s.Name == "" || s.Name == "closure" {
			t.Errorf("a closure became a symbol: %+v", s)
		}
	}
}

func TestRustBoolOpsAndLoopsCount(t *testing.T) {
	src := "fn f(a: bool, b: bool, v: &[i32]) -> i32 {\n if a && b || !a { return 1; }\n while a {}\n for _x in v {}\n 0\n}\n"
	sigs, err := ExtractSignatures(src, "rust")
	if err != nil || len(sigs) != 1 {
		t.Fatalf("sigs=%v err=%v", sigs, err)
	}
	// if, &&, ||, while, for
	if sigs[0].Complexity != 6 {
		t.Errorf("complexity = %d, want 6", sigs[0].Complexity)
	}
}
