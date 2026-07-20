// SPDX-License-Identifier: Elastic-2.0

// Package matrix builds per-test adequacy — for each test, which of a run's
// mutants it catches — by scoring each test alone. It is the pure orchestration
// core of the tests x mutants matrix (swarm slice 5); the jail/driver wiring
// lives in internal/advpool.
package matrix

import (
	"context"
	"sync"
)

type TestRef struct{ Selector, TestFile string }

type TestAdequacy struct {
	Selector, TestFile string
	Kills              int
	KilledMutantIDs    []string
	MutantsTotal       int
	Scored             bool
	DeleteCandidate    bool
}

type Result struct {
	Rows         []TestAdequacy
	MutantsTotal int
	Catchable    bool
}

type ScoreFn func(ctx context.Context, t TestRef) (kills int, killedIDs []string, scored bool)

// Build scores every test concurrently (up to `workers`, min 1) and assembles a
// deterministic Result. A row is a DeleteCandidate iff it was Scored, there were
// mutants, and it caught none. Catchable is true iff some scored test caught at
// least one mutant — the soundness floor for auto-confirming a zero-kill finding.
func Build(ctx context.Context, tests []TestRef, mutantsTotal, workers int, score ScoreFn) Result {
	if workers < 1 {
		workers = 1
	}
	rows := make([]TestAdequacy, len(tests))
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i, tr := range tests {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, tr TestRef) {
			defer wg.Done()
			defer func() { <-sem }()
			kills, killed, scored := score(ctx, tr)
			rows[i] = TestAdequacy{
				Selector: tr.Selector, TestFile: tr.TestFile,
				Kills: kills, KilledMutantIDs: killed, MutantsTotal: mutantsTotal,
				Scored:          scored,
				DeleteCandidate: scored && mutantsTotal > 0 && kills == 0,
			}
		}(i, tr)
	}
	wg.Wait()
	res := Result{Rows: rows, MutantsTotal: mutantsTotal}
	for _, r := range rows {
		if r.Scored && r.Kills > 0 {
			res.Catchable = true
			break
		}
	}
	return res
}
