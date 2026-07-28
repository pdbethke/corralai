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
		test string // "" means NOT a candidate; see reason
		// reason is the expected exclusion reason when test == "". Defaults
		// to ReasonNoPairedTest (the zero value "") so every pre-existing
		// case in this table is unaffected; set explicitly to
		// ReasonAmbiguousTest for a collision case.
		reason string
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
			wants: []want{{path: "aisuite/agents/artifact_store.py", test: "tests/agents/test_artifact_store.py"}},
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
				{path: "pkgA/agents/x.py", test: "tests/pkgA/agents/test_x.py"},
				{path: "pkgB/agents/x.py", test: "tests/pkgB/agents/test_x.py"},
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
				{path: "aisuite/agents/artifact_store.py", test: "tests/agents/test_artifact_store.py"},
				{path: "aisuite/agents_extra/artifact_store.py", test: ""},
			},
		},
		{
			name: "python: flat tests/ tree",
			files: map[string]string{
				"toplevel.py":            "x = 1\n",
				"tests/test_toplevel.py": "def test_x(): pass\n",
			},
			wants: []want{{path: "toplevel.py", test: "tests/test_toplevel.py"}},
		},
		{
			name: "python: genuinely untested file stays unpaired",
			files: map[string]string{
				"aisuite/agents/lonely.py": "x = 1\n",
			},
			wants: []want{{path: "aisuite/agents/lonely.py", test: ""}},
		},
		{
			name: "ruby: lib/ vs test/ parallel tree",
			files: map[string]string{
				"lib/mypkg/foo.rb":       "class Foo; end\n",
				"test/mypkg/foo_test.rb": "# minitest\n",
			},
			wants: []want{{path: "lib/mypkg/foo.rb", test: "test/mypkg/foo_test.rb"}},
		},
		{
			name: "ruby: lib/ vs spec/ parallel tree",
			files: map[string]string{
				"lib/mypkg/bar.rb":       "class Bar; end\n",
				"spec/mypkg/bar_spec.rb": "# rspec\n",
			},
			wants: []want{{path: "lib/mypkg/bar.rb", test: "spec/mypkg/bar_spec.rb"}},
		},
		{
			name: "javascript: __tests__ folder beside the source",
			files: map[string]string{
				"src/calc.js":                "// calc\n",
				"src/__tests__/calc.test.js": "// test\n",
			},
			wants: []want{{path: "src/calc.js", test: "src/__tests__/calc.test.js"}},
		},
		{
			name: "javascript: parallel test/ tree, leading segment stripped",
			files: map[string]string{
				"src/pkg/sort.js":       "// sort\n",
				"test/pkg/sort.test.js": "// test\n",
			},
			wants: []want{{path: "src/pkg/sort.js", test: "test/pkg/sort.test.js"}},
		},
		{
			name: "typescript: parallel tests/ tree, leading segment stripped",
			files: map[string]string{
				"src/pkg/util.ts":        "// util\n",
				"tests/pkg/util.test.ts": "// test\n",
			},
			wants: []want{{path: "src/pkg/util.ts", test: "tests/pkg/util.test.ts"}},
		},
		{
			// The negative case candidate_pairing_test.go was missing: the
			// pkgA/pkgB case above ALSO supplies both full mirrors, so the
			// collision never actually materializes there — it passes for
			// the happy reason. Here NEITHER full mirror exists, so both
			// sources resolve the ambiguous stripped form at the SAME
			// specificity rank and neither can safely win.
			name: "python: ambiguous stripped form with no mirrors demotes BOTH claimants",
			files: map[string]string{
				"pkgA/agents/x.py":       "a = 1\n",
				"pkgB/agents/x.py":       "b = 1\n",
				"tests/agents/test_x.py": "def test_ambiguous(): pass\n",
			},
			wants: []want{
				{path: "pkgA/agents/x.py", reason: ReasonAmbiguousTest},
				{path: "pkgB/agents/x.py", reason: ReasonAmbiguousTest},
			},
		},
		{
			// The real collision that shipped unnoticed: flask's
			// tests/test_views.py was claimed by src/flask/views.py (2
			// segments deep — correct) AND by two example apps 3-4 segments
			// deep, whose only route to tests/test_views.py was the
			// depth-unbounded flat fallback. The depth bound (python.go)
			// removes the example apps' flat candidate entirely, so there is
			// no collision left for the ambiguous-test pass to catch here —
			// this pins that (a) alone fixes this exact shape.
			name: "python: flask tests/test_views.py — deep example apps excluded by depth bound",
			files: map[string]string{
				"src/flask/views.py":                      "class View: ...\n",
				"examples/celery/src/task_app/views.py":   "def index(): ...\n",
				"examples/javascript/js_example/views.py": "def add(): ...\n",
				"tests/test_views.py":                     "def test_view(): pass\n",
			},
			wants: []want{
				{path: "src/flask/views.py", test: "tests/test_views.py"},
				{path: "examples/celery/src/task_app/views.py", reason: ReasonNoPairedTest},
				{path: "examples/javascript/js_example/views.py", reason: ReasonNoPairedTest},
			},
		},
		{
			// flask's second observed collision: tests/test_blueprints.py
			// claimed by both src/flask/blueprints.py (2 segments — flat
			// eligible) and src/flask/sansio/blueprints.py (3 segments —
			// excluded by the depth bound before it can even reach the
			// ambiguous-test pass).
			name: "python: flask tests/test_blueprints.py — sansio/ excluded by depth bound",
			files: map[string]string{
				"src/flask/blueprints.py":        "class Blueprint: ...\n",
				"src/flask/sansio/blueprints.py": "class BlueprintSetupState: ...\n",
				"tests/test_blueprints.py":       "def test_bp(): pass\n",
			},
			wants: []want{
				{path: "src/flask/blueprints.py", test: "tests/test_blueprints.py"},
				{path: "src/flask/sansio/blueprints.py", reason: ReasonNoPairedTest},
			},
		},
		{
			// requests' collision: tests/test_utils.py claimed by BOTH
			// src/requests/utils.py (flat form, rank 4 — depth 2, so still
			// eligible) and tests/utils.py itself. tests/utils.py has no
			// test-name marker (pre-existing on main, not introduced by this
			// change — see the report), so it is misclassified as SOURCE
			// rather than test, and its own sibling convention resolves to
			// tests/test_utils.py at rank 0 — strictly better than
			// src/requests/utils.py's rank-4 flat match. Per the
			// strictly-better-rank rule, tests/utils.py (the pre-existing,
			// unrelated pairing) keeps tests/test_utils.py, and
			// src/requests/utils.py — which no longer gets to silently
			// co-claim it — is demoted to ambiguous-test instead of being
			// wrongly graded against a test suite that was never meant for
			// it.
			name: "python: requests tests/test_utils.py — pre-existing tests/utils.py misclassification wins the strict-rank tiebreak",
			files: map[string]string{
				"src/requests/utils.py": "def x(): ...\n",
				"tests/utils.py":        "def y(): ...\n",
				"tests/test_utils.py":   "def test_x(): pass\n",
			},
			wants: []want{
				{path: "src/requests/utils.py", reason: ReasonAmbiguousTest},
				{path: "tests/utils.py", test: "tests/test_utils.py"},
			},
		},
		{
			// Round-3 regression: rank must denote CONVENTION KIND, not
			// position in the deduped candidate list. Before the fix, a
			// zero-directory-evidence match (the flat form, or a degenerate
			// mirror/stripped form that collapses onto it) got a DIFFERENT
			// numeric rank depending on how many more-specific forms
			// happened to also collapse for that particular source's depth —
			// so two equally-uninformative matches at different depths never
			// tied, and demote-all never fired. examples/views.py (1
			// directory segment, so its "stripped" form degenerates to the
			// flat string at index 3) and src/flask/views.py (2 segments, a
			// genuine flat match at index 4) both resolve to
			// tests/test_views.py with ZERO real directory evidence either
			// way — they must tie and both demote, not silently pick
			// whichever source happened to collapse fewer forms.
			name: "python: same-evidence flat matches at different depths must tie (examples/ vs src/flask/)",
			files: map[string]string{
				"examples/views.py":   "def index(): ...\n",
				"src/flask/views.py":  "class View: ...\n",
				"tests/test_views.py": "def test_view(): pass\n",
			},
			wants: []want{
				{path: "examples/views.py", reason: ReasonAmbiguousTest},
				{path: "src/flask/views.py", reason: ReasonAmbiguousTest},
			},
		},
		{
			// Same shape, different names: docs/conf.py (1 segment) vs
			// mypkg/sub/conf.py (2 segments) both resolve to
			// tests/test_conf.py with no real directory evidence.
			name: "python: same-evidence flat matches at different depths must tie (docs/ vs mypkg/sub/)",
			files: map[string]string{
				"docs/conf.py":       "html_theme = 'x'\n",
				"mypkg/sub/conf.py":  "DEBUG = False\n",
				"tests/test_conf.py": "def test_conf(): pass\n",
			},
			wants: []want{
				{path: "docs/conf.py", reason: ReasonAmbiguousTest},
				{path: "mypkg/sub/conf.py", reason: ReasonAmbiguousTest},
			},
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
					wantReason := w.reason
					if wantReason == "" {
						wantReason = ReasonNoPairedTest
					}
					if _, ok := byPath[w.path]; ok {
						t.Errorf("%s: became a candidate, want %q", w.path, wantReason)
					}
					if reasons[w.path] != wantReason {
						t.Errorf("%s: reason = %q, want %q", w.path, reasons[w.path], wantReason)
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
