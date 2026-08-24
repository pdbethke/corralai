// SPDX-License-Identifier: Elastic-2.0

package modelcorr

import (
	"sort"

	"github.com/pdbethke/corralai/internal/scanstore"
)

// FromAttempts groups stored seat outcomes by (scan, path) and compares the
// two models that faced that mutant set.
//
// A group with anything other than exactly two models is SKIPPED, not
// approximated: correlation is defined here as a within-run, two-seat
// comparison over one fixed mutant set.
func FromAttempts(as []scanstore.MutantAttempt) ([]Pair, error) {
	type key struct {
		scanID int64
		path   string
	}
	groups := map[key]map[string]map[string]bool{}
	for _, a := range as {
		k := key{a.ScanID, a.Path}
		if groups[k] == nil {
			groups[k] = map[string]map[string]bool{}
		}
		if groups[k][a.Model] == nil {
			groups[k][a.Model] = map[string]bool{}
		}
		groups[k][a.Model][a.MutantID] = a.Outcome == "killed"
	}
	var out []Pair
	for _, byModel := range groups {
		if len(byModel) != 2 {
			continue
		}
		models := make([]string, 0, 2)
		for m := range byModel {
			models = append(models, m)
		}
		sort.Strings(models) // deterministic A/B ordering
		p, err := Compare(
			Vector{Model: models[0], Killed: byModel[models[0]]},
			Vector{Model: models[1], Killed: byModel[models[1]]},
		)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ModelA != out[j].ModelA {
			return out[i].ModelA < out[j].ModelA
		}
		return out[i].ModelB < out[j].ModelB
	})
	return out, nil
}
