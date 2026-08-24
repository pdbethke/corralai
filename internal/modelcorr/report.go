// SPDX-License-Identifier: Elastic-2.0

package modelcorr

import (
	"sort"

	"github.com/pdbethke/corralai/internal/mutantattempts"
)

// FromAttempts groups stored seat outcomes by (RECORD, path) and compares the
// two models that faced that mutant set.
//
// The RECORD id is the run discriminator, and it is not optional. Grouping by
// path alone would merge two audits of the same file — different mutants,
// different survivor sets — into one last-write-wins vector, which is exactly
// the cross-run pooling this package rejects: correlation is defined WITHIN a
// run, over one fixed mutant set.
//
// A group with anything other than exactly two models is SKIPPED, not
// approximated, for the same reason.
func FromAttempts(as []mutantattempts.Attempt) ([]Pair, error) {
	type key struct {
		recordID int64
		path     string
	}
	groups := map[key]map[string]map[string]bool{}
	for _, a := range as {
		k := key{a.RecordID, a.Path}
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
