// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/modelcorr"
)

// psf/requests, 2026-09-04: the shadow writer proved 10 of 12 on utils.py
// and 15 of 15 on adapters.py against the same survivors the primary proved
// 12 of 12 and 15 of 15 on. Both writers missing almost nothing left too few
// misses for a coefficient, the ledger stored NULL, and the counts lived only
// in the run log — a measurement computed and not recorded. The report now
// says what each seat proved and why the overlap is withheld.
func TestWriterPairLineSaysWhatEachSeatProvedEvenWhenTheOverlapIsWithheld(t *testing.T) {
	p := &modelcorr.Pair{ModelA: "gemini-3.6-flash", ModelB: "claude-sonnet-5", Mutants: 12, SurvivedA: 0, SurvivedB: 2, UnionSurvivors: 2, SharedSurvivors: 0}
	line := writerPairLine(p)
	for _, want := range []string{"gemini-3.6-flash proved 12 of 12", "challenger claude-sonnet-5 proved 10 of 12", "withheld: 2 in the union, fewer than the 3"} {
		if !strings.Contains(line, want) {
			t.Errorf("line %q lacks %q", line, want)
		}
	}
	p.Sufficient, p.UnionSurvivors, p.SharedSurvivors, p.Jaccard = true, 13, 9, 0.692
	if line := writerPairLine(p); !strings.Contains(line, "both missed 9 of the 13 either missed (Jaccard 0.692)") {
		t.Errorf("sufficient pair line = %q", line)
	}
	if writerPairLine(nil) != "" {
		t.Error("no pair must print nothing, not a zero pair")
	}
}
