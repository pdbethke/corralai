// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"errors"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
)

// A run that produced no usable mutants cannot recover by being retried: the
// generator regions are dropped permanently (droppedKeys records each exactly
// once), so the seat is never re-invoked and every subsequent tick re-derives
// the same failure from the same dropped state.
//
// Observed: the generator ran 3 times, then the drive loop retried 21 more
// times WITHOUT EVER CALLING IT AGAIN, printing an identical error each round.
// driver.go's own comment already called this fatal ("Zero mutants to grade
// against is fatal regardless of why"); the error just was not classified so
// the caller could act on it.
func TestErrNoUsableMutantsIsRecognizable(t *testing.T) {
	err := ErrNoUsableMutants{Regions: 1, Dropped: 1}
	var got ErrNoUsableMutants
	if !errors.As(error(err), &got) {
		t.Fatal("ErrNoUsableMutants is not recognizable via errors.As — a caller cannot tell it is terminal")
	}
	if got.Regions != 1 || got.Dropped != 1 {
		t.Errorf("counts lost through errors.As: regions=%d dropped=%d", got.Regions, got.Dropped)
	}
}

// It must survive wrapping, because the driver returns it up through tick
// plumbing that adds context.
func TestErrNoUsableMutantsSurvivesWrapping(t *testing.T) {
	wrapped := errors.Join(errors.New("advpool: run 1"), ErrNoUsableMutants{Regions: 2, Dropped: 2})
	var got ErrNoUsableMutants
	if !errors.As(wrapped, &got) {
		t.Fatal("does not survive wrapping — the drive loop sees the wrapped error, not the bare one")
	}
}

// The message must still say what it said before: an operator reading the log
// should learn the same facts, plus that retrying will not help.
func TestErrNoUsableMutantsMessage(t *testing.T) {
	msg := ErrNoUsableMutants{Regions: 3, Dropped: 2}.Error()
	for _, want := range []string{"3", "2", "nothing to grade"} {
		if !contains(msg, want) {
			t.Errorf("message lost %q: %s", want, msg)
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// IsTerminalRunErr is the SINGLE classification both tick loops call. Each
// branch is pinned here so the loops can stay one-liners, and so a future
// terminal cause cannot be added to one loop and forgotten in the other.
func TestIsTerminalRunErr(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"no usable mutants", ErrNoUsableMutants{Regions: 1, Dropped: 1}, true},
		{"no usable mutants, wrapped", errors.Join(errors.New("run 1"), ErrNoUsableMutants{Regions: 1, Dropped: 1}), true},
		{"snap toolchain", adequacy.ErrSnapToolchain{Command: "go", Path: "/snap/go/bin/go"}, true},
		{"snap toolchain, wrapped", errors.Join(errors.New("score"), adequacy.ErrSnapToolchain{Command: "go"}), true},
		// The safe default: an unknown failure is TRANSIENT. A wrongly
		// transient error costs retries; a wrongly terminal one costs a run.
		{"ordinary error", errors.New("connection reset"), false},
	} {
		if got := IsTerminalRunErr(c.err); got != c.want {
			t.Errorf("IsTerminalRunErr(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}
