package main

import (
	"context"
	"crypto/ed25519"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

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

func TestRealJailRunsInSandboxOrFailsClosed(t *testing.T) {
	// This test asserts the fail-closed contract without requiring bwrap:
	// with no exec backend available/allowed, realJail.Run must return an
	// error (never a silent unsandboxed run).
	t.Setenv("AGENT_EXEC_UNSAFE_HOST", "") // ensure the unsafe host backend is NOT opted in
	dir := t.TempDir()
	_, _, _, err := realJail{}.Run(context.Background(), "true", dir, false, 5*time.Second)
	if err == nil {
		t.Skip("a real jail backend is available in this environment; fail-closed path not exercised here")
	}
	// err != nil is the required fail-closed behavior when no backend resolves.
}

// TestRealJailFailsClosedOnUnavailableBackend exercises the fail-closed
// contract DETERMINISTICALLY, regardless of whether a real bwrap backend
// exists on the host: an unknown backend name makes sandbox.Resolve error,
// so realJail.Run must return that error and never fall back to running the
// check unsandboxed. This is the security-critical property, so it must be
// verified in CI (where bwrap resolves and the skip-based test above skips).
func TestRealJailFailsClosedOnUnavailableBackend(t *testing.T) {
	t.Setenv("CORRALAI_EXEC_BACKEND", "definitely-not-a-real-backend")
	dir := t.TempDir()
	exit, out, _, err := realJail{}.Run(context.Background(), "true", dir, false, 5*time.Second)
	if err == nil {
		t.Fatal("realJail.Run returned nil error with an unavailable backend — it must fail closed, never run unsandboxed")
	}
	if exit != -1 || out != nil {
		t.Errorf("fail-closed run must not report a check result: exit=%d out=%q", exit, out)
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
