package main

import (
	"crypto/ed25519"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/certverify"
)

// gitInitRepo makes a throwaway git repo with one committed file and one
// uncommitted edit, returning the repo dir and the committed content.
func gitInitRepo(t *testing.T) (dir, committed string) {
	t.Helper()
	dir = t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	committed = "committed contents\n"
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte(committed), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "file.txt")
	run("commit", "-q", "-m", "first")
	// an uncommitted edit that MUST NOT appear in the archived tree
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("DIRTY\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, committed
}

func TestExtractCommitArchivesTheCommitNotTheWorktree(t *testing.T) {
	repo, committed := gitInitRepo(t)
	// run extractCommit with cwd = repo
	restore := chdir(t, repo)
	defer restore()

	workdir, sha, cleanup, err := extractCommit("HEAD")
	if err != nil {
		t.Fatalf("extractCommit: %v", err)
	}
	defer cleanup()
	if len(sha) != 40 {
		t.Errorf("sha = %q, want a 40-char commit sha", sha)
	}
	got, err := os.ReadFile(filepath.Join(workdir, "file.txt"))
	if err != nil {
		t.Fatalf("reading extracted file: %v", err)
	}
	if string(got) != committed {
		t.Errorf("extracted file = %q, want the COMMITTED %q (uncommitted edits must be excluded)", got, committed)
	}
}

func TestSignBuildLocallyRoundTripsThroughVerify(t *testing.T) {
	t.Setenv("CORRALAI_CERTIFY_KEY", "") // force the file/generated path off the env seed
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := buildRecord{
		Repo: "owner/x", Commit: "abc123", Branch: "main",
		Command: "go test ./...", ExitCode: 0, DurationS: 1.5,
		OutputDigest: "sha256:deadbeef",
	}
	res, err := signBuildLocally(rec, priv)
	if err != nil {
		t.Fatalf("signBuildLocally: %v", err)
	}
	if res.Signature == "" || res.Head == "" || res.PublicKey == "" {
		t.Fatalf("incomplete record: %+v", res)
	}
	pub := priv.Public().(ed25519.PublicKey)
	checks, ok := certverify.VerifyRecord(certverify.Record{
		Statement: res.Statement,
		Signature: res.Signature,
		Steps:     res.Steps,
		Head:      res.Head,
		Anchored:  res.Anchored,
	}, pub, nil, true)
	if !ok {
		t.Fatalf("VerifyRecord failed on a locally-signed record: %+v", checks)
	}
}

// chdir switches to dir for the duration of the test.
func chdir(t *testing.T, dir string) func() {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	return func() { _ = os.Chdir(prev) }
}
