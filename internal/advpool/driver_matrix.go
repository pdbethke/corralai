// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"log"

	"github.com/pdbethke/corralai/internal/adequacy"
	golang "github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/matrix"
)

// matrixDefaultWorkers is Driver.MatrixWorkers' fallback when unset (<= 0).
const matrixDefaultWorkers = 4

// maxMatrixTests bounds the tests dimension of the tests×mutants matrix. The
// matrix runs T×M jail executions and tickMatrix blocks on all of them inside a
// single Tick (so RunDeadline, checked only between Ticks, can't bound it) — an
// enormous suite would pin the host. Every other advpool compute knob (mutants,
// shard width) is likewise ceilinged; this ceilings T. A suite larger than this
// is truncated with a loud log, never silently. Generous enough that real
// single-file suites are never clipped.
const maxMatrixTests = 500

// capMatrixTests truncates an over-large enumerated test list to maxMatrixTests,
// returning the kept selectors and how many were dropped (0 when under the cap).
// The safety ceiling that keeps the tests×mutants matrix from a runaway; pulled
// out so it is unit-testable without the whole tickMatrix jail harness.
func capMatrixTests(sels []string) (kept []string, dropped int) {
	if len(sels) <= maxMatrixTests {
		return sels, 0
	}
	return sels[:maxMatrixTests], len(sels) - maxMatrixTests
}

// tickMatrix is the tests×mutants matrix phase (swarm slice 5): once
// pool-adequacy is scored, enumerate the dev suite's individual tests and
// score EACH ALONE against the run's own mutant set, so tickAggregate can
// drive critic finding adjudication off real, per-test execution data
// instead of one re-scored selector. Opt-in on two axes (RunSpec.Matrix AND
// a wired Enumerator) and fail-soft throughout: an unavailable language
// plugin, an enumeration error, or any single test's scoring error never
// fails the audit — it just leaves run.matrix nil (or that one row
// unscored) and the pre-matrix single-test critic path in tickAggregate
// still runs. Guarded to run AT MOST ONCE per run by the matrixDone flag the
// caller (Tick) sets is checked before calling this.
func (d *Driver) tickMatrix(ctx context.Context, run *runState) {
	run.matrixDone = true // set FIRST: even a skip/failure must not retry every tick

	if !run.rs.Matrix || d.Enumerator == nil || len(run.mutants) == 0 {
		return
	}

	p := langFor(run.rs)
	argv := adequacy.ShellSplit(run.rs.TestCmd)
	// A runner whose ids are resolved through its own list is left to the
	// critic's list-and-resolve path. Its list is what the command runs, not
	// the test file's tests: the cargo rows list the crate, and a jest, mocha
	// or rspec command that already names a directory (or a .mocharc spec)
	// lists every file under it. A matrix over that would offer other files'
	// tests for deletion, and a critic's title resolved against the matrix's
	// rows would not see the tests the matrix left out.
	if golang.ResolvesByList(p, argv) {
		log.Printf("advpool: matrix: unavailable for %q (its runner lists what the command runs, not the test file; the critic path checks claims instead)", run.rs.Lang)
		return
	}
	listCmd, ok := golang.ListTestsCmd(p, argv, run.rs.DevTestPath)
	if !ok {
		log.Printf("advpool: matrix: unavailable for %q", run.rs.Lang)
		return
	}

	out, err := d.Enumerator.Enumerate(ctx, run.rs.CodePath, run.rs.Code, run.rs.DevTestCode, listCmd)
	if err != nil {
		log.Printf("advpool: matrix: enumerate failed, skipping matrix: %v", err)
		return
	}

	// Only a test that runs alone gets a row: a selection that also runs
	// another listed test (two tests sharing a full name) would credit that
	// test's kills to this one, and a zero for the pair could mark a test
	// for deletion that never ran by itself.
	listed := golang.ParseTestList(p, argv, out)
	var sels []string
	for _, id := range listed {
		if golang.RunsAlone(p, argv, listed, id) {
			sels = append(sels, id)
		}
	}
	if len(sels) == 0 {
		return
	}
	sels, dropped := capMatrixTests(sels)
	if dropped > 0 {
		log.Printf("advpool: matrix: suite has %d tests, capping to %d to bound the tests×mutants work (%d not scored this run)",
			len(sels)+dropped, maxMatrixTests, dropped)
	}
	refs := make([]matrix.TestRef, len(sels))
	for i, sel := range sels {
		refs[i] = matrix.TestRef{Selector: sel, TestFile: run.rs.DevTestPath}
	}

	workers := d.MatrixWorkers
	if workers <= 0 {
		workers = matrixDefaultWorkers
	}
	log.Printf("advpool: matrix: %d tests × %d mutants (%d cells) across %d workers",
		len(sels), len(run.mutants), len(sels)*len(run.mutants), workers)

	scoreFn := func(sctx context.Context, tr matrix.TestRef) (int, []string, bool) {
		cmd, ok := golang.SingleTestCmd(p, argv, tr.TestFile, tr.Selector)
		if !ok {
			return 0, nil, false
		}
		rep, serr := d.Scorer.ScoreReport(sctx, run.rs.CodePath, run.rs.Code, run.rs.DevTestCode, run.mutants, adequacy.ShellJoin(cmd))
		if serr != nil {
			log.Printf("advpool: matrix: score failed for %q: %v", tr.Selector, serr)
			return 0, nil, false
		}
		// Both validity flags, and CanaryKilled is NOT redundant with the dev
		// suite's: this cell issues its OWN command (SingleTestCmd for one
		// selector), and `pytest test_x.py::test_y` can fail to import the
		// module the whole-suite command imports. When that command's canary
		// survives, adequacy.Score returns CompliantPass true with Total 0 and
		// no kills — scoring it would record "this test caught nothing, delete
		// it" and auto-CONFIRM a vacuous-test finding against it, both from an
		// invocation that graded nothing, and both persisted to the
		// signed-record-linked Matrix/CriticFindings sinks. Unscored instead:
		// matrix.Build accounts ok=false as a row that was never measured.
		if !rep.CompliantPass || !rep.CanaryKilled {
			return 0, nil, false
		}
		return len(rep.Killed), rep.Killed, true
	}

	res := matrix.Build(ctx, refs, len(run.mutants), workers, scoreFn)

	// Read by RunStatus under d.mu (like verdict/authoredTest), so publish it
	// under the same lock — the brain's poller reads on a different goroutine
	// than the Tick loop.
	d.mu.Lock()
	run.matrix = &res
	d.mu.Unlock()

	deleteCandidates := 0
	for _, row := range res.Rows {
		if row.DeleteCandidate {
			deleteCandidates++
		}
	}
	log.Printf("advpool: matrix: %d delete-candidate test(s)", deleteCandidates)
}
