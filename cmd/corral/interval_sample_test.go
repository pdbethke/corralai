// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// The 95% interval a reader sees is the one the certification gate used:
// over DISTINCT measured edits, never over MutantsTotal, which counts
// duplicates. Eight copies of one killed edit are n=1, a band of 0.21–1.00,
// not n=8 and 0.68–1.00. Found by a claims review of the docs, which said
// the printed interval was over distinct edits when the code took it over
// every copy.
func TestPrintedIntervalIsOverDistinctEdits(t *testing.T) {
	pool := advpool.Verdict{Status: advpool.StatusNeedsReview, DevKillRate: 1, MutantsTotal: 8, MutantsDistinct: 1, DevScored: true, PoolScored: true}
	got := advVerdictFromPool(pool)
	if got.MutantsDistinct != 1 {
		t.Fatalf("the --local conversion dropped the distinct sample: %+v", got)
	}
	var b strings.Builder
	renderAdvVerdict(&b, "x.go", got)
	if !strings.Contains(b.String(), "95% interval:  0.21–1.00 (n=1 distinct)") {
		t.Errorf("--local interval is not over the distinct edits:\n%s", b.String())
	}

	rep := reposcan.FileResult{Job: reposcan.Job{Path: "a.go", Lang: "go"}, Gradable: true, Verdict: pool}
	w := reposcan.Aggregate("o", "r", "c", 1, 1, []reposcan.FileResult{rep}, nil).Weakest
	if len(w) != 1 || w[0].MutantsDistinct != 1 {
		t.Fatalf("the --repo report dropped the distinct sample: %+v", w)
	}
	if line := examConfidenceLine(w[0]); !strings.Contains(line, "95% interval 0.21–1.00 (n=1 distinct)") {
		t.Errorf("--repo interval is not over the distinct edits: %q", line)
	}
	if lo, hi, ok := fileInterval(w[0]); !ok || int(lo*100) != 20 || hi != 1 {
		t.Errorf("signed statement interval = %.2f–%.2f, %v; want 0.21–1.00", lo, hi, ok)
	}
	// No distinct sample, no interval: a band over an unknown n is not a band.
	if _, _, ok := fileInterval(reposcan.WeakFile{MutantsGraded: 8}); ok {
		t.Error("an interval was produced with no distinct sample")
	}
}

// The ledger row carries the distinct sample, read from the stored verdict,
// so the public record page can take its interval over it too.
func TestLedgerRowCarriesTheDistinctSample(t *testing.T) {
	files := []scanstore.File{
		{Path: "a.go", Disposition: "audited", MutantsGraded: 8, VerdictJSON: `{"mutants_distinct":3,"mutants_distinct_survived":1}`},
		{Path: "b.go", Disposition: "rejected"},
	}
	b := buildBundle(scanstore.Scan{Repo: "o/r", Commit: "deadbeef"}, 0, files, nil, nil, nil, auditpush.Link{}, false, "o/r", "deadbeef", "", bundleMeta{})
	if r := b.Files[0]; r.MutantsDistinct == nil || *r.MutantsDistinct != 3 || r.MutantsDistinctSurvived == nil || *r.MutantsDistinctSurvived != 1 {
		t.Errorf("row a.go lost the distinct sample: %v %v", r.MutantsDistinct, r.MutantsDistinctSurvived)
	}
	if r := b.Files[1]; r.MutantsDistinct != nil {
		t.Errorf("a rejected file got a distinct sample: %v", *r.MutantsDistinct)
	}
}
