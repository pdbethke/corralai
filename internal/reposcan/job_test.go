package reposcan

import (
	"errors"
	"fmt"
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

// The audit's substrate is part of the cache key's identity: a verdict
// earned under bwrap and one earned in a CI runner's own checkout are
// different claims (different isolation, different toolchain provenance).
// Without this, a cached jail verdict would satisfy a seal claiming runner
// provenance — this proves the value actually reaches the key, not just that
// the field exists.
func TestEmitJobsCacheKeyTracksSubstrate(t *testing.T) {
	root := writeTree(t, map[string]string{
		"pkg/a.go": "package pkg\n", "pkg/a_test.go": "package pkg\n",
	})
	cands := []Candidate{{Path: "pkg/a.go", TestPath: "pkg/a_test.go", Lang: "go"}}
	gs := stubGoals{"pkg/a.go": "g"}

	jailJobs, _, err := EmitJobs(EmitConfig{Owner: "o", Root: root, Substrate: SubstrateJail}, cands, gs)
	if err != nil {
		t.Fatal(err)
	}
	wsJobs, _, err := EmitJobs(EmitConfig{Owner: "o", Root: root, Substrate: SubstrateWorkspace}, cands, gs)
	if err != nil {
		t.Fatal(err)
	}
	if len(jailJobs) != 1 || len(wsJobs) != 1 {
		t.Fatalf("want 1 job each, got %d and %d", len(jailJobs), len(wsJobs))
	}
	if jailJobs[0].CacheKey == wsJobs[0].CacheKey {
		t.Fatal("substrate did not reach the cache key: jail and workspace verdicts key identically")
	}
}

type errorGoalSource struct{}

func (e errorGoalSource) GoalFor(c Candidate) (Goal, bool, error) {
	return Goal{}, false, errFail
}

// A GoalSource error is ACCOUNTED per candidate, not propagated as a
// scan-fatal error. It was fatal while goals came only from a file (a
// malformed goals file is fatal for every candidate); with derivation, one
// failed model call must not cost the operator every other file.
func TestEmitJobsAccountsGoalSourceErrorPerCandidate(t *testing.T) {
	root := writeTree(t, map[string]string{
		"pkg/a.go": "package pkg\n", "pkg/a_test.go": "package pkg\n",
	})
	cands := []Candidate{{Path: "pkg/a.go", TestPath: "pkg/a_test.go", Lang: "go"}}

	jobs, excl, err := EmitJobs(EmitConfig{Owner: "o", Root: root}, cands, errorGoalSource{})
	if err != nil {
		t.Fatalf("a per-candidate goal failure must not abort the scan: %v", err)
	}
	if len(jobs) != 0 {
		t.Errorf("jobs = %+v, want none: no goal was obtained", jobs)
	}
	// Never conflated with ungoaled: that would report a broken run as a repo
	// with unclear code.
	if len(excl) != 1 || excl[0].Reason != ReasonDeriveFailed {
		t.Errorf("excl = %+v, want one %s", excl, ReasonDeriveFailed)
	}
}

type failOneGoals struct{ failPath string }

func (f failOneGoals) GoalFor(c Candidate) (Goal, bool, error) {
	if c.Path == f.failPath {
		return Goal{}, false, errors.New("429 rate limited")
	}
	return Goal{Text: "g", Provenance: "file"}, true, nil
}

func TestEmitJobsOneDeriveFailureDoesNotAbortTheScan(t *testing.T) {
	root := writeTree(t, map[string]string{
		"a.go": "package p\n", "a_test.go": "package p\n",
		"b.go": "package p\n", "b_test.go": "package p\n",
	})
	cands := []Candidate{
		{Path: "a.go", TestPath: "a_test.go", Lang: "go"},
		{Path: "b.go", TestPath: "b_test.go", Lang: "go"},
	}
	jobs, excl, err := EmitJobs(EmitConfig{Owner: "o", Root: root}, cands, failOneGoals{failPath: "a.go"})
	if err != nil {
		t.Fatalf("one failing candidate must not abort the scan: %v", err)
	}
	if len(jobs) != 1 || jobs[0].Path != "b.go" {
		t.Fatalf("jobs = %+v, want just b.go", jobs)
	}
	if len(excl) != 1 || excl[0].Reason != ReasonDeriveFailed {
		t.Fatalf("excl = %+v, want one derive-failed", excl)
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

type tooLargeGoals struct{}

func (tooLargeGoals) GoalFor(c Candidate) (Goal, bool, error) {
	return Goal{}, false, fmt.Errorf("reposcan: %s: %w (9 bytes, cap 4)", c.Path, ErrSourceTooLarge)
}

// An oversized file gets its OWN reason. Letting it land in derive-failed would
// tell an operator to go check their API key for a fact about their repo — and
// derive-failed is the one bucket in this taxonomy that means "not the repo".
func TestEmitJobsAccountsSourceTooLargeUnderItsOwnReason(t *testing.T) {
	root := writeTree(t, map[string]string{
		"pkg/a.go": "package pkg\n", "pkg/a_test.go": "package pkg\n",
	})
	cands := []Candidate{{Path: "pkg/a.go", TestPath: "pkg/a_test.go", Lang: "go"}}

	jobs, excl, err := EmitJobs(EmitConfig{Owner: "o", Root: root}, cands, tooLargeGoals{})
	if err != nil {
		t.Fatalf("an oversized candidate must not abort the scan: %v", err)
	}
	if len(jobs) != 0 {
		t.Errorf("jobs = %+v, want none", jobs)
	}
	if len(excl) != 1 || excl[0].Reason != ReasonSourceTooLarge {
		t.Errorf("excl = %+v, want one %s (never %s)", excl, ReasonSourceTooLarge, ReasonDeriveFailed)
	}
}
