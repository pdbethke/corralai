// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeDeriver struct {
	text    string
	ok      bool
	err     error
	calls   int
	sawArgs []string // every source string it was handed
}

func (f *fakeDeriver) Derive(ctx context.Context, c Candidate, source string) (string, bool, error) {
	f.calls++
	f.sawArgs = append(f.sawArgs, source)
	return f.text, f.ok, f.err
}

func derivRoot(t *testing.T) string {
	t.Helper()
	return writeTree(t, map[string]string{
		"pkg/a.go":      "package pkg // SOURCE_MARKER\n",
		"pkg/a_test.go": "package pkg // TEST_MARKER_MUST_NEVER_REACH_THE_MODEL\n",
	})
}

// THE test for this slice. Source-only is a structural guarantee, not a prompt
// instruction: a goal derived from the test is one the test already satisfies,
// so kill rates would inflate and corral would flatter every suite it grades.
func TestDeriverNeverSeesTheTest(t *testing.T) {
	root := derivRoot(t)
	f := &fakeDeriver{text: "must not panic", ok: true}
	gs := NewDerivingGoalSource(root, f, "m", "v", 2)

	if _, ok, err := gs.GoalFor(Candidate{Path: "pkg/a.go", TestPath: "pkg/a_test.go"}); err != nil || !ok {
		t.Fatalf("GoalFor: ok=%v err=%v", ok, err)
	}
	for _, seen := range f.sawArgs {
		if strings.Contains(seen, "TEST_MARKER") {
			t.Fatal("the deriver was handed test content; source-only is structural")
		}
		if !strings.Contains(seen, "SOURCE_MARKER") {
			t.Error("the deriver was not handed the source")
		}
	}
}

func TestDeriverGoalCarriesProvenance(t *testing.T) {
	root := derivRoot(t)
	gs := NewDerivingGoalSource(root, &fakeDeriver{text: "no negative balances", ok: true}, "claude-x", "v1.2.3", 2)

	g, ok, err := gs.GoalFor(Candidate{Path: "pkg/a.go", TestPath: "pkg/a_test.go"})
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if g.Text != "no negative balances" {
		t.Errorf("Text = %q", g.Text)
	}
	// Provenance is the ENTIRE defence for a machine-invented goal: there is
	// no goal-critic, so the record is the accountability mechanism.
	if g.Provenance != "derived:claude-x@v1.2.3" {
		t.Errorf("Provenance = %q, want derived:claude-x@v1.2.3", g.Provenance)
	}
}

// "Nothing usable to say" is a property of the FILE.
func TestDeriverEmptyResultIsUngoaledNotAnError(t *testing.T) {
	root := derivRoot(t)
	gs := NewDerivingGoalSource(root, &fakeDeriver{ok: false}, "m", "v", 2)

	_, ok, err := gs.GoalFor(Candidate{Path: "pkg/a.go", TestPath: "pkg/a_test.go"})
	if err != nil {
		t.Fatalf("an empty result is not an error: %v", err)
	}
	if ok {
		t.Fatal("want ungoaled")
	}
}

// Infrastructure failure is NOT ungoaled. A repo whose scan failed because the
// API was down must not be reported as a repo with unclear code.
func TestDeriverInfrastructureFailureErrorsAfterRetries(t *testing.T) {
	root := derivRoot(t)
	f := &fakeDeriver{err: errors.New("429 rate limited")}
	// Struct literal, not the constructor: the production backoff is real, and
	// this test is about the ATTEMPT COUNT, not about waiting 1.5s for it.
	gs := derivingGoalSource{root: root, d: f, model: "m", engineVersion: "v", retries: 3,
		maxBytes: maxSourceBytes, sleep: func(time.Duration) {}}

	_, ok, err := gs.GoalFor(Candidate{Path: "pkg/a.go", TestPath: "pkg/a_test.go"})
	if err == nil {
		t.Fatal("an outage must surface as an error, never as ungoaled")
	}
	if ok {
		t.Fatal("ok must be false on error")
	}
	if f.calls != 3 {
		t.Errorf("deriver called %d times, want 3 retries", f.calls)
	}
}

func TestDeriverUnreadableSourceIsAnError(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	gs := NewDerivingGoalSource(root, &fakeDeriver{text: "x", ok: true}, "m", "v", 1)
	if _, _, err := gs.GoalFor(Candidate{Path: "pkg/missing.go"}); err == nil {
		t.Fatal("a missing source file must error, not silently become ungoaled")
	}
}

// --- final-review fix wave -------------------------------------------------

// TestDeriverOversizedSourceIsItsOwnReasonNotAnOutage is I4. Without a cap the
// whole file goes to the provider, comes back a 400 (context length), and lands
// in derive-failed — the "infrastructure, not the repo" bucket — after burning
// every retry. An oversized generated blob is a property of the FILE.
func TestDeriverOversizedSourceIsItsOwnReasonNotAnOutage(t *testing.T) {
	root := writeTree(t, map[string]string{
		"pkg/big.go":      "package pkg\n" + strings.Repeat("x", 300),
		"pkg/big_test.go": "package pkg\n",
	})
	f := &fakeDeriver{text: "must not panic", ok: true}
	gs := derivingGoalSource{root: root, d: f, model: "m", engineVersion: "v", retries: 3, maxBytes: 128}

	_, ok, err := gs.GoalFor(Candidate{Path: "pkg/big.go", TestPath: "pkg/big_test.go"})
	if ok {
		t.Fatal("an oversized file must not produce a goal")
	}
	if !errors.Is(err, ErrSourceTooLarge) {
		t.Fatalf("err = %v, want ErrSourceTooLarge so EmitJobs can account it as %s", err, ReasonSourceTooLarge)
	}
	// And it must cost nothing: no call, therefore no retries either.
	if f.calls != 0 {
		t.Errorf("deriver called %d times for an oversized file, want 0", f.calls)
	}
}

// A file at the cap is still derived: the cap must not quietly shrink the
// audited surface by one byte's worth of paranoia.
func TestDeriverSourceExactlyAtTheCapIsStillDerived(t *testing.T) {
	body := "package pkg\n"
	root := writeTree(t, map[string]string{
		"pkg/a.go":      body,
		"pkg/a_test.go": "package pkg\n",
	})
	f := &fakeDeriver{text: "must not panic", ok: true}
	gs := derivingGoalSource{root: root, d: f, model: "m", engineVersion: "v", retries: 1, maxBytes: int64(len(body))}
	if _, ok, err := gs.GoalFor(Candidate{Path: "pkg/a.go"}); err != nil || !ok {
		t.Fatalf("a file exactly at the cap must derive: ok=%v err=%v", ok, err)
	}
}

// The production constructor must carry a real cap, not a zero one — a zero
// maxBytes would mean "no limit" and put I4 straight back.
func TestNewDerivingGoalSourceCarriesTheProductionCapAndBackoff(t *testing.T) {
	gs, isConcrete := NewDerivingGoalSource(t.TempDir(), &fakeDeriver{}, "m", "v", 3).(derivingGoalSource)
	if !isConcrete {
		t.Fatal("NewDerivingGoalSource no longer returns derivingGoalSource")
	}
	if gs.maxBytes != maxSourceBytes {
		t.Errorf("maxBytes = %d, want %d", gs.maxBytes, maxSourceBytes)
	}
	if gs.baseDelay != defaultDeriveBackoff {
		t.Errorf("baseDelay = %v, want %v", gs.baseDelay, defaultDeriveBackoff)
	}
}

// TestDeriverRetriesBackOffExponentially is I5: three back-to-back calls land
// inside the same rate-limit window, so retrying a 429 with no delay is close
// to decorative — and every wasted retry shrinks the audited surface.
func TestDeriverRetriesBackOffExponentially(t *testing.T) {
	root := derivRoot(t)
	f := &fakeDeriver{err: errors.New("429 rate limited")}
	var slept []time.Duration
	gs := derivingGoalSource{
		root: root, d: f, model: "m", engineVersion: "v", retries: 3,
		maxBytes:  maxSourceBytes,
		baseDelay: 100 * time.Millisecond,
		sleep:     func(d time.Duration) { slept = append(slept, d) },
	}

	if _, _, err := gs.GoalFor(Candidate{Path: "pkg/a.go"}); err == nil {
		t.Fatal("an outage must still surface as an error")
	}
	want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond}
	if len(slept) != len(want) {
		t.Fatalf("slept %v, want %v (one delay BETWEEN attempts, none after the last)", slept, want)
	}
	for i := range want {
		if slept[i] != want[i] {
			t.Errorf("delay %d = %v, want %v (exponential)", i, slept[i], want[i])
		}
	}
}

// A run that SUCCEEDS must not sleep at all.
func TestDeriverDoesNotSleepWhenTheFirstAttemptSucceeds(t *testing.T) {
	root := derivRoot(t)
	slept := 0
	gs := derivingGoalSource{
		root: root, d: &fakeDeriver{text: "must not panic", ok: true},
		model: "m", engineVersion: "v", retries: 3,
		maxBytes: maxSourceBytes, baseDelay: time.Hour,
		sleep: func(time.Duration) { slept++ },
	}
	if _, ok, err := gs.GoalFor(Candidate{Path: "pkg/a.go"}); err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if slept != 0 {
		t.Errorf("slept %d times on a first-attempt success, want 0", slept)
	}
}
