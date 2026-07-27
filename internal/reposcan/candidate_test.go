package reposcan

import (
	"os"
	"path/filepath"
	"sort"
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

	skippedFiles := []string{"dist/d.go", "build/b.go", "target/t.go", ".tox/x.py", "lib/site-packages/s.py", "lib/site-packages/test_s.py"}
	for _, path := range skippedFiles {
		if reasons[path] != ReasonSkippedDir {
			t.Errorf("%s reason = %q, want %q (skipped dirs must be accounted)", path, reasons[path], ReasonSkippedDir)
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

func TestEnumerateRealRepoStats(t *testing.T) {
	// This test enumerates the real repository and reports statistics about
	// skipped-dir exclusions. It's informational and not normally run.
	if os.Getenv("VERBOSE") == "" {
		t.Skip("skipping real repo stats (set VERBOSE=1 to run)")
	}

	root := "/home/pdbethke/PycharmProjects/corralai/.claude/worktrees/canary-punch-list"
	cands, excl, err := Enumerate(root)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}

	// Count exclusions by reason
	reasonCount := map[string]int{}
	for _, e := range excl {
		reasonCount[e.Reason]++
	}

	t.Logf("Real repo enumeration stats:")
	t.Logf("  Candidates: %d", len(cands))
	t.Logf("  Total exclusions: %d", len(excl))
	t.Logf("  Exclusions by reason:")

	// Sort for consistent output
	reasons := make([]string, 0, len(reasonCount))
	for r := range reasonCount {
		reasons = append(reasons, r)
	}
	sort.Strings(reasons)

	for _, reason := range reasons {
		t.Logf("    %s: %d", reason, reasonCount[reason])
	}

	// Break down skipped-dir by directory
	skippedByDir := map[string]int{}
	skippedFiles := []string{}
	for _, e := range excl {
		if e.Reason == ReasonSkippedDir {
			// Extract top-level directory
			dir := e.Path
			if idx := strings.IndexByte(e.Path, '/'); idx != -1 {
				dir = e.Path[:idx]
			}
			skippedByDir[dir]++
			skippedFiles = append(skippedFiles, e.Path)
		}
	}

	if len(skippedByDir) > 0 {
		t.Logf("  Skipped-dir exclusions by directory:")
		dirs := make([]string, 0, len(skippedByDir))
		for d := range skippedByDir {
			dirs = append(dirs, d)
		}
		sort.Strings(dirs)
		for _, dir := range dirs {
			t.Logf("    %s: %d", dir, skippedByDir[dir])
		}
		if len(skippedFiles) <= 10 {
			sort.Strings(skippedFiles)
			t.Logf("  Skipped files:")
			for _, f := range skippedFiles {
				t.Logf("    %s", f)
			}
		}
	}
}
