// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/mutantsources"
)

// A both-sources exam samples the tool's mutants under the same per-file
// cap the (possibly deadline-fitted) generator was asked for, so the exam
// can be up to twice what the budget line says. The line says both shares
// and the sum, and is the plain budget line when no tool shares the exam.
func TestTheBudgetLineSaysTheToolsShareOfTheExam(t *testing.T) {
	b := MutantBudget{Rule: BudgetRuleFitted, Total: 12, BeforeFit: 20, Complexity: 9, DeadlineMillis: 600000, Trees: 4, RoundCostMillis: 30000}
	if got := b.WithTool(0); got != b.String() {
		t.Errorf("no tool share: %q, want the plain budget line %q", got, b.String())
	}
	got := b.WithTool(12)
	for _, want := range []string{"12 model + 12 tool", "up to 24", "same per-file cap", b.String()} {
		if !strings.Contains(got, want) {
			t.Errorf("WithTool(12) = %q, lacks %q", got, want)
		}
	}
}

// ToolShare counts the exam's tool mutants only beside a seated generator:
// a tool-only run (no generator seat) has no model share to add them to,
// and its budget line already says the budget capped the tool's sample.
func TestToolShareCountsToolMutantsBesideAGenerator(t *testing.T) {
	srcs := map[string]mutantsources.MutantSource{
		"m1":          {Kind: mutantsources.KindModel, Model: "g"},
		"t/stryker/1": {Kind: mutantsources.KindTool, Tool: "stryker@10.0.0"},
		"t/stryker/2": {Kind: mutantsources.KindTool, Tool: "stryker@10.0.0"},
	}
	if got := ToolShare(Verdict{RegionsTotal: 1, MutantSources: srcs}); got != 2 {
		t.Errorf("both sources: ToolShare = %d, want 2", got)
	}
	if got := ToolShare(Verdict{RegionsTotal: 0, MutantSources: srcs}); got != 0 {
		t.Errorf("tool-only: ToolShare = %d, want 0", got)
	}
	if got := ToolShare(Verdict{RegionsTotal: 1}); got != 0 {
		t.Errorf("no sources: ToolShare = %d, want 0", got)
	}
}
