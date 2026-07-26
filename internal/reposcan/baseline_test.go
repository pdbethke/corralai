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
