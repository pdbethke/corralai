package reposcan

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRun runs one git command in root, skipping the test only when git itself
// is unusable. It does NOT skip on a command that merely had nothing to do —
// see touchCommit.
func gitRun(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...) // #nosec G204 -- fixed binary, literal test args
	cmd.Dir = root
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git unusable on this host (%v): %s", err, out)
	}
}

// touchCommit CHANGES a file and commits it. The change is the point: `git
// add` on an unmodified file stages nothing, and `git commit` then fails with
// "nothing to commit, working tree clean" — which a naive helper reports as
// "git unavailable" and skips, silently leaving the churn signal untested.
func touchCommit(t *testing.T, root, rel string) {
	t.Helper()
	full := filepath.Join(root, rel)
	f, err := os.OpenFile(full, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("// touched\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", rel)
	gitRun(t, root, "commit", "-q", "-m", "c", "--no-gpg-sign")
}

func TestRankPutsHighChurnLargeFilesFirst(t *testing.T) {
	// touchCommit appends "// touched\n" (11 bytes) twice on hot.go, growing it
	// by 22 bytes. To isolate churn from size, pad cold.go so it's the same
	// size as hot.go AFTER the touches, then assert equality.
	base := "package p\n" + string(make([]byte, 4000))
	// cold.go needs extra 22 bytes to match hot.go's post-touch size.
	coldPadded := base + string(make([]byte, 22))
	root := writeTree(t, map[string]string{
		"hot.go":       base,
		"hot_test.go":  "package p\n",
		"cold.go":      coldPadded,
		"cold_test.go": "package p\n",
	})
	// hot.go is touched three times, cold.go once. Each later commit MODIFIES
	// hot.go — re-adding an unchanged file stages nothing and the commit fails.
	gitRun(t, root, "init", "-q")
	gitRun(t, root, "add", ".")
	gitRun(t, root, "commit", "-q", "-m", "c", "--no-gpg-sign")
	touchCommit(t, root, "hot.go")
	touchCommit(t, root, "hot.go")

	// Guard: files must be the same size after touches so churn is the deciding
	// factor. If this ever fails, churn cannot be isolated.
	hotInfo, err := os.Stat(filepath.Join(root, "hot.go"))
	coldInfo, err2 := os.Stat(filepath.Join(root, "cold.go"))
	if err != nil || err2 != nil {
		t.Fatalf("fixture stat failed: hot=%v cold=%v", err, err2)
	}
	if hotInfo.Size() != coldInfo.Size() {
		t.Fatalf("fixture files not equal size: hot=%d cold=%d (test cannot isolate churn)", hotInfo.Size(), coldInfo.Size())
	}

	// Assert git actually recorded the history we think it did.
	if out, err := exec.Command("git", "-C", root, "log", "--format=", "--name-only").Output(); err != nil || !strings.Contains(string(out), "hot.go") {
		t.Fatalf("fixture did not produce git history (err=%v): %s", err, out)
	}

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
		t.Errorf("ranked %q first, want hot.go (churn × size)", got[0].Path)
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

// A shallow clone has a working .git but truncated history, so git log succeeds
// with biased counts. Ranking on that would silently bias a signal disclosed in
// a report. Shallow clones degrade to size-only ranking with an explanation.
func TestRankDegradesToSizeInShallowClone(t *testing.T) {
	// Create a source repo with commit history.
	source := writeTree(t, map[string]string{
		"a.go":      "package p\n" + string(make([]byte, 100)),
		"a_test.go": "package p\n",
		"b.go":      "package p\n" + string(make([]byte, 200)),
		"b_test.go": "package p\n",
	})
	gitRun(t, source, "init", "-q")
	gitRun(t, source, "add", ".")
	gitRun(t, source, "commit", "-q", "-m", "c1", "--no-gpg-sign")
	// Modify b.go several times to give it high churn.
	for i := 0; i < 3; i++ {
		touchCommit(t, source, "b.go")
	}

	// Shallow clone into a temp directory.
	clonePath := t.TempDir()
	cloneCmd := exec.Command("git", "clone", "--depth", "1", "file://"+source, clonePath) // #nosec G204 -- fixed binary, literal test args
	if out, err := cloneCmd.CombinedOutput(); err != nil {
		t.Skipf("shallow clone failed (git may not support file:// clones): %v: %s", err, out)
	}

	// Verify the clone is actually shallow.
	isShallow := exec.Command("git", "-C", clonePath, "rev-parse", "--is-shallow-repository") // #nosec G204 -- fixed binary, literal test args
	out, err := isShallow.Output()
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		t.Fatalf("clone fixture is not shallow (err=%v, output=%q); test cannot verify shallow detection", err, string(out))
	}

	// Rank in the shallow clone; should degrade to size-only.
	cands := []Candidate{
		{Path: "a.go", TestPath: "a_test.go", Lang: "go"},
		{Path: "b.go", TestPath: "b_test.go", Lang: "go"},
	}
	got, info, err := Rank(clonePath, cands)
	if err != nil {
		t.Fatalf("Rank: %v", err)
	}
	if info.Signal != "size-only" {
		t.Errorf("Signal = %q, want size-only in shallow clone", info.Signal)
	}
	if info.Note == "" {
		t.Error("degraded ranking in shallow clone must explain itself")
	}
	// Size-only ranking should put b.go (200 bytes) before a.go (100 bytes).
	if got[0].Path != "b.go" {
		t.Errorf("ranked %q first, want b.go (size-only in shallow clone)", got[0].Path)
	}
}
