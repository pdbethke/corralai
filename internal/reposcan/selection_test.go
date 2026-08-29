// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"context"
	"errors"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
)

func TestSelectionEvidenceNoSelectorIsWholeSuiteDisclosed(t *testing.T) {
	ruby, _ := lang.ByName("ruby")
	ev := CollectSelectionEvidence(context.Background(), &fakeRunner{}, nil, ruby, []string{"rspec"})
	if ev.Ran {
		t.Fatal("ran with no selector")
	}
	sel := ev.For(ruby, "", "lib/a.rb", "spec/a_spec.rb", []string{"rspec"})
	if sel.Fallback != "no selector for ruby" || sel.Cmd != nil || sel.Method != "" {
		t.Errorf("got %+v", sel)
	}
}

func TestSelectionEvidenceRunFailureIsDisclosedPerFile(t *testing.T) {
	py, _ := lang.ByName("python")
	r := &fakeRunner{err: errors.New("boom")}
	ev := CollectSelectionEvidence(context.Background(), r, nil, py, []string{"pytest"})
	if ev.Ran || ev.Note != "python: selection evidence run failed: boom" {
		t.Errorf("got %+v", ev)
	}
	if r.got == nil || r.got[0] != "sh" {
		t.Errorf("the runner was not handed Instrument's command: %v", r.got)
	}
	sel := ev.For(py, "", "pkg/a.py", "tests/test_a.py", []string{"pytest"})
	if sel.Fallback != ev.Note {
		t.Errorf("Fallback = %q, want the note", sel.Fallback)
	}
}

func TestSelectionEvidenceForNarrowsFromRecordedEvidence(t *testing.T) {
	py, _ := lang.ByName("python")
	raw := `{"meta":{"show_contexts":true},"totals":{"covered_lines":1},"files":{"pkg/a.py":{"summary":{"num_statements":1,"covered_lines":1},"contexts":{"1":["tests/test_a.py::test_x|run"]}},"tests/test_a.py":{"summary":{"num_statements":1,"covered_lines":1},"contexts":{"1":["tests/test_a.py::test_x|run"]}}}}`
	ev := CollectSelectionEvidence(context.Background(), &fakeRunner{out: raw}, nil, py, []string{"pytest"})
	if !ev.Ran {
		t.Fatalf("did not run: %s", ev.Note)
	}
	sel := ev.For(py, "", "pkg/a.py", "tests/test_a.py", []string{"pytest"})
	if sel.Fallback != "" || len(sel.Tests) != 1 || sel.Tests[0] != "tests/test_a.py::test_x" {
		t.Errorf("got %+v", sel)
	}
	// Absent file whose paired test DID run: uncovered, not a fallback.
	sel = ev.For(py, "", "pkg/other.py", "tests/test_a.py", []string{"pytest"})
	if sel.Fallback != "" || sel.Method != "coverage-context" || len(sel.Tests) != 0 {
		t.Errorf("absent file with a present test must be uncovered, got %+v", sel)
	}
	// Absent file whose paired test never appeared: whole suite, with the
	// selector's own error as the reason.
	sel = ev.For(py, "", "pkg/other.py", "tests/test_never.py", []string{"pytest"})
	if sel.Fallback == "" || sel.Cmd != nil {
		t.Errorf("unmeasured file must fall back disclosed, got %+v", sel)
	}
}

func TestSelectionEvidenceInstrumentRefusalIsDisclosed(t *testing.T) {
	py, _ := lang.ByName("python")
	ev := CollectSelectionEvidence(context.Background(), &fakeRunner{}, nil, py, []string{"make", "test"})
	if ev.Ran || ev.Note != "python: cannot instrument test command [make test]" {
		t.Errorf("got %+v", ev)
	}
}
