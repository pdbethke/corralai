// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/pdbethke/corralai/internal/matrix"
)

// recEvent is one beat of a replay tape — the exact {ts,kind,actor,subject,
// detail} shape the corralai.dev cockpit (recordings.astro / replay-player.js)
// reconstructs a run from. ts is a monotonic 1-based index (the scrub position);
// the cockpit orders and plays beats by it.
type recEvent struct {
	Ts      int            `json:"ts"`
	Kind    string         `json:"kind"`
	Actor   string         `json:"actor,omitempty"`
	Subject string         `json:"subject,omitempty"`
	Detail  map[string]any `json:"detail,omitempty"`
}

// matrixDeleteCandidateCaveat is the honest caveat printed on every
// delete-candidate line, verbatim, both on the CLI (renderMatrixSummary)
// and on `corral matrix list` (cmd/corral/matrix.go). A zero-kill result is
// relative to the mutants THIS run happened to plant — it is not proof the
// test guards nothing; a mutant that would have exercised the behavior may
// simply never have been generated.
const matrixDeleteCandidateCaveat = "Relative to this mutant set; a test may still guard behavior no mutant probed."

// renderMatrixSummary prints the tests×mutants matrix section (swarm slice
// 5, --matrix): how many of the dev suite's own tests were individually
// re-scored against the run's mutant set, and — the safe-to-delete list —
// which ones caught none of them. Never printed when --matrix was off or the
// phase didn't run (the caller only calls this when st.Matrix != nil).
func renderMatrixSummary(w io.Writer, res matrix.Result) {
	scored := 0
	var candidates []matrix.TestAdequacy
	for _, row := range res.Rows {
		if row.Scored {
			scored++
		}
		if row.DeleteCandidate {
			candidates = append(candidates, row)
		}
	}
	fmt.Fprintf(w, "\nmatrix: %d test(s) scored, %d delete-candidate(s)\n", scored, len(candidates))
	for _, row := range candidates {
		fmt.Fprintf(w, "  • %s — caught 0 of %d planted mutants — review for deletion. %s\n",
			row.Selector, row.MutantsTotal, matrixDeleteCandidateCaveat)
	}
}

// matrixTapeDetail is the pool_matrix tape event's detail payload — the same
// counts renderMatrixSummary prints, plus the raw delete-candidate selectors,
// so a cockpit replay can show the matrix beat without re-deriving it from
// the driver.
func matrixTapeDetail(res matrix.Result) map[string]any {
	scored := 0
	candidates := []string{}
	for _, row := range res.Rows {
		if row.Scored {
			scored++
		}
		if row.DeleteCandidate {
			candidates = append(candidates, row.Selector)
		}
	}
	return map[string]any{
		"tests_scored":      scored,
		"tests_total":       len(res.Rows),
		"mutants_total":     res.MutantsTotal,
		"delete_candidates": candidates,
	}
}

// recordSink collects a --local run's events into a replayable tape. It doubles
// as the driver's advpool.EventSink (the pool_subject/pool_dev_adequacy/
// pool_verdict reasoning beats) AND is fed the task lifecycle + findings from
// the in-process drive loop, so one ordered stream carries everything the
// cockpit needs. Concurrency-safe: the driver and the drive loop are the same
// goroutine here, but guard anyway so a future concurrent worker stays correct.
type recordSink struct {
	mu     sync.Mutex
	ts     int
	events []recEvent
}

func (r *recordSink) add(kind, actor, subject string, detail map[string]any) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ts++
	r.events = append(r.events, recEvent{Ts: r.ts, Kind: kind, Actor: actor, Subject: subject, Detail: detail})
}

// Emit implements advpool.EventSink: the driver's pool reasoning beats, all
// attributed to the pool itself (matching the corral-advpool actor the hosted
// recordings use).
func (r *recordSink) Emit(_ int64, kind, subject string, detail map[string]any) {
	r.add(kind, "corral-advpool", subject, detail)
}

func (r *recordSink) writeTape(path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.events == nil {
		r.events = []recEvent{}
	}
	data, err := json.MarshalIndent(map[string]any{"events": r.events}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644) // #nosec G306 -- a replay tape is a public artifact
}

// recordActor renders a stable, role-distinct worker id for the tape's roster/
// canvas, e.g. "claude-sonnet-5/test-writer" — so the decorrelated herd shows
// each seat separately even when two roles share a model.
func recordActor(role, model string) string {
	if model == "" {
		return role
	}
	return model + "/" + role
}
