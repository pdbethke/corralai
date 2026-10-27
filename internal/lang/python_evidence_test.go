// SPDX-License-Identifier: Elastic-2.0

package lang

import "testing"

var _ TestEvidence = pyPlugin{}

// Evidence and Index read one document; they must not drift. For every file
// Evidence returns, each test's executed lines summed equal Index's count, and
// the static/statement flags agree.
func TestPythonEvidenceAgreesWithIndex(t *testing.T) {
	raw := recordedEvidence(t)
	doc, err := pyPlugin{}.Evidence(raw)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := pyPlugin{}.Index(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Files) == 0 || doc.Of == 0 {
		t.Fatalf("fixture produced no evidence: %+v", doc)
	}
	for path, f := range doc.Files {
		p, ok := alignPyPath(path, "")
		if !ok {
			continue
		}
		cov, ok := idx[p]
		if !ok {
			t.Fatalf("Index lacks %s", p)
		}
		if len(f.Tests) != len(cov.Tests) {
			t.Errorf("%s: %d tests vs Index %d", p, len(f.Tests), len(cov.Tests))
		}
		for id, rngs := range f.Tests {
			n := 0
			for _, r := range rngs {
				n += r.End - r.Start + 1
			}
			if got, ok := cov.Tests[id]; !ok || got != n {
				t.Errorf("%s %s: lines %d, Index %d (present %v)", p, id, n, got, ok)
			}
		}
		if (len(f.Static) > 0) != cov.HasStatic || f.HasStatements != cov.HasStatements {
			t.Errorf("%s: flags disagree: %+v vs %+v", p, f, cov)
		}
	}
}

func TestPythonEvidenceRefusesOtherFormats(t *testing.T) {
	if _, err := (pyPlugin{}).Evidence([]byte(`{"format":"nope","files":{}}`)); err == nil {
		t.Fatal("a document of another format must be refused")
	}
}
