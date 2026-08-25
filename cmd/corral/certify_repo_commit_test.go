// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"os/exec"
	"testing"
)

// TestGitHeadCommitResolvesInCheckout pins the fallback that keeps the scan
// ledger's `commit` column from being empty.
//
// Every other certify mode defaults --commit to `git rev-parse HEAD`; --repo
// did not, so a scan recorded with --record named no revision and could not be
// joined to the code it graded. A row that cannot name its commit is not
// evidence a third party can check.
func TestGitHeadCommitResolvesInCheckout(t *testing.T) {
	dir := t.TempDir()
	for _, argv := range [][]string{
		{"init"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "seed"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, argv...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v: %s", err, out)
		}
	}

	got := gitHeadCommit(dir)
	if len(got) != 40 {
		t.Fatalf("gitHeadCommit(checkout) = %q, want a 40-char sha", got)
	}
}

// A path that is not a checkout must yield "" rather than a bogus value — the
// caller turns that into an explicit refusal, which is the honest outcome.
func TestGitHeadCommitEmptyOutsideCheckout(t *testing.T) {
	if got := gitHeadCommit(t.TempDir()); got != "" {
		t.Fatalf("gitHeadCommit(non-repo) = %q, want empty", got)
	}
}

// TestResolveRepoNamePrefersRemote pins the other half of scan-row provenance.
//
// The ledger recorded repo=filepath.Base(*repoDir), which is "." for every
// scan run as `--repo .` — and repo is the KEY dimension in a warehouse that
// spans projects, so every row collapsed onto one indistinguishable name.
// auditSubject already resolved this properly for the statement path; the
// ledger row did not use it.
func TestResolveRepoNamePrefersRemote(t *testing.T) {
	dir := t.TempDir()
	for _, argv := range [][]string{
		{"init"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "seed"},
		{"remote", "add", "origin", "https://github.com/acme/widget.git"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, argv...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v: %s", err, out)
		}
	}

	if got := resolveRepoName(dir, "."); got != "https://github.com/acme/widget" {
		t.Fatalf("resolveRepoName(remote) = %q, want the origin URL without .git", got)
	}
}

// With no remote, the row must still name itself distinguishably rather than
// falling back to ".".
func TestResolveRepoNameFallsBackToDirName(t *testing.T) {
	dir := t.TempDir()
	got := resolveRepoName(dir, ".")
	if got == "." || got == "" {
		t.Fatalf("resolveRepoName(no remote) = %q, want a distinguishing name", got)
	}
}

// An explicit name the operator supplied is authoritative and never overridden.
func TestResolveRepoNameKeepsExplicit(t *testing.T) {
	if got := resolveRepoName(t.TempDir(), "acme/widget"); got != "acme/widget" {
		t.Fatalf("resolveRepoName(explicit) = %q, want it preserved", got)
	}
}
