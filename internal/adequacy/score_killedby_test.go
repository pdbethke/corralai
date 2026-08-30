// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
)

// killedBySuite is a one-line stand-in for a test suite: it passes only while
// a.py still says ORIGINAL, and on failure it prints the SAME short-summary
// line pytest prints. That makes it a real exercise of the parse path — the
// id in the ledger has to come from these bytes and nowhere else.
var killedBySuite = []string{"sh", "-c", `grep -q ORIGINAL a.py || { echo "FAILED a.py::test_x - boom"; exit 1; }`}

func pythonFailureParser(t *testing.T) lang.FailureParser {
	t.Helper()
	p, ok := lang.ByName("python")
	if !ok {
		t.Fatal("python plugin not registered")
	}
	fp, ok := p.(lang.FailureParser)
	if !ok {
		t.Fatal("python plugin does not implement lang.FailureParser")
	}
	return fp
}

// A KILLED mutant names the test that caught it, read out of the runner's own
// output. A SURVIVOR names nothing: no test failed, so no test can be named.
func TestScoreRecordsKilledByFromTheRunnersOutput(t *testing.T) {
	root := wsTree(t, map[string]string{"a.py": "ORIGINAL\n"})
	w := NewWorkspaceRunner(root, 0)

	rep, err := Score(context.Background(), w, map[string]string{}, "a.py", "ORIGINAL\n",
		[]Mutant{
			{ID: "m-killed", Code: "MUTANT\n"},
			{ID: "m-survivor", Code: "ORIGINAL # harmless\n"},
		},
		killedBySuite,
		WithFailureParser(pythonFailureParser(t)))
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if !rep.CompliantPass || !rep.CanaryKilled {
		t.Fatalf("fixture suite did not grade: CompliantPass=%v CanaryKilled=%v", rep.CompliantPass, rep.CanaryKilled)
	}
	if len(rep.Killed) != 1 || rep.Killed[0] != "m-killed" {
		t.Fatalf("Killed = %v, want [m-killed]", rep.Killed)
	}
	if len(rep.Survived) != 1 || rep.Survived[0] != "m-survivor" {
		t.Fatalf("Survived = %v, want [m-survivor]", rep.Survived)
	}
	if got := rep.PerMutant["m-killed"].KilledBy; got != "a.py::test_x" {
		t.Errorf("killed mutant KilledBy = %q, want %q", got, "a.py::test_x")
	}
	if got := rep.PerMutant["m-survivor"].KilledBy; got != "" {
		t.Errorf("survivor KilledBy = %q, want empty — nothing caught it", got)
	}
}

// No parser supplied: the run is byte-for-byte what it always was, and the
// column stays empty rather than being filled by a guess.
func TestScoreWithoutAParserNamesNothing(t *testing.T) {
	root := wsTree(t, map[string]string{"a.py": "ORIGINAL\n"})
	w := NewWorkspaceRunner(root, 0)

	rep, err := Score(context.Background(), w, map[string]string{}, "a.py", "ORIGINAL\n",
		[]Mutant{{ID: "m-killed", Code: "MUTANT\n"}}, killedBySuite)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if len(rep.Killed) != 1 {
		t.Fatalf("Killed = %v, want one", rep.Killed)
	}
	if got := rep.PerMutant["m-killed"].KilledBy; got != "" {
		t.Errorf("KilledBy = %q, want empty with no parser wired", got)
	}
}

// plainKilledByJail implements ONLY Jail — no RunTestDetailed. The parser is
// supplied anyway, and the run must still succeed with an empty column: the
// feature is best-effort and never a new failure mode.
type plainKilledByJail struct{}

func (plainKilledByJail) RunTest(_ context.Context, files map[string]string, _ []string) (bool, error) {
	return files["a.py"] == "ORIGINAL\n", nil
}

func TestScoreOnAJailWithoutDetailedOutputNamesNothing(t *testing.T) {
	rep, err := Score(context.Background(), plainKilledByJail{}, map[string]string{}, "a.py", "ORIGINAL\n",
		[]Mutant{{ID: "m-killed", Code: "MUTANT\n"}}, []string{"whatever"},
		WithFailureParser(pythonFailureParser(t)))
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if len(rep.Killed) != 1 || rep.Killed[0] != "m-killed" {
		t.Fatalf("Killed = %v, want [m-killed]", rep.Killed)
	}
	if got := rep.PerMutant["m-killed"].KilledBy; got != "" {
		t.Errorf("KilledBy = %q, want empty — this jail cannot report output", got)
	}
}

// The workspace runner and its pool BOTH have to offer the detailed seam, or
// substituting one for the other silently drops the column.
func TestWorkspaceSubstratesOfferDetailedRuns(t *testing.T) {
	root := wsTree(t, map[string]string{"a.py": "ORIGINAL\n"})
	w := NewWorkspaceRunner(root, 0)
	if _, ok := any(w).(DetailedJail); !ok {
		t.Error("WorkspaceRunner does not implement DetailedJail")
	}
	if _, ok := any(&WorkspacePool{}).(DetailedJail); !ok {
		t.Error("WorkspacePool does not implement DetailedJail")
	}

	ok, out, err := w.RunTestDetailed(context.Background(),
		map[string]string{"a.py": "MUTANT\n"}, killedBySuite)
	if err != nil {
		t.Fatalf("RunTestDetailed: %v", err)
	}
	if ok {
		t.Fatal("the suite passed on the mutant")
	}
	if got := pythonFailureParser(t).FirstFailure(out); got != "a.py::test_x" {
		t.Errorf("parsed %q from the captured output, want a.py::test_x", got)
	}
}
