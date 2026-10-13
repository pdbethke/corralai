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
