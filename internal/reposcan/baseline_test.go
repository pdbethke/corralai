package reposcan

import (
	"errors"
	"testing"
)

type scriptedBaseline struct {
	results []bool
	i       int
	err     error
}

func (s *scriptedBaseline) RunBaseline() (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	r := s.results[s.i%len(s.results)]
	s.i++
	return r, nil
}

// faultyBaseline errors after N successful runs.
type faultyBaseline struct {
	results []bool
	i       int
	errAt   int // error starting at call #errAt (1-indexed)
	errMsg  string
}

func (f *faultyBaseline) RunBaseline() (bool, error) {
	f.i++
	if f.i >= f.errAt {
		return false, errors.New(f.errMsg)
	}
	r := f.results[f.i-1]
	return r, nil
}

func TestBaselineStableWhenConsistent(t *testing.T) {
	stable, err := CheckBaselineStable(&scriptedBaseline{results: []bool{true, true, true}}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !stable {
		t.Fatal("consistently-passing baseline reported unstable")
	}
}

func TestBaselineUnstableWhenFlapping(t *testing.T) {
	stable, err := CheckBaselineStable(&scriptedBaseline{results: []bool{true, false, true}}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if stable {
		t.Fatal("flapping baseline reported stable — a coin-flip score would be signed")
	}
}

// A consistently FAILING baseline is stable-but-failing: that is the existing
// BaselineFailed case, not flakiness. Distinguishing them matters because the
// reasons reported differ.
func TestBaselineConsistentFailureIsStable(t *testing.T) {
	stable, err := CheckBaselineStable(&scriptedBaseline{results: []bool{false, false}}, 2)
	if err != nil || !stable {
		t.Fatalf("stable=%v err=%v; consistent failure is stable", stable, err)
	}
}

func TestBaselineErrorPropagates(t *testing.T) {
	_, err := CheckBaselineStable(&scriptedBaseline{err: errors.New("jail exploded")}, 2)
	if err == nil {
		t.Fatal("runner error was swallowed")
	}
}

func TestBaselineRejectsZeroRuns(t *testing.T) {
	_, err := CheckBaselineStable(&scriptedBaseline{results: []bool{true}}, 0)
	if err == nil {
		t.Fatal("runs=0 should error")
	}
}

func TestBaselineRejectsOneRun(t *testing.T) {
	_, err := CheckBaselineStable(&scriptedBaseline{results: []bool{true}}, 1)
	if err == nil {
		t.Fatal("runs=1 should error")
	}
}

// Mid-loop error (2nd run onwards) must propagate, not just first-run errors.
func TestBaselineErrorPropagatesOnSecondRun(t *testing.T) {
	// First call succeeds (true), second call errors.
	_, err := CheckBaselineStable(&faultyBaseline{results: []bool{true, true}, errAt: 2, errMsg: "error on second run"}, 3)
	if err == nil {
		t.Fatal("error on second run was swallowed")
	}
}
