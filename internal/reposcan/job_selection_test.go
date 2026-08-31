// SPDX-License-Identifier: Elastic-2.0

package reposcan

import "testing"

// TestFileAuditConfigKeysThePerFileGradingMode pins the rule the verdict cache
// exists to keep: a verdict earned under one measurement is never served for
// another. The SCAN-level AuditConfig says selection ran; it cannot say that
// THIS file fell back to the whole suite because the evidence never saw it.
func TestFileAuditConfigKeysThePerFileGradingMode(t *testing.T) {
	root := writeTree(t, map[string]string{
		"pkg/a.go": "package pkg\n", "pkg/a_test.go": "package pkg\n",
		"pkg/b.go": "package pkg\n", "pkg/b_test.go": "package pkg\n",
	})
	cands := []Candidate{
		{Path: "pkg/a.go", TestPath: "pkg/a_test.go", Lang: "go"},
		{Path: "pkg/b.go", TestPath: "pkg/b_test.go", Lang: "go"},
	}
	gs := stubGoals{"pkg/a.go": "g", "pkg/b.go": "g"}
	base := EmitConfig{Owner: "o", Repo: "r", Commit: "c", Root: root, AuditConfig: "test-selection=coverage-context"}

	plain, _, err := EmitJobs(base, cands, gs)
	if err != nil {
		t.Fatalf("EmitJobs (no per-file config): %v", err)
	}

	perFile := base
	perFile.FileAuditConfig = func(c Candidate) string {
		if c.Path == "pkg/a.go" {
			return "file-selection=coverage-context"
		}
		return "file-selection=whole-suite"
	}
	got, _, err := EmitJobs(perFile, cands, gs)
	if err != nil {
		t.Fatalf("EmitJobs (per-file config): %v", err)
	}
	if len(got) != 2 || len(plain) != 2 {
		t.Fatalf("want 2 jobs each, got %d and %d", len(got), len(plain))
	}
	if got[0].CacheKey == got[1].CacheKey {
		t.Error("two files graded by DIFFERENT modes key identically — a whole-suite verdict could be served as a selected one")
	}
	for i := range got {
		if got[i].CacheKey == plain[i].CacheKey {
			t.Errorf("%s: the per-file mode did not move the key", got[i].Path)
		}
	}

	// The MODE is not the whole measurement: two files graded by
	// coverage-context against DIFFERENT sets of tests are two different
	// answers, and the caller keys the ids (see cmd/corral's
	// fileSelectionKey). Pin that a component differing only in that digest
	// really does move the key.
	ids := base
	ids.FileAuditConfig = func(c Candidate) string {
		if c.Path == "pkg/a.go" {
			return "file-selection=coverage-context,selected-tests=aaaa"
		}
		return "file-selection=coverage-context,selected-tests=bbbb"
	}
	byIDs, _, err := EmitJobs(ids, cands, gs)
	if err != nil {
		t.Fatalf("EmitJobs (selected-tests): %v", err)
	}
	if byIDs[0].CacheKey == byIDs[1].CacheKey {
		t.Error("two selections differing in one id key identically — a verdict measured by tests that no longer grade the file would be served")
	}

	// An empty per-file component must key exactly as no hook at all: a dry
	// run supplies nothing, and it must not invalidate every cached verdict.
	empty := base
	empty.FileAuditConfig = func(Candidate) string { return "" }
	same, _, err := EmitJobs(empty, cands, gs)
	if err != nil {
		t.Fatalf("EmitJobs (empty per-file config): %v", err)
	}
	for i := range same {
		if same[i].CacheKey != plain[i].CacheKey {
			t.Errorf("%s: an empty per-file component must not change the key", same[i].Path)
		}
	}
}

// THE CONTROLLER RULING: an evidence-only candidate's cache key uses the
// SUITE digest, not a per-file test digest — TestPath stays empty (no
// FileScopedTests can single it out; see gradesFileScoped's own TestPath==""
// guard), so EmitJobs's suite-digest branch is the only one that can ever
// key it. Pinned directly: a candidate whose ONLY difference from a
// pairing-based one is which test path field is empty must key on the same
// suite digest that every other file in the scan does.
func TestEvidenceOnlyCandidateKeysOnTheSuiteDigestNotAPerFileDigest(t *testing.T) {
	root := writeTree(t, map[string]string{
		"pkg/api.py":        "x = 1\n",
		"pkg/utils.py":      "y = 2\n",
		"tests/test_api.py": "def test_a(): pass\n",
	})
	cfg := EmitConfig{Owner: "o", Repo: "r", Commit: "c", Root: root}
	gs := stubGoals{"pkg/api.py": "g", "pkg/utils.py": "g"}

	paired := Candidate{Path: "pkg/api.py", TestPath: "tests/test_api.py", Lang: "python"}
	evidenceOnly := Candidate{Path: "pkg/utils.py", TestPath: "", Lang: "python", CoveringTestPath: "tests/test_api.py"}

	jobs, excl, err := EmitJobs(cfg, []Candidate{paired, evidenceOnly}, gs)
	if err != nil {
		t.Fatalf("EmitJobs: %v", err)
	}
	if len(excl) != 0 || len(jobs) != 2 {
		t.Fatalf("jobs=%d excl=%d, want 2 jobs and no exclusions", len(jobs), len(excl))
	}

	var evJob, pairedJob Job
	for _, j := range jobs {
		switch j.Path {
		case "pkg/utils.py":
			evJob = j
		case "pkg/api.py":
			pairedJob = j
		}
	}
	if evJob.TestPath != "" {
		t.Errorf("evidence-only job TestPath = %q, want empty", evJob.TestPath)
	}
	if evJob.CoveringTestPath != "tests/test_api.py" {
		t.Errorf("evidence-only job CoveringTestPath = %q, want tests/test_api.py (the authored-landing hint)", evJob.CoveringTestPath)
	}
	if pairedJob.CoveringTestPath != "" {
		t.Errorf("a pairing-based job's CoveringTestPath = %q, want empty — it already has TestPath as its landing hint", pairedJob.CoveringTestPath)
	}

	// Re-key the SAME evidence-only candidate alone, with cfg unchanged: if
	// the suite digest (over every candidate's TestPath — here just
	// pkg/api.py's tests/test_api.py — plus TestSurfacePaths) is what the
	// evidence-only job actually keyed on, adding a SECOND, unrelated
	// evidence-only candidate that changes NOTHING about the test surface
	// must not move its key; but changing what the suite digest covers
	// (adding a new file to TestSurfacePaths) MUST move it — proving the key
	// is a function of the suite, not of the (empty) per-file TestPath.
	widerCfg := cfg
	widerCfg.TestSurfacePaths = []string{"tests/test_api.py"} // already covered; digest unchanged
	same, _, err := EmitJobs(widerCfg, []Candidate{paired, evidenceOnly}, gs)
	if err != nil {
		t.Fatalf("EmitJobs (same surface): %v", err)
	}
	var sameEvKey string
	for _, j := range same {
		if j.Path == "pkg/utils.py" {
			sameEvKey = j.CacheKey
		}
	}
	if sameEvKey != evJob.CacheKey {
		t.Errorf("adding a TestSurfacePaths entry ALREADY covered by the suite digest moved the evidence-only key: %q -> %q", evJob.CacheKey, sameEvKey)
	}

	root2 := writeTree(t, map[string]string{
		"pkg/api.py":          "x = 1\n",
		"pkg/utils.py":        "y = 2\n",
		"tests/test_api.py":   "def test_a(): pass\n",
		"tests/test_extra.py": "def test_b(): pass\n",
	})
	movedCfg := cfg
	movedCfg.Root = root2
	movedCfg.TestSurfacePaths = []string{"tests/test_extra.py"}
	moved, _, err := EmitJobs(movedCfg, []Candidate{paired, evidenceOnly}, gs)
	if err != nil {
		t.Fatalf("EmitJobs (widened surface): %v", err)
	}
	var movedEvKey string
	for _, j := range moved {
		if j.Path == "pkg/utils.py" {
			movedEvKey = j.CacheKey
		}
	}
	if movedEvKey == evJob.CacheKey {
		t.Error("widening the SUITE surface did not move the evidence-only candidate's key — it is not keying on the suite digest")
	}
}
