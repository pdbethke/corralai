// SPDX-License-Identifier: Elastic-2.0

package adequacy_test

import (
	"context"
	"os"
	"reflect"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/lang"
)

// TestScore_PassingBaselineNamesItsFlakyTests: a nextest baseline that passed
// only because a test was retried is graded as passed (it exited 0) and the
// tests it leaned on are carried on the Report, read by the run's own
// FailureParser — the Flaky list part 1 parsed is no longer dropped. The
// output is a real capture (internal/lang/testdata/rust/nextest/flaky.txt).
func TestScore_PassingBaselineNamesItsFlakyTests(t *testing.T) {
	out, err := os.ReadFile("../lang/testdata/rust/nextest/flaky.txt")
	if err != nil {
		t.Fatal(err)
	}
	p, _ := lang.ByName("rust")
	fp, ok := p.(lang.FailureParser)
	if !ok {
		t.Fatal("the rust plugin is not a FailureParser")
	}
	jail := &verboseFakeJail{pass: true, output: string(out)}
	rep, err := adequacy.Score(context.Background(), jail, nil, "src/a.rs", "code", nil,
		[]string{"cargo", "nextest", "run"}, adequacy.WithFailureParser(fp))
	if err != nil {
		t.Fatal(err)
	}
	if !rep.CompliantPass || !reflect.DeepEqual(rep.BaselineFlaky, []string{"fuzzy_match tests::planted_flaky"}) {
		t.Fatalf("CompliantPass=%v BaselineFlaky=%q, want a pass naming the flaky test", rep.CompliantPass, rep.BaselineFlaky)
	}
	// A parser that cannot report flakiness reports none.
	py, _ := lang.ByName("python")
	pfp, _ := py.(lang.FailureParser)
	rep, err = adequacy.Score(context.Background(), jail, nil, "a.py", "code", nil, []string{"pytest"}, adequacy.WithFailureParser(pfp))
	if err != nil || rep.BaselineFlaky != nil {
		t.Fatalf("python: BaselineFlaky=%q err=%v", rep.BaselineFlaky, err)
	}
}
