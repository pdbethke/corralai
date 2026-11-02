// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

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
		"s1": {UnrepeatedKilledBy: "a.py::test_x", KillConfirmation: adequacy.KillNotRepeated},
	})
	if refs[0].KillConfirmation != adequacy.KillConfirmedAlone || refs[0].UnrepeatedKilledBy != "" {
		t.Errorf("killed ref = %+v", refs[0])
	}
	if refs[1].UnrepeatedKilledBy != "a.py::test_x" || refs[1].KillConfirmation != adequacy.KillNotRepeated {
		t.Errorf("survivor ref = %+v", refs[1])
	}
}

// The dev pass's confirmation wall clock reaches the verdict at BOTH of its
// construction sites — the converged run (tickAggregate) and the stalled one
// (timeoutVerdict) — so the run line can say what confirming cost.
func TestVerdictCarriesTheConfirmDuration(t *testing.T) {
	d, missionID, fs := fanoutRun(t, WriterModePerSurvivor, map[string]string{}, nil)
	fs.confirmDuration = 42 * time.Second
	devTick(t, d, missionID)
	if v := driveFanout(t, d, missionID, nil); v.ConfirmDuration != 42*time.Second {
		t.Errorf("converged verdict ConfirmDuration = %s, want 42s", v.ConfirmDuration)
	}
	if v := (&Driver{}).timeoutVerdict(&runState{rs: RunSpec{Repo: "r", Commit: "c", Lang: "go"}, confirmDuration: 7 * time.Second}); v.ConfirmDuration != 7*time.Second {
		t.Errorf("timeout verdict ConfirmDuration = %s, want 7s", v.ConfirmDuration)
	}
}

// flakyOnceJail passes every run on the compliant code, and every command
// but the grading one; the grading command fails on any other source (the
// canary) always, and on the mutant the FIRST time only. The shape of t14:
// a first-run failure that does not repeat.
type flakyOnceJail struct {
	codePath, compliant, mutant, gradeCmd string
	mu                                    sync.Mutex
	graded                                int
}

func (j *flakyOnceJail) RunTest(_ context.Context, files map[string]string, cmd []string) (bool, error) {
	if files[j.codePath] == j.compliant || strings.Join(cmd, " ") != j.gradeCmd {
		return true, nil
	}
	if files[j.codePath] != j.mutant {
		return false, nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.graded++
	return j.graded > 1, nil
}

// The negative control on the two other scoring paths: ScoreFor (the dev
// pass under a per-mutant command) and ScoreAuthoredReport (the proving
// pass). A first-run failure that does not repeat must come back a
// survivor on both — which it can only do if each path runs the
// confirmation pass. A path that skipped it would report a kill.
func TestEveryScoringPathRefutesAFailureThatDoesNotRepeat(t *testing.T) {
	const codePath = "a.py"
	const code = "def f():\n    return 1\n"
	grade := "python3 -m pytest test_a.py"
	mutants := []adequacy.Mutant{{ID: "m1", Search: "return 1", Replace: "return 2"}}

	mutant := "def f():\n    return 2\n"
	j := &flakyOnceJail{codePath: codePath, compliant: code, mutant: mutant, gradeCmd: grade}
	s := JailScorer{Jail: j}
	cmdFor := func(adequacy.Mutant) adequacy.MutantCommand {
		return adequacy.MutantCommand{Cmd: adequacy.ShellSplit(grade)}
	}
	rate, survivors, err := s.ScoreFor(context.Background(), codePath, code, "def test_x(): pass\n", mutants, grade, cmdFor)
	if err != nil {
		t.Fatal(err)
	}
	if rate != 0 || len(survivors) != 1 || survivors[0].ID != "m1" {
		t.Errorf("ScoreFor: rate %v survivors %v, want m1 surviving the refuted kill", rate, survivors)
	}

	if j.graded != 2 {
		t.Errorf("ScoreFor: the mutant was graded %d time(s), want 2 (the run and its repeat)", j.graded)
	}

	j = &flakyOnceJail{codePath: codePath, compliant: code, mutant: mutant, gradeCmd: grade}
	s = JailScorer{Jail: j}
	rep, err := s.ScoreAuthoredReport(context.Background(), codePath, code, "def test_x(): pass\n", mutants, grade)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Killed) != 0 || len(rep.Survived) != 1 {
		t.Fatalf("ScoreAuthoredReport: Killed %v Survived %v, want m1 surviving", rep.Killed, rep.Survived)
	}
	if g := rep.PerMutant["m1"]; g.KillConfirmation != adequacy.KillNotRepeated {
		t.Errorf("ScoreAuthoredReport: KillConfirmation = %q, want %q", g.KillConfirmation, adequacy.KillNotRepeated)
	}
	if j.graded != 2 {
		t.Errorf("ScoreAuthoredReport: the mutant was graded %d time(s), want 2 (the run and its repeat)", j.graded)
	}
}
