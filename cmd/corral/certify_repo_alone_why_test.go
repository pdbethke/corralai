// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/reposcan"
)

// erringBaseline is a baseline whose run did not complete.
type erringBaseline struct{ err error }

func (b erringBaseline) RunBaseline() (bool, error) { return false, b.err }

// THE RECORD NEVER QUOTES WHY A UNIT WAS NOT RERUN. A unit a red baseline
// names failing is rerun alone, and why it is left out is stored on the
// selection (ExcludedWhy), copied verbatim onto the verdict
// (TestSelection.ExcludedReason, ExcludedRedInBaseline) and signed into the
// published ledger. aloneWhy once wrote the failing call's error there — a
// jail preparation error naming host paths, a runner's message echoing the
// operator's argv. The reason is one fixed phrase per failing call; the
// error is said on the run's progress output, where it is shown here to
// have been planted, and is stored nowhere.
func TestAloneWhyRecordsAFixedPhraseAndPrintsTheError(t *testing.T) {
	const (
		secret = "API_KEY=sk-live-PLANTED"
		host   = "/home/operator/private-checkout/.corral/jail"
	)
	planted := errors.New("prepare jail at " + host + " for `vitest run --env " + secret + "`: permission denied")
	cases := []struct {
		name  string
		build func(reposcan.BaselineRunner) (reposcan.BaselineRunner, error)
		want  string
	}{
		{"the jail could not be prepared", func(reposcan.BaselineRunner) (reposcan.BaselineRunner, error) { return nil, planted }, redNoJail},
		{"the run did not complete", func(reposcan.BaselineRunner) (reposcan.BaselineRunner, error) { return erringBaseline{planted}, nil }, redNotCompleted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var cmds [][]string
			var in localAuditInput
			var out strings.Builder
			ex := baselineRedExecutor(t, []string{"tests/four.test.ts"}, []string{"tests/five.test.ts"},
				[]string{"tests/four.test.ts", "tests/five.test.ts"}, &cmds, &in, &out)
			inner := ex.newBaseline
			alone := vitestUnitsCmd(t, "tests/five.test.ts")
			ex.newBaseline = func(ctx context.Context, li localAuditInput) (reposcan.BaselineRunner, func(), error) {
				r, cleanup, err := inner(ctx, li)
				if err != nil || !reflect.DeepEqual(li.checkArgv, alone) {
					return r, cleanup, err
				}
				got, err := tc.build(r)
				return got, cleanup, err
			}
			res, err := ex.Execute(context.Background(), tsJob("src/a.ts", "tests/one.test.ts"))
			if err != nil {
				t.Fatal(err)
			}
			if !res.Gradable {
				t.Fatalf("the second retry's baseline was green, yet the file was not graded: %+v\n%s", res, out.String())
			}
			if got := in.selection.ExcludedWhy; !reflect.DeepEqual(got, map[string]string{"tests/five.test.ts": tc.want}) {
				t.Errorf("ExcludedWhy = %v, want tests/five as %q", got, tc.want)
			}
			// The operator's terminal keeps the whole error.
			if !strings.Contains(out.String(), secret) || !strings.Contains(out.String(), host) {
				t.Fatalf("the run's progress output lacks the planted error, so this test would prove nothing:\n%s", out.String())
			}
			// Selection and map are everything verdictFromSpec reads to fill
			// TestSelection; neither may carry the error.
			sel, err := json.Marshal(in.selection)
			if err != nil {
				t.Fatal(err)
			}
			var errs []string
			for _, n := range in.selectionMap.Tests {
				errs = append(errs, n.Err)
			}
			for _, leak := range []string{secret, "sk-live", host, "/home/operator"} {
				if strings.Contains(string(sel), leak) || strings.Contains(strings.Join(errs, "\n"), leak) {
					t.Errorf("the record's selection carries %q: %s", leak, sel)
				}
			}
		})
	}
}

// A unit pass that did not run grades every file whole-suite, and its
// reason is each file's Fallback, which the ledger publishes as
// SelectionFallback. The scan takes that Fallback from the pass's fixed
// Note and says the pass's TerminalDetail — the command, the error — only on the
// terminal (ruling R-F2).
func TestAUnitPassThatDidNotRunRecordsItsReasonAndSaysItsDetail(t *testing.T) {
	const secret = "API_KEY=sk-live-PLANTED"
	ex := &localExecutor{units: reposcan.UnitEvidence{
		Note:           "typescript: the unit pass did not run: its command could not be built",
		TerminalDetail: "typescript: vitest --env " + secret + ": not found",
	}}
	sel := ex.selectionFor(tsJob("src/a.ts", "tests/one.test.ts"))
	if sel.Fallback != ex.units.Note {
		t.Errorf("Fallback = %q, want the pass's fixed Note %q", sel.Fallback, ex.units.Note)
	}
	var out strings.Builder
	printUnitGrading(&out, ex, "typescript")
	printUnitPass(&out, ex)
	if !strings.Contains(out.String(), secret) {
		t.Errorf("the terminal lacks the pass's TerminalDetail:\n%s", out.String())
	}
}
