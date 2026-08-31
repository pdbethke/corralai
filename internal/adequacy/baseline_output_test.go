// SPDX-License-Identifier: Elastic-2.0

package adequacy_test

import (
	"context"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
)

// verboseFakeJail implements BOTH Jail and the verbose variant, like the real
// bwrapJail does.
type verboseFakeJail struct {
	pass   bool
	output string
	calls  int
}

func (v *verboseFakeJail) RunTest(ctx context.Context, files map[string]string, testCmd []string) (bool, error) {
	v.calls++
	return v.pass, nil
}

func (v *verboseFakeJail) RunTestVerbose(ctx context.Context, files map[string]string, testCmd []string) (bool, string, error) {
	v.calls++
	return v.pass, v.output, nil
}

// A failing baseline is the single most common way a real audit dies, and until
// now it reported only "baseline does not pass unmutated" — throwing away the
// compiler/runner output that says exactly WHY. Two paid audits on two
// different repos (flask, gin) each dead-ended on that missing string.
//
// The output must ride onto the Report so a caller can print it.
func TestScore_FailingBaselineCarriesItsOutput(t *testing.T) {
	jail := &verboseFakeJail{
		pass:   false,
		output: "ModuleNotFoundError: No module named 'werkzeug'",
	}

	rep, err := adequacy.Score(context.Background(), jail, nil, "a.py", "code",
		[]adequacy.Mutant{{ID: "m1", Replace: "x"}}, []string{"pytest"})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if rep.CompliantPass {
		t.Fatal("CompliantPass should be false")
	}
	if !strings.Contains(rep.BaselineOutput, "No module named 'werkzeug'") {
		t.Fatalf("BaselineOutput = %q, want the runner's own error — without it a failing baseline is undiagnosable", rep.BaselineOutput)
	}
}

// Capturing the output must NOT cost an extra suite run. The baseline is one
// run either way; a naive "re-run it verbosely to find out why" would double the
// most expensive single step of an audit on any repo with a real suite.
func TestScore_BaselineOutputCostsNoExtraRun(t *testing.T) {
	jail := &verboseFakeJail{pass: false, output: "boom"}

	if _, err := adequacy.Score(context.Background(), jail, nil, "a.py", "code",
		[]adequacy.Mutant{{ID: "m1", Replace: "x"}}, []string{"pytest"}); err != nil {
		t.Fatalf("Score: %v", err)
	}
	if jail.calls != 1 {
		t.Fatalf("baseline took %d jail runs, want exactly 1 — capturing output must not re-run the suite", jail.calls)
	}
}

// A jail that does NOT implement the verbose variant must keep working exactly
// as before, with an empty BaselineOutput. Every existing fake in this package
// is that shape, so this is the compatibility floor.
func TestScore_NonVerboseJailStillWorks(t *testing.T) {
	fj := &plainFakeJail{pass: false}

	rep, err := adequacy.Score(context.Background(), fj, nil, "a.py", "code",
		[]adequacy.Mutant{{ID: "m1", Replace: "x"}}, []string{"pytest"})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if rep.CompliantPass {
		t.Fatal("CompliantPass should be false")
	}
	if rep.BaselineOutput != "" {
		t.Fatalf("BaselineOutput = %q, want empty for a jail that cannot report it", rep.BaselineOutput)
	}
}

type plainFakeJail struct{ pass bool }

func (p *plainFakeJail) RunTest(ctx context.Context, files map[string]string, testCmd []string) (bool, error) {
	return p.pass, nil
}
