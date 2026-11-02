// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
)

// killWireJail answers every run by the code under audit: the compliant file
// passes every command, a mutated file fails the grading command (with output
// the failure parser reads) and the narrowed one. Any other command on a
// mutant (a compile check) passes.
type killWireJail struct {
	codePath, compliant string
	gradeCmd, killCmd   string
}

func (j killWireJail) RunTestDetailed(_ context.Context, files map[string]string, cmd []string) (bool, []byte, error) {
	if files[j.codePath] == j.compliant {
		return true, nil, nil
	}
	switch strings.Join(cmd, " ") {
	case j.gradeCmd:
		return false, []byte("FAILED a.py::test_x\n"), nil
	case j.killCmd:
		return false, []byte("FAILED a.py::test_x\n"), nil
	}
	return true, nil, nil
}

func (j killWireJail) RunTest(ctx context.Context, files map[string]string, cmd []string) (bool, error) {
	ok, _, err := j.RunTestDetailed(ctx, files, cmd)
	return ok, err
}

type fixedParser string

func (p fixedParser) FirstFailure([]byte) string { return string(p) }

// TestJailScorerPassesTheKillConfirmer is the wire test for KillerCmd, as
// TestScoreOptsCarriesTheMutantCompileGate is for the compile gate: an option
// nobody passes is a feature that does nothing. One real kill must reach the
// confirmer exactly once, with the command the mutant was graded with and the
// test the failing run blamed, and come back confirmed alone.
func TestJailScorerPassesTheKillConfirmer(t *testing.T) {
	const codePath = "a.py"
	const code = "def f():\n    return 1\n"
	grade := []string{"python3", "-m", "pytest", "test_a.py"}
	kill := []string{"python3", "-m", "pytest", "a.py::test_x"}

	var mu sync.Mutex
	type call struct {
		base     []string
		killedBy string
	}
	var calls []call
	s := JailScorer{
		Jail: killWireJail{codePath: codePath, compliant: code,
			gradeCmd: strings.Join(grade, " "), killCmd: strings.Join(kill, " ")},
		FailureParser: fixedParser("a.py::test_x"),
		KillerCmd: func(base []string, killedBy string) ([]string, bool) {
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, call{append([]string(nil), base...), killedBy})
			return kill, true
		},
	}
	mutants := []adequacy.Mutant{{ID: "m1", Search: "return 1", Replace: "return 2"}}
	rep, err := s.ScoreReport(context.Background(), codePath, code, "def test_x(): pass\n", mutants, strings.Join(grade, " "))
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("the confirmer was called %d times, want exactly 1: %+v", len(calls), calls)
	}
	if !reflect.DeepEqual(calls[0].base, grade) || calls[0].killedBy != "a.py::test_x" {
		t.Errorf("confirmer called with (%q, %q), want (%q, %q)", calls[0].base, calls[0].killedBy, grade, "a.py::test_x")
	}
	g, ok := rep.PerMutant["m1"]
	if !ok {
		t.Fatalf("no grading for m1: %+v", rep)
	}
	if g.KillConfirmation != adequacy.KillConfirmedAlone {
		t.Errorf("KillConfirmation = %q, want %q", g.KillConfirmation, adequacy.KillConfirmedAlone)
	}
}

// The verdict's refs carry the grading's confirmation: toMutantRefsWith is
// the first converter on the record's path, and the one that reads
// adequacy's own MutantGrading.
func TestMutantRefsCarryTheKillConfirmation(t *testing.T) {
	ms := []adequacy.Mutant{{ID: "k1"}, {ID: "s1"}}
	refs := toMutantRefsWith(ms, map[string]adequacy.MutantGrading{
		"k1": {KillConfirmation: adequacy.KillConfirmedAlone, KilledBy: "a.py::test_x"},
		"s1": {UnrepeatedKilledBy: "a.py::test_x"},
	})
	if refs[0].KillConfirmation != adequacy.KillConfirmedAlone || refs[0].UnrepeatedKilledBy != "" {
		t.Errorf("killed ref = %+v", refs[0])
	}
	if refs[1].UnrepeatedKilledBy != "a.py::test_x" || refs[1].KillConfirmation != "" {
		t.Errorf("survivor ref = %+v", refs[1])
	}
}
