// SPDX-License-Identifier: Elastic-2.0

package testgen

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/pdbethke/corralai/internal/adequacy"
)

// Verdict is the reviewer's classification of one surviving mutant: a real
// coverage GAP the candidate test should have caught, or an EQUIVALENT
// mutant that no test could meaningfully distinguish.
type Verdict struct {
	MutantID  string
	RealGap   bool
	Rationale string
}

// parseVerdicts extracts one Verdict per line of the form
// "MUTANT <id>: <GAP|EQUIVALENT>: <rationale>". Class is case-insensitive;
// RealGap is true only for GAP. Lines that don't match (preamble, an
// unrecognized class) are skipped. Kept verdicts preserve response order.
func parseVerdicts(resp string) []Verdict {
	var out []Verdict
	for _, line := range strings.Split(resp, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "MUTANT ")
		if !ok {
			continue
		}
		id, after, ok := strings.Cut(rest, ":")
		if !ok {
			continue
		}
		classStr, rationale, ok := strings.Cut(after, ":")
		if !ok {
			continue
		}
		cls := strings.ToUpper(strings.TrimSpace(classStr))
		if cls != "GAP" && cls != "EQUIVALENT" {
			continue
		}
		out = append(out, Verdict{
			MutantID:  strings.TrimSpace(id),
			RealGap:   cls == "GAP",
			Rationale: strings.TrimSpace(rationale),
		})
	}
	return out
}

// reviewSystem instructs the model to act as an independent TEST-REVIEWER —
// a third generative agent, distinct from the writer (writeTestSystem) and
// the generator (genMutantsSystem). It classifies each surviving mutant the
// candidate test failed to catch as a real coverage GAP or an EQUIVALENT
// mutant, turning raw survivors into curated feedback for the CISO.
const reviewSystem = `You are a TEST-REVIEWER. You are given a security GOAL, the compliant code, a candidate test, and MUTATIONS that violate the goal but the test did NOT catch. For EACH mutation decide:
- GAP: the mutation genuinely violates the goal and the test SHOULD have caught it — a real coverage gap.
- EQUIVALENT: the mutation does not actually violate the goal under any legitimate input (or is behaviourally equivalent to the compliant code) — not a real gap.
Return ONE line per mutation, EXACTLY: "MUTANT <id>: <GAP|EQUIVALENT>: <one-line rationale>". No other prose.`

// TriageSurvivors asks m to classify each surviving mutant — one the
// candidate test failed to kill — as a real coverage GAP or an EQUIVALENT
// mutant. Survivors that reach here already escaped the writer's and
// generator's judgment; this is an independent third read, not a rubber
// stamp. Empty survivors short-circuit before any model call.
func TriageSurvivors(ctx context.Context, m LLM, goal, code, test string, survivors []adequacy.Mutant) ([]Verdict, error) {
	if len(survivors) == 0 {
		return nil, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "GOAL:\n%s\n\nCOMPLIANT CODE:\n%s\n\nCANDIDATE TEST:\n%s\n\nUNCAUGHT MUTATIONS:\n", goal, code, test)
	for _, s := range survivors {
		fmt.Fprintf(&b, "MUTANT %s:\n%s\n\n", s.ID, s.Code)
	}
	resp, err := m.Ask(ctx, reviewSystem, b.String())
	if err != nil {
		return nil, err
	}
	verdicts := parseVerdicts(resp)
	if len(verdicts) == 0 {
		return nil, errors.New("testgen: reviewer returned no parseable verdicts")
	}
	return verdicts, nil
}
