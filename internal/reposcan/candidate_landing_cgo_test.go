// SPDX-License-Identifier: Elastic-2.0

//go:build cgo

package reposcan

import (
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
)

// TestEnumerateAdmitsADeclaredRustSource is 7a at the scan's door: a Rust
// source whose own declaration loads a separate test file is a candidate,
// paired to that file — the file its authored test will land in — while an
// inline-tested one and one whose declaration leaves the repository are still
// excluded as test-landing-not-built. The landing and the pairing read the
// same walk, so the candidate's TestPath IS its landing.
func TestEnumerateAdmitsADeclaredRustSource(t *testing.T) {
	root := writeTree(t, map[string]string{
		"src/foo.rs":       "fn private_double(x: i32) -> i32 { x * 2 }\n\n#[cfg(test)]\n#[path = \"foo_tests.rs\"]\nmod tests;\n",
		"src/foo_tests.rs": "use super::*;\n\n#[test]\nfn t() { assert_eq!(private_double(1), 2); }\n",
		"src/bar.rs":       "pub fn b() {}\n\n#[cfg(test)]\nmod tests {\n    #[test]\n    fn t() {}\n}\n",
		"src/out.rs":       "pub fn o() {}\n\n#[cfg(test)]\n#[path = \"../../escape_tests.rs\"]\nmod tests;\n",
	})
	cands, excl, err := Enumerate(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].Path != "src/foo.rs" || cands[0].TestPath != "src/foo_tests.rs" {
		t.Fatalf("candidates = %+v, want only src/foo.rs paired to src/foo_tests.rs", cands)
	}
	p, _ := lang.ByName("rust")
	l, err := lang.TestLanding("src/foo.rs", DiskSources(p, root))
	if err != nil || l.Path != cands[0].TestPath {
		t.Fatalf("landing %+v (err %v) is not the pairing %s", l, err, cands[0].TestPath)
	}
	reasons := map[string]string{}
	for _, e := range excl {
		reasons[e.Path] = e.Reason
	}
	for _, p := range []string{"src/bar.rs", "src/out.rs"} {
		if reasons[p] != ReasonTestLandingNotBuilt {
			t.Errorf("%s excluded as %q, want %q", p, reasons[p], ReasonTestLandingNotBuilt)
		}
	}
}

// TestAnUnreadableDeclarationIsAnExclusionNeverAnError stands in for a no-cgo
// build (this package cannot build without cgo — its advpool import pulls in
// duckdb — so readsTestModules is swapped instead): Enumerate over a tree
// with a declared Rust file completes and excludes it with the unreadable
// reason, while the Python file beside it is still a candidate; FindTest and
// ResolutionPaths answer a miss that says why, never an error.
func TestAnUnreadableDeclarationIsAnExclusionNeverAnError(t *testing.T) {
	orig := readsTestModules
	readsTestModules = func(string) bool { return false }
	t.Cleanup(func() { readsTestModules = orig })

	root := writeTree(t, map[string]string{
		"calc.py":          "def add(a, b):\n    return a + b\n",
		"test_calc.py":     "from calc import add\n\ndef test_add():\n    assert add(1, 2) == 3\n",
		"src/foo.rs":       "fn f() {}\n\n#[cfg(test)]\n#[path = \"foo_tests.rs\"]\nmod tests;\n",
		"src/foo_tests.rs": "#[test]\nfn t() {}\n",
	})
	cands, excl, err := Enumerate(root)
	if err != nil {
		t.Fatalf("Enumerate aborted: %v", err)
	}
	if len(cands) != 1 || cands[0].Path != "calc.py" {
		t.Errorf("candidates = %+v, want only calc.py", cands)
	}
	got := ""
	for _, e := range excl {
		if e.Path == "src/foo.rs" {
			got = e.Reason
		}
	}
	if got != ReasonDeclarationsUnreadable || !IsLandingRefusal(got) || UnauditableChangedSource(got) {
		t.Errorf("src/foo.rs excluded as %q, want %q (a landing refusal that does not fail the gate)", got, ReasonDeclarationsUnreadable)
	}
	p, _ := lang.ByName("rust")
	res, err := FindTest(p, root, "src/foo.rs")
	if err != nil || res.Found || !res.DeclarationsUnreadable {
		t.Errorf("FindTest = %+v, err %v; want a miss saying the declarations were unreadable", res, err)
	}
	if _, err := ResolutionPaths(p, root, "src/foo.rs"); err != nil {
		t.Errorf("ResolutionPaths errored: %v", err)
	}
}
