// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// countingJail records how many RunTest calls are in flight at once, so a test
// can prove mutants actually ran concurrently rather than merely being allowed
// to. Deliberately concurrency-safe itself — unlike WorkspaceRunner, which is
// exactly why the parallel path must never be enabled for that substrate.
type countingJail struct {
	mu       sync.Mutex
	inFlight int
	maxSeen  int
	// killOn reports, for a given mutant code, whether the suite FAILS (a kill).
	killOn map[string]bool
	// hold is how long each call lingers, so overlap is observable. A sleep
	// rather than a release-gate on purpose: a gate opened only once N calls
	// overlap deadlocks on the SEQUENTIAL compliant and canary runs that
	// precede the mutants and can never overlap with anything.
	hold time.Duration
}

func (j *countingJail) RunTest(ctx context.Context, files map[string]string, cmd []string) (bool, error) {
	j.mu.Lock()
	j.inFlight++
	if j.inFlight > j.maxSeen {
		j.maxSeen = j.inFlight
	}
	j.mu.Unlock()

	if j.hold > 0 {
		time.Sleep(j.hold)
	}

	j.mu.Lock()
	j.inFlight--
	j.mu.Unlock()

	code := files["code.py"]
	// The canary MUST fail, or Score fails closed (CanaryKilled=false) and
	// returns before scoring a single mutant — a suite that passes on
	// deliberately invalid source provably never reads the file.
	if code == CanaryCode {
		return false, nil
	}
	if kill, ok := j.killOn[code]; ok && kill {
		return false, nil // suite failed => mutant killed
	}
	return true, nil
}

func (j *countingJail) peakConcurrency() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.maxSeen
}

func mutantsN(n int) []Mutant {
	out := make([]Mutant, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, Mutant{ID: fmt.Sprintf("m%d", i), Code: fmt.Sprintf("mut%d", i)})
	}
	return out
}

// TestScoreConcurrency_PreservesDeterministicOrder is the property that makes
// parallelism safe to adopt at all. Score's contract is that Killed/Survived
// follow the mutants' own slice order and are never collected via a map, so the
// report is reproducible. Concurrency must not weaken that: an ordering that
// depended on scheduling would make two runs of identical inputs disagree, and
// this report is signed into a tamper-evident ledger.
func TestScoreConcurrency_PreservesDeterministicOrder(t *testing.T) {
	muts := mutantsN(12)
	// Kill every third mutant, so both slices are non-trivially interleaved.
	kill := map[string]bool{}
	for i, m := range muts {
		kill[m.Code] = i%3 == 0
	}

	run := func(conc int) Report {
		t.Helper()
		j := &countingJail{killOn: kill}
		rep, err := Score(context.Background(), j, map[string]string{}, "code.py", "clean", muts,
			[]string{"pytest"}, WithConcurrency(conc))
		if err != nil {
			t.Fatalf("Score(concurrency=%d): %v", conc, err)
		}
		return rep
	}

	seq := run(1)
	par := run(6)

	if len(seq.Killed) == 0 || len(seq.Survived) == 0 {
		t.Fatalf("degenerate fixture: killed=%v survived=%v", seq.Killed, seq.Survived)
	}
	if fmt.Sprint(seq.Killed) != fmt.Sprint(par.Killed) {
		t.Errorf("Killed order differs under concurrency:\n seq=%v\n par=%v", seq.Killed, par.Killed)
	}
	if fmt.Sprint(seq.Survived) != fmt.Sprint(par.Survived) {
		t.Errorf("Survived order differs under concurrency:\n seq=%v\n par=%v", seq.Survived, par.Survived)
	}
	if seq.Total != par.Total || seq.KillRate() != par.KillRate() {
		t.Errorf("totals differ: seq=%d/%v par=%d/%v", seq.Total, seq.KillRate(), par.Total, par.KillRate())
	}
}

// TestScoreConcurrency_ActuallyRunsInParallel proves the option does something.
// A bound that is accepted and then ignored is the silently-discarded-input
// shape this codebase keeps producing, and here it would quietly leave the
// entire speedup on the floor while looking configured.
func TestScoreConcurrency_ActuallyRunsInParallel(t *testing.T) {
	j := &countingJail{killOn: map[string]bool{}, hold: 30 * time.Millisecond}

	if _, err := Score(context.Background(), j, map[string]string{}, "code.py", "clean", mutantsN(8),
		[]string{"pytest"}, WithConcurrency(4)); err != nil {
		t.Fatalf("Score: %v", err)
	}
	if got := j.peakConcurrency(); got < 2 {
		t.Fatalf("peak concurrent RunTest calls = %d — the bound was accepted but never used", got)
	}
	if got := j.peakConcurrency(); got > 4 {
		t.Fatalf("peak concurrent RunTest calls = %d, want <= the requested bound of 4", got)
	}
}

// TestScoreConcurrency_DefaultIsSequential pins that omitting the option keeps
// today's behaviour exactly. The workspace substrate shares one checkout and has
// no mutex, so anything that made parallelism the default would corrupt it.
func TestScoreConcurrency_DefaultIsSequential(t *testing.T) {
	j := &countingJail{killOn: map[string]bool{}, hold: 5 * time.Millisecond}
	if _, err := Score(context.Background(), j, map[string]string{}, "code.py", "clean", mutantsN(6),
		[]string{"pytest"}); err != nil {
		t.Fatalf("Score: %v", err)
	}
	if got := j.peakConcurrency(); got != 1 {
		t.Fatalf("peak concurrent calls = %d with no option set, want 1 — parallelism must be opt-in", got)
	}
}
