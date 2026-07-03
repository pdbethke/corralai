// SPDX-License-Identifier: Elastic-2.0

package learn

import "testing"

func TestSignature(t *testing.T) {
	got := Signature(FindingSignal{Type: "missing-req", Target: "go.mod"})
	if got != "missing-req|go.mod" {
		t.Fatalf("got %q", got)
	}
}

func TestClusterLessonsGroupsNearDuplicates(t *testing.T) {
	docs := []LessonDoc{
		{Name: "go-mod-init-1", Body: "empty workspace needs go mod init before go build can pass"},
		{Name: "go-mod-init-2", Body: "run go mod init first: an empty workspace fails go build without it"},
		{Name: "go-mod-init-3", Body: "go build fails in an empty workspace until go mod init runs"},
		{Name: "css-colors", Body: "prefer var(--fg) tokens over hex literals in the swarm ui"},
	}
	clusters := ClusterLessons(docs, 3)
	if len(clusters) != 1 || len(clusters[0]) != 3 {
		t.Fatalf("expected one 3-doc cluster, got %v", clusters)
	}
	if got := ClusterLessons(docs, 4); len(got) != 0 {
		t.Fatalf("minSize=4 should produce no clusters, got %v", got)
	}
}

func TestClusterLessonsIgnoresShortDocSubsetFalsePositive(t *testing.T) {
	// A short body whose few kept tokens are largely a subset of an
	// unrelated longer body must not link: {"install","tool","globally"}
	// shares only {"install","tool"} with the long doc, yet the overlap
	// coefficient alone scores that pair 2/3 ≈ 0.67 ≥ 0.5.
	docs := []LessonDoc{
		{Name: "npm-global", Body: "install tool globally"},
		{Name: "release-pipeline", Body: "the release pipeline should install every build tool, run the linter, publish artifacts, and page on-call when coverage drops below the agreed threshold"},
	}
	if got := ClusterLessons(docs, 2); len(got) != 0 {
		t.Fatalf("unrelated short/long docs must not cluster, got %v", got)
	}
}

func TestSweepCollectsDistinctRolesAcrossSignals(t *testing.T) {
	s := open(t)
	findings := []FindingSignal{
		{Type: "flaky", Target: "ci.yml", Role: "builder", Evidence: "e1"},
		{Type: "flaky", Target: "ci.yml", Role: "tester", Evidence: "e2"},
		{Type: "flaky", Target: "ci.yml", Role: "builder", Evidence: "e3"},
	}
	opened, err := s.Sweep(findings, nil)
	if err != nil || len(opened) != 1 {
		t.Fatalf("opened=%v err=%v", opened, err)
	}
	if opened[0].Roles != "builder,tester" {
		t.Fatalf("roles should be distinct, first-seen order: got %q", opened[0].Roles)
	}
}

func TestSweepOpensProposals(t *testing.T) {
	s := open(t)
	findings := []FindingSignal{
		{Type: "missing-req", Target: "go.mod", Role: "builder", Evidence: "e1"},
		{Type: "missing-req", Target: "go.mod", Role: "builder", Evidence: "e2"},
		{Type: "missing-req", Target: "go.mod", Role: "builder", Evidence: "e3"},
		{Type: "bug", Target: "once.sh", Role: "tester", Evidence: "only twice"},
		{Type: "bug", Target: "once.sh", Role: "tester", Evidence: "still twice"},
	}
	opened, err := s.Sweep(findings, nil)
	if err != nil || len(opened) != 1 || opened[0].Signature != "missing-req|go.mod" {
		t.Fatalf("opened=%v err=%v", opened, err)
	}
	// Idempotent: same input opens nothing new (dedup bumps the pending row).
	opened2, _ := s.Sweep(findings, nil)
	if len(opened2) != 0 {
		t.Fatalf("re-sweep must not open duplicates: %v", opened2)
	}
}
