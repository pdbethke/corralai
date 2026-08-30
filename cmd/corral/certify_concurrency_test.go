// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
)

// The pin that USED to say "workspace is always 1" now says how the budget is
// divided. One tree per worker makes the substrate concurrency-safe, so the
// only remaining question is how many trees the box can afford: a quarter of
// the budget (each tree's suite still wants CPU of its own), never more
// mutants than there are jobs to score, never less than one.
func TestResolveMutantConcurrency_WorkspaceSpendsAQuarterOfTheBudget(t *testing.T) {
	for _, tc := range []struct {
		name               string
		budget, jobs, want int
	}{
		{"24-core box, plenty of mutants", 24, 40, 6},
		{"small box floors at one tree", 4, 40, 1},
		{"never more trees than mutants", 24, 3, 3},
		{"degenerate budget fails closed", 0, 40, 1},
		{"degenerate job count fails closed", 24, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveMutantConcurrency(tc.budget, substrateWorkspace, 1, tc.jobs); got != tc.want {
				t.Fatalf("resolveMutantConcurrency(%d, workspace, 1, %d) = %d, want %d", tc.budget, tc.jobs, got, tc.want)
			}
		})
	}
}

// And the jail branch is UNTOUCHED by all of this: it divides the budget by
// the workers that will actually claim work, and that arithmetic is a shipped
// behaviour, not a detail. Pinned on one input so a change to the workspace
// formula that leaks into the jail one fails here.
func TestResolveMutantConcurrency_JailBranchUnchanged(t *testing.T) {
	if got := resolveMutantConcurrency(24, substrateJail, 1, 40); got != 24 {
		t.Fatalf("resolveMutantConcurrency(24, jail, 1, 40) = %d, want 24 (one active worker takes the whole budget)", got)
	}
}

// workspaceProbeRepo is a git checkout whose "suite" is a shell command that
// passes on the real file and fails on adequacy.CanaryCode — the two answers
// the concurrency probe needs, with no toolchain to install.
func workspaceProbeRepo(t *testing.T) (root string, testCmd []string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; the pool's universe comes from git ls-files")
	}
	root = t.TempDir()
	write := func(rel, body string) {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.go", "package a // OK\n")
	write("a_test.go", "package a\n")
	cmd := exec.Command("git", "init")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	// Passes against the real file, fails against the canary (which contains
	// no "OK"), in whatever directory the runner puts it — so it is a genuine
	// per-tree measurement, not a check of the operator's checkout.
	return root, []string{"sh", "-c", "grep -q OK a.go"}
}

// THE WIRING. resolveMutantConcurrency can be perfect and the pool can be
// flawless and the audit still runs on ONE checkout, sequentially, forever —
// silently, because a serial audit looks exactly like a correct one. This
// drives the real baseline seam the scan uses and pins that a pool of three
// trees was built, probed, and DISCLOSED.
func TestBaselineRunnerBuildsAndProbesTheWorkspacePool(t *testing.T) {
	root, testCmd := workspaceProbeRepo(t)

	in := localAuditInput{
		repoDir:           root,
		codePath:          "a.go",
		testPath:          "a_test.go",
		goal:              "the file keeps saying OK",
		lang:              "go",
		substrate:         substrateWorkspace,
		mutantConcurrency: 3,
		checkArgv:         testCmd,
		timeout:           time.Minute,
		concurrency:       new(adequacy.Disclosure),
		stdout:            io.Discard,
		stderr:            io.Discard,
	}

	runner, cleanup, err := baselineRunnerFor(context.Background(), in)
	if err != nil {
		t.Fatalf("baselineRunnerFor: %v", err)
	}
	defer cleanup()

	if in.concurrency.Trees != 3 {
		t.Fatalf("concurrency disclosure = %d tree(s) (note %q), want 3 — the workspace substrate is still scoring one mutant at a time",
			in.concurrency.Trees, in.concurrency.Note)
	}
	if in.concurrency.Note != "" {
		t.Fatalf("a healthy probe must disclose no downgrade reason, got %q", in.concurrency.Note)
	}

	// The pool is not just built, it RUNS: the baseline the scan's stability
	// check depends on has to pass through it.
	ok, err := runner.RunBaseline()
	if err != nil {
		t.Fatalf("RunBaseline through the pool: %v", err)
	}
	if !ok {
		t.Fatal("baseline failed through the pool — the trees are not running the suite the checkout runs")
	}
}

// The other half of the wiring: the scorer must be TOLD to use the trees.
// A pool of three with Concurrency 1 is three copies of a checkout and no
// speedup at all.
func TestWorkspaceWiringHandsTheTreeCountToTheScorer(t *testing.T) {
	root, testCmd := workspaceProbeRepo(t)

	w, err := buildJailWiring(context.Background(), jailWiringInput{
		substrate:         substrateWorkspace,
		repoDir:           root,
		codePath:          "a.go",
		testPath:          "a_test.go",
		langName:          "go",
		fsPath:            func(q string) string { return filepath.Join(root, q) },
		code:              []byte("package a // OK\n"),
		devTest:           []byte("package a\n"),
		checkArgv:         testCmd,
		timeout:           time.Minute,
		mutantConcurrency: 3,
		stdout:            io.Discard,
	})
	if err != nil {
		t.Fatalf("buildJailWiring: %v", err)
	}
	defer w.cleanup()

	if w.scorer.Concurrency != 3 {
		t.Fatalf("scorer Concurrency = %d, want 3 — the trees exist and nothing scores in them", w.scorer.Concurrency)
	}
	pool, ok := w.scorer.Jail.(*adequacy.WorkspacePool)
	if !ok {
		t.Fatalf("scorer Jail is %T, want *adequacy.WorkspacePool — a bare WorkspaceRunner mutates ONE checkout with no mutex", w.scorer.Jail)
	}
	if got := pool.Trees(); got != 3 {
		t.Fatalf("pool has %d tree(s), want 3", got)
	}
}

// A suite that is NOT concurrency-safe must come back as one tree WITH THE
// REASON, never as a corrupted parallel run. Here the "suite" asserts it is
// running in the operator's own checkout, which is true in exactly one tree
// and false in a copy.
func TestWorkspaceWiringDisclosesTheDowngrade(t *testing.T) {
	root, _ := workspaceProbeRepo(t)
	// Fails in any tree that is not the checkout itself.
	testCmd := []string{"sh", "-c", "test \"$PWD\" = \"" + root + "\" && grep -q OK a.go"}

	in := localAuditInput{
		repoDir:           root,
		codePath:          "a.go",
		testPath:          "a_test.go",
		goal:              "the file keeps saying OK",
		lang:              "go",
		substrate:         substrateWorkspace,
		mutantConcurrency: 3,
		checkArgv:         testCmd,
		timeout:           time.Minute,
		concurrency:       new(adequacy.Disclosure),
		stdout:            io.Discard,
		stderr:            io.Discard,
	}
	_, cleanup, err := baselineRunnerFor(context.Background(), in)
	if err != nil {
		t.Fatalf("baselineRunnerFor: %v", err)
	}
	defer cleanup()

	if in.concurrency.Trees != 1 {
		t.Fatalf("trees = %d, want 1: a suite that only passes in the original checkout must NOT be scored in copies", in.concurrency.Trees)
	}
	if !strings.Contains(in.concurrency.Note, "baseline failed under 3") {
		t.Fatalf("downgrade note = %q, want the probe's own reason (the operator has to be able to see WHY the audit is slow)", in.concurrency.Note)
	}
}
