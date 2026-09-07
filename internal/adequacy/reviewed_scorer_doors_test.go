// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// The four findings of review b3307bbbb5e6 (a Claude Code reviewer on
// internal/adequacy, Gemini verifying, 2026-09-07), all confirmed on
// adjudication. R1–R3 carried reproductions (programs driving Score
// through a fake jail); they are kept here inverted, asserting the
// behaviour the scorer now has. Every one fails on the code as it stood.

const (
	doorsCompliant = "def f(x):\n    return x + 1\n"
	doorsMutated   = "def f(x):\n    return x - 1\n"
)

var (
	doorsShared = []string{"pytest", "-q"}
	doorsNarrow = []string{"pytest", "-q", "-k", "test_unrelated"}
)

func doorsIsNarrow(cmd []string) bool { return len(cmd) > 2 && cmd[2] == "-k" }

// doorsJail models a narrowed selection that picked tests which never
// import a.py: those tests pass whatever a.py contains — including source
// that cannot be parsed. The SHARED suite does import a.py: it fails on the
// canary and it catches the mutant.
type doorsJail struct {
	mu   sync.Mutex
	runs []string
}

func (j *doorsJail) RunTest(_ context.Context, files map[string]string, cmd []string) (bool, error) {
	code := files["a.py"]
	label := "compliant"
	switch code {
	case CanaryCode:
		label = "CANARY"
	case doorsMutated:
		label = "mutant"
	}
	j.mu.Lock()
	j.runs = append(j.runs, strings.Join(cmd, " ")+" on "+label)
	j.mu.Unlock()
	if doorsIsNarrow(cmd) {
		return true, nil // never imports a.py
	}
	if code == CanaryCode || code == doorsMutated {
		return false, nil
	}
	return true, nil
}

// R1 — the canary is proven for EVERY grading command. A narrowed command
// that never reaches the file must leave its mutants UNMEASURED, not report
// them survived under the shared command's CanaryKilled.
func TestCanaryIsProvenPerGradingCommand(t *testing.T) {
	mutants := []Mutant{{ID: "m1", Search: "return x + 1", Replace: "return x - 1"}}
	a := &doorsJail{}
	repA, err := Score(context.Background(), a, map[string]string{}, "a.py", doorsCompliant, mutants, doorsShared)
	if err != nil || len(repA.Killed) != 1 {
		t.Fatalf("control: the shared command kills m1: %v %+v", err, repA)
	}
	b := &doorsJail{}
	repB, err := Score(context.Background(), b, map[string]string{}, "a.py", doorsCompliant, mutants, doorsShared,
		WithCommandFor(func(Mutant) MutantCommand {
			return MutantCommand{Cmd: doorsNarrow, Tests: 1, Rule: "lines"}
		}))
	if err != nil {
		t.Fatal(err)
	}
	canaryRanNarrow := false
	for _, r := range b.runs {
		if strings.Contains(r, "-k") && strings.Contains(r, "CANARY") {
			canaryRanNarrow = true
		}
	}
	if !canaryRanNarrow {
		t.Fatalf("the narrowed grading command was never run against CanaryCode — the canary was proven for the shared command and assumed for it:\n%s", strings.Join(b.runs, "\n"))
	}
	if len(repB.Survived) != 0 || len(repB.Killed) != 0 {
		t.Fatalf("a mutant graded by a command that never reaches the file is not a survivor (nor a kill): killed %v survived %v", repB.Killed, repB.Survived)
	}
	if len(repB.Unmeasured) != 1 || !strings.Contains(repB.UnmeasuredReasons["m1"], "never executes this file") {
		t.Fatalf("m1 must be UNMEASURED with the reason on record: %v %v", repB.Unmeasured, repB.UnmeasuredReasons)
	}
	if !repB.CanaryKilled || repB.Total != 0 || repB.KillRate() != 0 {
		t.Fatalf("the shared canary stands; nothing was graded: %+v", repB)
	}
	// One canary run per distinct command, not per mutant.
	two := []Mutant{{ID: "m1", Search: "return x + 1", Replace: "return x - 1"}, {ID: "m2", Search: "x + 1", Replace: "x + 2"}}
	c := &doorsJail{}
	if _, err := Score(context.Background(), c, map[string]string{}, "a.py", doorsCompliant, two, doorsShared,
		WithCommandFor(func(Mutant) MutantCommand { return MutantCommand{Cmd: doorsNarrow, Tests: 1, Rule: "lines"} })); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range c.runs {
		if strings.Contains(r, "-k") && strings.Contains(r, "CANARY") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the narrowed command's canary ran %d times for 2 mutants; once per distinct command:\n%s", n, strings.Join(c.runs, "\n"))
	}
}

// capJail: the narrowed command's own compliant run is slow; the mutant's
// run records the budget it was given.
type capJail struct {
	mu         sync.Mutex
	mutantCap  time.Duration
	sawMutant  bool
	narrowHold time.Duration
}

func (j *capJail) RunTest(ctx context.Context, files map[string]string, cmd []string) (bool, error) {
	code := files["a.py"]
	if code == CanaryCode {
		return false, nil
	}
	if code == "x = 1\n" {
		if dl, ok := ctx.Deadline(); ok {
			j.mu.Lock()
			j.mutantCap, j.sawMutant = time.Until(dl), true
			j.mu.Unlock()
		}
		return true, nil
	}
	if len(cmd) >= 4 && cmd[2] == "-k" { // this grading command's OWN compliant run is slow
		time.Sleep(j.narrowHold)
	}
	return true, nil
}

// R2 — the per-command cap applies with fail-fast on. It was keyed by the
// raw argv and looked up with the fail-fast argv, so with both options set
// (as advpool/gate.go always sets them) the lookup never hit and the mutant
// got the file-level floor instead of 3× its own command's baseline.
func TestPerCommandCapAppliesUnderFailFast(t *testing.T) {
	narrow := []string{"pytest", "-q", "-k", "t1"}
	hold := 150 * time.Millisecond
	grade := func(withFailFast bool) *capJail {
		j := &capJail{narrowHold: hold}
		opts := []ScoreOption{
			func(c *scoreConfig) { c.capFloor = 50 * time.Millisecond }, // see scoreConfig.capFloor
			WithCommandFor(func(Mutant) MutantCommand { return MutantCommand{Cmd: narrow, Tests: 1, Rule: "lines"} }),
		}
		if withFailFast {
			opts = append(opts, WithMutantFailFast(func([]string) ([]string, bool) { return []string{"-x"}, true }))
		}
		rep, err := Score(context.Background(), j, map[string]string{}, "a.py", "x = 0\n",
			[]Mutant{{ID: "m1", Search: "x = 0", Replace: "x = 1"}}, doorsShared, opts...)
		if err != nil {
			t.Fatal(err)
		}
		if withFailFast && !rep.FailFast {
			t.Fatal("fixture: fail-fast must be on")
		}
		if !j.sawMutant {
			t.Fatal("fixture: the mutant never ran")
		}
		return j
	}
	off, on := grade(false), grade(true)
	// Both must carry the cap derived from the narrowed command's own
	// baseline (3 × ~150ms ≈ 450ms), not the file-level floor (50ms here).
	for name, j := range map[string]*capJail{"fail-fast off": off, "fail-fast on": on} {
		if j.mutantCap < 3*hold-50*time.Millisecond || j.mutantCap > 3*hold+hold {
			t.Errorf("%s: mutant graded under %v; want ≈3× the narrowed command's %v baseline — the per-command cap did not apply", name, j.mutantCap, hold)
		}
	}
}

// reprobeJail models the premise of test selection: the WHOLE suite is slow
// (400ms), the narrowed per-mutant command is instant, and this mutant does
// not terminate under its own narrowed command. The box is healthy throughout.
type reprobeJail struct {
	mu   sync.Mutex
	runs []string
}

func (j *reprobeJail) RunTest(ctx context.Context, files map[string]string, cmd []string) (bool, error) {
	code := files["a.py"]
	label := "compliant"
	switch code {
	case CanaryCode:
		label = "canary"
	case "x = 1\n":
		label = "MUTANT"
	}
	j.mu.Lock()
	j.runs = append(j.runs, strings.Join(cmd, " ")+" on "+label)
	j.mu.Unlock()
	if code == CanaryCode {
		return false, nil
	}
	if code == "x = 1\n" {
		<-ctx.Done() // a genuinely non-terminating mutant
		return false, fmt.Errorf("%w: the mutant hangs", ErrTestTimeout)
	}
	if len(cmd) >= 4 && cmd[2] == "-k" {
		return true, nil // one selected test: instant, and green
	}
	select { // the whole suite needs 400ms
	case <-time.After(400 * time.Millisecond):
		return true, nil
	case <-ctx.Done():
		return false, fmt.Errorf("%w: the whole suite needs 400ms", ErrTestTimeout)
	}
}

// R3 — a timed-out mutant is re-probed with the command that GRADED it,
// under its budget. Re-probing the shared suite under a narrowed command's
// budget aborted the whole file's report as "too loaded" for a real
// non-terminating mutant on a healthy box.
func TestTimeoutReprobeUsesTheMutantsOwnCommand(t *testing.T) {
	narrow := []string{"pytest", "-q", "-k", "t1"}
	j := &reprobeJail{}
	rep, err := Score(context.Background(), j, map[string]string{}, "a.py", "x = 0\n",
		[]Mutant{{ID: "m1", Search: "x = 0", Replace: "x = 1"}}, doorsShared,
		WithMutantTimeout(200*time.Millisecond),
		WithCommandFor(func(Mutant) MutantCommand { return MutantCommand{Cmd: narrow, Tests: 1, Rule: "lines"} }))
	if err != nil {
		t.Fatalf("a non-terminating mutant on a healthy box is a kill, not an abort: %v\n%s", err, strings.Join(j.runs, "\n"))
	}
	if len(rep.Killed) != 1 || rep.Killed[0] != "m1" {
		t.Fatalf("m1 must be recorded as the kill it is: %+v", rep)
	}
	last := j.runs[len(j.runs)-1]
	if !strings.HasPrefix(last, strings.Join(narrow, " ")) || !strings.Contains(last, "compliant") {
		t.Fatalf("the re-probe must run the mutant's own command on compliant code; the last run was %q", last)
	}
	// And when the box IS too loaded — the mutant's own command cannot
	// finish on compliant code either — the honest report is an error.
	slow := &slowNarrowJail{}
	_, err = Score(context.Background(), slow, map[string]string{}, "a.py", "x = 0\n",
		[]Mutant{{ID: "m1", Search: "x = 0", Replace: "x = 1"}}, doorsShared,
		WithMutantTimeout(100*time.Millisecond),
		WithCommandFor(func(Mutant) MutantCommand { return MutantCommand{Cmd: narrow, Tests: 1, Rule: "lines"} }))
	if err == nil || !errors.Is(err, err) || !strings.Contains(err.Error(), "too loaded") {
		t.Fatalf("a re-probe that also times out is the loaded-machine abort: %v", err)
	}
}

// slowNarrowJail: the narrowed command hangs on everything but the canary
// and the proving run — the box is genuinely too loaded when it matters.
type slowNarrowJail struct{ proved bool }

func (j *slowNarrowJail) RunTest(ctx context.Context, files map[string]string, cmd []string) (bool, error) {
	code := files["a.py"]
	if code == CanaryCode {
		return false, nil
	}
	if len(cmd) >= 4 && cmd[2] == "-k" && code == "x = 0\n" && !j.proved {
		j.proved = true // the proving run: instant
		return true, nil
	}
	if len(cmd) >= 4 && cmd[2] == "-k" {
		<-ctx.Done()
		return false, fmt.Errorf("%w: loaded", ErrTestTimeout)
	}
	return true, nil
}

// R4 — an UNMEASURED mutant keeps its grading record: which command would
// have graded it, under which rule. It was built and thrown away.
func TestUnmeasuredMutantKeepsItsGradingRecord(t *testing.T) {
	failing := []string{"pytest", "-q", "-k", "fails_on_compliant"}
	j := jailFunc(func(_ context.Context, files map[string]string, cmd []string) (bool, error) {
		if files["a.py"] == CanaryCode {
			return false, nil
		}
		if len(cmd) >= 4 && cmd[2] == "-k" {
			return false, nil // fails on compliant code
		}
		return true, nil
	})
	rep, err := Score(context.Background(), j, map[string]string{}, "a.py", "x = 0\n",
		[]Mutant{{ID: "m1", Search: "x = 0", Replace: "x = 1"}}, doorsShared,
		WithCommandFor(func(Mutant) MutantCommand { return MutantCommand{Cmd: failing, Tests: 3, Rule: "lines"} }))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Unmeasured) != 1 {
		t.Fatalf("fixture: m1 must be unmeasured: %+v", rep)
	}
	g, ok := rep.PerMutant["m1"]
	if !ok || g.TestsRun != 3 || g.Rule != "lines" {
		t.Fatalf("the unmeasured mutant's grading record was dropped: %+v", rep.PerMutant)
	}
	if rep.Total != 0 {
		t.Fatalf("keeping the record must not move the numbers: Total %d", rep.Total)
	}
}

// jailFunc adapts a function to the Jail interface.
type jailFunc func(ctx context.Context, files map[string]string, cmd []string) (bool, error)

func (f jailFunc) RunTest(ctx context.Context, files map[string]string, cmd []string) (bool, error) {
	return f(ctx, files, cmd)
}
