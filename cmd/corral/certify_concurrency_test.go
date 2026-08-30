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
	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/reposcan"
)

// The pin that USED to say "workspace is always 1" now says how the budget is
// divided. One tree per worker makes the substrate concurrency-safe, so the
// only remaining question is how many trees the box can afford: a quarter of
// the budget, because each tree runs a REAL suite that wants CPU of its own.
//
// The jobs argument is swept and must not matter. jobs is the FILE count, and
// this number is trees-PER-FILE; files are already serialized on this
// substrate. Capping by it pinned the headline case — a diff-scoped audit of
// ONE changed file, the shape with 23 of 24 cores idle — to a single tree
// forever.
func TestResolveMutantConcurrency_WorkspaceSpendsAQuarterOfTheBudget(t *testing.T) {
	for _, tc := range []struct {
		name         string
		budget, want int
	}{
		{"24-core box", 24, 6},
		{"small box floors at one tree", 4, 1},
		{"degenerate budget fails closed", 0, 1},
		{"negative budget fails closed", -8, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, jobs := range []int{0, 1, 3, 40} {
				if got := resolveMutantConcurrency(tc.budget, substrateWorkspace, 1, jobs); got != tc.want {
					t.Fatalf("resolveMutantConcurrency(%d, workspace, 1, %d) = %d, want %d", tc.budget, jobs, got, tc.want)
				}
			}
		})
	}
}

// THE CASE THE CAP BROKE, called out on its own because it is the one the
// design was written for: one changed file, a 24-core box, and — before this
// — exactly one tree, because len(jobs) is the number of FILES.
func TestResolveMutantConcurrency_OneFileStillGetsItsTrees(t *testing.T) {
	if got := resolveMutantConcurrency(24, substrateWorkspace, 1, 1); got != 6 {
		t.Fatalf("a one-file workspace audit on a 24-core box = %d tree(s), want 6 — the diff-scoped audit is the shape this exists for", got)
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

// ONE pool per file-job: built once, probed once.
//
// The first cut of this wiring built a pool inside the baseline-stability
// runner, deleted its N tree copies when that runner was released, and built
// a SECOND one for the audit — two copies of the whole checkout and four
// probe rounds (4N suite invocations) per file where the design priced one
// copy and two rounds. Worse, two probes of a marginally-flaky suite can
// answer differently, so the line printed on screen and the number recorded
// on the verdict could disagree about what actually ran.
//
// None of that is visible in the RESULT — both pools work — so it is counted
// at the seam instead.
func TestOnePoolIsBuiltAndProbedPerFileJob(t *testing.T) {
	root, testCmd := workspaceProbeRepo(t)

	builds, probes := 0, 0
	realNew, realProbe := newWorkspacePool, probeWorkspacePool
	t.Cleanup(func() { newWorkspacePool, probeWorkspacePool = realNew, realProbe })
	newWorkspacePool = func(ctx context.Context, r string, n int, timeout time.Duration, opts ...adequacy.WorkspaceOption) (*adequacy.WorkspacePool, adequacy.Disclosure, error) {
		builds++
		return realNew(ctx, r, n, timeout, opts...)
	}
	probeWorkspacePool = func(ctx context.Context, p *adequacy.WorkspacePool, base map[string]string, codePath, code string, cmd []string) (*adequacy.WorkspacePool, adequacy.Disclosure) {
		probes++
		return realProbe(ctx, p, base, codePath, code, cmd)
	}

	ex := newLocalExecutor(root, testCmd, substrateWorkspace, time.Minute, nil)
	ex.mutantConcurrency = 3
	// The real newBaseline (the seam's default) builds the pool; the audit is
	// stubbed down to the one thing that matters here — it drives the REAL
	// prepareAuditJail with the input Execute built, which is where a second
	// pool would be constructed.
	plug, ok := lang.ByName("go")
	if !ok {
		t.Fatal("no go plugin")
	}
	var scored int
	// The box the EXECUTED job used — not a fresh one from auditInputFor,
	// which is always nil and made the release assertion below vacuous.
	var used *workspacePool
	ex.audit = func(ctx context.Context, in localAuditInput) (advpool.Verdict, error) {
		used = in.pool
		prep, err := prepareAuditJail(ctx, in, plug, time.Minute, io.Discard)
		if err != nil {
			return advpool.Verdict{}, err
		}
		defer prep.cleanup()
		scored = prep.wiring.scorer.Concurrency
		return advpool.Verdict{DevKillRate: 1}, nil
	}

	job := reposcan.Job{Path: "a.go", TestPath: "a_test.go", Lang: "go", Goal: reposcan.Goal{Text: "keeps saying OK"}}
	if _, err := ex.Execute(context.Background(), job); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if builds != 1 || probes != 1 {
		t.Fatalf("built %d pool(s) and probed %d time(s) for ONE file, want 1 and 1 — a second pool is a second copy of the whole checkout, a second probe, and a disclosure that can disagree with the one on the verdict", builds, probes)
	}
	if scored != 3 {
		t.Fatalf("the audit scored against %d tree(s), want 3 — the audit must reuse the job's probed pool, not fall back to one tree", scored)
	}
	// And the trees are gone: the JOB owns them, so Execute releases them —
	// asserted on the box the job ACTUALLY used. N copies of a checkout left
	// in /tmp per file is how a long scan fills the disk.
	if used == nil {
		t.Fatal("the audit never saw the job's pool box")
	}
	if used.pool != nil {
		t.Fatal("Execute must release the job's trees; the box still holds a pool")
	}
}
