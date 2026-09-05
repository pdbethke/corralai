// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"fmt"
	"os"
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

// surfacePathsIn calls testSurfacePaths through a root opened on the tree, the
// way runCertifyRepo does — reads stay confined to the repository.
func surfacePathsIn(t *testing.T, root string, cands []reposcan.Candidate, excl []reposcan.Exclusion) []string {
	t.Helper()
	r, err := os.OpenRoot(root)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	defer func() { _ = r.Close() }()
	return testSurfacePaths(r, cands, excl)
}

// keysFor emits jobs against a tree and returns path->cache key, wiring the
// two production decisions (gradesFileScoped, testSurfacePaths) exactly as
// runCertifyRepo does.
func keysFor(t *testing.T, root string, cands, selected []reposcan.Candidate, excl []reposcan.Exclusion, argv []string) map[string]string {
	t.Helper()
	jobs, _, err := reposcan.EmitJobs(reposcan.EmitConfig{
		Owner: "o", Repo: "r", Commit: "c", Root: root,
		FileScopedTests:  gradesFileScoped(argv, selected, cands, excl),
		TestSurfacePaths: surfacePathsIn(t, root, cands, excl),
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
	if gradesFileScoped([]string{"pytest", "-q", "tests/test_a.py"}, sel, sel, nil) {
		t.Fatal("an argv naming only ONE of two selected candidates' tests was called file-scoped — b.py would key on a test that never runs, and the test that really grades it would appear in no key at all")
	}
	// The genuine single-file case must still be file-scoped: over-invalidating
	// there throws away every verdict in the repo for a change that cannot
	// reach them.
	one := sel[:1]
	if !gradesFileScoped([]string{"pytest", "-q", "tests/test_a.py"}, one, sel, nil) {
		t.Fatal("a one-candidate scan whose only test is named in the argv is genuinely file-scoped")
	}
	if !gradesFileScoped([]string{"pytest", "tests/test_a.py::test_x"}, one, sel, nil) {
		t.Fatal("a pytest node id names the same file")
	}
	if gradesFileScoped([]string{"pytest", "-q", "tests/unit"}, one, sel, nil) {
		t.Fatal("an argv naming a DIRECTORY is a subset of the suite, not one file")
	}
	if gradesFileScoped([]string{"pytest", "-q"}, one, sel, nil) {
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
	strong := keysFor(t, mk("def test_a():\n    assert a() == 1\n"), cands, cands, nil, argv)
	weak := keysFor(t, mk("def test_a():\n    pass\n"), cands, cands, nil, argv)

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

	strong := keysFor(t, rootA, candsA, candsA, exclA, nil)
	weak := keysFor(t, rootB, candsB, candsB, exclB, nil)

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
		"pkg/a.py":          "def a():\n    return 1\n",
		"tests/test_a.py":   "def test_a():\n    assert True\n",
		"tests/conftest.py": "import pytest\n",
		"tests/helpers.py":  "X = 1\n",
		"tests/golden.json": "{}\n",
		// A test-tree file in a directory that holds no recognized test:
		// the one shape the "beside a test" rule cannot reach. It is in
		// the surface by where it lives (reposcan.ReasonTestSupport).
		"tests/testserver/server.py": "def serve():\n    return 0\n",
		// A repo-root conftest.py: beside no test, under no test root, and
		// read by every test under tests/ — reached by NAME.
		"conftest.py":    "import pytest\n",
		"pyproject.toml": "[tool.pytest.ini_options]\ntestpaths = ['tests']\n",
		"src/jest.setup.js":          "module.exports = {}\n",
		"src/a.test.js":              "test('a', () => {})\n",
		"docs/notes.md":              "not a test\n",
		"docs/whatever.json":         "{}\n",
	})
	cands, excl, err := reposcan.Enumerate(root)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, p := range surfacePathsIn(t, root, cands, excl) {
		got[p] = true
	}
	for _, want := range []string{
		"tests/test_a.py", "tests/conftest.py", "tests/helpers.py", "tests/golden.json",
		"tests/testserver/server.py", "src/a.test.js", "src/jest.setup.js",
		"conftest.py", "pyproject.toml",
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
	// Under the test root, a file is test-support BY ENUMERATION — that is
	// reposcan's own rule (ReasonTestSupport), not the surface widening's:
	// the surface only reads the classification, it never writes one.
	// src/jest.setup.js lives beside a test but under no test root, so it
	// keeps the name-shaped reason and stays eligible for evidence pairing.
	wantReason := map[string]string{
		"tests/conftest.py": reposcan.ReasonTestSupport,
		"tests/helpers.py":  reposcan.ReasonTestSupport,
		"tests/fixtures.py": reposcan.ReasonTestSupport,
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
	strong := keysFor(t, mk("def test_b():\n    assert b() == 2\n"), cands, selected, nil, argv)
	weak := keysFor(t, mk("def test_b():\n    pass\n"), cands, selected, nil, argv)

	if strong["a.py"] == weak["a.py"] {
		t.Fatal("weakening tests/test_b.py left a.py's key unchanged — that file runs in the same command and grades a.py's mutants, so the cache would serve a.py's old kill rate for a suite that got worse")
	}
}

// RESIDUAL 3. Go golden fixtures live in testdata/, which is in skipDirs, so
// they come back as `skipped-dir` exclusions — held out of the surface
// wholesale. Weakening a golden is the commonest way a Go suite changes what
// it measures, and Go is this repository's own language.
func TestKeyMovesWhenAGoTestdataGoldenIsWeakened(t *testing.T) {
	mk := func(golden string) (string, []reposcan.Candidate, []reposcan.Exclusion) {
		root := t.TempDir()
		writeTree(t, root, map[string]string{
			"internal/foo/foo.go":               "package foo\n\nfunc Foo() int { return 1 }\n",
			"internal/foo/foo_test.go":          "package foo\n\nimport \"testing\"\n\nfunc TestFoo(t *testing.T) {}\n",
			"internal/foo/testdata/golden.json": golden,
		})
		cands, excl, err := reposcan.Enumerate(root)
		if err != nil {
			t.Fatal(err)
		}
		return root, cands, excl
	}
	rootA, candsA, exclA := mk("{\"want\":1}\n")
	rootB, candsB, exclB := mk("{\"want\":2}\n")

	strong := keysFor(t, rootA, candsA, candsA, exclA, nil)
	weak := keysFor(t, rootB, candsB, candsB, exclB, nil)

	if len(strong) == 0 {
		t.Fatal("no jobs emitted — the Go tree paired nothing")
	}
	for p, k := range strong {
		if weak[p] == k {
			t.Fatalf("weakening internal/foo/testdata/golden.json left %s's key unchanged — the golden decides what the suite asserts, so the ledger would repeat the old kill rate", p)
		}
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
	if gradesFileScoped([]string{"pytest", "tests/test_a.py", "tests/test_b.py"}, one, cands, nil) {
		t.Fatal("an argv naming an UNSELECTED candidate's test was called file-scoped — that test runs in the same command and grades a.py, so weakening it would leave a.py's key unmoved")
	}
	// A test file that is nobody's pair (Enumerate calls it `is-test`) counts
	// too: it runs and it grades.
	excl := []reposcan.Exclusion{{Path: "tests/test_extra.py", Reason: reposcan.ReasonIsTest}}
	if gradesFileScoped([]string{"pytest", "tests/test_a.py", "tests/test_extra.py"}, one, one, excl) {
		t.Fatal("an argv naming an is-test file outside the selected set was called file-scoped")
	}
	// Flags and flag values are not test paths and must not change the answer.
	if !gradesFileScoped([]string{"pytest", "-q", "-k", "not slow", "tests/test_a.py"}, one, cands, nil) {
		t.Fatal("flag tokens and their values are not test paths — they must not force whole-suite")
	}
}

// The testdata widening is a KEYING rule only. It must never change which
// files get audited: a golden fixture is not a candidate and its directory
// stays a skipped-dir exclusion.
func TestTestdataWideningDoesNotChangeCandidateClassification(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"internal/foo/foo.go":               "package foo\n\nfunc Foo() int { return 1 }\n",
		"internal/foo/foo_test.go":          "package foo\n\nimport \"testing\"\n\nfunc TestFoo(t *testing.T) {}\n",
		"internal/foo/testdata/golden.json": "{}\n",
		"internal/foo/testdata/in/deep.txt": "x\n",
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
	if len(gotCands) != 1 || gotCands[0] != "internal/foo/foo.go->internal/foo/foo_test.go" {
		t.Fatalf("candidates = %v, want just internal/foo/foo.go->internal/foo/foo_test.go", gotCands)
	}
	gotReason := map[string]string{}
	for _, e := range excl {
		gotReason[e.Path] = e.Reason
	}
	for _, p := range []string{"internal/foo/testdata/golden.json", "internal/foo/testdata/in/deep.txt"} {
		if gotReason[p] != reposcan.ReasonSkippedDir {
			t.Errorf("%s classified %q, want %q — widening the KEYING surface must not change what gets audited", p, gotReason[p], reposcan.ReasonSkippedDir)
		}
	}
	// ...and both are nonetheless in the surface, at any depth under testdata.
	r, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	got := map[string]bool{}
	for _, p := range testSurfacePaths(r, cands, excl) {
		got[p] = true
	}
	for _, want := range []string{"internal/foo/testdata/golden.json", "internal/foo/testdata/in/deep.txt"} {
		if !got[want] {
			t.Errorf("%s is missing from the test surface — a golden decides what the suite asserts", want)
		}
	}
}

// RESIDUAL 1, second shape. Exclusivity matched known test paths EXACTLY, so
// a DIRECTORY token slipped through: `-- pytest tests/test_a.py tests/unit`
// covers the one selected candidate's test, and tests/unit is not itself a
// known test file — so the scan keyed file-scoped while tests/unit/test_b.py
// ran in that same command and graded a.py's mutants.
func TestKeyMovesWhenAnUnselectedTestIsNamedViaItsDirectory(t *testing.T) {
	cands := []reposcan.Candidate{
		{Path: "a.py", TestPath: "tests/test_a.py", Lang: "python"},
		{Path: "b.py", TestPath: "tests/unit/test_b.py", Lang: "python"},
	}
	selected := cands[:1]
	argv := []string{"pytest", "-q", "tests/test_a.py", "tests/unit"}
	if gradesFileScoped(argv, selected, cands, nil) {
		t.Fatal("an argv naming the DIRECTORY holding an unselected candidate's test was called file-scoped — that test runs in the same command and grades a.py")
	}
	mk := func(otherTest string) string {
		root := t.TempDir()
		writeTree(t, root, map[string]string{
			"a.py": "def a():\n    return 1\n", "b.py": "def b():\n    return 2\n",
			"tests/test_a.py":      "def test_a():\n    assert True\n",
			"tests/unit/test_b.py": otherTest,
		})
		return root
	}
	strong := keysFor(t, mk("def test_b():\n    assert b() == 2\n"), cands, selected, nil, argv)
	weak := keysFor(t, mk("def test_b():\n    pass\n"), cands, selected, nil, argv)

	if strong["a.py"] == weak["a.py"] {
		t.Fatal("weakening tests/unit/test_b.py left a.py's key unchanged — the argv named its directory, so it runs and grades a.py, and auditConfigKey digests only the argv TEXT")
	}
}

// ...and the converse must stay allowed: a directory token that contains ONLY
// selected candidates' tests names nothing outside the set, so it cannot make
// the scan grade by anything the key does not already cover.
func TestGradesFileScopedAllowsADirectoryHoldingOnlySelectedTests(t *testing.T) {
	sel := []reposcan.Candidate{{Path: "a.py", TestPath: "tests/unit/test_a.py", Lang: "python"}}
	argv := []string{"pytest", "tests/unit", "tests/unit/test_a.py"}
	if !gradesFileScoped(argv, sel, sel, nil) {
		t.Fatal("a directory token holding only the selected candidate's own test must not force whole-suite — over-invalidating there throws away every verdict for a change that cannot reach them")
	}
	// A trailing slash is the same directory.
	if gradesFileScoped([]string{"pytest", "tests/unit/test_a.py", "tests/other/"}, sel,
		append(append([]reposcan.Candidate{}, sel...),
			reposcan.Candidate{Path: "b.py", TestPath: "tests/other/test_b.py", Lang: "python"}), nil) {
		t.Fatal("`tests/other/` and `tests/other` name the same directory — a trailing slash must not defeat the check")
	}
}

// RESIDUAL 1, third shape. `.` is the repo root, so it names every test in the
// tree — but its prefix form is "./", which prefixes no repo-relative path, so
// the directory check ignored it and the scan keyed file-scoped while the whole
// suite graded the run.
func TestKeyMovesWhenTheUnselectedTestIsNamedViaTheRepoRoot(t *testing.T) {
	cands := []reposcan.Candidate{
		{Path: "a.py", TestPath: "tests/test_a.py", Lang: "python"},
		{Path: "b.py", TestPath: "tests/unit/test_b.py", Lang: "python"},
	}
	selected := cands[:1]
	for _, root := range []string{".", "./"} {
		if gradesFileScoped([]string{"pytest", "tests/test_a.py", root}, selected, cands, nil) {
			t.Fatalf("an argv naming the repo root (%q) was called file-scoped — it runs every test in the tree, including the one grading a.py", root)
		}
	}
	argv := []string{"pytest", "-q", "tests/test_a.py", "."}
	mk := func(otherTest string) string {
		dir := t.TempDir()
		writeTree(t, dir, map[string]string{
			"a.py": "def a():\n    return 1\n", "b.py": "def b():\n    return 2\n",
			"tests/test_a.py":      "def test_a():\n    assert True\n",
			"tests/unit/test_b.py": otherTest,
		})
		return dir
	}
	strong := keysFor(t, mk("def test_b():\n    assert b() == 2\n"), cands, selected, nil, argv)
	weak := keysFor(t, mk("def test_b():\n    pass\n"), cands, selected, nil, argv)

	if strong["a.py"] == weak["a.py"] {
		t.Fatal("weakening tests/unit/test_b.py left a.py's key unchanged — the argv named the repo root, so every test runs and grades a.py, and auditConfigKey digests only the argv TEXT")
	}
	// The root token must still be allowed when there is no test outside the
	// selected set for it to pull in.
	one := []reposcan.Candidate{{Path: "a.py", TestPath: "tests/test_a.py", Lang: "python"}}
	if !gradesFileScoped([]string{"pytest", ".", "tests/test_a.py"}, one, one, nil) {
		t.Fatal("the repo root names nothing outside the selected set when the selected set is every known test")
	}
}
