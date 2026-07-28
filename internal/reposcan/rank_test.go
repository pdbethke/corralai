package reposcan

import (
	"os/exec"
	"testing"
)

func gitInit(t *testing.T, root string, commits [][]string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git unavailable or failed (%v): %s", err, out)
		}
	}
	run("init", "-q")
	for _, files := range commits {
		args := append([]string{"add"}, files...)
		run(args...)
		run("commit", "-q", "-m", "c", "--no-gpg-sign")
	}
}

func TestRankPutsHighChurnLargeFilesFirst(t *testing.T) {
	root := writeTree(t, map[string]string{
		"hot.go":       "package p\n" + string(make([]byte, 4000)),
		"hot_test.go":  "package p\n",
		"cold.go":      "package p\n",
		"cold_test.go": "package p\n",
	})
	// hot.go is touched three times, cold.go once.
	gitInit(t, root, [][]string{
		{"cold.go", "cold_test.go", "hot.go", "hot_test.go"},
		{"hot.go"},
		{"hot.go"},
	})

	cands := []Candidate{
		{Path: "cold.go", TestPath: "cold_test.go", Lang: "go"},
		{Path: "hot.go", TestPath: "hot_test.go", Lang: "go"},
	}
	got, info, err := Rank(root, cands)
	if err != nil {
		t.Fatalf("Rank: %v", err)
	}
	if info.Signal != "churn-x-size" {
		t.Errorf("Signal = %q, want churn-x-size", info.Signal)
	}
	if got[0].Path != "hot.go" {
		t.Errorf("ranked %q first, want hot.go (more churn, larger)", got[0].Path)
	}
}

// Without git history — a tarball, a shallow clone — ranking degrades to size
// and SAYS SO rather than failing. A scan of an exported tree must still run.
func TestRankDegradesToSizeWithoutGitHistory(t *testing.T) {
	root := writeTree(t, map[string]string{
		"big.go":       "package p\n" + string(make([]byte, 4000)),
		"big_test.go":  "package p\n",
		"tiny.go":      "package p\n",
		"tiny_test.go": "package p\n",
	})
	cands := []Candidate{
		{Path: "tiny.go", TestPath: "tiny_test.go", Lang: "go"},
		{Path: "big.go", TestPath: "big_test.go", Lang: "go"},
	}
	got, info, err := Rank(root, cands)
	if err != nil {
		t.Fatalf("Rank must not fail without git: %v", err)
	}
	if info.Signal != "size-only" {
		t.Errorf("Signal = %q, want size-only", info.Signal)
	}
	if info.Note == "" {
		t.Error("degraded ranking must explain itself")
	}
	if got[0].Path != "big.go" {
		t.Errorf("ranked %q first, want big.go", got[0].Path)
	}
}

// Ties must not reorder run to run: a scan of the same tree produces the same
// selection, or two runs disagree about what they covered.
func TestRankIsStableForTies(t *testing.T) {
	root := writeTree(t, map[string]string{
		"a.go": "package p\n", "a_test.go": "package p\n",
		"b.go": "package p\n", "b_test.go": "package p\n",
	})
	cands := []Candidate{
		{Path: "a.go", TestPath: "a_test.go", Lang: "go"},
		{Path: "b.go", TestPath: "b_test.go", Lang: "go"},
	}
	first, _, err := Rank(root, cands)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		again, _, err := Rank(root, cands)
		if err != nil {
			t.Fatal(err)
		}
		for j := range first {
			if again[j].Path != first[j].Path {
				t.Fatalf("run %d reordered ties: %v vs %v", i, again, first)
			}
		}
	}
}
