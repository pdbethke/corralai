// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"log"
	"strings"

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
	listCmd, ok := p.ListTestsCmd(run.rs.DevTestPath)
	if !ok {
		log.Printf("advpool: matrix: unavailable for %q", run.rs.Lang)
		return
	}

	out, err := d.Enumerator.Enumerate(ctx, run.rs.CodePath, run.rs.Code, run.rs.DevTestCode, listCmd)
	if err != nil {
		log.Printf("advpool: matrix: enumerate failed, skipping matrix: %v", err)
		return
	}

	sels := p.ParseTestList(out)
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
		cmd, ok := p.SingleTestCmd(tr.TestFile, tr.Selector)
		if !ok {
			return 0, nil, false
		}
		rep, serr := d.Scorer.ScoreReport(sctx, run.rs.CodePath, run.rs.Code, run.rs.DevTestCode, run.mutants, strings.Join(cmd, " "))
		if serr != nil {
			log.Printf("advpool: matrix: score failed for %q: %v", tr.Selector, serr)
			return 0, nil, false
		}
		if !rep.CompliantPass {
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
