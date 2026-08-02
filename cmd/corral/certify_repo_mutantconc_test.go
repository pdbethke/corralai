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

// THE SAFETY RULE, and the one that must never regress: WorkspaceRunner mutates
// ONE checkout in place and has NO mutex. Two concurrent applyFiles interleave
// and corrupt the tree — job B's suite runs against job A's mutant, so B's
// survivors record as KILLED and corral signs an inflated kill rate that is not
// detectable after the fact. The workspace substrate is serialized whatever the
// budget says, and unlike the file axis it cannot be "spent" elsewhere.
func TestResolveMutantConcurrency_WorkspaceAlwaysSequential(t *testing.T) {
	for _, budget := range []int{1, 4, 16, 128} {
		for _, workers := range []int{1, 2, 8} {
			if got := resolveMutantConcurrency(budget, substrateWorkspace, workers, 4); got != 1 {
				t.Fatalf("workspace substrate budget=%d workers=%d = %d, want 1 — WorkspaceRunner has no mutex and MUST NOT be parallelized", budget, workers, got)
			}
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

// And the safety boundary end-to-end: whatever the operator's budget, a
// workspace-substrate scan must hand 1 down. resolveMutantConcurrency is the
// gate, but this pins the composed result, since that is what actually decides
// whether an unsynchronized checkout gets written by two goroutines.
func TestLocalExecutor_WorkspaceSubstrateNeverParallelizesMutants(t *testing.T) {
	workers, _ := resolveScanWorkers(16, substrateWorkspace)
	got := resolveMutantConcurrency(resolveSwarm(16), substrateWorkspace, workers, 4)

	ex := newLocalExecutor("/tmp/repo", nil, substrateWorkspace, 0, nil)
	ex.mutantConcurrency = got
	in := ex.auditInputFor(reposcan.Job{Path: "src/a.go", TestPath: "src/a_test.go", Lang: "go"})
	if in.mutantConcurrency != 1 {
		t.Fatalf("workspace scan handed mutantConcurrency=%d — WorkspaceRunner has no mutex; concurrent applyFiles corrupt the tree and record survivors as KILLED", in.mutantConcurrency)
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
