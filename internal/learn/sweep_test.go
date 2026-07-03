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
