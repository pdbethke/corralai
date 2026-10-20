// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/auditpush"
)

// reviewLedgerFixture runs one canned review over a tiny committed repo and
// returns the checkout, so each test below varies only where the ledgers are.
func reviewLedgerFixture(t *testing.T, extra ...string) (root string, code int, out, errs string) {
	t.Helper()
	requireReviewSandbox(t)
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	root = t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n\nfunc Add(a, b int) int { return a - b }\n")
	mustWrite(t, filepath.Join(root, "go.mod"), "module x\n\ngo 1.22\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")

	orig := newReviewerBackend
	t.Cleanup(func() { newReviewerBackend = orig })
	newReviewerBackend = func(string, string) (agentbackend.Backend, error) {
		return cannedReviewer{reply: `{"opinion":"o","findings":[{"claim":"a caller might overflow","tier":"HYPOTHESIS"}],"sound":[]}`}, nil
	}
	var o, e bytes.Buffer
	args := append([]string{"--repo", root, "--scope", "pkg", "--reviewer-model", "reviewer-x"}, extra...)
	code = runReview(args, &o, &e)
	return root, code, o.String(), e.String()
}

// TestReviewWritesMachineLedger: a review lands in the repo ledger AND, as the
// same entry, in the machine ledger; and --no-ledger writes to neither.
func TestReviewWritesMachineLedger(t *testing.T) {
	machine := filepath.Join(t.TempDir(), "machine")
	t.Setenv("CORRALAI_MACHINE_LEDGER", machine)
	ledger := filepath.Join(t.TempDir(), "ledger")
	root, code, out, errs := reviewLedgerFixture(t, "--ledger", ledger)
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errs)
	}
	repoEntries, _ := auditpush.ReadLedgerDir(ledger)
	machEntries, _ := auditpush.ReadLedgerDir(machine)
	if len(repoEntries) != 1 || len(machEntries) != 1 {
		t.Fatalf("repo ledger %d entries, machine ledger %d; want 1 and 1\n%s", len(repoEntries), len(machEntries), out)
	}
	if machEntries[0].Kind != auditpush.KindReview || machEntries[0].Review.Commit != repoEntries[0].Review.Commit || machEntries[0].Review.Scope != repoEntries[0].Review.Scope {
		t.Errorf("the machine copy is not the same review: %+v vs %+v", machEntries[0].Review, repoEntries[0].Review)
	}
	if !strings.Contains(out, "machine ledger: entry written to "+machine) {
		t.Errorf("the machine copy was not announced:\n%s", out)
	}
	// Never inside the audited checkout.
	if got, _ := filepath.Glob(filepath.Join(root, ".corral", "*")); len(got) != 0 {
		t.Errorf("the checkout gained %v", got)
	}

	// --no-ledger: neither.
	machine2 := filepath.Join(t.TempDir(), "machine2")
	t.Setenv("CORRALAI_MACHINE_LEDGER", machine2)
	if _, code, out, errs := reviewLedgerFixture(t, "--no-ledger"); code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errs)
	}
	if m, _ := auditpush.ReadLedgerDir(machine2); len(m) != 0 {
		t.Errorf("--no-ledger wrote %d entries to the machine ledger", len(m))
	}
}

// --ledger pointed AT the machine ledger is one entry, not two in one chain;
// and an unusable machine ledger is printed without changing the exit code.
func TestReviewMachineLedgerDedupAndFailureAreSafe(t *testing.T) {
	machine := filepath.Join(t.TempDir(), "machine")
	t.Setenv("CORRALAI_MACHINE_LEDGER", machine)
	if _, code, out, errs := reviewLedgerFixture(t, "--ledger", machine); code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errs)
	}
	if m, _ := auditpush.ReadLedgerDir(machine); len(m) != 1 {
		t.Errorf("--ledger <machine> recorded the review %d times", len(m))
	}

	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	mustWrite(t, blocker, "x")
	t.Setenv("CORRALAI_MACHINE_LEDGER", blocker)
	ledger := filepath.Join(t.TempDir(), "ledger")
	_, code, out, errs := reviewLedgerFixture(t, "--ledger", ledger)
	if code != 0 {
		t.Fatalf("a machine-ledger failure changed the exit code to %d\n%s%s", code, out, errs)
	}
	if !strings.Contains(errs, "corral review: machine ledger: ") {
		t.Errorf("the failure was not printed:\n%s", errs)
	}
	if r, _ := auditpush.ReadLedgerDir(ledger); len(r) != 1 {
		t.Errorf("the repo entry must stand: %d", len(r))
	}
}
