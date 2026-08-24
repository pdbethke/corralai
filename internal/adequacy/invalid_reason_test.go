// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"strings"
	"testing"
)

// verboseGateJail answers the compile gate AND reports what the checker printed,
// so Score can record WHY a mutant was rejected.
type verboseGateJail struct {
	compileCmd   string
	uncompilable map[string]string // mutant code -> compiler output
	baseline     string
}

func (g *verboseGateJail) RunTest(ctx context.Context, files map[string]string, cmd []string) (bool, error) {
	pass, _, err := g.RunTestVerbose(ctx, files, cmd)
	return pass, err
}

func (g *verboseGateJail) RunTestVerbose(ctx context.Context, files map[string]string, cmd []string) (bool, string, error) {
	code := files["x.go"]
	if len(cmd) > 0 && cmd[0] == g.compileCmd {
		if out, bad := g.uncompilable[code]; bad {
			return false, out, nil
		}
		return true, "", nil
	}
	if code == g.baseline {
		return true, "", nil
	}
	for _, m := range gateMutants() {
		if code == m.Code {
			return true, "", nil // compiling mutants SURVIVE, so no kill masks the gate
		}
	}
	return false, "", nil // canary
}

// The count alone said the exam shrank; it never said WHY. Diagnosing a 56-92%
// invalid rate — and any hope of feeding the error back to the generator so it
// can correct itself — needs the compiler's own words, which the gate was
// discarding.
func TestScore_RecordsWhyAMutantWasRejected(t *testing.T) {
	const boom = "./x.go:7:2: undefined: helper"
	j := &verboseGateJail{
		compileCmd:   "COMPILE",
		uncompilable: map[string]string{gateMutants()[1].Code: boom},
		baseline:     gateBase,
	}
	rep, err := Score(context.Background(), j, map[string]string{}, "x.go", gateBase,
		gateMutants(), []string{"TEST"},
		WithMutantCompileCheck([][]string{{"COMPILE", "x.go"}}))
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if len(rep.Invalid) != 1 || rep.Invalid[0] != "m2" {
		t.Fatalf("Invalid = %v, want [m2]", rep.Invalid)
	}
	got := rep.InvalidReasons["m2"]
	if got == "" {
		t.Fatal("no reason recorded — the operator sees a count with no cause, and the generator cannot be told what it got wrong")
	}
	if !strings.Contains(got, "undefined: helper") {
		t.Errorf("reason = %q, want the compiler's own message", got)
	}
}

// A plain Jail (no verbose sibling) must still work — the reason is simply
// absent rather than the run failing.
func TestScore_InvalidReasonAbsentOnAPlainJail(t *testing.T) {
	j := &gateJail{
		compileCmd:    "COMPILE",
		uncompilable:  map[string]bool{gateMutants()[1].Code: true},
		testPasses:    true,
		baselineFiles: gateBase,
	}
	rep, err := Score(context.Background(), j, map[string]string{}, "x.go", gateBase,
		gateMutants(), []string{"TEST"},
		WithMutantCompileCheck([][]string{{"COMPILE", "x.go"}}))
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if len(rep.Invalid) != 1 {
		t.Fatalf("Invalid = %v, want one entry", rep.Invalid)
	}
	if r := rep.InvalidReasons["m2"]; r != "" {
		t.Errorf("reason = %q on a jail that cannot report output, want empty", r)
	}
}
