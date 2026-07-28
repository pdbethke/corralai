package reposcan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	gs := NewDerivingGoalSource(root, f, "m", "v", 3)

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
