package reposcan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ReasonDeriveFailed marks a candidate whose goal could not be derived because
// the model call FAILED — timeout, rate limit, auth, outage. Deliberately
// distinct from ReasonUngoaled, which means the model read the file and had
// nothing usable to say. Merging them would report a broken run as a repo with
// unclear code, and AuditedFraction feeds the coverage floor.
const ReasonDeriveFailed = "derive-failed"

// Deriver proposes a correctness/security goal for a file from its SOURCE
// ALONE. ok=false with a nil error means the model had nothing usable to say;
// a non-nil error means infrastructure failed.
type Deriver interface {
	Derive(ctx context.Context, c Candidate, source string) (text string, ok bool, err error)
}

type derivingGoalSource struct {
	root          string
	d             Deriver
	model         string
	engineVersion string
	retries       int
}

// NewDerivingGoalSource returns a GoalSource that asks a model for each
// candidate's goal. It reads ONLY the candidate's source: a goal derived from
// the paired test is one the test already satisfies, which would inflate every
// kill rate and make corral flatter the suites it grades. The test path is
// never opened here, so that property cannot drift.
func NewDerivingGoalSource(root string, d Deriver, model, engineVersion string, retries int) GoalSource {
	if retries < 1 {
		retries = 1
	}
	return derivingGoalSource{root: root, d: d, model: model, engineVersion: engineVersion, retries: retries}
}

func (s derivingGoalSource) GoalFor(c Candidate) (Goal, bool, error) {
	src, err := os.ReadFile(filepath.Join(s.root, filepath.FromSlash(c.Path))) // #nosec G304 -- path is a candidate produced by Enumerate, confined to root
	if err != nil {
		return Goal{}, false, fmt.Errorf("reposcan: reading %s for goal derivation: %w", c.Path, err)
	}

	var lastErr error
	for attempt := 0; attempt < s.retries; attempt++ {
		text, ok, derr := s.d.Derive(context.Background(), c, string(src))
		if derr != nil {
			lastErr = derr
			continue
		}
		if !ok || strings.TrimSpace(text) == "" {
			return Goal{}, false, nil // the file's own property: ungoaled
		}
		return Goal{
			Text:       strings.TrimSpace(text),
			Provenance: fmt.Sprintf("derived:%s@%s", s.model, s.engineVersion),
		}, true, nil
	}
	return Goal{}, false, fmt.Errorf("reposcan: deriving a goal for %s failed after %d attempt(s): %w", c.Path, s.retries, lastErr)
}
