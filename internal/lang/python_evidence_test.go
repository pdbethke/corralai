// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"reflect"
	"testing"
)

var _ TestEvidence = pyPlugin{}

func TestPythonEvidenceRefusesOtherFormats(t *testing.T) {
	if _, err := (pyPlugin{}).Evidence([]byte(`{"format":"nope","files":{}}`)); err == nil {
		t.Fatal("a document of another format must be refused")
	}
}

// Pinned against the recorded fixture with values written out by hand, NOT
// derived through Index (which is built on Evidence and so cannot catch a
// wrong Evidence).
func TestPythonEvidencePinnedToFixture(t *testing.T) {
	doc, err := pyPlugin{}.Evidence(recordedEvidence(t))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Of != 3 || len(doc.Files) != 5 {
		t.Fatalf("Of=%d files=%d, want 3 and 5", doc.Of, len(doc.Files))
	}
	want := map[string]FileEvidence{
		"pkg/calc.py": {
			Tests: map[string][]LineRange{
				"tests/test_calc.py::test_add":  {{Start: 2, End: 2}},
				"tests/test_other.py::test_sub": {{Start: 6, End: 6}},
			},
			Static:        []LineRange{{Start: 1, End: 1}, {Start: 5, End: 5}},
			HasStatements: true,
		},
		"tests/test_other.py": {
			Tests: map[string][]LineRange{
				"tests/test_other.py::test_sub":     {{Start: 5, End: 5}},
				"tests/test_other.py::test_trivial": {{Start: 9, End: 9}},
			},
			Static:        []LineRange{{Start: 1, End: 1}, {Start: 4, End: 4}, {Start: 8, End: 8}},
			HasStatements: true,
		},
	}
	for path, w := range want {
		if got := doc.Files[path]; !reflect.DeepEqual(got, w) {
			t.Errorf("%s:\n got %+v\nwant %+v", path, got, w)
		}
	}
	if e := doc.Files["pkg/__init__.py"]; len(e.Tests) != 0 || len(e.Static) != 0 {
		t.Errorf("empty file carries evidence: %+v", e)
	}
}

// A lines index that names no test drops that range; the test stays listed.
func TestPythonEvidenceMalformedLinesIndexDropsRangeKeepsTest(t *testing.T) {
	raw := `{"format":"corral-selection-3","tests":2,"files":{"a.py":{"tests":["t1","t2"],"lines":{"0":[[1,2]],"9":[[5,5]],"x":[[6,6]]},"static":[],"statements":0}}}`
	doc, err := pyPlugin{}.Evidence([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	f := doc.Files["a.py"]
	want := map[string][]LineRange{"t1": {{Start: 1, End: 2}}, "t2": nil}
	if !reflect.DeepEqual(f.Tests, want) {
		t.Fatalf("tests = %+v, want %+v", f.Tests, want)
	}
	if f.HasStatements {
		t.Error("statements:0 must read as no statements")
	}
}
