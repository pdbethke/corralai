// SPDX-License-Identifier: Elastic-2.0

package reposcan

// ReasonNotSelected marks a candidate the scan deliberately did not audit
// because it fell outside the bound. It is ACCOUNTED, never dropped: a reader
// must be able to see the scan chose a subset and how large that subset was,
// which is what makes a bounded scan honest rather than a silent truncation.
const ReasonNotSelected = "not-selected"

// Select takes the head of a ranked candidate list and accounts the rest.
// limit <= 0 means no bound.
func Select(ranked []Candidate, limit int) ([]Candidate, []Exclusion) {
	if limit <= 0 || limit >= len(ranked) {
		return ranked, nil
	}
	excluded := make([]Exclusion, 0, len(ranked)-limit)
	for _, c := range ranked[limit:] {
		excluded = append(excluded, Exclusion{Path: c.Path, Reason: ReasonNotSelected})
	}
	return ranked[:limit], excluded
}
