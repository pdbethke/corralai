// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/reposcan"
)

// The together result is computed in the pool and must survive both CLI
// boundaries — the --local conversion and the --repo report — or it is the
// recurring bug: a measurement made and dropped on the floor.
func TestAdvVerdictCarriesAndShowsAuthoredTogether(t *testing.T) {
	got := advVerdictFromPool(advpool.Verdict{ProvenMissed: 2, AuthoredTogether: advpool.AuthoredTogetherFail, AuthoredTogetherOutput: "second saw shared state", Status: advpool.StatusNeedsReview})
	if got.AuthoredTogether != advpool.AuthoredTogetherFail || got.AuthoredTogetherOutput == "" {
		t.Fatalf("the CLI mapping dropped the together result: %+v", got)
	}
	var b strings.Builder
	renderAdvVerdict(&b, "x.go", got)
	if !strings.Contains(b.String(), "FAIL when run together") || !strings.Contains(b.String(), "second saw shared state") {
		t.Errorf("rendered verdict hides the together failure:\n%s", b.String())
	}
	b.Reset()
	renderAdvVerdict(&b, "x.go", advVerdict{ProvenMissed: 2, AuthoredTogether: advpool.AuthoredTogetherPass})
	if !strings.Contains(b.String(), "pass when run together") {
		t.Errorf("a passing together run must say so:\n%s", b.String())
	}
	b.Reset()
	renderAdvVerdict(&b, "x.go", advVerdict{ProvenMissed: 2})
	if strings.Contains(b.String(), "run together") {
		t.Errorf("an unchecked run must not claim a together result:\n%s", b.String())
	}
}

func TestPrintRepoReportWarnsWhenProvenTestsFailTogether(t *testing.T) {
	rep := reposcan.Aggregate("o", "r", "c", 1, 1, []reposcan.FileResult{
		{Job: reposcan.Job{Path: "src/lib.rs"}, Gradable: true,
			Verdict: advpool.Verdict{DevKillRate: 0, Survivors: 2, MutantsTotal: 2, ProvenMissed: 2, PoolScored: true, DevScored: true,
				AuthoredTest: "#[test]\nfn a() {}\n", AuthoredTogether: advpool.AuthoredTogetherFail, AuthoredTogetherOutput: "test b ... FAILED"}},
	}, nil)
	var buf bytes.Buffer
	printRepoReport(&buf, rep, false, nil, nil, nil, time.Time{})
	got := buf.String()
	if !strings.Contains(got, "FAIL when run together") || !strings.Contains(got, "test b ... FAILED") {
		t.Errorf("the report hands over the tests without saying they fail together:\n%s", got)
	}
}

// The run line counts how the kills were confirmed from the verdict's own
// mutant refs, and a kill refuted on repeat is counted as a survivor that
// did not repeat, never as a kill — by its KillConfirmation, so a refutation
// whose first run named no test is counted too.
func TestRunLineCountsKillConfirmations(t *testing.T) {
	v := advVerdictFromPool(advpool.Verdict{
		DevKilledMutants: []advpool.MutantRef{
			{ID: "a", KillConfirmation: "alone"}, {ID: "b", KillConfirmation: "alone"}, {ID: "c", KillConfirmation: "repeated"},
			{ID: "t", KillConfirmation: "reprobed"},
		},
		DevSurvivedMutants: []advpool.MutantRef{
			{ID: "d", UnrepeatedKilledBy: "test_slow", KillConfirmation: "not-repeated"},
			{ID: "f", KillConfirmation: "not-repeated"},
			{ID: "e"},
		},
		ConfirmDuration: 90 * time.Second,
	})
	var b strings.Builder
	renderAdvVerdict(&b, "x.go", v)
	want := "  kills confirmed: 2 alone, 1 by repeating the run, 1 by timeout; 2 first-run failures did not repeat (now survivors) (confirmation took 1m30s)\n"
	if !strings.Contains(b.String(), want) {
		t.Errorf("missing the confirmation line %q in:\n%s", want, b.String())
	}
	b.Reset()
	renderAdvVerdict(&b, "x.go", advVerdictFromPool(advpool.Verdict{DevKilledMutants: []advpool.MutantRef{{ID: "t", KillConfirmation: "reprobed"}}}))
	if want := "  kills confirmed: 0 alone, 0 by repeating the run, 1 by timeout; 0 first-run failures did not repeat (now survivors)\n"; !strings.Contains(b.String(), want) {
		t.Errorf("a timeout kill alone still prints the line, with no duration when none was measured; want %q in:\n%s", want, b.String())
	}
	b.Reset()
	renderAdvVerdict(&b, "x.go", advVerdictFromPool(advpool.Verdict{DevSurvivedMutants: []advpool.MutantRef{{ID: "e"}}}))
	if strings.Contains(b.String(), "kills confirmed") {
		t.Errorf("no candidate, no line:\n%s", b.String())
	}
}
