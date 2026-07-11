// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

	// verify-commit double: gitVerifyRaw/gitVerifyExitZero stand in for a real
	// `git verify-commit --raw` invocation's output/exit status; gitVerifyErr
	// stands in for git being entirely unavailable (not a repo, no git binary).
	gitVerifyRaw      string
	gitVerifyExitZero bool
	gitVerifyErr      error

	ranArgv []string
}

func (f *fakeRunner) GitOutput(args ...string) (string, error) {
	return f.gitOutputs[strings.Join(args, " ")], nil
}

func (f *fakeRunner) GitVerifyCommit(sha string) (string, bool, error) {
	return f.gitVerifyRaw, f.gitVerifyExitZero, f.gitVerifyErr
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
		PublicKey: "pubkey-hex",
		Steps: []map[string]any{
			{"seq": 0.0, "kind": "execution", "hash": "deadbeef", "prev": strings.Repeat("0", 68)},
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

// TestRunCertify_OutWritesFullSelfVerifyingRecord locks sub-change 5: --out
// must write the COMPLETE record {statement, signature, steps, head,
// public_key} — not just the bare statement — so `corral certify verify`
// can check it with no brain round trip at all.
func TestRunCertify_OutWritesFullSelfVerifyingRecord(t *testing.T) {
	run := &fakeRunner{exitCode: 0, output: []byte("ok\n")}
	post := &fakePoster{result: stubResult()}
	var stdout, stderr bytes.Buffer
	outPath := filepath.Join(t.TempDir(), "record.json")

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
	for _, key := range []string{"statement", "signature", "steps", "head", "public_key"} {
		if _, ok := got[key]; !ok {
			t.Errorf("--out record missing %q key: %v", key, got)
		}
	}
	if got["head"] != "deadbeef" {
		t.Errorf("head = %v, want deadbeef", got["head"])
	}
	if got["signature"] != "sig-hex" {
		t.Errorf("signature = %v, want sig-hex", got["signature"])
	}
	if got["public_key"] != "pubkey-hex" {
		t.Errorf("public_key = %v, want pubkey-hex", got["public_key"])
	}
	steps, ok := got["steps"].([]any)
	if !ok || len(steps) != 1 {
		t.Errorf("steps = %v, want a 1-element array", got["steps"])
	}
}

// TestRunCertify_GitLinkCaptureGoodSig locks Task 2: a GOODSIG commit must
// come back with commit_signature.verified=="good" and the signer captured,
// alongside the commit message/author/date pulled via git show.
func TestRunCertify_GitLinkCaptureGoodSig(t *testing.T) {
	run := &fakeRunner{
		exitCode: 0,
		output:   []byte("ok\n"),
		gitOutputs: map[string]string{
			"show -s --format=%s abc123":        "fix the thing",
			"show -s --format=%an <%ae> abc123": "Peter Bethke <peter@example.com>",
			"show -s --format=%cI abc123":       "2026-07-09T12:00:00-05:00",
		},
		gitVerifyRaw:      "[GNUPG:] GOODSIG 1234567890ABCDEF Peter Bethke <peter@example.com>",
		gitVerifyExitZero: true,
	}
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
		t.Fatalf("unexpected exit code %d: %s", code, stderr.String())
	}
	if post.rec.CommitMessage != "fix the thing" {
		t.Errorf("commit_message = %q, want %q", post.rec.CommitMessage, "fix the thing")
	}
	if post.rec.CommitAuthor != "Peter Bethke <peter@example.com>" {
		t.Errorf("commit_author = %q, want %q", post.rec.CommitAuthor, "Peter Bethke <peter@example.com>")
	}
	if post.rec.CommitDate != "2026-07-09T12:00:00-05:00" {
		t.Errorf("commit_date = %q, want %q", post.rec.CommitDate, "2026-07-09T12:00:00-05:00")
	}
	sig := post.rec.CommitSignature
	if sig["verified"] != "good" {
		t.Fatalf("commit_signature.verified = %v, want %q (sig=%v)", sig["verified"], "good", sig)
	}
	if sig["signed"] != true {
		t.Errorf("commit_signature.signed = %v, want true", sig["signed"])
	}
	if sig["signer"] != "Peter Bethke <peter@example.com>" {
		t.Errorf("commit_signature.signer = %v, want %q", sig["signer"], "Peter Bethke <peter@example.com>")
	}
	if sig["mechanism"] != "gpg" {
		t.Errorf("commit_signature.mechanism = %v, want %q", sig["mechanism"], "gpg")
	}
}

// TestRunCertify_GitLinkCaptureUnsigned confirms an unsigned commit (verify-
// commit exits nonzero with no GOODSIG/status output) is recorded as
// verified=="unsigned" — never treated as a failure, certify still succeeds.
func TestRunCertify_GitLinkCaptureUnsigned(t *testing.T) {
	run := &fakeRunner{
		exitCode:          0,
		output:            []byte("ok\n"),
		gitVerifyRaw:      "",
		gitVerifyExitZero: false,
	}
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
		t.Fatalf("an unsigned commit must not fail certify, got exit %d: %s", code, stderr.String())
	}
	sig := post.rec.CommitSignature
	if sig["verified"] != "unsigned" {
		t.Fatalf("commit_signature.verified = %v, want %q", sig["verified"], "unsigned")
	}
	if sig["signed"] != false {
		t.Errorf("commit_signature.signed = %v, want false", sig["signed"])
	}
}

// TestRunCertify_GitLinkCaptureBadSigAndUnknownKey lock the honesty
// contract beyond the good/unsigned pair: a BADSIG verdict must never be
// reported as "good", and a signature from an unrecognized key must be
// "unknown-key", not silently promoted to "good".
func TestRunCertify_GitLinkCaptureBadSigAndUnknownKey(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"bad", "[GNUPG:] BADSIG 1234567890ABCDEF Mallory <mallory@example.com>", "bad"},
		{"unknown-key", "[GNUPG:] NO_PUBKEY 1234567890ABCDEF", "unknown-key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := &fakeRunner{
				exitCode:          0,
				output:            []byte("ok\n"),
				gitVerifyRaw:      tc.raw,
				gitVerifyExitZero: false,
			}
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
				t.Fatalf("must not fail certify, got exit %d: %s", code, stderr.String())
			}
			sig := post.rec.CommitSignature
			if sig["verified"] != tc.want {
				t.Fatalf("commit_signature.verified = %v, want %q", sig["verified"], tc.want)
			}
			if sig["signed"] != true {
				t.Errorf("commit_signature.signed = %v, want true (a signature was present, just not trustworthy)", sig["signed"])
			}
			if sig["verified"] == "good" {
				t.Fatal("must never report a BADSIG/unknown-key verdict as good")
			}
		})
	}
}

// TestRunCertify_GitLinkCaptureGitUnavailable confirms a git-unavailable
// environment (not a repo, git missing) degrades to unsigned and never
// makes certify fatal — the check already ran; recording signature status
// is best-effort telemetry, not a gate.
func TestRunCertify_GitLinkCaptureGitUnavailable(t *testing.T) {
	run := &fakeRunner{
		exitCode:     0,
		output:       []byte("ok\n"),
		gitVerifyErr: errors.New("exec: \"git\": executable file not found in $PATH"),
	}
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
		t.Fatalf("git being unavailable must never make certify fatal, got exit %d: %s", code, stderr.String())
	}
	if !post.called {
		t.Fatal("expected the build to still be posted despite git verify-commit being unavailable")
	}
	sig := post.rec.CommitSignature
	if sig["verified"] != "unsigned" {
		t.Fatalf("commit_signature.verified = %v, want %q", sig["verified"], "unsigned")
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
