// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"fmt"
	"sort"
	"testing"

	"github.com/pdbethke/corralai/internal/reposcan"
)

// surfaceGoals gives every candidate the same goal, so a key difference in
// these tests can only come from the test surface.
type surfaceGoals struct{}

func (surfaceGoals) GoalFor(reposcan.Candidate) (reposcan.Goal, bool, error) {
	return reposcan.Goal{Text: "g", Provenance: "file"}, true, nil
}

// keysFor emits jobs against a tree and returns path->cache key, wiring the
// two production decisions (gradesFileScoped, testSurfacePaths) exactly as
// runCertifyRepo does.
func keysFor(t *testing.T, root string, cands, selected []reposcan.Candidate, excl []reposcan.Exclusion, argv []string, scopeTests bool) map[string]string {
	t.Helper()
	jobs, _, err := reposcan.EmitJobs(reposcan.EmitConfig{
		Owner: "o", Repo: "r", Commit: "c", Root: root,
		FileScopedTests:  gradesFileScoped(argv, scopeTests, selected, cands, excl),
		TestSurfacePaths: testSurfacePaths(cands, excl),
	}, selected, surfaceGoals{})
	if err != nil {
		t.Fatalf("EmitJobs: %v", err)
	}
	out := map[string]string{}
	for _, j := range jobs {
		out[j.Path] = j.CacheKey
	}
	return out
}

// FINDING 1. `-- pytest tests/test_a.py` names ONE candidate's test, but
// testCmd hands that same argv to EVERY job — so b.py is graded by
// tests/test_a.py too. Calling the scan file-scoped keys b.py on
// tests/test_b.py, a file that never runs, and leaves the file that actually
// grades it out of the key entirely.
func TestGradesFileScopedNeedsEverySelectedCandidatesTest(t *testing.T) {
	sel := []reposcan.Candidate{
		{Path: "a.py", TestPath: "tests/test_a.py", Lang: "python"},
		{Path: "b.py", TestPath: "tests/test_b.py", Lang: "python"},
	}
	if gradesFileScoped([]string{"pytest", "-q", "tests/test_a.py"}, false, sel, sel, nil) {
		t.Fatal("an argv naming only ONE of two selected candidates' tests was called file-scoped — b.py would key on a test that never runs, and the test that really grades it would appear in no key at all")
	}
	// The genuine single-file case must still be file-scoped: over-invalidating
	// there throws away every verdict in the repo for a change that cannot
	// reach them.
	one := sel[:1]
	if !gradesFileScoped([]string{"pytest", "-q", "tests/test_a.py"}, false, one, sel, nil) {
		t.Fatal("a one-candidate scan whose only test is named in the argv is genuinely file-scoped")
	}
	if !gradesFileScoped([]string{"pytest", "tests/test_a.py::test_x"}, false, one, sel, nil) {
		t.Fatal("a pytest node id names the same file")
	}
	if gradesFileScoped([]string{"pytest", "-q", "tests/unit"}, false, one, sel, nil) {
		t.Fatal("an argv naming a DIRECTORY is a subset of the suite, not one file")
	}
	if gradesFileScoped([]string{"pytest", "-q"}, false, one, sel, nil) {
		t.Fatal("an argv naming no test file at all runs the whole suite")
	}
}

// The same defect at the level that matters: the KEY. Weaken tests/test_a.py —
// which grades b.py too, because every job runs the same argv — and b.py's key
// must move. Against the old explicit-argv rule it does not.
func TestKeyMovesWhenTheSharedArgvTestIsWeakened(t *testing.T) {
	cands := []reposcan.Candidate{
		{Path: "a.py", TestPath: "tests/test_a.py", Lang: "python"},
		{Path: "b.py", TestPath: "tests/test_b.py", Lang: "python"},
	}
	argv := []string{"pytest", "-q", "tests/test_a.py"}
	mk := func(sharedTest string) string {
		root := t.TempDir()
		writeTree(t, root, map[string]string{
			"a.py": "def a():\n    return 1\n", "b.py": "def b():\n    return 2\n",
			"tests/test_a.py": sharedTest, "tests/test_b.py": "def test_b():\n    assert True\n",
		})
		return root
	}
	strong := keysFor(t, mk("def test_a():\n    assert a() == 1\n"), cands, cands, nil, argv, false)
	weak := keysFor(t, mk("def test_a():\n    pass\n"), cands, cands, nil, argv, false)

	if strong["b.py"] == weak["b.py"] {
		t.Fatal("weakening tests/test_a.py left b.py's key unchanged — that command grades b.py, so the cache would serve b.py's old kill rate for a suite that got worse")
	}
}

// FINDING 2. tests/conftest.py is nobody's paired test and matches no
// test-filename marker, so Enumerate files it as no-paired-test and it never
// reached the surface. It is the single most common Python shared-fixture
// file: weaken a fixture in it and every file's key used to stay put.
func TestKeyMovesWhenASharedFixtureFileIsWeakened(t *testing.T) {
	mk := func(conftest string) (string, []reposcan.Candidate, []reposcan.Exclusion) {
		root := t.TempDir()
		writeTree(t, root, map[string]string{
			"pkg/a.py":          "def a():\n    return 1\n",
			"tests/test_a.py":   "def test_a():\n    assert True\n",
			"tests/conftest.py": conftest,
			"tests/helpers.py":  "X = 1\n",
			"tests/golden.json": "{}\n",
		})
		cands, excl, err := reposcan.Enumerate(root)
		if err != nil {
			t.Fatal(err)
		}
		return root, cands, excl
	}
	rootA, candsA, exclA := mk("import pytest\n\n@pytest.fixture\ndef db():\n    return strict()\n")
	rootB, candsB, exclB := mk("import pytest\n\n@pytest.fixture\ndef db():\n    return lenient()\n")

	strong := keysFor(t, rootA, candsA, candsA, exclA, nil, false)
	weak := keysFor(t, rootB, candsB, candsB, exclB, nil, false)

	if len(strong) == 0 {
		t.Fatal("no jobs emitted — the fixture tree paired nothing")
	}
	for p, k := range strong {
		if weak[p] == k {
			t.Fatalf("weakening tests/conftest.py left %s's key unchanged — the suite it is graded by genuinely got worse and the ledger would repeat the old kill rate", p)
		}
	}
}

// Every file beside a recognized test is part of the surface: fixture modules,
// setup files and golden data all change what the suite measures.
func TestTestSurfacePathsCoversTheWholeTestDirectory(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"pkg/a.py":           "def a():\n    return 1\n",
		"tests/test_a.py":    "def test_a():\n    assert True\n",
		"tests/conftest.py":  "import pytest\n",
		"tests/helpers.py":   "X = 1\n",
		"tests/golden.json":  "{}\n",
		"src/jest.setup.js":  "module.exports = {}\n",
		"src/a.test.js":      "test('a', () => {})\n",
		"docs/notes.md":      "not a test\n",
		"docs/whatever.json": "{}\n",
	})
	cands, excl, err := reposcan.Enumerate(root)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, p := range testSurfacePaths(cands, excl) {
		got[p] = true
	}
	for _, want := range []string{
		"tests/test_a.py", "tests/conftest.py", "tests/helpers.py", "tests/golden.json",
		"src/a.test.js", "src/jest.setup.js",
	} {
		if !got[want] {
			t.Errorf("%s is missing from the test surface — changing it changes what the suite measures", want)
		}
	}
	for _, nope := range []string{"docs/notes.md", "docs/whatever.json"} {
		if got[nope] {
			t.Errorf("%s is in the test surface but no test file lives beside it", nope)
		}
	}
}

// The surface rule must NEVER become an audit-selection rule. Widening what
// gets DIGESTED is cheap (a needless miss costs money); widening what gets
// AUDITED changes the verdict itself. This pins Enumerate's classification of
// exactly the files the surface widening reaches.
func TestSurfaceWideningDoesNotChangeCandidateClassification(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"pkg/a.py":          "def a():\n    return 1\n",
		"tests/test_a.py":   "def test_a():\n    assert True\n",
		"tests/conftest.py": "import pytest\n",
		"tests/helpers.py":  "X = 1\n",
		"tests/fixtures.py": "Y = 2\n",
		"tests/golden.json": "{}\n",
		"src/a.test.js":     "test('a', () => {})\n",
		"src/jest.setup.js": "module.exports = {}\n",
	})
	cands, excl, err := reposcan.Enumerate(root)
	if err != nil {
		t.Fatal(err)
	}
	var gotCands []string
	for _, c := range cands {
		gotCands = append(gotCands, fmt.Sprintf("%s->%s", c.Path, c.TestPath))
	}
	sort.Strings(gotCands)
	wantCands := []string{"pkg/a.py->tests/test_a.py"}
	if len(gotCands) != len(wantCands) {
		t.Fatalf("candidates = %v, want %v", gotCands, wantCands)
	}
	for i := range wantCands {
		if gotCands[i] != wantCands[i] {
			t.Fatalf("candidates = %v, want %v", gotCands, wantCands)
		}
	}
	wantReason := map[string]string{
		"tests/conftest.py": reposcan.ReasonNoPairedTest,
		"tests/helpers.py":  reposcan.ReasonNoPairedTest,
		"tests/fixtures.py": reposcan.ReasonNoPairedTest,
		"tests/golden.json": reposcan.ReasonNoLanguage,
		"src/jest.setup.js": reposcan.ReasonNoPairedTest,
		"tests/test_a.py":   reposcan.ReasonIsTest,
		"src/a.test.js":     reposcan.ReasonIsTest,
	}
	gotReason := map[string]string{}
	for _, e := range excl {
		gotReason[e.Path] = e.Reason
	}
	for p, want := range wantReason {
		if gotReason[p] != want {
			t.Errorf("%s classified %q, want %q — the test-surface rule must not change what gets audited", p, gotReason[p], want)
		}
	}
}

// RESIDUAL 1. The coverage rule ("every selected candidate's test is named")
// is necessary but not sufficient. `-- pytest tests/test_a.py tests/test_b.py`
// with only a.py selected passes coverage, yet tests/test_b.py runs in that
// same command and its assertions kill a.py's mutants. Keying a.py on
// tests/test_a.py alone leaves the other grading file out of the key entirely
// — and auditConfigKey digests the argv TEXT, which does not move when a named
// file's CONTENTS change.
func TestKeyMovesWhenAnUnselectedNamedTestIsWeakened(t *testing.T) {
	cands := []reposcan.Candidate{
		{Path: "a.py", TestPath: "tests/test_a.py", Lang: "python"},
		{Path: "b.py", TestPath: "tests/test_b.py", Lang: "python"},
	}
	selected := cands[:1] // as --top 1 or --diff-base would leave it
	argv := []string{"pytest", "-q", "tests/test_a.py", "tests/test_b.py"}
	mk := func(otherTest string) string {
		root := t.TempDir()
		writeTree(t, root, map[string]string{
			"a.py": "def a():\n    return 1\n", "b.py": "def b():\n    return 2\n",
			"tests/test_a.py": "def test_a():\n    assert True\n",
			"tests/test_b.py": otherTest,
		})
		return root
	}
	strong := keysFor(t, mk("def test_b():\n    assert b() == 2\n"), cands, selected, nil, argv, false)
	weak := keysFor(t, mk("def test_b():\n    pass\n"), cands, selected, nil, argv, false)

	if strong["a.py"] == weak["a.py"] {
		t.Fatal("weakening tests/test_b.py left a.py's key unchanged — that file runs in the same command and grades a.py's mutants, so the cache would serve a.py's old kill rate for a suite that got worse")
	}
}

// The exclusivity rule at unit level, plus the tokens that must NOT trip it:
// flags and flag values are not test paths, so they are simply ignored.
func TestGradesFileScopedRejectsATestNamedOutsideTheSelectedSet(t *testing.T) {
	cands := []reposcan.Candidate{
		{Path: "a.py", TestPath: "tests/test_a.py", Lang: "python"},
		{Path: "b.py", TestPath: "tests/test_b.py", Lang: "python"},
	}
	one := cands[:1]
	if gradesFileScoped([]string{"pytest", "tests/test_a.py", "tests/test_b.py"}, false, one, cands, nil) {
		t.Fatal("an argv naming an UNSELECTED candidate's test was called file-scoped — that test runs in the same command and grades a.py, so weakening it would leave a.py's key unmoved")
	}
	// A test file that is nobody's pair (Enumerate calls it `is-test`) counts
	// too: it runs and it grades.
	excl := []reposcan.Exclusion{{Path: "tests/test_extra.py", Reason: reposcan.ReasonIsTest}}
	if gradesFileScoped([]string{"pytest", "tests/test_a.py", "tests/test_extra.py"}, false, one, one, excl) {
		t.Fatal("an argv naming an is-test file outside the selected set was called file-scoped")
	}
	// Flags and flag values are not test paths and must not change the answer.
	if !gradesFileScoped([]string{"pytest", "-q", "-k", "not slow", "tests/test_a.py"}, false, one, cands, nil) {
		t.Fatal("flag tokens and their values are not test paths — they must not force whole-suite")
	}
}
