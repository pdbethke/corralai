package reposcan

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Ranking a SUBDIRECTORY must not claim churn-x-size while every candidate
// fell to churn 1. git log names paths from the work-tree root; candidates
// are named from the scan root. (Round six, R1.)
func TestChurnMatchesCandidatesWhenScanningASubdirectory(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	sub := filepath.Join(repo, "svc", "api")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := os.WriteFile(filepath.Join(sub, "hot.go"), []byte("package a\n//"+string(rune('a'+i))+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		run("add", "-A")
		run("commit", "-qm", "c")
	}
	if err := os.WriteFile(filepath.Join(sub, "cold.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-qm", "cold")

	churn, info := fileChurn(sub)
	if info.Signal != "churn-x-size" {
		t.Fatalf("Signal = %q (%s) — history exists inside this subtree", info.Signal, info.Note)
	}
	if churn["hot.go"] < 2 {
		t.Errorf("hot.go churn = %d, want >= 2 — churn keys are work-tree-relative and never matched the scan-root-relative candidate, so every file ranked as churn 1 while the report claimed churn-x-size", churn["hot.go"])
	}
}
