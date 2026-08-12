// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"os"
	"testing"
)

// openRootT opens an *os.Root on dir for a test, the same confinement
// DigestFile/DigestDir are always called through in production.
func openRootT(t *testing.T, dir string) *os.Root {
	t.Helper()
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot(%s): %v", dir, err)
	}
	return r
}

// The whole-suite grading surface is the whole suite, so the key must cover
// the whole suite. Keying only on the audited file's OWN paired test means a
// weakened test ANYWHERE ELSE leaves every other file's key unchanged: source
// byte-identical, package byte-identical, paired test byte-identical, so the
// ledger repeats an old kill rate for a suite that genuinely got worse.
//
// The two files live in DIFFERENT directories deliberately: a same-directory
// change would move PackageDigest and the test would pass without the test
// surface being keyed at all.
func TestEmitJobsWholeSuiteKeyTracksAnotherFilesTest(t *testing.T) {
	mk := func(otherTest string) string {
		return writeTree(t, map[string]string{
			"pkg/a.go": "package pkg\n", "pkg/a_test.go": "package pkg\n",
			"other/b.go": "package other\n", "other/b_test.go": otherTest,
		})
	}
	cands := []Candidate{
		{Path: "pkg/a.go", TestPath: "pkg/a_test.go", Lang: "go"},
		{Path: "other/b.go", TestPath: "other/b_test.go", Lang: "go"},
	}
	gs := stubGoals{"pkg/a.go": "g", "other/b.go": "g"}

	strong, _, err := EmitJobs(EmitConfig{Owner: "o", Root: mk("package other // strong\n")}, cands, gs)
	if err != nil {
		t.Fatal(err)
	}
	weak, _, err := EmitJobs(EmitConfig{Owner: "o", Root: mk("package other // weakened\n")}, cands, gs)
	if err != nil {
		t.Fatal(err)
	}
	if strong[0].Path != "pkg/a.go" || weak[0].Path != "pkg/a.go" {
		t.Fatalf("expected pkg/a.go first, got %q and %q", strong[0].Path, weak[0].Path)
	}
	if strong[0].CacheKey == weak[0].CacheKey {
		t.Fatal("weakening ANOTHER file's test did not change pkg/a.go's key — on the whole-suite path that suite grades pkg/a.go too, so its verdict was reused against a suite that changed")
	}
}

// A test file that is nobody's paired test — a shared helper — still grades
// every file in a whole-suite run. Enumerate hands it back as an `is-test`
// exclusion, and EmitConfig.TestSurfacePaths is how it reaches the key.
func TestEmitJobsWholeSuiteKeyTracksAnUnpairedHelper(t *testing.T) {
	mk := func(helper string) string {
		return writeTree(t, map[string]string{
			"pkg/a.go": "package pkg\n", "pkg/a_test.go": "package pkg\n",
			"helpers/shared_test.go": helper,
		})
	}
	cands := []Candidate{{Path: "pkg/a.go", TestPath: "pkg/a_test.go", Lang: "go"}}
	gs := stubGoals{"pkg/a.go": "g"}
	extra := []string{"helpers/shared_test.go"}

	strong, _, err := EmitJobs(EmitConfig{Owner: "o", Root: mk("package helpers // strong\n"), TestSurfacePaths: extra}, cands, gs)
	if err != nil {
		t.Fatal(err)
	}
	weak, _, err := EmitJobs(EmitConfig{Owner: "o", Root: mk("package helpers // weakened\n"), TestSurfacePaths: extra}, cands, gs)
	if err != nil {
		t.Fatal(err)
	}
	if strong[0].CacheKey == weak[0].CacheKey {
		t.Fatal("weakening a shared test helper did not change the key — the helper grades this file too")
	}
}

// The file-scoped path (--scope-tests, or an explicit `-- <cmd>` naming one
// test file) genuinely grades against ONE file, so it keeps the single-file
// digest: over-invalidating there would throw away every verdict in the repo
// for a change that cannot reach them.
func TestEmitJobsFileScopedKeyIgnoresOtherFilesTests(t *testing.T) {
	mk := func(otherTest string) string {
		return writeTree(t, map[string]string{
			"pkg/a.go": "package pkg\n", "pkg/a_test.go": "package pkg\n",
			"other/b.go": "package other\n", "other/b_test.go": otherTest,
		})
	}
	cands := []Candidate{
		{Path: "pkg/a.go", TestPath: "pkg/a_test.go", Lang: "go"},
		{Path: "other/b.go", TestPath: "other/b_test.go", Lang: "go"},
	}
	gs := stubGoals{"pkg/a.go": "g", "other/b.go": "g"}

	one, _, err := EmitJobs(EmitConfig{Owner: "o", Root: mk("package other // v1\n"), FileScopedTests: true}, cands, gs)
	if err != nil {
		t.Fatal(err)
	}
	two, _, err := EmitJobs(EmitConfig{Owner: "o", Root: mk("package other // v2\n"), FileScopedTests: true}, cands, gs)
	if err != nil {
		t.Fatal(err)
	}
	if one[0].CacheKey != two[0].CacheKey {
		t.Fatal("a file-scoped run invalidated pkg/a.go over another file's test — that test never runs against it")
	}
}

// Length-prefixing, the same discipline DigestDir uses: no two different
// (path, content) sets may fold to the same digest by concatenation.
func TestDigestTestSurfaceCannotCollideByConcatenation(t *testing.T) {
	root := writeTree(t, map[string]string{
		"a_test.go": "x", "ab_test.go": "y",
	})
	r := openRootT(t, root)
	defer r.Close()

	one, err := DigestTestSurface(r, []string{"a_test.go", "ab_test.go"})
	if err != nil {
		t.Fatal(err)
	}
	two, err := DigestTestSurface(r, []string{"ab_test.go", "a_test.go"})
	if err != nil {
		t.Fatal(err)
	}
	if one != two {
		t.Fatal("DigestTestSurface is order-dependent — it must fold in sorted path order")
	}
	only, err := DigestTestSurface(r, []string{"a_test.go"})
	if err != nil {
		t.Fatal(err)
	}
	if only == one {
		t.Fatal("dropping a test file from the surface did not change the digest")
	}
}
