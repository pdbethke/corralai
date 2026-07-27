package reposcan

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		full := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestEnumerateClassifies(t *testing.T) {
	root := writeTree(t, map[string]string{
		"pkg/a.go":      "package pkg\n",
		"pkg/a_test.go": "package pkg\n",
		"pkg/b.go":      "package pkg\n", // no paired test
		"README.md":     "# hi\n",        // no language
	})

	cands, excl, err := Enumerate(root)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}

	if len(cands) != 1 {
		t.Fatalf("want 1 candidate, got %d: %+v", len(cands), cands)
	}
	if cands[0].Path != "pkg/a.go" || cands[0].TestPath != "pkg/a_test.go" || cands[0].Lang != "go" {
		t.Errorf("bad candidate: %+v", cands[0])
	}

	reasons := map[string]string{}
	for _, e := range excl {
		reasons[e.Path] = e.Reason
	}
	if reasons["pkg/a_test.go"] != "is-test" {
		t.Errorf("a_test.go reason = %q, want is-test", reasons["pkg/a_test.go"])
	}
	if reasons["pkg/b.go"] != "no-paired-test" {
		t.Errorf("b.go reason = %q, want no-paired-test", reasons["pkg/b.go"])
	}
	if reasons["README.md"] != "no-language" {
		t.Errorf("README.md reason = %q, want no-language", reasons["README.md"])
	}
}

func TestEnumerateIsDeterministic(t *testing.T) {
	root := writeTree(t, map[string]string{
		"z/z.go": "package z\n", "z/z_test.go": "package z\n",
		"a/a.go": "package a\n", "a/a_test.go": "package a\n",
	})
	first, _, err := Enumerate(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].Path != "a/a.go" || first[1].Path != "z/z.go" {
		t.Fatalf("candidates not sorted by path: %+v", first)
	}
}

func TestEnumeratePythonConventions(t *testing.T) {
	root := writeTree(t, map[string]string{
		"app.py":      "# app\n",
		"test_app.py": "# test\n",
	})

	cands, excl, err := Enumerate(root)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}

	if len(cands) != 1 {
		t.Fatalf("want 1 candidate, got %d", len(cands))
	}
	if cands[0].Path != "app.py" || cands[0].Lang != "python" {
		t.Errorf("bad candidate: %+v", cands[0])
	}

	reasons := map[string]string{}
	for _, e := range excl {
		reasons[e.Path] = e.Reason
	}
	if reasons["test_app.py"] != "is-test" {
		t.Errorf("test_app.py reason = %q, want is-test", reasons["test_app.py"])
	}
}

func TestEnumerateRubyConventions(t *testing.T) {
	root := writeTree(t, map[string]string{
		"user.rb":      "# user\n",
		"user_test.rb": "# minitest\n",
	})

	cands, excl, err := Enumerate(root)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}

	if len(cands) != 1 {
		t.Fatalf("want 1 candidate (Ruby minitest), got %d", len(cands))
	}
	if cands[0].Path != "user.rb" || cands[0].Lang != "ruby" {
		t.Errorf("bad candidate: %+v", cands[0])
	}

	reasons := map[string]string{}
	for _, e := range excl {
		reasons[e.Path] = e.Reason
	}
	if reasons["user_test.rb"] != "is-test" {
		t.Errorf("user_test.rb reason = %q, want is-test", reasons["user_test.rb"])
	}
}

func TestEnumerateRubyRSpecConvention(t *testing.T) {
	// Ruby RSpec uses foo_spec.rb convention
	root := writeTree(t, map[string]string{
		"order.rb":      "# order\n",
		"order_spec.rb": "# rspec\n",
	})

	_, excl, err := Enumerate(root)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}

	// order_spec.rb should be detected as a test via _spec. suffix
	reasons := map[string]string{}
	for _, e := range excl {
		reasons[e.Path] = e.Reason
	}
	if reasons["order_spec.rb"] != "is-test" {
		t.Errorf("order_spec.rb reason = %q, want is-test (RSpec convention)", reasons["order_spec.rb"])
	}
}

func TestEnumerateJavaScriptConventions(t *testing.T) {
	root := writeTree(t, map[string]string{
		"calc.js":      "// calc\n",
		"calc.test.js": "// test\n",
		"sort.js":      "// sort\n",
		"sort.test.js": "// test\n",
	})

	cands, excl, err := Enumerate(root)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}

	if len(cands) != 2 {
		t.Fatalf("want 2 candidates, got %d", len(cands))
	}

	reasons := map[string]string{}
	for _, e := range excl {
		reasons[e.Path] = e.Reason
	}
	if reasons["calc.test.js"] != "is-test" {
		t.Errorf("calc.test.js reason = %q, want is-test (.test convention)", reasons["calc.test.js"])
	}
	if reasons["sort.test.js"] != "is-test" {
		t.Errorf("sort.test.js reason = %q, want is-test (.test convention)", reasons["sort.test.js"])
	}
}

func TestEnumerateTypeScriptConventions(t *testing.T) {
	root := writeTree(t, map[string]string{
		"util.ts":      "// util\n",
		"util.test.ts": "// test\n",
		"math.ts":      "// math\n",
		"math.test.ts": "// test\n",
	})

	cands, excl, err := Enumerate(root)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}

	if len(cands) != 2 {
		t.Fatalf("want 2 candidates, got %d", len(cands))
	}

	reasons := map[string]string{}
	for _, e := range excl {
		reasons[e.Path] = e.Reason
	}
	if reasons["util.test.ts"] != "is-test" {
		t.Errorf("util.test.ts reason = %q, want is-test (.test convention)", reasons["util.test.ts"])
	}
	if reasons["math.test.ts"] != "is-test" {
		t.Errorf("math.test.ts reason = %q, want is-test (.test convention)", reasons["math.test.ts"])
	}
}

func TestEnumerateDirectorySubstringDoesNotTriggerTestDetection(t *testing.T) {
	// Regression test for Finding 2: directory names containing _test. should not
	// cause files within them to be misclassified as tests.
	root := writeTree(t, map[string]string{
		"e2e_test.fixtures/schema.go":          "package fixtures\n",
		"e2e_test.fixtures/schema_test.go":     "package fixtures\n",
		"integration.spec.assets/icon.js":      "// icon\n",
		"integration.spec.assets/icon.test.js": "// test\n",
	})

	cands, excl, err := Enumerate(root)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}

	// schema.go should be a candidate, not excluded as "is-test"
	if len(cands) != 2 {
		t.Fatalf("want 2 candidates (schema.go and icon.js), got %d", len(cands))
	}

	reasons := map[string]string{}
	for _, e := range excl {
		reasons[e.Path] = e.Reason
	}

	if reasons["e2e_test.fixtures/schema.go"] == "is-test" {
		t.Errorf("schema.go under e2e_test.fixtures/ was incorrectly flagged as is-test due to directory name")
	}
	if reasons["integration.spec.assets/icon.js"] == "is-test" {
		t.Errorf("icon.js under integration.spec.assets/ was incorrectly flagged as is-test due to directory name")
	}

	// Verify the actual test files are correctly identified
	if reasons["e2e_test.fixtures/schema_test.go"] != "is-test" {
		t.Errorf("schema_test.go reason = %q, want is-test", reasons["e2e_test.fixtures/schema_test.go"])
	}
	if reasons["integration.spec.assets/icon.test.js"] != "is-test" {
		t.Errorf("icon.test.js reason = %q, want is-test", reasons["integration.spec.assets/icon.test.js"])
	}
}

// TestEnumerateExcludesSymlinkOutOfTree is the containment invariant: the scan
// AUTO-DISCOVERS its subjects, so a checked-in symlink must never be able to
// point the audit at a file outside the repository. `secrets.py ->
// ~/.aws/credentials` would otherwise be digested, shipped to a model provider
// and copied into the jail workspace.
func TestEnumerateExcludesSymlinkOutOfTree(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside_secret.go")
	if err := os.WriteFile(outside, []byte("package pkg // SECRET\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	root := writeTree(t, map[string]string{
		"pkg/a.go":         "package pkg\n",
		"pkg/a_test.go":    "package pkg\n",
		"pkg/leak_test.go": "package pkg\n", // a paired test, so only the link type can exclude it
	})
	if err := os.Symlink(outside, filepath.Join(root, "pkg/leak.go")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	cands, excl, err := Enumerate(root)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	for _, c := range cands {
		if c.Path == "pkg/leak.go" {
			t.Fatalf("a symlink out of the tree became an audit candidate: %+v", c)
		}
	}
	reasons := map[string]string{}
	for _, e := range excl {
		reasons[e.Path] = e.Reason
	}
	if reasons["pkg/leak.go"] != ReasonNotRegularFile {
		t.Fatalf("pkg/leak.go reason = %q, want %q", reasons["pkg/leak.go"], ReasonNotRegularFile)
	}

	// ...and it must not become a JOB either, which is the thing that would
	// carry the out-of-tree bytes into a cache key and into the jail.
	jobs, _, err := EmitJobs(EmitConfig{Owner: "o", Root: root}, cands, stubGoals{
		"pkg/a.go": "g", "pkg/leak.go": "g",
	})
	if err != nil {
		t.Fatalf("EmitJobs: %v", err)
	}
	for _, j := range jobs {
		if j.Path == "pkg/leak.go" {
			t.Fatalf("a symlink out of the tree became a job: %+v", j)
		}
	}
}

// TestEnumerateExcludesFIFO: a named pipe cannot be audited and a read of it
// would block the scan forever. It is accounted for, not enumerated.
func TestEnumerateExcludesFIFO(t *testing.T) {
	root := writeTree(t, map[string]string{"pkg/a.go": "package pkg\n"})
	fifo := filepath.Join(root, "pkg", "pipe.go")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	_, excl, err := Enumerate(root)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	for _, e := range excl {
		if e.Path == "pkg/pipe.go" {
			if e.Reason != ReasonNotRegularFile {
				t.Fatalf("FIFO reason = %q, want %q", e.Reason, ReasonNotRegularFile)
			}
			return
		}
	}
	t.Fatal("the FIFO was not accounted for as an exclusion")
}

// TestEnumerateSkipsBuildOutputDirs keeps vendored and generated trees out of
// the audited surface: they are not candidates. However, the files are still
// accounted for in the exclusions with reason "skipped-dir" so the report
// maintains an honest count of all files on disk.
func TestEnumerateSkipsBuildOutputDirs(t *testing.T) {
	root := writeTree(t, map[string]string{
		"pkg/a.go":                    "package pkg\n",
		"pkg/a_test.go":               "package pkg\n",
		"dist/d.go":                   "package d\n",
		"build/b.go":                  "package b\n",
		"target/t.go":                 "package t\n",
		".tox/x.py":                   "x = 1\n",
		"lib/site-packages/s.py":      "s = 1\n",
		"lib/site-packages/test_s.py": "s = 1\n",
	})
	cands, excl, err := Enumerate(root)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}

	// Only pkg/a.go should be a candidate
	if len(cands) != 1 || cands[0].Path != "pkg/a.go" {
		t.Errorf("candidates = %+v, want only pkg/a.go", cands)
	}

	// Verify that the skipped-dir files are accounted for in exclusions
	reasons := map[string]string{}
	for _, e := range excl {
		reasons[e.Path] = e.Reason
	}

	// BUILD OUTPUT is accounted: small, derived from this repo, and a reader
	// benefits from seeing the scan chose not to look there.
	accounted := []string{"dist/d.go", "build/b.go", "target/t.go"}
	for _, path := range accounted {
		if reasons[path] != ReasonSkippedDir {
			t.Errorf("%s reason = %q, want %q (build output must be accounted)", path, reasons[path], ReasonSkippedDir)
		}
	}

	// DEPENDENCY trees are invisible, not accounted. .tox holds full
	// virtualenvs and site-packages is installed third-party code — both are
	// routinely 10k+ files, so enumerating them buries the entries a reader
	// actually needs in a report that gets signed and anchored.
	invisible := []string{".tox/x.py", "lib/site-packages/s.py", "lib/site-packages/test_s.py"}
	for _, path := range invisible {
		if _, listed := reasons[path]; listed {
			t.Errorf("%s was enumerated; dependency trees must stay invisible", path)
		}
	}

	// Verify the test file is excluded as a test
	if reasons["pkg/a_test.go"] != ReasonIsTest {
		t.Errorf("pkg/a_test.go reason = %q, want %q", reasons["pkg/a_test.go"], ReasonIsTest)
	}
}

func TestEnumerateAccountsSkippedDirs(t *testing.T) {
	root := writeTree(t, map[string]string{
		"pkg/a.go":      "package pkg\n",
		"pkg/a_test.go": "package pkg\n",
		"build/gen.go":  "package build\n",
		"build/x.txt":   "data\n",
	})

	cands, excl, err := Enumerate(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 {
		t.Fatalf("want 1 candidate, got %d", len(cands))
	}

	var skipped int
	for _, e := range excl {
		if e.Reason == ReasonSkippedDir {
			skipped++
		}
	}
	if skipped != 2 {
		t.Errorf("skipped-dir exclusions = %d, want 2 — build/ files must be accounted, not invisible", skipped)
	}
	if total := len(cands) + len(excl); total != 4 {
		t.Errorf("walked total = %d, want 4 (every file on disk accounted)", total)
	}
}

func TestEnumerateAccountsNestedFilesInSkippedDirs(t *testing.T) {
	// FINDING 1: Verify that skippedDirFiles recursively descends into nested
	// subdirectories within a skipped directory. A future edit that returned
	// SkipDir for nested dirs would silently reintroduce invisible files two
	// levels down.
	root := writeTree(t, map[string]string{
		"pkg/a.go":               "package pkg\n",
		"pkg/a_test.go":          "package pkg\n",
		"build/x.go":             "package build\n",
		"build/nested/y.go":      "package nested\n",
		"build/nested/deep/z.go": "package deep\n",
	})

	cands, excl, err := Enumerate(root)
	if err != nil {
		t.Fatal(err)
	}

	if len(cands) != 1 {
		t.Fatalf("want 1 candidate, got %d", len(cands))
	}

	// Count skipped-dir exclusions and verify all build/ files are accounted
	var skipped int
	skippedPaths := map[string]bool{}
	for _, e := range excl {
		if e.Reason == ReasonSkippedDir {
			skipped++
			skippedPaths[e.Path] = true
		}
	}

	expectedSkipped := []string{"build/x.go", "build/nested/y.go", "build/nested/deep/z.go"}
	for _, path := range expectedSkipped {
		if !skippedPaths[path] {
			t.Errorf("nested file %s not accounted as skipped-dir", path)
		}
	}

	if skipped != 3 {
		t.Errorf("skipped-dir exclusions = %d, want 3 (including nested files)", skipped)
	}
}

func TestEnumerateInvisibleGitDir(t *testing.T) {
	// FINDING 2: Verify that .git/ directory files stay completely invisible
	// (not accounted as exclusions). This is a security invariant: .git is not
	// source code, and listing its objects would swamp a signed report.
	root := writeTree(t, map[string]string{
		"pkg/a.go":             "package pkg\n",
		"pkg/a_test.go":        "package pkg\n",
		".git/HEAD":            "ref: refs/heads/main\n",
		".git/objects/ab/cdef": "binary\n",
		".git/refs/heads/main": "hash\n",
	})

	cands, excl, err := Enumerate(root)
	if err != nil {
		t.Fatal(err)
	}

	// Verify no exclusions mention .git
	for _, e := range excl {
		if strings.Contains(e.Path, ".git") {
			t.Errorf(".git file leaked into exclusions: %s (reason: %s)", e.Path, e.Reason)
		}
	}

	// Verify .git is completely invisible: not in cands, not in excl
	for _, c := range cands {
		if strings.Contains(c.Path, ".git") {
			t.Errorf(".git file leaked into candidates: %s", c.Path)
		}
	}

	// Verify only the legitimate files are accounted
	if len(cands) != 1 || cands[0].Path != "pkg/a.go" {
		t.Errorf("candidates = %+v, want only pkg/a.go", cands)
	}
	reasons := map[string]bool{}
	for _, e := range excl {
		reasons[e.Reason] = true
	}
	if reasons[ReasonSkippedDir] {
		t.Errorf(".git leaked as skipped-dir exclusion")
	}
}

func TestEnumerateExcludesSymlinkInSkippedDir(t *testing.T) {
	// FINDING 3: Verify that symlinks inside skipped directories are not
	// followed (the same security invariant as TestEnumerateExcludesSymlinkOutOfTree,
	// but for the skipped-dir sub-walk). A symlink in build/ pointing to ~/.aws/credentials
	// must not be followed. Non-regular entries in skipped dirs are silently filtered
	// (not listed as exclusions) to keep the report clean.
	outside := filepath.Join(t.TempDir(), "secret.go")
	if err := os.WriteFile(outside, []byte("package secret // LEAKED\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	root := writeTree(t, map[string]string{
		"pkg/a.go":         "package pkg\n",
		"pkg/a_test.go":    "package pkg\n",
		"build/regular.go": "package build\n",
	})

	// Try to create a symlink in the skipped dir pointing outside the tree
	linkPath := filepath.Join(root, "build", "link.go")
	if err := os.Symlink(outside, linkPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	cands, excl, err := Enumerate(root)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}

	// Verify the symlink inside the skipped dir is NOT followed:
	// it should not appear as a candidate
	for _, c := range cands {
		if strings.Contains(c.Path, "link.go") {
			t.Fatalf("symlink in skipped dir became a candidate: %s", c.Path)
		}
	}

	// Verify the symlink was not traversed: outside file should not appear anywhere
	allPaths := map[string]bool{}
	for _, c := range cands {
		allPaths[c.Path] = true
	}
	for _, e := range excl {
		allPaths[e.Path] = true
	}
	if allPaths[filepath.Base(outside)] || allPaths["secret.go"] {
		t.Fatalf("symlink in skipped dir was followed, leaking outside the tree")
	}

	// Verify only the regular file in build/ is accounted as skipped-dir
	var foundRegular bool
	for _, e := range excl {
		if e.Path == "build/regular.go" && e.Reason == ReasonSkippedDir {
			foundRegular = true
		}
		// Symlink should not appear in exclusions (silently filtered)
		if e.Path == "build/link.go" {
			t.Errorf("symlink should not appear in exclusions (non-regular entries in skipped dirs are silently filtered)")
		}
	}
	if !foundRegular {
		t.Errorf("build/regular.go not found as skipped-dir exclusion")
	}
}

// TestEnumerateDependencyTreesAreInvisible pins the report-readability
// invariant: dependency trees are skipped WITHOUT accounting, exactly like
// .git. They are not this repo's source, and node_modules alone is routinely
// tens of thousands of files — enumerating them would bury every entry a
// reader actually needs in a report that gets signed and anchored.
func TestEnumerateDependencyTreesAreInvisible(t *testing.T) {
	root := writeTree(t, map[string]string{
		"pkg/a.go":                        "package pkg\n",
		"pkg/a_test.go":                   "package pkg\n",
		"node_modules/left-pad/index.js":  "// dep\n",
		"node_modules/left-pad/deep/x.js": "// dep\n",
		"vendor/github.com/x/y/y.go":      "package y\n",
		"build/b.go":                      "package b\n",
	})

	cands, excl, err := Enumerate(root)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	if len(cands) != 1 || cands[0].Path != "pkg/a.go" {
		t.Errorf("candidates = %+v, want only pkg/a.go", cands)
	}
	for _, e := range excl {
		if strings.HasPrefix(e.Path, "node_modules/") || strings.HasPrefix(e.Path, "vendor/") {
			t.Errorf("dependency tree leaked into exclusions: %s (reason %s)", e.Path, e.Reason)
		}
	}
	// Build output is still ACCOUNTED — the widening must not swallow it.
	var sawBuild bool
	for _, e := range excl {
		if e.Path == "build/b.go" && e.Reason == ReasonSkippedDir {
			sawBuild = true
		}
	}
	if !sawBuild {
		t.Error("build/b.go must still be accounted as skipped-dir; only VCS and dependency trees are invisible")
	}
}

// TestEnumerateUnreadableSkippedDirIsNotFatal: an unreadable subtree inside a
// tree the audit deliberately does not look at must not fail the whole scan.
// Before skipped-dir accounting existed the walk never descended there at all,
// so a permission-denied build/ could not affect a scan; making it scan-fatal
// would be a regression. It degrades to a single skipped-dir entry.
func TestEnumerateUnreadableSkippedDirIsNotFatal(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode 0000 is still readable")
	}
	root := writeTree(t, map[string]string{
		"pkg/a.go":              "package pkg\n",
		"pkg/a_test.go":         "package pkg\n",
		"build/locked/deep.go":  "package deep\n",
		"build/readable/ok.txt": "data\n",
	})
	locked := filepath.Join(root, "build", "locked")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Skipf("chmod unavailable: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	cands, excl, err := Enumerate(root)
	if err != nil {
		t.Fatalf("an unreadable skipped subtree made the scan fatal: %v", err)
	}
	if len(cands) != 1 || cands[0].Path != "pkg/a.go" {
		t.Errorf("candidates = %+v, want only pkg/a.go — the rest of the scan must still complete", cands)
	}
	var sawReadable, sawLocked bool
	for _, e := range excl {
		if e.Path == "build/readable/ok.txt" && e.Reason == ReasonSkippedDir {
			sawReadable = true
		}
		if e.Path == "build/locked" && e.Reason == ReasonSkippedDir {
			sawLocked = true
		}
	}
	if !sawReadable {
		t.Error("the readable half of the skipped tree stopped being accounted")
	}
	if !sawLocked {
		t.Errorf("the unreadable subtree must be recorded as one skipped-dir entry; exclusions = %+v", excl)
	}
}

// A Python virtualenv is a dependency tree, not build output: enumerating
// site-packages would drown a signed report exactly as node_modules would.
func TestEnumerateVirtualenvsAreInvisible(t *testing.T) {
	root := writeTree(t, map[string]string{
		"pkg/a.py":                       "x = 1\n",
		"pkg/test_a.py":                  "def test_x(): pass\n",
		".venv/lib/site-packages/dep.py": "y = 2\n",
		"venv/lib/other.py":              "z = 3\n",
		".tox/py311/lib/tox_dep.py":      "w = 4\n",
		".bundle/gems/g.rb":              "v = 5\n",
		"build/out.py":                   "b = 6\n",
	})

	cands, excl, err := Enumerate(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range excl {
		for _, dep := range []string{".venv/", "venv/", ".tox/", ".bundle/"} {
			if strings.HasPrefix(e.Path, dep) {
				t.Errorf("dependency tree enumerated into the report: %q", e.Path)
			}
		}
	}
	for _, c := range cands {
		if strings.Contains(c.Path, "venv") || strings.Contains(c.Path, ".tox") {
			t.Errorf("dependency file became a candidate: %q", c.Path)
		}
	}
	// Build output is still accounted — that half must not regress.
	var sawBuild bool
	for _, e := range excl {
		if e.Path == "build/out.py" && e.Reason == ReasonSkippedDir {
			sawBuild = true
		}
	}
	if !sawBuild {
		t.Error("build/out.py should still be an accounted skipped-dir exclusion")
	}
}

// A dependency tree nested inside an ACCOUNTED skipped dir is still
// invisible: accounting build/ must not drag build/node_modules in with it.
func TestEnumerateNestedDependencyTreeStaysInvisible(t *testing.T) {
	root := writeTree(t, map[string]string{
		"pkg/a.go":                        "package pkg\n",
		"pkg/a_test.go":                   "package pkg\n",
		"build/gen.go":                    "package build\n",
		"build/node_modules/dep/index.js": "module.exports = 1\n",
	})

	_, excl, err := Enumerate(root)
	if err != nil {
		t.Fatal(err)
	}
	var sawGen bool
	for _, e := range excl {
		if strings.Contains(e.Path, "node_modules") {
			t.Errorf("nested dependency tree enumerated: %q", e.Path)
		}
		if e.Path == "build/gen.go" {
			sawGen = true
		}
	}
	if !sawGen {
		t.Error("build/gen.go should still be accounted")
	}
}
