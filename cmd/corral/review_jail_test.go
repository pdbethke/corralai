// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/sandbox"
)

// requireReviewSandbox skips, by name, where no sandbox can isolate: a
// review's reproductions run only in the jail.
func requireReviewSandbox(t *testing.T) {
	t.Helper()
	if _, err := resolveScanJail(); err != nil {
		t.Skipf("SKIP: no sandbox for review reproductions: %v", err)
	}
}

// A reproduction is a model's shell script. It runs in the same offline jail
// certify's tests run in: it sees the tree at the commit and the operator's
// dependency directories (read-only), and nothing else of the host. Before
// this it ran in a plain worktree on the host, with the network on.
func TestReviewReproductionsRunInTheJail(t *testing.T) {
	requireReviewSandbox(t)
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n\nfunc Add(a, b int) int { return a - b }\n")
	mustWrite(t, filepath.Join(root, ".gitignore"), "node_modules/\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	// Untracked, as installed dependencies are: absent from the commit, bound
	// from the operator's checkout.
	mustWrite(t, filepath.Join(root, "node_modules", "dep", "index.js"), "module.exports = 1\n")

	host := t.TempDir()
	escaped := filepath.Join(host, "escaped")
	orig := newReviewerBackend
	t.Cleanup(func() { newReviewerBackend = orig })
	newReviewerBackend = func(string, string) (agentbackend.Backend, error) {
		return cannedReviewer{reply: `{"opinion":"R1 sees the tree; R2 writes to the host.",
"findings":[
 {"claim":"the tree and its dependencies are there","tier":"REPRODUCED","file":"pkg/a.go","line":3,"severity":"high","script":"grep -q 'return a - b' pkg/a.go && test -f node_modules/dep/index.js"},
 {"claim":"a script can write to the host","tier":"REPRODUCED","file":"pkg/a.go","line":1,"severity":"low","script":"echo leaked > ` + escaped + `"}
],"sound":[]}`}, nil
	}

	ledger := filepath.Join(t.TempDir(), "ledger")
	var out, errb bytes.Buffer
	if code := runReview([]string{"--repo", root, "--scope", "pkg", "--reviewer-model", "reviewer-x", "--ledger", ledger}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s %s", code, out.String(), errb.String())
	}
	s := out.String()
	if !strings.Contains(s, "R1  REPRODUCED") {
		t.Errorf("the script could not see the tree at the commit or the bound dependencies:\n%s", s)
	}
	if !strings.Contains(s, "R2  CODE-READ (declared REPRODUCED)") {
		t.Errorf("a script writing outside the jail was not demoted:\n%s", s)
	}
	if _, err := os.Stat(escaped); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a reproduction wrote to the host: %v", err)
	}
	entries, err := auditpush.ReadLedgerDir(ledger)
	if err != nil || len(entries) != 1 {
		t.Fatalf("ledger: %v", err)
	}
	if got := entries[0].Review.Substrate; !strings.HasPrefix(got, "jail") {
		t.Errorf("the entry records substrate %q, want the jail", got)
	}
}

// With no sandbox, review refuses before any seat is called — the same rule
// certify keeps, never a fallback to the host.
func TestReviewRefusesWithoutASandboxBeforeAnySpend(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "a.go"), "package a\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")

	origJail := resolveJailFn
	t.Cleanup(func() { resolveJailFn = origJail })
	resolveJailFn = func(string, bool) (sandbox.Isolator, error) { return nil, errors.New("no bwrap here") }
	orig := newReviewerBackend
	t.Cleanup(func() { newReviewerBackend = orig })
	newReviewerBackend = func(string, string) (agentbackend.Backend, error) {
		t.Fatal("a seat was called with no sandbox to run its scripts in")
		return nil, nil
	}
	var out, errb bytes.Buffer
	code := runReview([]string{"--repo", root, "--scope", "a.go", "--reviewer-model", "reviewer-x", "--no-ledger"}, &out, &errb)
	if code != 2 || !strings.Contains(errb.String(), "no bwrap here") {
		t.Errorf("exit %d, stderr %q", code, errb.String())
	}
}

// --substrate workspace is the opt-in certify --repo also has, for where the
// caller is the isolation boundary (a CI runner, which the Action is): the
// old detached worktree on the host, needing no sandbox, and the entry says
// which one ran. Anything else is refused before any spend.
func TestReviewSubstrateWorkspaceIsOptInAndRecorded(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	root := t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "a.go"), "package a\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")

	origJail := resolveJailFn
	t.Cleanup(func() { resolveJailFn = origJail })
	resolveJailFn = func(string, bool) (sandbox.Isolator, error) { return nil, errors.New("no bwrap here") }
	orig := newReviewerBackend
	t.Cleanup(func() { newReviewerBackend = orig })
	newReviewerBackend = func(string, string) (agentbackend.Backend, error) {
		return cannedReviewer{reply: `{"opinion":"o","findings":[{"claim":"a.go exists","tier":"REPRODUCED","file":"a.go","line":1,"severity":"low","script":"test -f a.go"}],"sound":[]}`}, nil
	}
	ledger := filepath.Join(t.TempDir(), "ledger")
	var out, errb bytes.Buffer
	if code := runReview([]string{"--repo", root, "--scope", "a.go", "--reviewer-model", "reviewer-x", "--ledger", ledger, "--substrate", "workspace"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s %s", code, out.String(), errb.String())
	}
	entries, err := auditpush.ReadLedgerDir(ledger)
	if err != nil || len(entries) != 1 {
		t.Fatalf("ledger: %v", err)
	}
	if got := entries[0].Review.Substrate; !strings.HasPrefix(got, "workspace") {
		t.Errorf("the entry records substrate %q", got)
	}
	errb.Reset()
	if code := runReview([]string{"--repo", root, "--scope", "a.go", "--reviewer-model", "reviewer-x", "--no-ledger", "--substrate", "host"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "--substrate") {
		t.Errorf("an unknown substrate: exit %d, %q", code, errb.String())
	}
}

// review recheck takes the same --substrate: a host with no sandbox can still
// recheck a CI review where it is the isolation boundary, and a default
// recheck there could-not-run rather than falling back to the host.
func TestReviewRecheckTakesTheSameSubstrate(t *testing.T) {
	repo, ledger, hash := recheckFixture(t, map[string]string{"R1": "test -f marker.txt"})
	origJail := resolveJailFn
	t.Cleanup(func() { resolveJailFn = origJail })
	resolveJailFn = func(string, bool) (sandbox.Isolator, error) { return nil, errors.New("no bwrap here") }
	for _, c := range []struct {
		args []string
		code int
		want string
	}{
		{nil, 3, recheckUnrun},
		{[]string{"--substrate", "workspace"}, 0, recheckStill},
	} {
		var out, errb bytes.Buffer
		args := append([]string{"recheck", ledger, hash + "#R1", "--repo", repo, "--json"}, c.args...)
		if code := runReview(args, &out, &errb); code != c.code || !strings.Contains(out.String(), `"outcome":"`+c.want+`"`) {
			t.Errorf("%q: exit %d, %s %s", c.args, code, out.String(), errb.String())
		}
	}
}
