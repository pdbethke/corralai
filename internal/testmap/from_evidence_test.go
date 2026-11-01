// SPDX-License-Identifier: Elastic-2.0

package testmap

import (
	"reflect"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
)

func TestFromEvidenceBuildsTestGrainNodes(t *testing.T) {
	doc := lang.EvidenceDoc{Of: 3, Files: map[string]lang.FileEvidence{
		"./pkg/a.py": {
			Tests: map[string][]lang.LineRange{
				"tests/test_a.py::test_x": {{Start: 1, End: 3}, {Start: 7, End: 7}},
				"tests/test_b.py::test_y": nil,
			},
			Static:        []lang.LineRange{{Start: 1, End: 1}},
			HasStatements: true,
		},
		"/elsewhere/c.py": {Tests: map[string][]lang.LineRange{"tests/test_a.py::test_x": {{Start: 1, End: 1}}}},
	}}
	align := func(p string) (string, bool) {
		if strings.HasPrefix(p, "/") {
			return "", false
		}
		return strings.TrimPrefix(p, "./"), true
	}
	unitOf := func(id string) string { return strings.SplitN(id, "::", 2)[0] }
	m := FromEvidence(doc, align, unitOf)

	if m.Method != "coverage-context" || m.Of != 3 || m.Schema != Schema {
		t.Fatalf("header: %+v", m)
	}
	if len(m.Sources) != 1 {
		t.Fatalf("a path align rejects must be dropped, got %v", m.Sources)
	}
	s := m.Sources["pkg/a.py"]
	if !s.HasStatic || !s.HasStatements || len(s.Static) != 1 {
		t.Fatalf("source: %+v", s)
	}
	if len(m.Tests) != 2 {
		t.Fatalf("tests: %v", m.Tests)
	}
	for id, n := range m.Tests {
		if n.Grain != GrainTest || n.ID != id || n.Unit != unitOf(id) {
			t.Errorf("node %s: %+v", id, n)
		}
	}
	want := []Edge{
		{Test: "tests/test_a.py::test_x", Source: "pkg/a.py", Lines: []lang.LineRange{{Start: 1, End: 3}, {Start: 7, End: 7}}, Executed: 4},
		{Test: "tests/test_b.py::test_y", Source: "pkg/a.py", Executed: 0},
	}
	if !reflect.DeepEqual(m.Edges, want) {
		t.Fatalf("edges:\n got %+v\nwant %+v", m.Edges, want)
	}
	if len(m.EdgesTo("pkg/a.py")) != 2 {
		t.Fatal("EdgesTo")
	}
}
