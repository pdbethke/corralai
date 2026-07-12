// SPDX-License-Identifier: Elastic-2.0

package testgen

import "strings"

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
