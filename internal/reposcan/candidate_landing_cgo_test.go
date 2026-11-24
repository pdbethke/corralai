// SPDX-License-Identifier: Elastic-2.0

//go:build cgo

package reposcan

import (
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
)

// TestEnumerateAdmitsADeclaredRustSource is 7a and 7b at the scan's door: a
// Rust source whose own declaration loads a separate test file is a
// candidate, paired to that file — the file its authored test will land in —
// and an inline-tested one is a candidate paired to itself, where its
// authored test lands; one whose declaration leaves the repository is still
// excluded as test-landing-not-built. The landing and the pairing read the
// same walk, so each candidate's TestPath IS its landing.
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
	want := map[string]string{"src/bar.rs": "src/bar.rs", "src/foo.rs": "src/foo_tests.rs"}
	if len(cands) != len(want) {
		t.Fatalf("candidates = %+v, want %v", cands, want)
	}
	p, _ := lang.ByName("rust")
	for _, c := range cands {
		if want[c.Path] != c.TestPath {
			t.Errorf("%s paired to %q, want %q", c.Path, c.TestPath, want[c.Path])
		}
		l, err := lang.TestLanding(c.Path, DiskSources(p, root))
		if err != nil || l.Path != c.TestPath {
			t.Errorf("%s: landing %+v (err %v) is not the pairing %s", c.Path, l, err, c.TestPath)
		}
	}
	reasons := map[string]string{}
	for _, e := range excl {
		reasons[e.Path] = e.Reason
	}
	if reasons["src/out.rs"] != ReasonTestLandingNotBuilt {
		t.Errorf("src/out.rs excluded as %q, want %q", reasons["src/out.rs"], ReasonTestLandingNotBuilt)
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
