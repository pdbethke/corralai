// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"fmt"
	"sort"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/mutantsources"
)

// checkMutantSources refuses a RunSpec whose handed-in mutants and their
// sources do not agree (RunSpec.ToolMutants, RunSpec.MutantSources). Every
// refusal here is a combination cmd/ never builds, so it is a programming
// error, stopped before anything is enqueued rather than resolved by a rule
// nobody chose — a source the run had to guess is a record that cannot be
// queried for what it claims.
func checkMutantSources(rs RunSpec) error {
	if rs.PresetMutants != nil && rs.ToolMutants != nil {
		return fmt.Errorf("both PresetMutants and ToolMutants are set: a tool-only run is a preset run, a both-sources run appends ToolMutants to generation; the two are different exams")
	}
	given := map[string]bool{}
	for _, m := range rs.ToolMutants {
		src, ok := rs.MutantSources[m.ID]
		if !ok {
			return fmt.Errorf("tool mutant %q has no MutantSources entry", m.ID)
		}
		if src.Kind != mutantsources.KindTool {
			return fmt.Errorf("tool mutant %q has a %q source, want %q", m.ID, src.Kind, mutantsources.KindTool)
		}
		given[m.ID] = true
	}
	if rs.PresetMutants != nil && rs.MutantSources != nil {
		for _, m := range rs.PresetMutants {
			if _, ok := rs.MutantSources[m.ID]; !ok {
				return fmt.Errorf("preset mutant %q has no MutantSources entry, and the others do", m.ID)
			}
			given[m.ID] = true
		}
	}
	for _, id := range sortedSourceIDs(rs.MutantSources) {
		if !given[id] {
			return fmt.Errorf("MutantSources names %q, a mutant the run was not given (generated mutants' sources are the driver's to fill)", id)
		}
		switch k := rs.MutantSources[id].Kind; k {
		case mutantsources.KindModel, mutantsources.KindTool:
		default:
			return fmt.Errorf("MutantSources[%q] has kind %q, want %q or %q", id, k, mutantsources.KindModel, mutantsources.KindTool)
		}
	}
	return nil
}

// examSources is the source of every mutant in the assembled exam: the
// handed-in source where the spec has one, and — for a mutant this run's
// generator planted (generated[id]) — Kind model under generatorModel.
// Keyed by exactly the exam's IDs: a mutant with no known source (a replay
// handed none) gets no entry, and no entry outlives its mutant. nil when
// nothing has a source.
func examSources(exam []adequacy.Mutant, given map[string]mutantsources.MutantSource, generated map[string]bool, generatorModel string) map[string]mutantsources.MutantSource {
	var out map[string]mutantsources.MutantSource
	for _, m := range exam {
		src, ok := given[m.ID]
		if !ok && generated[m.ID] {
			src, ok = mutantsources.MutantSource{Kind: mutantsources.KindModel, Model: generatorModel}, true
		}
		if !ok {
			continue
		}
		if out == nil {
			out = make(map[string]mutantsources.MutantSource, len(exam))
		}
		out[m.ID] = src
	}
	return out
}

func sortedSourceIDs(srcs map[string]mutantsources.MutantSource) []string {
	ids := make([]string, 0, len(srcs))
	for id := range srcs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
