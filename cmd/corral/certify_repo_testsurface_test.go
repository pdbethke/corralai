// SPDX-License-Identifier: Elastic-2.0

package main

import (
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
		FileScopedTests:  gradesFileScoped(argv, scopeTests, selected),
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
	if gradesFileScoped([]string{"pytest", "-q", "tests/test_a.py"}, false, sel) {
		t.Fatal("an argv naming only ONE of two selected candidates' tests was called file-scoped — b.py would key on a test that never runs, and the test that really grades it would appear in no key at all")
	}
	// The genuine single-file case must still be file-scoped: over-invalidating
	// there throws away every verdict in the repo for a change that cannot
	// reach them.
	one := sel[:1]
	if !gradesFileScoped([]string{"pytest", "-q", "tests/test_a.py"}, false, one) {
		t.Fatal("a one-candidate scan whose only test is named in the argv is genuinely file-scoped")
	}
	if !gradesFileScoped([]string{"pytest", "tests/test_a.py::test_x"}, false, one) {
		t.Fatal("a pytest node id names the same file")
	}
	if gradesFileScoped([]string{"pytest", "-q", "tests/unit"}, false, one) {
		t.Fatal("an argv naming a DIRECTORY is a subset of the suite, not one file")
	}
	if gradesFileScoped([]string{"pytest", "-q"}, false, one) {
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
