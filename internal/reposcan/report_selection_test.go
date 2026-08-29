// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"fmt"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
)

func TestRepoReportCountsSelectionModes(t *testing.T) {
	results := []FileResult{
		{Gradable: true, Verdict: advpool.Verdict{TestSelection: advpool.TestSelection{Method: "coverage-context", Selected: 3, Of: 10}}},
		{Gradable: true, Verdict: advpool.Verdict{TestSelection: advpool.TestSelection{Fallback: "no selector for ruby"}}},
		{Gradable: true, Verdict: advpool.Verdict{TestSelection: advpool.TestSelection{Method: "coverage-context"}, Uncovered: true}},
	}
	for i := range results {
		results[i].Job.Path = fmt.Sprintf("pkg/f%d.py", i)
	}
	r := Aggregate("o", "r", "c", 3, 3, results, nil) // report.go:178
	if r.SelectedFiles != 2 || r.WholeSuiteFiles != 1 || r.UncoveredFiles != 1 {
		t.Errorf("got selected=%d whole=%d uncovered=%d", r.SelectedFiles, r.WholeSuiteFiles, r.UncoveredFiles)
	}
	var w *WeakFile
	for i := range r.Weakest {
		if r.Weakest[i].Path == "pkg/f2.py" {
			w = &r.Weakest[i]
		}
	}
	if w == nil || !w.Uncovered {
		t.Errorf("uncovered weak file must be marked: %+v", w)
	}
}
