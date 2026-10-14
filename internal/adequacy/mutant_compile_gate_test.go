// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// gateJail distinguishes a COMPILE-CHECK command from the TEST command, so a
// test can make a mutant fail to compile while its suite would have passed.
type gateJail struct {
	compileCmd   string          // argv[0] that means "this is the compile check"
	uncompilable map[string]bool // mutant code -> fails the compile check
	testPasses   bool            // what the suite reports for a mutant that DOES compile
	compileErr   error           // infra failure from the gate itself
	// compileErrFor fails the gate for one code body only (nil: none), so a
	// test can break the gate on a mutant while the probe of the compliant
	// file succeeds.
	compileErrFor map[string]error
	testRuns      int // how many full suite runs happened
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
		if err := g.compileErrFor[code]; err != nil {
			return false, err
		}
		return !g.uncompilable[code], nil
	}
	g.testRuns++
	if code == g.baselineFiles {
		return true, nil // healthy baseline passes
	}
	for _, m := range gateMutants() {
		if code == m.Replace {
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
		{ID: "m1", Replace: "package p\nfunc f() int { return 2 }\n"},  // compiles
		{ID: "m2", Replace: "package p\nfunc f() int { return zz }\n"}, // does NOT compile
	}
}

// THE BUG: a mutant the compiler rejected was scored as KILLED — evidence the
// tests caught something, when nothing ran. In a system whose product is a
// SIGNED record asserting "your tests catch K% of injected bugs", that signs a
// false claim.
func TestScore_UncompilableMutantIsInvalidNotKilled(t *testing.T) {
	j := &gateJail{
		compileCmd:    "COMPILE",
		uncompilable:  map[string]bool{gateMutants()[1].Replace: true},
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
		uncompilable:  map[string]bool{gateMutants()[1].Replace: true},
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
		uncompilable:  map[string]bool{gateMutants()[1].Replace: true},
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

// A gate that cannot run on the COMPLIANT file never costs the file its
// grade. It used to: on Warp, an unscoped `cargo test --no-run` ran past the
// per-mutant timeout on the unmutated code and the whole file was
// COULD-NOT-GRADE before one mutant was scored. The gate is an optimisation
// and a classifier; the measurement is the suite. So the gate is switched off
// for the file, the report says why, and every mutant is graded by execution.
// What the old rule protected still holds: a broken gate never reads as "all
// mutants invalid".
func TestScore_AGateThatCannotRunIsDisabledNotFatal(t *testing.T) {
	boom := errors.New("jail exploded")
	j := &gateJail{compileCmd: "COMPILE", compileErr: boom, testPasses: true, baselineFiles: gateBase}
	rep, err := Score(context.Background(), j, map[string]string{}, "x.go", gateBase,
		gateMutants(), []string{"TEST"},
		WithMutantCompileCheck([][]string{{"COMPILE", "x.go"}}))
	if err != nil {
		t.Fatalf("Score: %v — a compile gate that cannot run must not fail the file", err)
	}
	if len(rep.Invalid) != 0 {
		t.Errorf("Invalid = %v — a broken gate must never read as invalid mutants", rep.Invalid)
	}
	if rep.Total != 2 {
		t.Errorf("Total = %d, want 2 — both mutants graded by execution", rep.Total)
	}
	if !strings.Contains(rep.CompileGateNote, "DISABLED") || !strings.Contains(rep.CompileGateNote, "jail exploded") {
		t.Errorf("CompileGateNote = %q, want it to say the gate was disabled and why", rep.CompileGateNote)
	}
}

// The probe of the compliant file is not held to the per-mutant TEST timeout:
// a build can take longer than the suite it builds (Rust's first compile), and
// the probe is one run. It runs within the file's own budget and is timed.
func TestScore_TheGateProbeIsNotCappedByTheTestTimeout(t *testing.T) {
	j := &slowGateJail{gateJail: gateJail{compileCmd: "COMPILE", testPasses: true, baselineFiles: gateBase},
		compileTakes: 60 * time.Millisecond}
	rep, err := Score(context.Background(), j, map[string]string{}, "x.go", gateBase,
		gateMutants(), []string{"TEST"},
		WithMutantTimeout(20*time.Millisecond),
		WithMutantCompileCheck([][]string{{"COMPILE", "x.go"}}))
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if rep.CompileGateNote != "" {
		t.Errorf("CompileGateNote = %q — a gate slower than one test run is still a working gate", rep.CompileGateNote)
	}
	if len(rep.Invalid) != 0 {
		t.Errorf("Invalid = %v — every mutant compiles within the probe's own measured time", rep.Invalid)
	}
}

// A mutant whose compile check fails to RUN (not a rejection, not a
// timeout) is graded by execution like any mutant with no gate, and the
// report counts it. One mutant's broken gate run is not the file's.
func TestScore_AGateErrorOnOneMutantGradesItByExecution(t *testing.T) {
	m := gateMutants()
	j := &gateJail{compileCmd: "COMPILE", testPasses: true, baselineFiles: gateBase,
		compileErrFor: map[string]error{m[0].Replace: errors.New("flaky gate")}}
	rep, err := Score(context.Background(), j, map[string]string{}, "x.go", gateBase,
		m[:1], []string{"TEST"},
		WithMutantCompileCheck([][]string{{"COMPILE", "x.go"}}))
	if err != nil {
		t.Fatalf("Score: %v — one mutant's gate error must not fail the file", err)
	}
	if len(rep.Survived) != 1 {
		t.Errorf("Survived = %v, want [m1] — graded by execution", rep.Survived)
	}
	if !strings.Contains(rep.CompileGateNote, "1 mutant") {
		t.Errorf("CompileGateNote = %q, want it to count the mutant the gate could not judge", rep.CompileGateNote)
	}
}

// A mutant whose compile check runs past the cap the compliant file's own
// compile set is invalid, with the reason: the unmutated code provably
// compiles in time, so a build that does not finish is a fact about the
// mutant, like a build that fails.
func TestScore_AMutantWhoseBuildRunsPastTheCapIsInvalid(t *testing.T) {
	m := gateMutants()
	j := &gateJail{compileCmd: "COMPILE", testPasses: true, baselineFiles: gateBase,
		compileErrFor: map[string]error{m[0].Replace: ErrTestTimeout}}
	rep, err := Score(context.Background(), j, map[string]string{}, "x.go", gateBase,
		m[:1], []string{"TEST"},
		WithMutantCompileCheck([][]string{{"COMPILE", "x.go"}}))
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if len(rep.Invalid) != 1 {
		t.Fatalf("Invalid = %v, want [m1]", rep.Invalid)
	}
	if r := rep.InvalidReasons["m1"]; !strings.Contains(r, "did not finish") {
		t.Errorf("invalid reason = %q, want it to say the build did not finish in time", r)
	}
}

// slowGateJail makes the compile check take a fixed time and honour its
// context, so a test can tell which deadline the gate ran under.
type slowGateJail struct {
	gateJail
	compileTakes time.Duration
}

func (s *slowGateJail) RunTest(ctx context.Context, files map[string]string, cmd []string) (bool, error) {
	if len(cmd) > 0 && cmd[0] == s.compileCmd {
		select {
		case <-time.After(s.compileTakes):
		case <-ctx.Done():
			return false, ErrTestTimeout
		}
	}
	return s.gateJail.RunTest(ctx, files, cmd)
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
