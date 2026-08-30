// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"testing"

	"github.com/pdbethke/corralai/internal/reposcan"
)

// The whole point of the split: ONE bounded budget of concurrent jails, divided
// between two independent axes (files in parallel × mutants in parallel). The
// invariant that must never break is workers × mutantConcurrency <= budget —
// without it the two multiply and a 16-core box opens 256 jails.
func TestResolveMutantConcurrency_NeverExceedsBudget(t *testing.T) {
	for _, budget := range []int{1, 2, 3, 5, 8, 16, 64} {
		for workers := 1; workers <= budget; workers++ {
			for _, jobs := range []int{1, 2, 3, 8, 64} {
				got := resolveMutantConcurrency(budget, substrateJail, workers, jobs)
				if got < 1 {
					t.Fatalf("budget=%d workers=%d jobs=%d: concurrency %d < 1 — must never mean unbounded", budget, workers, jobs, got)
				}
				// The real bound is over the workers that will ACTUALLY claim
				// work: a pool of 7 with 1 job only ever opens 1 file's jails.
				active := workers
				if jobs < active {
					active = jobs
				}
				if active*got > budget {
					t.Fatalf("budget=%d workers=%d jobs=%d: concurrency %d multiplies to %d jails, over budget", budget, workers, jobs, got, active*got)
				}
			}
		}
	}
}

// The case this exists for. Diff-scoped PRs — the Actions product shape — audit
// ONE file, so file-parallelism can spend none of the budget and the box sits
// idle while ~42 mutants score one at a time. Mutant concurrency picks up
// exactly that slack.
func TestResolveMutantConcurrency_SingleFileGetsTheWholeBudget(t *testing.T) {
	if got := resolveMutantConcurrency(16, substrateJail, 1, 1); got != 16 {
		t.Fatalf("one file on a 16 budget = %d, want the whole 16 — this is the diff-scoped case the lever exists for", got)
	}
}

// And the converse: when files already saturate the budget, mutant scoring must
// stay sequential rather than stacking on top of it.
func TestResolveMutantConcurrency_SaturatedByFilesStaysSequential(t *testing.T) {
	if got := resolveMutantConcurrency(16, substrateJail, 16, 16); got != 1 {
		t.Fatalf("16 files on a 16 budget = %d, want 1", got)
	}
}

// THE SAFETY RULE, restated for the substrate that now has private trees.
// The old rule was "workspace is ALWAYS 1", and it was right for as long as
// every job mutated ONE shared checkout with no mutex: two concurrent
// applyFiles interleave, job B's suite runs against job A's mutant, B's
// survivors record as KILLED and corral signs an inflated kill rate nobody
// can detect afterwards. adequacy.WorkspacePool removes the shared tree, so
// the rule becomes an ARITHMETIC one — a quarter of the budget, never more
// trees than mutants, never fewer than one — and the correctness argument
// moves to the pool's probe (baseline in all N at once) which is what may
// still send a run back to a single tree.
func TestResolveMutantConcurrency_WorkspaceGetsAQuarterOfTheBudget(t *testing.T) {
	for _, tc := range []struct{ budget, jobs, want int }{
		{1, 4, 1}, {4, 4, 1}, {16, 40, 4}, {128, 40, 32}, {16, 2, 2},
	} {
		for _, workers := range []int{1, 2, 8} {
			// workers is deliberately swept and deliberately IGNORED by the
			// formula: resolveScanWorkers already holds this substrate at one
			// file at a time, so dividing by a worker count that never runs
			// would hand the mutant axis nothing.
			if got := resolveMutantConcurrency(tc.budget, substrateWorkspace, workers, tc.jobs); got != tc.want {
				t.Fatalf("workspace budget=%d jobs=%d workers=%d = %d, want %d", tc.budget, tc.jobs, workers, got, tc.want)
			}
		}
	}
}

// Fail closed on this branch too: a missing budget or an empty job list must
// mean one tree, never unbounded concurrency over a real checkout.
func TestResolveMutantConcurrency_WorkspaceDegenerateInputsFailClosed(t *testing.T) {
	for _, tc := range []struct{ budget, jobs int }{{0, 4}, {-1, 4}, {16, 0}, {16, -2}, {-3, -3}} {
		if got := resolveMutantConcurrency(tc.budget, substrateWorkspace, 1, tc.jobs); got != 1 {
			t.Fatalf("workspace budget=%d jobs=%d = %d, want 1 (fail closed)", tc.budget, tc.jobs, got)
		}
	}
}

// A nonsense budget or worker count must fail CLOSED to sequential, never to
// unbounded. Zero/negative is the shape a missing flag takes.
func TestResolveMutantConcurrency_DegenerateInputsFailClosed(t *testing.T) {
	for _, tc := range []struct{ budget, workers int }{
		{0, 0}, {0, 4}, {-1, 1}, {4, 0}, {4, -3}, {-5, -5},
	} {
		if got := resolveMutantConcurrency(tc.budget, substrateJail, tc.workers, 1); got != 1 {
			t.Fatalf("budget=%d workers=%d = %d, want 1 (fail closed)", tc.budget, tc.workers, got)
		}
	}
}

// The wiring, not just the arithmetic. resolveMutantConcurrency can be perfect
// and the feature still do NOTHING if the value never reaches the scorer — and
// that failure is SILENT: the scan simply stays sequential and looks fine. Pin
// that the executor hands it to each file's audit input.
func TestLocalExecutor_PassesMutantConcurrencyToAudit(t *testing.T) {
	ex := newLocalExecutor("/tmp/repo", nil, substrateJail, 0, nil)
	ex.mutantConcurrency = 7

	in := ex.auditInputFor(reposcan.Job{Path: "src/a.go", TestPath: "src/a_test.go", Lang: "go"})
	if in.mutantConcurrency != 7 {
		t.Fatalf("audit input mutantConcurrency = %d, want 7 — the budget never reached the scorer, so the scan would silently stay sequential", in.mutantConcurrency)
	}
}

// And the composed result end-to-end: a workspace-substrate scan must hand
// the file's audit the TREE COUNT, because that is the number that decides
// how many private trees the pool builds and how many mutants score at once.
// It used to be pinned to 1 here; the pin moved to the pool's probe, which is
// the only thing that can actually establish the suite is safe under N.
func TestLocalExecutor_WorkspaceSubstratePassesTheTreeCountDown(t *testing.T) {
	workers, _ := resolveScanWorkers(16, substrateWorkspace)
	got := resolveMutantConcurrency(resolveSwarm(16), substrateWorkspace, workers, 40)
	if got != 4 {
		t.Fatalf("composed workspace concurrency = %d, want 4 (a quarter of --swarm 16)", got)
	}

	ex := newLocalExecutor("/tmp/repo", nil, substrateWorkspace, 0, nil)
	ex.mutantConcurrency = got
	in := ex.auditInputFor(reposcan.Job{Path: "src/a.go", TestPath: "src/a_test.go", Lang: "go"})
	if in.mutantConcurrency != 4 {
		t.Fatalf("audit input mutantConcurrency = %d, want 4 — the tree count never reached the pool, so the audit stays serial and silent about it", in.mutantConcurrency)
	}
	if in.concurrency == nil {
		t.Fatal("audit input has no concurrency sink — the probe's answer would be discarded and the report could not disclose it")
	}
}

// THE REGRESSION THIS FEATURE ALMOST SHIPPED WITH. resolveScanWorkers sizes the
// pool from the host's cores and knows nothing about how many files were
// selected, so a 1-file scan on an 8-core box reports 7 workers with 6 idle.
// Dividing the budget by the CONFIGURED 7 yields 1 — silently disabling the
// feature in precisely the diff-scoped case it was built for. Found by running
// it on the box; the unit tests were passing, because they had been fed the
// honest-but-wrong configured worker count.
func TestResolveMutantConcurrency_IdleWorkersDoNotEatTheBudget(t *testing.T) {
	// 8-core box: budget 7, pool 7, but only ONE file to audit.
	if got := resolveMutantConcurrency(7, substrateJail, 7, 1); got != 7 {
		t.Fatalf("1 job on a 7-worker pool = %d, want 7 — idle workers must not consume the budget", got)
	}
	// Two files: two workers actually run, so each gets half.
	if got := resolveMutantConcurrency(8, substrateJail, 8, 2); got != 4 {
		t.Fatalf("2 jobs on an 8-worker pool = %d, want 4", got)
	}
}
