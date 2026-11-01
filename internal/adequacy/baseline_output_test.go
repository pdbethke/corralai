// SPDX-License-Identifier: Elastic-2.0

package adequacy_test

import (
	"context"
	"fmt"
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

// detailedFakeJail is a jail with both captures, as bwrapJail has: the verbose
// one keeps the HEAD of a long run, the detailed one its TAIL.
type detailedFakeJail struct {
	verboseFakeJail
	tail string
}

func (d *detailedFakeJail) RunTestDetailed(ctx context.Context, files map[string]string, testCmd []string) (bool, []byte, error) {
	d.calls++
	return d.pass, []byte(d.tail), nil
}

// REA's sixth run (2026-10-30) failed its baseline and printed 64 KiB of
// passing acceptance tests, cut off before vitest's failure summary: the
// verbose capture keeps a run's HEAD, and a runner names what failed at the
// END. The baseline is read from the tail-keeping capture when the jail has
// one, still in one run.
func TestScore_FailingBaselineCarriesTheTailOfItsOutput(t *testing.T) {
	jail := &detailedFakeJail{
		verboseFakeJail: verboseFakeJail{pass: false, output: "✓ a passing banner"},
		tail:            "FAIL src/cliJsonPaths.test.ts > keeps POSIX symlink-parent traversal",
	}
	rep, err := adequacy.Score(context.Background(), jail, nil, "a.ts", "code",
		[]adequacy.Mutant{{ID: "m1", Replace: "x"}}, []string{"vitest", "run"})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if !strings.Contains(rep.BaselineOutput, "FAIL src/cliJsonPaths.test.ts") {
		t.Fatalf("BaselineOutput = %q, want the tail that names the failing test", rep.BaselineOutput)
	}
	if jail.calls != 1 {
		t.Fatalf("baseline took %d jail runs, want 1", jail.calls)
	}
}

// timeoutDetailedJail is a detailed jail whose run times out, as bwrapJail's
// does when the suite outlives its bound: an error wrapping ErrTestTimeout,
// with the output it printed before it was killed.
type timeoutDetailedJail struct{ detailedFakeJail }

func (d *timeoutDetailedJail) RunTestDetailed(ctx context.Context, files map[string]string, testCmd []string) (bool, []byte, error) {
	d.calls++
	return false, []byte(d.tail), fmt.Errorf("%w: killed after 30m0s", adequacy.ErrTestTimeout)
}

// REA's eleventh run (2026-10-31) printed "baseline does not pass unmutated"
// over 64 KiB of passing tests and no failure: the suite had been killed at
// its bound, and the report said only that it did not pass. A timed-out
// baseline says so.
func TestScore_ABaselineThatTimesOutSaysItTimedOut(t *testing.T) {
	jail := &timeoutDetailedJail{detailedFakeJail{tail: "✓ a passing test, then nothing"}}
	rep, err := adequacy.Score(context.Background(), jail, nil, "a.ts", "code",
		[]adequacy.Mutant{{ID: "m1", Replace: "x"}}, []string{"vitest", "run"})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if rep.CompliantPass || !rep.BaselineTimedOut || !strings.Contains(rep.BaselineOutput, "then nothing") {
		t.Fatalf("report = %+v, want a failed, timed-out baseline carrying its output", rep)
	}
	// Negative control: a baseline that fails on its own did not time out.
	red := &detailedFakeJail{verboseFakeJail: verboseFakeJail{pass: false}, tail: "FAIL x"}
	if rep, _ := adequacy.Score(context.Background(), red, nil, "a.ts", "code",
		[]adequacy.Mutant{{ID: "m1", Replace: "x"}}, []string{"vitest", "run"}); rep.BaselineTimedOut {
		t.Error("a red baseline that finished was reported as timed out")
	}
}
