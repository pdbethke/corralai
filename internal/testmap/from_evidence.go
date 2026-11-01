// SPDX-License-Identifier: Elastic-2.0

package testmap

import (
	"sort"

	"github.com/pdbethke/corralai/internal/lang"
)

// MethodCoverageContext names a map built from one whole-suite coverage run
// that recorded which test executed which line.
const MethodCoverageContext = "coverage-context"

// FromEvidence builds a map from one parsed coverage-context run. align turns
// a path as the evidence stores it into the repo-relative key the map uses
// (false drops the file: it is outside the root, so nothing can be said of
// it); unitOf names the unit a test id belongs to. Both are passed in so this
// package stays language-free. Lang, Runner, Command, Scope and Tree are the
// caller's to set: they describe how the evidence was collected, which only
// the caller knows.
func FromEvidence(doc lang.EvidenceDoc, align func(path string) (string, bool), unitOf func(testID string) string) Map {
	m := Map{
		Schema:  Schema,
		Method:  MethodCoverageContext,
		Of:      doc.Of,
		Sources: map[string]Source{},
		Tests:   map[string]Node{},
	}
	for path, f := range doc.Files {
		src, ok := align(path)
		if !ok {
			continue
		}
		m.Sources[src] = Source{HasStatic: len(f.Static) > 0, HasStatements: f.HasStatements, Static: f.Static}
		for id, rngs := range f.Tests {
			if _, seen := m.Tests[id]; !seen {
				m.Tests[id] = Node{ID: id, Grain: GrainTest, Unit: unitOf(id)}
			}
			n := 0
			for _, r := range rngs {
				n += r.End - r.Start + 1
			}
			m.Edges = append(m.Edges, Edge{Test: id, Source: src, Lines: rngs, Executed: n})
		}
	}
	sort.Slice(m.Edges, func(i, j int) bool {
		if m.Edges[i].Source != m.Edges[j].Source {
			return m.Edges[i].Source < m.Edges[j].Source
		}
		return m.Edges[i].Test < m.Edges[j].Test
	})
	return m
}
