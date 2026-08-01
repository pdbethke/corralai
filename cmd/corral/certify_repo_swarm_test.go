// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"testing"

	"github.com/pdbethke/corralai/internal/reposcan"
)

// TestPerFileSwarmSpendsTheBudgetTheSubstrateLeavesIdle fixes a measured waste.
//
// The scan pins every file's own audit to swarm:1, reasoning that the budget is
// spent on file-level fan-out and a nested per-file swarm would multiply it.
// That reasoning holds for the JAIL substrate, where files really do run
// concurrently. It does NOT hold for the WORKSPACE substrate: that one mutates a
// single checkout in place, so resolveScanWorkers already forces file-level
// concurrency to exactly 1 and prints "jobs run one at a time".
//
// So on the workspace substrate the budget was never spent by anyone. The box
// sat idle while a file's ~8 mutant-generator shards, its test-writer and its
// test-critic ran strictly one after another — measured at roughly 104s of the
// ~165s each flask file took.
//
// Safe precisely because in-process workers are LLM-only: agentworker.RunRole is
// a single model.Chat call, and nothing in the worker path references sandbox,
// adequacy or the workspace. Extra workers therefore parallelise model calls and
// never touch the shared checkout — which matters, because WorkspaceRunner has
// no mutex and concurrent applyFiles would corrupt the tree.
func TestPerFileSwarmSpendsTheBudgetTheSubstrateLeavesIdle(t *testing.T) {
	job := reposcan.Job{Path: "a.py", TestPath: "test_a.py", Lang: "python"}

	t.Run("workspace substrate spends the budget per file", func(t *testing.T) {
		ex := newLocalExecutor(t.TempDir(), []string{"true"}, substrateWorkspace, 0, nil)
		ex.perFileSwarm = 8
		if got := ex.auditInputFor(job).swarm; got != 8 {
			t.Fatalf("swarm = %d, want 8 — files are already serialized on this substrate, so the per-file audit must use the budget nobody else is spending", got)
		}
	})

	t.Run("jail substrate stays at one worker per file", func(t *testing.T) {
		ex := newLocalExecutor(t.TempDir(), []string{"true"}, "bwrap", 0, nil)
		ex.perFileSwarm = 8
		if got := ex.auditInputFor(job).swarm; got != 1 {
			t.Fatalf("swarm = %d, want 1 — files run CONCURRENTLY on a jail substrate, so a nested per-file swarm would multiply the budget by the worker count", got)
		}
	})

	t.Run("unset budget keeps today's behaviour", func(t *testing.T) {
		ex := newLocalExecutor(t.TempDir(), []string{"true"}, substrateWorkspace, 0, nil)
		if got := ex.auditInputFor(job).swarm; got != 1 {
			t.Fatalf("swarm = %d, want 1 — a zero budget must not become unbounded concurrency", got)
		}
	})
}

// TestResolveScanWorkersUnchanged pins that this does NOT alter file-level
// concurrency: the workspace substrate must still serialize files, because they
// share one checkout and WorkspaceRunner has no lock. Only the per-file worker
// count changes.
func TestResolveScanWorkersUnchanged(t *testing.T) {
	if n, _ := resolveScanWorkers(8, substrateWorkspace); n != 1 {
		t.Fatalf("file-level workers = %d, want 1 — files share one checkout and must stay serialized", n)
	}
}
