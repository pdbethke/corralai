// SPDX-License-Identifier: Elastic-2.0

package testmap

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
)

func sample() Map {
	return Map{
		Schema: Schema, Lang: "python", Runner: "pytest", Command: []string{"pytest", "-q"},
		Scope: []string{"src"}, Tree: "abc", Method: "coverage-context", Of: 2,
		Sources: map[string]Source{
			"src/a.py": {HasStatic: true, HasStatements: true, Static: []lang.LineRange{{Start: 1, End: 3}, {Start: 7, End: 9}}},
			"src/b.py": {HasStatements: true},
		},
		Tests: map[string]Node{
			"t1": {ID: "t1", Grain: GrainTest, Unit: "u1", Files: []string{"tests/test_a.py"}},
			"u1": {ID: "u1", Grain: GrainUnit, Err: "no evidence"},
		},
		Edges: []Edge{
			{Test: "t1", Source: "src/b.py", Lines: []lang.LineRange{{Start: 2, End: 2}}, Executed: 1},
			{Test: "t1", Source: "src/a.py", Lines: []lang.LineRange{{Start: 1, End: 3}, {Start: 8, End: 8}}, Executed: 4},
		},
		Note: "partial",
	}
}

func TestMapRoundTripsThroughJSON(t *testing.T) {
	in := sample()
	var buf bytes.Buffer
	if err := Write(&buf, in); err != nil {
		t.Fatal(err)
	}
	out, err := Read(&buf)
	if err != nil {
		t.Fatal(err)
	}
	// Write sorts Edges, so compare against the sorted form of the input.
	want := in
	want.Edges = []Edge{in.Edges[1], in.Edges[0]}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("round trip differs:\n got %#v\nwant %#v", out, want)
	}
}

func TestDigestIsStableAcrossMapOrder(t *testing.T) {
	a := sample()
	b := sample()
	b.Sources = map[string]Source{}
	b.Tests = map[string]Node{}
	for _, k := range []string{"src/b.py", "src/a.py"} {
		b.Sources[k] = a.Sources[k]
	}
	for _, k := range []string{"u1", "t1"} {
		b.Tests[k] = a.Tests[k]
	}
	b.Edges = []Edge{a.Edges[1], a.Edges[0]}
	if a.Digest() != b.Digest() {
		t.Fatalf("digests differ: %s vs %s", a.Digest(), b.Digest())
	}
	b.Note = "other"
	if a.Digest() == b.Digest() {
		t.Fatal("digest ignored a changed field")
	}
}

func TestReadRefusesAnotherSchema(t *testing.T) {
	_, err := Read(strings.NewReader(`{"schema": 2}`))
	if err == nil || !strings.Contains(err.Error(), "schema 2") {
		t.Fatalf("want schema 2 refusal, got %v", err)
	}
	if _, err := Read(strings.NewReader(`{}`)); err == nil || !strings.Contains(err.Error(), "schema 0") {
		t.Fatalf("a missing schema must be refused as schema 0, got %v", err)
	}
}

func TestUnreportedListsNodesWithErr(t *testing.T) {
	m := Map{Tests: map[string]Node{"z": {ID: "z", Err: "x"}, "a": {ID: "a", Err: "y"}, "ok": {ID: "ok"}}}
	if got, want := m.Unreported(), []string{"a", "z"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if got := (Map{}).Unreported(); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestEdgesToReturnsOnlyThatSource(t *testing.T) {
	m := sample()
	got := m.EdgesTo("src/a.py")
	if len(got) != 1 || got[0].Source != "src/a.py" {
		t.Fatalf("got %#v", got)
	}
	if len(m.EdgesTo("nope.py")) != 0 {
		t.Fatal("unknown source must have no edges")
	}
}
