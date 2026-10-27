// SPDX-License-Identifier: Elastic-2.0

package testmap

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
)

// TEMPORARY: deleted with pyPlugin's selector methods (Task 6). It captures
// testdata/python-selector-golden.json from Python's own selector and proves,
// while that selector still exists, that the golden is its answer.

func (e pyEquiv) python() observer {
	ts := e.p.(lang.TestSelector)
	return observer{
		sel: func(c selectCase) (lang.Selection, error) {
			return ts.Select(c.evidence, c.root, c.code, c.test, c.cmd)
		},
		forSpan: ts.ForSpan,
		authored: func(sel lang.Selection, testCmd []string, _, path string) ([]string, bool) {
			return ts.WithAuthoredTest(sel, testCmd, path), true
		},
	}
}

func TestCapturePythonSelectorGolden(t *testing.T) {
	if os.Getenv("CORRAL_CAPTURE_PY_GOLDEN") != "1" {
		t.Skip("capture only on request")
	}
	e := newPyEquiv(t)
	live, why := liveFixtureEvidence(t, e)
	if live == nil {
		t.Fatalf("the golden must hold the live observations too: %s", why)
	}
	b, err := json.MarshalIndent(observe(t, e.python(), live), "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pyGolden, append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPythonSelectorMatchesGolden(t *testing.T) {
	e := newPyEquiv(t)
	live, _ := liveFixtureEvidence(t, e)
	want := readPyGolden(t)
	got := observe(t, e.python(), live)
	for k, w := range want {
		if live == nil && len(k) > 0 && got[k] == "" {
			continue
		}
		if got[k] != w {
			t.Errorf("%s:\n python %s\n golden %s", k, got[k], w)
		}
	}
	if len(got) != len(want) && live != nil {
		t.Errorf("python observed %d, golden holds %d", len(got), len(want))
	}
}
