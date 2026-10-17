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
