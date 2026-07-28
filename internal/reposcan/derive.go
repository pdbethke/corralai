package reposcan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ReasonDeriveFailed marks a candidate whose goal could not be derived because
// the model call FAILED — timeout, rate limit, auth, outage. Deliberately
// distinct from ReasonUngoaled, which means the model read the file and had
// nothing usable to say. Merging them would report a broken run as a repo with
// unclear code, and AuditedFraction feeds the coverage floor.
const ReasonDeriveFailed = "derive-failed"

// ReasonSourceTooLarge marks a candidate whose source is too big to hand to the
// deriver at all. It is a property of the FILE — a generated table, a vendored
// blob, a checked-in fixture — not of the infrastructure, so it gets its own
// reason instead of landing in derive-failed and reading as "the API was down".
// Without it an oversized file also burns every retry to reach a 400 that was
// never going to change.
const ReasonSourceTooLarge = "source-too-large"

// ErrSourceTooLarge is returned (wrapped) by the deriving GoalSource when a
// candidate exceeds maxSourceBytes. EmitJobs matches it with errors.Is to
// account the candidate under ReasonSourceTooLarge rather than
// ReasonDeriveFailed — the outcome taxonomy this slice rests on stays distinct.
var ErrSourceTooLarge = errors.New("source exceeds the goal deriver's input cap")

// maxSourceBytes caps what is sent to the deriver.
//
// 256 KiB is roughly 64k tokens of source — already past the useful window of
// the cheap models this role runs on, and far past any hand-written file: the
// largest file in this repository is under 40 KiB. Anything above the cap is
// generated or vendored, which is exactly the class of file that has no
// hand-authored correctness property worth mutating for. Checked BEFORE the
// read, so an enormous file costs neither the memory nor a provider round-trip.
const maxSourceBytes int64 = 256 << 10

// defaultDeriveBackoff is the first inter-attempt delay; each further attempt
// doubles it. Derivation is SEQUENTIAL, so the worst case a scan can inherit is
// bounded: with the production 3 attempts it is 0.5s + 1s = 1.5s per file that
// fails outright. Retrying a 429 with no delay at all is close to decorative —
// the three calls land inside the same rate-limit window — and every wasted
// retry shrinks the audited surface.
const defaultDeriveBackoff = 500 * time.Millisecond

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

	// maxBytes is the input cap; zero means maxSourceBytes.
	maxBytes int64
	// baseDelay is the first inter-attempt delay, doubled per attempt.
	baseDelay time.Duration
	// sleep is the test seam. Nil means time.Sleep, so no test that wants to
	// observe the backoff schedule has to actually wait for it.
	sleep func(time.Duration)
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
	return derivingGoalSource{
		root: root, d: d, model: model, engineVersion: engineVersion, retries: retries,
		maxBytes:  maxSourceBytes,
		baseDelay: defaultDeriveBackoff,
	}
}

func (s derivingGoalSource) GoalFor(c Candidate) (Goal, bool, error) {
	limit := s.maxBytes
	if limit <= 0 {
		limit = maxSourceBytes
	}
	full := filepath.Join(s.root, filepath.FromSlash(c.Path))

	// Size is checked before the file is read, so an oversized candidate never
	// costs the memory, and never costs the retries it would burn reaching a
	// provider's context-length 400.
	if fi, err := os.Stat(full); err == nil && fi.Size() > limit {
		return Goal{}, false, fmt.Errorf("reposcan: %s: %w (%d bytes, cap %d)", c.Path, ErrSourceTooLarge, fi.Size(), limit)
	}

	src, err := os.ReadFile(full) // #nosec G304 -- path is a candidate produced by Enumerate, confined to root
	if err != nil {
		return Goal{}, false, fmt.Errorf("reposcan: reading %s for goal derivation: %w", c.Path, err)
	}
	// Re-checked against what was actually read: the Stat above can be skipped
	// (a Stat error is not fatal here) or raced by a writer, and the cap is the
	// thing that keeps an oversized file out of the derive-failed bucket.
	if int64(len(src)) > limit {
		return Goal{}, false, fmt.Errorf("reposcan: %s: %w (%d bytes, cap %d)", c.Path, ErrSourceTooLarge, len(src), limit)
	}

	var lastErr error
	for attempt := 0; attempt < s.retries; attempt++ {
		text, ok, derr := s.d.Derive(context.Background(), c, string(src))
		if derr != nil {
			lastErr = derr
			// Backoff between attempts, never after the last one: sleeping on
			// the way out would delay an answer already known to be final.
			if attempt < s.retries-1 {
				s.nap(s.backoffFor(attempt))
			}
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

// backoffFor is the delay AFTER the given zero-based attempt: base, 2×base,
// 4×base, ...
func (s derivingGoalSource) backoffFor(attempt int) time.Duration {
	base := s.baseDelay
	if base <= 0 {
		return 0
	}
	return base << attempt
}

func (s derivingGoalSource) nap(d time.Duration) {
	if d <= 0 {
		return
	}
	if s.sleep != nil {
		s.sleep(d)
		return
	}
	time.Sleep(d)
}
