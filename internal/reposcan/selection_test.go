// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/sandbox"
)

// fakeDetailedRunner is a stub detailedRunner (and, by having Enumerate too,
// a commandRunner) — a runner that CAN report an instrumented run's exit
// code and stderr, the seam EnumerateDetailed exists for.
type fakeDetailedRunner struct {
	res sandbox.EnumerateResult
	err error
	got []string
}

func (f *fakeDetailedRunner) Enumerate(_ context.Context, _ map[string]string, cmd []string) (string, error) {
	f.got = cmd
	return f.res.Output, f.err
}

func (f *fakeDetailedRunner) EnumerateDetailed(_ context.Context, _ map[string]string, cmd []string) (sandbox.EnumerateResult, error) {
	f.got = cmd
	return f.res, f.err
}

func TestSelectionEvidenceNoSelectorIsWholeSuiteDisclosed(t *testing.T) {
	ruby, _ := lang.ByName("ruby")
	ev := CollectSelectionEvidence(context.Background(), &fakeRunner{}, nil, ruby, []string{"rspec"}, nil)
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
	ev := CollectSelectionEvidence(context.Background(), r, nil, py, []string{"pytest"}, nil)
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
	raw := `{"format":"corral-selection-3","tests":1,"files":{` +
		`"pkg/a.py":{"tests":["tests/test_a.py::test_x"],"lines":{},"static":[]},` +
		`"tests/test_a.py":{"tests":["tests/test_a.py::test_x"],"lines":{},"static":[]}}}`
	ev := CollectSelectionEvidence(context.Background(), &fakeRunner{out: raw}, nil, py, []string{"pytest"}, nil)
	if !ev.Ran {
		t.Fatalf("did not run: %s", ev.Note)
	}
	sel := ev.For(py, "", "pkg/a.py", "tests/test_a.py", []string{"pytest"})
	if sel.Fallback != "" || len(sel.Tests) != 1 || sel.Tests[0] != "tests/test_a.py::test_x" {
		t.Errorf("got %+v", sel)
	}
	// Absent file whose paired test DID run: STILL not "uncovered" — the
	// evidence never measured it at all (absence of evidence is not
	// evidence of absence: an editable/src-layout install can legitimately
	// drop a genuinely-covered file from the report). Falls back to the
	// whole suite, disclosed, exactly like the unmeasured-test case below.
	sel = ev.For(py, "", "pkg/other.py", "tests/test_a.py", []string{"pytest"})
	if sel.Fallback == "" || sel.Method != "" || len(sel.Tests) != 0 {
		t.Errorf("absent file must fall back disclosed, not read as uncovered, got %+v", sel)
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
	ev := CollectSelectionEvidence(context.Background(), &fakeRunner{}, nil, py, []string{"make", "test"}, nil)
	if ev.Ran || ev.Note != "python: cannot instrument test command [make test]" {
		t.Errorf("got %+v", ev)
	}
}

// A zero-value SelectionEvidence — never collected, no note — must not yield
// an UNDISCLOSED whole-suite grade. Structural: every non-answer says why.
func TestSelectionEvidenceZeroValueIsDisclosedWholeSuite(t *testing.T) {
	py, _ := lang.ByName("python")
	var ev SelectionEvidence
	sel := ev.For(py, "", "pkg/a.py", "tests/test_a.py", []string{"pytest"})
	if sel.Fallback != "no selection evidence was collected" || sel.Method != "" || sel.Cmd != nil {
		t.Errorf("got %+v", sel)
	}
}

// THE DEFECT: an instrumented run whose shell wrapper exits non-zero BEFORE
// its own JSON-emitting step ever runs (python's Instrument: `…; rc=$?; [
// "$rc" -eq 0 ] || exit "$rc"; …`) prints NOTHING on stdout, and Enumerate
// reports that as (out="", err=nil) — a non-zero exit is a RESULT there,
// not an error. Recording that as Ran:true would be a Ran-but-empty
// evidence: RAN, but measuring nothing, indistinguishable on its face from
// a suite that genuinely covers zero files. Must be Ran:false, with the
// exit status named.
func TestSelectionEvidenceEmptyOutputIsNotRan(t *testing.T) {
	py, _ := lang.ByName("python")
	r := &fakeDetailedRunner{res: sandbox.EnumerateResult{Output: "", ExitCode: 4}}
	ev := CollectSelectionEvidence(context.Background(), r, nil, py, []string{"pytest"}, nil)
	if ev.Ran {
		t.Fatalf("an instrumented run that printed nothing must not be Ran, got %+v", ev)
	}
	if ev.Note == "" || !strings.Contains(ev.Note, "4") {
		t.Errorf("Note must name the exit status, got %q", ev.Note)
	}
	sel := ev.For(py, "", "pkg/a.py", "tests/test_a.py", []string{"pytest"})
	if sel.Fallback != ev.Note {
		t.Errorf("Fallback = %q, want the note", sel.Fallback)
	}
}

// The Python-specific hint: pytest's OWN error text for a venv missing the
// pytest-cov plugin Instrument's command requires — must be named plainly
// as such, not left as an opaque "printed nothing".
func TestSelectionEvidenceEmptyOutputNamesMissingPytestCov(t *testing.T) {
	py, _ := lang.ByName("python")
	stderr := "usage: pytest [options]\npytest: error: unrecognized arguments: --cov --cov-context=test --cov-report=\n"
	r := &fakeDetailedRunner{res: sandbox.EnumerateResult{Output: "", ExitCode: 4, Stderr: stderr}}
	ev := CollectSelectionEvidence(context.Background(), r, nil, py, []string{"pytest"}, nil)
	if ev.Ran {
		t.Fatalf("got Ran=true, want false: %+v", ev)
	}
	if !strings.Contains(ev.Note, "pytest-cov") {
		t.Errorf("Note must name pytest-cov, got %q", ev.Note)
	}
	if !strings.Contains(ev.Note, "pip install pytest-cov") {
		t.Errorf("Note must say how to fix it, got %q", ev.Note)
	}
}

// A runner with no detailed contract at all (the ordinary commandRunner,
// what every substrate implemented before this fix) still must not claim
// Ran:true for empty output — the exit status is simply unavailable, not a
// reason to trust the empty string.
func TestSelectionEvidenceEmptyOutputWithoutDetailedContractIsStillNotRan(t *testing.T) {
	py, _ := lang.ByName("python")
	ev := CollectSelectionEvidence(context.Background(), &fakeRunner{out: "   \n"}, nil, py, []string{"pytest"}, nil)
	if ev.Ran {
		t.Fatalf("got Ran=true, want false: %+v", ev)
	}
	if ev.Note == "" {
		t.Error("want a non-empty Note")
	}
}

// THE THIRD DEFECT: an editable/src-layout install measures every source
// file OUTSIDE the repo root, so the reducer's own repo-root filter drops
// every one of them and only the in-tree test files survive. A document
// that measured nothing but test files is unusable — trusting it would
// grade every real source file "uncovered" (present nowhere, sawTest true)
// though the evidence never actually measured it. Must fall back,
// disclosed, naming the likely cause.
func TestSelectionEvidencePathologicalDocumentFallsBack(t *testing.T) {
	py, _ := lang.ByName("python")
	raw := `{"format":"corral-selection-3","tests":1,"files":{` +
		`"tests/test_a.py":{"tests":["tests/test_a.py::test_x"],"lines":{},"static":[]}}}`
	ev := CollectSelectionEvidence(context.Background(), &fakeRunner{out: raw}, nil, py, []string{"pytest"}, nil)
	if ev.Ran {
		t.Fatalf("a document with no source files must not be usable, got %+v", ev)
	}
	if !strings.Contains(ev.Note, "editable") || !strings.Contains(ev.Note, "src-layout") {
		t.Errorf("Note must name the editable/src-layout cause, got %q", ev.Note)
	}
}

// A document with zero files at all is the same pathology in its most
// degenerate form and must be treated identically.
func TestSelectionEvidenceEmptyDocumentFallsBack(t *testing.T) {
	py, _ := lang.ByName("python")
	raw := `{"format":"corral-selection-3","tests":0,"files":{}}`
	ev := CollectSelectionEvidence(context.Background(), &fakeRunner{out: raw}, nil, py, []string{"pytest"}, nil)
	if ev.Ran {
		t.Fatalf("got Ran=true, want false: %+v", ev)
	}
}

// A good document — at least one measured source file — is unchanged: Ran,
// usable, no pathology note. Guards the fix above from over-triggering.
func TestSelectionEvidenceGoodDocumentIsUnchanged(t *testing.T) {
	py, _ := lang.ByName("python")
	raw := `{"format":"corral-selection-3","tests":1,"files":{` +
		`"pkg/a.py":{"tests":["tests/test_a.py::test_x"],"lines":{},"static":[]},` +
		`"tests/test_a.py":{"tests":["tests/test_a.py::test_x"],"lines":{},"static":[]}}}`
	ev := CollectSelectionEvidence(context.Background(), &fakeRunner{out: raw}, nil, py, []string{"pytest"}, nil)
	if !ev.Ran || ev.Note != "" {
		t.Errorf("got %+v, want a plain Ran:true evidence", ev)
	}
}

// THE THIRD MEASUREMENT: coverage.py's default (bare --cov) scope is the
// whole cwd, which makes "uncovered" unreachable for a src-layout/editable
// install — coverage measures a file by where python actually imported it
// from, an editable install imports it from OUTSIDE the checked-out tree,
// and the reducer's own repo-root filter drops it entirely. sourceRootsFor
// derives what CollectSelectionEvidence feeds a
// lang.SourceRootInstrumenter to fix that.

func TestSourceRootsForDerivesSrcLayout(t *testing.T) {
	py, _ := lang.ByName("python")
	got := sourceRootsFor(py, []string{
		"src/pkg/mod.py", "src/pkg/__init__.py", "src/pkg/dead.py", "tests/test_mod.py",
	})
	want := []string{"src"}
	if !stringSlicesEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSourceRootsForDerivesFlatPackage(t *testing.T) {
	py, _ := lang.ByName("python")
	got := sourceRootsFor(py, []string{"mypkg/core.py", "mypkg/util.py", "test_core.py"})
	want := []string{"mypkg"}
	if !stringSlicesEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSourceRootsForDerivesDotForRootLevelSources(t *testing.T) {
	py, _ := lang.ByName("python")
	got := sourceRootsFor(py, []string{"itsdangerous.py", "serializer.py", "test_serializer.py"})
	want := []string{"."}
	if !stringSlicesEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// Multiple genuine source roots come back deduped and SORTED — stable
// regardless of sourcePaths' own order, since a cache key is derived from
// the instrumented command this feeds.
func TestSourceRootsForDedupsAndSorts(t *testing.T) {
	py, _ := lang.ByName("python")
	got := sourceRootsFor(py, []string{
		"src/a.py", "src/b.py", "otherpkg/c.py", "tests/test_a.py",
	})
	want := []string{"otherpkg", "src"}
	if !stringSlicesEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A file belonging to a DIFFERENT language must never contribute a root —
// this repo's enumerated source list is every language mixed together.
func TestSourceRootsForIgnoresOtherLanguages(t *testing.T) {
	py, _ := lang.ByName("python")
	got := sourceRootsFor(py, []string{"src/a.py", "cmd/main.go", "web/app.js"})
	want := []string{"src"}
	if !stringSlicesEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// No source files at all (nil, or every path filtered out) derives no
// roots — not an error, sourceRootsFor's caller treats it as "none known".
func TestSourceRootsForEmptyWhenNothingQualifies(t *testing.T) {
	py, _ := lang.ByName("python")
	if got := sourceRootsFor(py, nil); len(got) != 0 {
		t.Errorf("got %v, want none", got)
	}
	if got := sourceRootsFor(py, []string{"tests/test_a.py"}); len(got) != 0 {
		t.Errorf("a test-only source list must derive no roots, got %v", got)
	}
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// End-to-end: CollectSelectionEvidence must actually THREAD the derived
// roots into the instrumented command, not merely compute them and drop
// them on the floor.
func TestCollectSelectionEvidenceThreadsSourceRootsIntoInstrument(t *testing.T) {
	py, _ := lang.ByName("python")
	r := &fakeRunner{out: `{"format":"corral-selection-3","tests":1,"files":{` +
		`"src/pkg/a.py":{"tests":["tests/test_a.py::test_x"],"lines":{},"static":[]}}}`}
	sourcePaths := []string{"src/pkg/a.py", "tests/test_a.py"}
	ev := CollectSelectionEvidence(context.Background(), r, nil, py, []string{"pytest"}, sourcePaths)
	if !ev.Ran {
		t.Fatalf("did not run: %s", ev.Note)
	}
	if r.got == nil || r.got[0] != "sh" {
		t.Fatalf("the runner was not handed an instrumented command: %v", r.got)
	}
	script := r.got[2]
	if !strings.Contains(script, "--cov=src --cov-context=test") {
		t.Errorf("script must be scoped to the derived root \"src\", got:\n%s", script)
	}
	if strings.Contains(script, " --cov --cov-context") {
		t.Errorf("script must not fall back to bare --cov once a root was derivable:\n%s", script)
	}
}

// With no sourcePaths at all (nil — a caller with nothing to derive from,
// or a plugin that never implements the optional interface), the command
// is byte-identical to plain Instrument's bare --cov.
func TestCollectSelectionEvidenceWithNoSourcePathsFallsBackToBareCov(t *testing.T) {
	py, _ := lang.ByName("python")
	r := &fakeRunner{out: `{"format":"corral-selection-3","tests":1,"files":{` +
		`"pkg/a.py":{"tests":["tests/test_a.py::test_x"],"lines":{},"static":[]}}}`}
	CollectSelectionEvidence(context.Background(), r, nil, py, []string{"pytest"}, nil)
	script := r.got[2]
	if !strings.Contains(script, " --cov --cov-context=test") {
		t.Errorf("script must fall back to bare --cov with no sourcePaths, got:\n%s", script)
	}
}
