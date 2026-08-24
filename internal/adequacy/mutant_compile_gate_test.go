// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"errors"
	"testing"
)

// gateJail distinguishes a COMPILE-CHECK command from the TEST command, so a
// test can make a mutant fail to compile while its suite would have passed.
type gateJail struct {
	compileCmd    string          // argv[0] that means "this is the compile check"
	uncompilable  map[string]bool // mutant code -> fails the compile check
	testPasses    bool            // what the suite reports for a mutant that DOES compile
	compileErr    error           // infra failure from the gate itself
	testRuns      int             // how many full suite runs happened
	compileRuns   int
	baselineFiles string
}

func (g *gateJail) RunTest(ctx context.Context, files map[string]string, cmd []string) (bool, error) {
	code := files["x.go"]
	if len(cmd) > 0 && cmd[0] == g.compileCmd {
		g.compileRuns++
		if g.compileErr != nil {
			return false, g.compileErr
		}
		return !g.uncompilable[code], nil
	}
	g.testRuns++
	if code == g.baselineFiles {
		return true, nil // healthy baseline passes
	}
	for _, m := range gateMutants() {
		if code == m.Code {
			return g.testPasses, nil
		}
	}
	// Anything that is neither the baseline nor a known mutant is Score's own
	// deliberately-invalid CANARY, which MUST fail or Score refuses to grade.
	return false, nil
}

const gateBase = "package p\nfunc f() int { return 1 }\n"

func gateMutants() []Mutant {
	return []Mutant{
		{ID: "m1", Code: "package p\nfunc f() int { return 2 }\n"},  // compiles
		{ID: "m2", Code: "package p\nfunc f() int { return zz }\n"}, // does NOT compile
	}
}

// THE BUG: a mutant the compiler rejected was scored as KILLED — evidence the
// tests caught something, when nothing ran. In a system whose product is a
// SIGNED record asserting "your tests catch K% of injected bugs", that signs a
// false claim.
func TestScore_UncompilableMutantIsInvalidNotKilled(t *testing.T) {
	j := &gateJail{
		compileCmd:    "COMPILE",
		uncompilable:  map[string]bool{gateMutants()[1].Code: true},
		testPasses:    true, // a compiling mutant SURVIVES, so any "kill" can only come from the gate
		baselineFiles: gateBase,
	}
	rep, err := Score(context.Background(), j, map[string]string{}, "x.go", gateBase,
		gateMutants(), []string{"TEST"},
		WithMutantCompileCheck([][]string{{"COMPILE", "x.go"}}))
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if got := len(rep.Killed); got != 0 {
		t.Errorf("Killed = %d (%v), want 0 — nothing caught anything", got, rep.Killed)
	}
	if len(rep.Invalid) != 1 || rep.Invalid[0] != "m2" {
		t.Errorf("Invalid = %v, want [m2] — the uncompilable mutant", rep.Invalid)
	}
	if len(rep.Survived) != 1 || rep.Survived[0] != "m1" {
		t.Errorf("Survived = %v, want [m1]", rep.Survived)
	}
}

// The fix to the signed claim: an invalid mutant leaves the DENOMINATOR. It is
// evidence about the generator, not about the suite.
func TestScore_InvalidMutantsLeaveTheDenominator(t *testing.T) {
	j := &gateJail{
		compileCmd:    "COMPILE",
		uncompilable:  map[string]bool{gateMutants()[1].Code: true},
		testPasses:    false, // the compiling mutant IS killed
		baselineFiles: gateBase,
	}
	rep, err := Score(context.Background(), j, map[string]string{}, "x.go", gateBase,
		gateMutants(), []string{"TEST"},
		WithMutantCompileCheck([][]string{{"COMPILE", "x.go"}}))
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if rep.Total != 1 {
		t.Errorf("Total = %d, want 1 — only the GRADED mutants count", rep.Total)
	}
	if kr := rep.KillRate(); kr != 1.0 {
		t.Errorf("KillRate = %v, want 1.0 (1 killed of 1 graded). Counting the uncompilable mutant would give %v", kr, 0.5)
	}
}

// Cost: an invalid mutant must SKIP its expensive suite run.
func TestScore_InvalidMutantSkipsTheSuiteRun(t *testing.T) {
	j := &gateJail{
		compileCmd:    "COMPILE",
		uncompilable:  map[string]bool{gateMutants()[1].Code: true},
		testPasses:    true,
		baselineFiles: gateBase,
	}
	if _, err := Score(context.Background(), j, map[string]string{}, "x.go", gateBase,
		gateMutants(), []string{"TEST"},
		WithMutantCompileCheck([][]string{{"COMPILE", "x.go"}})); err != nil {
		t.Fatalf("Score: %v", err)
	}
	// baseline + canary + ONE mutant (m1). m2 never reaches the suite.
	if j.testRuns != 3 {
		t.Errorf("testRuns = %d, want 3 (baseline, canary, m1 only) — the invalid mutant must not pay for a suite run", j.testRuns)
	}
}

// FAIL CLOSED: if the gate itself cannot run, that is infrastructure failure.
// Silently calling every mutant "invalid" would erase the exam.
func TestScore_CompileGateInfraFailureIsAnError(t *testing.T) {
	boom := errors.New("jail exploded")
	j := &gateJail{compileCmd: "COMPILE", compileErr: boom, baselineFiles: gateBase}
	_, err := Score(context.Background(), j, map[string]string{}, "x.go", gateBase,
		gateMutants(), []string{"TEST"},
		WithMutantCompileCheck([][]string{{"COMPILE", "x.go"}}))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the gate's infra error — a broken gate must never read as 'all mutants invalid'", err)
	}
}

// BACK-COMPAT: without the option, behavior is exactly as before.
func TestScore_NoCompileCheckOptionKeepsOldBehavior(t *testing.T) {
	j := &gateJail{compileCmd: "COMPILE", testPasses: false, baselineFiles: gateBase}
	rep, err := Score(context.Background(), j, map[string]string{}, "x.go", gateBase,
		gateMutants(), []string{"TEST"})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if j.compileRuns != 0 {
		t.Errorf("compileRuns = %d, want 0 — no option means no gate", j.compileRuns)
	}
	if len(rep.Invalid) != 0 || rep.Total != 2 {
		t.Errorf("Invalid=%v Total=%d, want none/2 unchanged", rep.Invalid, rep.Total)
	}
}
