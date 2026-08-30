// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// TestScoreIsIdenticalUnderOneTreeAndFour is THE invariant the whole private-
// tree substrate rests on: parallelism must change how long the audit takes
// and nothing else. If a mutant scored in one of four trees can come back
// Killed where the same mutant scored alone comes back Survived, every
// verdict this branch produces is fiction — and the failure mode is silent,
// because both runs return a well-formed Report.
//
// The fixture's "suite" is a grep for the compliant return value, so a mutant
// that keeps the line survives and one that changes it is killed. Cheap,
// hermetic, and — crucially — sensitive to the tree's contents at the instant
// it runs, which is exactly the thing a leaking or unrestored tree corrupts.
//
// (The mutant set is fixed and recorded here rather than generated: generator
// variance is larger than most effects a comparison like this can measure, so
// two runs must sit the SAME exam.)
func TestScoreIsIdenticalUnderOneTreeAndFour(t *testing.T) {
	const compliant = "def f():\n    return 1\n"
	root := newGitRepo(t, map[string]string{
		"a.py":            compliant,
		"tests/test_a.py": "def test(): pass\n",
	}, nil)

	// Six mutants, three of which keep `return 1` (survivors) and three of
	// which do not (kills). Deliberately interleaved so an off-by-one in the
	// parallel path's result collection shows up as a DIFFERENT set, not just
	// a different order.
	mutants := []Mutant{
		{ID: "m0", Replace: "def f():\n    return 0\n"},
		{ID: "m1", Replace: "def f():\n    return 1  # noop\n"},
		{ID: "m2", Replace: "def f():\n    return 2\n"},
		{ID: "m3", Replace: "def f():\n    x = 2\n    return 1\n"},
		{ID: "m4", Replace: "def f():\n    pass\n"},
		{ID: "m5", Replace: "def f():\n    return 1\n    # tail\n"},
	}
	// The audited file is the only thing Score writes; the suite reads it off
	// disk, in whichever tree the run borrowed.
	testCmd := []string{"grep", "-q", "return 1", "a.py"}

	score := func(trees, conc int) Report {
		t.Helper()
		p, d, err := NewWorkspacePool(context.Background(), root, trees, time.Minute)
		if err != nil {
			t.Fatalf("NewWorkspacePool(%d): %v", trees, err)
		}
		defer p.Close()
		if d.Trees != trees {
			t.Fatalf("pool of %d downgraded to %d (%s); the comparison would be vacuous", trees, d.Trees, d.Note)
		}
		rep, err := Score(context.Background(), p, nil, "a.py", compliant, mutants, testCmd,
			WithConcurrency(conc))
		if err != nil {
			t.Fatalf("Score at %d trees: %v", trees, err)
		}
		if !rep.CanaryKilled {
			t.Fatalf("canary survived at %d trees; nothing below is a measurement", trees)
		}
		return rep
	}

	one := score(1, 1)
	four := score(4, 4)

	if !reflect.DeepEqual(one.Killed, four.Killed) {
		t.Errorf("Killed differs: 1 tree %v, 4 trees %v", one.Killed, four.Killed)
	}
	if !reflect.DeepEqual(one.Survived, four.Survived) {
		t.Errorf("Survived differs: 1 tree %v, 4 trees %v", one.Survived, four.Survived)
	}
	// And the exam was a real one in both: a run where everything survives
	// (or everything dies) would satisfy the equality above while proving
	// nothing about the substrate.
	if len(one.Killed) != 3 || len(one.Survived) != 3 {
		t.Fatalf("fixture is not discriminating: killed=%v survived=%v", one.Killed, one.Survived)
	}

	// The trees must also be left as they were found — the restore is what
	// makes the NEXT mutant's run a measurement of that mutant alone.
	if got := readFile(t, filepath.Join(root, "a.py")); got != compliant {
		t.Errorf("the checkout was left mutated: %q", got)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
