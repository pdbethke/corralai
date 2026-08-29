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
