// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"testing"
)

// TestEnumeratePairingConventions is a table per language mapping a repo
// layout to the expected pairing (or to no-pairing). Each case builds a real
// temp-dir repo via writeTree and asserts what Enumerate resolves.
//
// The "aisuite shape" case is the real layout measured against
// github.com/andrewyng/aisuite (aisuite/agents/artifact_store.py paired with
// tests/agents/test_artifact_store.py) — the motivating case for this whole
// change: before it, corral's Go-shaped sibling-only TestPath found ZERO of
// aisuite's 34 parallel-tree tests.
func TestEnumeratePairingConventions(t *testing.T) {
	type want struct {
		path string
		test string // "" means expect ReasonNoPairedTest, not a candidate
	}
	cases := []struct {
		name  string
		files map[string]string
		wants []want
	}{
		{
			name: "python: aisuite parallel-tree shape (leading segment stripped)",
			files: map[string]string{
				"aisuite/agents/artifact_store.py":    "x = 1\n",
				"tests/agents/test_artifact_store.py": "def test_x(): pass\n",
			},
			wants: []want{{"aisuite/agents/artifact_store.py", "tests/agents/test_artifact_store.py"}},
		},
		{
			name: "python: full-directory-mirror preferred over ambiguous stripped form",
			files: map[string]string{
				"pkgA/agents/x.py":            "a = 1\n",
				"pkgB/agents/x.py":            "b = 1\n",
				"tests/pkgA/agents/test_x.py": "def test_a(): pass\n",         // pkgA's own full mirror
				"tests/pkgB/agents/test_x.py": "def test_b(): pass\n",         // pkgB's own full mirror
				"tests/agents/test_x.py":      "def test_ambiguous(): pass\n", // would match BOTH under the stripped form
			},
			wants: []want{
				{"pkgA/agents/x.py", "tests/pkgA/agents/test_x.py"},
				{"pkgB/agents/x.py", "tests/pkgB/agents/test_x.py"},
			},
		},
		{
			name: "python: sibling-directory name is not a substring match",
			files: map[string]string{
				"aisuite/agents/artifact_store.py":       "x = 1\n",
				"tests/agents/test_artifact_store.py":    "def test_x(): pass\n",
				"aisuite/agents_extra/artifact_store.py": "y = 1\n", // same basename, different (similarly-named) dir — must NOT pair
			},
			wants: []want{
				{"aisuite/agents/artifact_store.py", "tests/agents/test_artifact_store.py"},
				{"aisuite/agents_extra/artifact_store.py", ""},
			},
		},
		{
			name: "python: flat tests/ tree",
			files: map[string]string{
				"toplevel.py":            "x = 1\n",
				"tests/test_toplevel.py": "def test_x(): pass\n",
			},
			wants: []want{{"toplevel.py", "tests/test_toplevel.py"}},
		},
		{
			name: "python: genuinely untested file stays unpaired",
			files: map[string]string{
				"aisuite/agents/lonely.py": "x = 1\n",
			},
			wants: []want{{"aisuite/agents/lonely.py", ""}},
		},
		{
			name: "ruby: lib/ vs test/ parallel tree",
			files: map[string]string{
				"lib/mypkg/foo.rb":       "class Foo; end\n",
				"test/mypkg/foo_test.rb": "# minitest\n",
			},
			wants: []want{{"lib/mypkg/foo.rb", "test/mypkg/foo_test.rb"}},
		},
		{
			name: "ruby: lib/ vs spec/ parallel tree",
			files: map[string]string{
				"lib/mypkg/bar.rb":       "class Bar; end\n",
				"spec/mypkg/bar_spec.rb": "# rspec\n",
			},
			wants: []want{{"lib/mypkg/bar.rb", "spec/mypkg/bar_spec.rb"}},
		},
		{
			name: "javascript: __tests__ folder beside the source",
			files: map[string]string{
				"src/calc.js":                "// calc\n",
				"src/__tests__/calc.test.js": "// test\n",
			},
			wants: []want{{"src/calc.js", "src/__tests__/calc.test.js"}},
		},
		{
			name: "javascript: parallel test/ tree, leading segment stripped",
			files: map[string]string{
				"src/pkg/sort.js":       "// sort\n",
				"test/pkg/sort.test.js": "// test\n",
			},
			wants: []want{{"src/pkg/sort.js", "test/pkg/sort.test.js"}},
		},
		{
			name: "typescript: parallel tests/ tree, leading segment stripped",
			files: map[string]string{
				"src/pkg/util.ts":        "// util\n",
				"tests/pkg/util.test.ts": "// test\n",
			},
			wants: []want{{"src/pkg/util.ts", "tests/pkg/util.test.ts"}},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := writeTree(t, c.files)
			cands, excl, err := Enumerate(root)
			if err != nil {
				t.Fatalf("Enumerate: %v", err)
			}
			byPath := map[string]Candidate{}
			for _, cd := range cands {
				byPath[cd.Path] = cd
			}
			reasons := map[string]string{}
			for _, e := range excl {
				reasons[e.Path] = e.Reason
			}
			for _, w := range c.wants {
				if w.test == "" {
					if _, ok := byPath[w.path]; ok {
						t.Errorf("%s: became a candidate, want no-paired-test", w.path)
					}
					if reasons[w.path] != ReasonNoPairedTest {
						t.Errorf("%s: reason = %q, want %q", w.path, reasons[w.path], ReasonNoPairedTest)
					}
					continue
				}
				cd, ok := byPath[w.path]
				if !ok {
					t.Errorf("%s: not a candidate (reason=%q), want paired with %s", w.path, reasons[w.path], w.test)
					continue
				}
				if cd.TestPath != w.test {
					t.Errorf("%s: TestPath = %q, want %q", w.path, cd.TestPath, w.test)
				}
			}
		})
	}
}

// TestIsTestFileParallelTreeShapes checks the structural test-file detector
// directly against paths whose "test path" shape is now a parallel tree, not
// a sibling — a test file audited as if it were source is a garbage-in
// result, so this is verified explicitly rather than only via Enumerate's
// end-to-end behavior.
func TestIsTestFileParallelTreeShapes(t *testing.T) {
	root := writeTree(t, map[string]string{
		"aisuite/agents/artifact_store.py":    "x = 1\n",
		"tests/agents/test_artifact_store.py": "def test_x(): pass\n",
		"lib/mypkg/foo.rb":                    "class Foo; end\n",
		"test/mypkg/foo_test.rb":              "# minitest\n",
		"lib/mypkg/bar.rb":                    "class Bar; end\n",
		"spec/mypkg/bar_spec.rb":              "# rspec\n",
		"src/pkg/sort.js":                     "// sort\n",
		"test/pkg/sort.test.js":               "// test\n",
	})
	_, excl, err := Enumerate(root)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	reasons := map[string]string{}
	for _, e := range excl {
		reasons[e.Path] = e.Reason
	}
	testFiles := []string{
		"tests/agents/test_artifact_store.py",
		"test/mypkg/foo_test.rb",
		"spec/mypkg/bar_spec.rb",
		"test/pkg/sort.test.js",
	}
	for _, tf := range testFiles {
		if reasons[tf] != ReasonIsTest {
			t.Errorf("%s: reason = %q, want %q — a parallel-tree test file must not become an audit subject", tf, reasons[tf], ReasonIsTest)
		}
	}
}
