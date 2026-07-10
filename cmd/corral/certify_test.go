// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeRunner is a cmdRunner double: git context comes from a fixed map (the
// tests inject --repo/--commit/--branch instead, so this stays empty in most
// cases), and the checked command returns a fixed exit code + output.
type fakeRunner struct {
	gitOutputs map[string]string
	exitCode   int
	output     []byte
	runErr     error

	ranArgv []string
}

func (f *fakeRunner) GitOutput(args ...string) (string, error) {
	return f.gitOutputs[strings.Join(args, " ")], nil
}

func (f *fakeRunner) RunCommand(argv []string, stdout, stderr io.Writer) (int, time.Duration, []byte, error) {
	f.ranArgv = argv
	if f.output != nil {
		_, _ = stdout.Write(f.output)
	}
	if f.runErr != nil {
		return 0, 5 * time.Millisecond, f.output, f.runErr
	}
	return f.exitCode, 5 * time.Millisecond, f.output, nil
}

// fakePoster is a buildPoster double capturing exactly what runCertify built.
type fakePoster struct {
	brainURL string
	rec      buildRecord
	result   buildResult
	err      error
	called   bool
}

func (f *fakePoster) Post(_ context.Context, brainURL string, rec buildRecord) (buildResult, error) {
	f.called = true
	f.brainURL = brainURL
	f.rec = rec
	return f.result, f.err
}

func stubResult() buildResult {
	return buildResult{
		ID:        7,
		Head:      "deadbeef",
		Signature: "sig-hex",
		Statement: map[string]any{
			"predicateType": "https://slsa.dev/provenance/v1",
			"head":          "deadbeef",
		},
	}
}

func TestRunCertify_ExitPassthrough_Success(t *testing.T) {
	run := &fakeRunner{exitCode: 0, output: []byte("ok\n")}
	post := &fakePoster{result: stubResult()}
	var stdout, stderr bytes.Buffer

	code := runCertify([]string{
		"--brain", "https://brain.example",
		"--repo", "pdbethke/corralai",
		"--commit", "abc123",
		"--branch", "main",
		"--", "go", "test", "./...",
	}, run, post, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("expected exit 0, got %d (stderr=%s)", code, stderr.String())
	}
	if !post.called {
		t.Fatal("expected the build record to be posted")
	}
	if post.rec.ExitCode != 0 {
		t.Errorf("posted exit_code = %d, want 0", post.rec.ExitCode)
	}
}

func TestRunCertify_ExitPassthrough_Failure(t *testing.T) {
	run := &fakeRunner{exitCode: 2, output: []byte("FAIL\n")}
	post := &fakePoster{result: stubResult()}
	var stdout, stderr bytes.Buffer

	code := runCertify([]string{
		"--brain", "https://brain.example",
		"--repo", "pdbethke/corralai",
		"--commit", "abc123",
		"--branch", "main",
		"--", "go", "test", "./...",
	}, run, post, &stdout, &stderr)

	if code != 2 {
		t.Fatalf("expected exit 2 (passthrough of the check's failure), got %d", code)
	}
	if !post.called {
		t.Fatal("a failed check must still be recorded")
	}
	if post.rec.ExitCode != 2 {
		t.Errorf("posted exit_code = %d, want 2", post.rec.ExitCode)
	}
}

func TestRunCertify_RecordCarriesCommandAndGitContext(t *testing.T) {
	run := &fakeRunner{exitCode: 0, output: []byte("all good\n")}
	post := &fakePoster{result: stubResult()}
	var stdout, stderr bytes.Buffer

	code := runCertify([]string{
		"--brain", "https://brain.example",
		"--repo", "pdbethke/corralai",
		"--commit", "abc123",
		"--branch", "feat/x",
		"--produced-by", "claude-opus,codex",
		"--", "go", "test", "./...",
	}, run, post, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("unexpected exit code %d: %s", code, stderr.String())
	}
	if post.brainURL != "https://brain.example" {
		t.Errorf("brainURL = %q, want https://brain.example", post.brainURL)
	}
	if post.rec.Repo != "pdbethke/corralai" || post.rec.Commit != "abc123" || post.rec.Branch != "feat/x" {
		t.Errorf("git context not carried through: %+v", post.rec)
	}
	if post.rec.Command != "go test ./..." {
		t.Errorf("command = %q, want %q", post.rec.Command, "go test ./...")
	}
	if len(post.rec.ProducedBy) != 2 || post.rec.ProducedBy[0] != "claude-opus" || post.rec.ProducedBy[1] != "codex" {
		t.Errorf("produced_by = %v, want [claude-opus codex]", post.rec.ProducedBy)
	}
	if post.rec.OutputDigest == "" || !strings.HasPrefix(post.rec.OutputDigest, "sha256:") {
		t.Errorf("output_digest = %q, want a sha256: prefixed digest", post.rec.OutputDigest)
	}
	if stdout.String() == "" {
		t.Error("expected the certified id/head to be printed to stdout")
	}
}

func TestRunCertify_OutWritesStatement(t *testing.T) {
	run := &fakeRunner{exitCode: 0, output: []byte("ok\n")}
	post := &fakePoster{result: stubResult()}
	var stdout, stderr bytes.Buffer
	outPath := filepath.Join(t.TempDir(), "statement.json")

	code := runCertify([]string{
		"--brain", "https://brain.example",
		"--repo", "pdbethke/corralai",
		"--commit", "abc123",
		"--branch", "main",
		"--out", outPath,
		"--", "go", "test", "./...",
	}, run, post, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("unexpected exit code %d: %s", code, stderr.String())
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("expected %s to be written: %v", outPath, err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("--out file is not valid JSON: %v", err)
	}
	if got["head"] != "deadbeef" {
		t.Errorf("statement head = %v, want deadbeef", got["head"])
	}
}

func TestRunCertify_MissingBrainIsAnError(t *testing.T) {
	run := &fakeRunner{exitCode: 0}
	post := &fakePoster{result: stubResult()}
	var stdout, stderr bytes.Buffer

	code := runCertify([]string{"--repo", "x", "--commit", "y", "--", "true"}, run, post, &stdout, &stderr)

	if code == 0 {
		t.Fatal("expected a non-zero exit when --brain/$CORRAL_BRAIN is not set")
	}
	if post.called {
		t.Error("must not post when brain is unresolved")
	}
}

// A post failure (e.g. the brain is unreachable) must never mask or flip the
// check's own exit code — the check's result is the ground truth.
func TestRunCertify_PostFailureStillReturnsCheckExitCode(t *testing.T) {
	run := &fakeRunner{exitCode: 0, output: []byte("ok\n")}
	post := &fakePoster{err: context.DeadlineExceeded}
	var stdout, stderr bytes.Buffer

	code := runCertify([]string{
		"--brain", "https://brain.example",
		"--repo", "x", "--commit", "y", "--branch", "main",
		"--", "true",
	}, run, post, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("post failure must not flip a passing check's exit code, got %d", code)
	}
	if stderr.String() == "" {
		t.Error("expected a warning on stderr about the failed post")
	}

	run2 := &fakeRunner{exitCode: 5, output: []byte("bad\n")}
	post2 := &fakePoster{err: context.DeadlineExceeded}
	code2 := runCertify([]string{
		"--brain", "https://brain.example",
		"--repo", "x", "--commit", "y", "--branch", "main",
		"--", "false",
	}, run2, post2, &stdout, &stderr)
	if code2 != 5 {
		t.Fatalf("post failure must not mask a failing check's exit code, got %d", code2)
	}
}

// TestBrainTokenResolvesFromCorralaiBrainToken locks sub-change 3: the CLI
// bearer token must come from CORRALAI_BRAIN_TOKEN, NOT CORRALAI_BRAIN_KEY
// (which main.go documents as the Ed25519 *identity seed* — reusing it as a
// bearer token would be an env collision between two different secrets).
func TestBrainTokenResolvesFromCorralaiBrainToken(t *testing.T) {
	t.Setenv("CORRAL_CREDS_DIR", t.TempDir())
	t.Setenv("CORRALAI_BRAIN_KEY", "not-a-token-this-is-the-identity-seed")
	t.Setenv("CORRALAI_BRAIN_TOKEN", "the-real-bearer-token")

	tok, err := brainToken()
	if err != nil {
		t.Fatalf("brainToken: %v", err)
	}
	if tok != "the-real-bearer-token" {
		t.Fatalf("brainToken = %q, want the-real-bearer-token (must read CORRALAI_BRAIN_TOKEN, not CORRALAI_BRAIN_KEY)", tok)
	}
}

func TestBrainTokenMissingIsAnError(t *testing.T) {
	t.Setenv("CORRAL_CREDS_DIR", t.TempDir())
	t.Setenv("CORRALAI_BRAIN_TOKEN", "")
	t.Setenv("CORRALAI_BRAIN_KEY", "")

	if _, err := brainToken(); err == nil {
		t.Fatal("expected an error when CORRALAI_BRAIN_TOKEN is unset")
	}
}
