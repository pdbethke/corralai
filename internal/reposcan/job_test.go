package reposcan

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

var errFail = errors.New("intentional error")

type stubGoals map[string]string

func (s stubGoals) GoalFor(c Candidate) (Goal, bool, error) {
	if t, ok := s[c.Path]; ok {
		return Goal{Text: t, Provenance: "file"}, true, nil
	}
	return Goal{}, false, nil
}

func TestEmitJobsBuildsOwnerKeyedEnvelopes(t *testing.T) {
	root := writeTree(t, map[string]string{
		"pkg/a.go": "package pkg\n", "pkg/a_test.go": "package pkg\n",
	})
	cfg := EmitConfig{
		Owner: "acme", Repo: "widget", Commit: "abc123", Root: root,
		EngineVersion: "v0.2.0", ModelSet: "claude", AuditConfig: "mutants=10",
	}
	cands := []Candidate{{Path: "pkg/a.go", TestPath: "pkg/a_test.go", Lang: "go"}}

	jobs, excl, err := EmitJobs(cfg, cands, stubGoals{"pkg/a.go": "no negative balances"})
	if err != nil {
		t.Fatalf("EmitJobs: %v", err)
	}
	if len(excl) != 0 {
		t.Fatalf("unexpected exclusions: %+v", excl)
	}
	if len(jobs) != 1 {
		t.Fatalf("want 1 job, got %d", len(jobs))
	}
	j := jobs[0]
	if j.Owner != "acme" || j.Repo != "widget" || j.Commit != "abc123" {
		t.Errorf("envelope identity wrong: %+v", j)
	}
	if j.CacheKey == "" {
		t.Error("CacheKey not computed")
	}
}

// A candidate the GoalSource declines becomes an accounted exclusion, never
// a job with an invented goal.
func TestEmitJobsUngoaledBecomesExclusion(t *testing.T) {
	root := writeTree(t, map[string]string{
		"pkg/a.go": "package pkg\n", "pkg/a_test.go": "package pkg\n",
	})
	cfg := EmitConfig{Owner: "acme", Repo: "w", Commit: "c", Root: root}
	cands := []Candidate{{Path: "pkg/a.go", TestPath: "pkg/a_test.go", Lang: "go"}}

	jobs, excl, err := EmitJobs(cfg, cands, stubGoals{})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatalf("want no jobs, got %+v", jobs)
	}
	if len(excl) != 1 || excl[0].Reason != ReasonUngoaled {
		t.Fatalf("want ungoaled exclusion, got %+v", excl)
	}
}

// Changing only the TEST file must change the cache key: adequacy is a
// property of code AND tests.
func TestEmitJobsCacheKeyTracksTestChanges(t *testing.T) {
	mk := func(testBody string) string {
		return writeTree(t, map[string]string{
			"pkg/a.go": "package pkg\n", "pkg/a_test.go": testBody,
		})
	}
	cands := []Candidate{{Path: "pkg/a.go", TestPath: "pkg/a_test.go", Lang: "go"}}
	gs := stubGoals{"pkg/a.go": "g"}

	j1, _, err := EmitJobs(EmitConfig{Owner: "o", Root: mk("package pkg // v1\n")}, cands, gs)
	if err != nil {
		t.Fatal(err)
	}
	j2, _, err := EmitJobs(EmitConfig{Owner: "o", Root: mk("package pkg // v2 weaker\n")}, cands, gs)
	if err != nil {
		t.Fatal(err)
	}
	if j1[0].CacheKey == j2[0].CacheKey {
		t.Fatal("weakening the test suite did not change the cache key")
	}
}

type errorGoalSource struct{}

func (e errorGoalSource) GoalFor(c Candidate) (Goal, bool, error) {
	return Goal{}, false, errFail
}

// GoalSource errors are propagated.
func TestEmitJobsPropagatesGoalSourceError(t *testing.T) {
	root := writeTree(t, map[string]string{
		"pkg/a.go": "package pkg\n", "pkg/a_test.go": "package pkg\n",
	})
	cands := []Candidate{{Path: "pkg/a.go", TestPath: "pkg/a_test.go", Lang: "go"}}

	_, _, err := EmitJobs(EmitConfig{Owner: "o", Root: root}, cands, errorGoalSource{})
	if err != errFail {
		t.Errorf("want errFail, got %v", err)
	}
}

// DigestFile errors on source file are propagated.
func TestEmitJobsPropagatesSourceDigestError(t *testing.T) {
	root := t.TempDir()
	// Empty root: source file does not exist
	cands := []Candidate{{Path: "pkg/a.go", TestPath: "pkg/a_test.go", Lang: "go"}}
	gs := stubGoals{"pkg/a.go": "g"}

	_, _, err := EmitJobs(EmitConfig{Owner: "o", Root: root}, cands, gs)
	if err == nil {
		t.Error("want error for missing source file, got nil")
	}
}

// DigestDir errors on package directory are propagated.
// Uses permission bits: directory must be unreadable (no read) but traversable (has execute)
// so files can still be opened by name but listing fails.
func TestEmitJobsPropagatesPackageDigestError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("test requires non-root user (root bypasses permission checks)")
	}

	root := writeTree(t, map[string]string{
		"pkg/a.go": "package pkg\n", "pkg/a_test.go": "package pkg\n",
	})
	pkgDir := filepath.Join(root, "pkg")

	// Remove read permission on directory to make os.ReadDir fail,
	// but keep execute permission so files can still be opened by name.
	if err := os.Chmod(pkgDir, 0o100); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() {
		os.Chmod(pkgDir, 0o755) // restore for cleanup
	})

	cands := []Candidate{{Path: "pkg/a.go", TestPath: "pkg/a_test.go", Lang: "go"}}
	gs := stubGoals{"pkg/a.go": "g"}

	_, _, err := EmitJobs(EmitConfig{Owner: "o", Root: root}, cands, gs)
	if err == nil {
		t.Error("want error for unreadable package directory, got nil")
	}
}

// DigestFile errors on test file are propagated.
func TestEmitJobsPropagatesTestDigestError(t *testing.T) {
	root := writeTree(t, map[string]string{
		"pkg/a.go": "package pkg\n",
	})
	// Create candidate with non-existent test file
	cands := []Candidate{{Path: "pkg/a.go", TestPath: "pkg/a_test.go", Lang: "go"}}
	gs := stubGoals{"pkg/a.go": "g"}

	_, _, err := EmitJobs(EmitConfig{Owner: "o", Root: root}, cands, gs)
	if err == nil {
		t.Error("want error for missing test file, got nil")
	}
}
