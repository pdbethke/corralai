// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"strings"
	"testing"
)

// The measured problem, from a gemini-3.6-flash audit of pallets/flask on
// 2026-07-31 — the first run whose authored test was ever retained and could
// be executed by hand:
//
//	13 authored tests -> 10 PASSED on clean code, 3 failed
//
// The compliant check is all-or-nothing per FILE, so those 3 discarded all 13.
// Ten tests that might well have killed survivors were thrown away because
// three carried wrong API assumptions.
//
// Asking the model to repair itself is one answer (and is what the reissue
// loop does), but it depends on the model actually being able to. Deselecting
// the failing tests and scoring with the remainder does not depend on the
// model at all — it is arithmetic on the runner's own output.
func TestPythonFailedTests(t *testing.T) {
	p, _ := ByName("python")
	fd, ok := p.(FailureDeselector)
	if !ok {
		t.Fatal("the python plugin must implement FailureDeselector — pytest names its failures precisely and supports --deselect")
	}

	// Real pytest output shape, including the trailing " - <error>" summary
	// tail that must NOT become part of the selector.
	const out = `============================= test session starts ==============================
collected 13 items

tests/test_cli_corral.py ..F..F.......                                   [100%]

=================================== FAILURES ===================================
E       AttributeError: 'function' object has no attribute 'make_context'
=========================== short test summary info ============================
FAILED tests/test_cli_corral.py::test_with_appcontext_ctx_invoke - AttributeError: ...
FAILED tests/test_cli_corral.py::test_set_app_option - assert 2 == 0
3 failed, 10 passed in 0.06s`

	got := fd.FailedTests(out)
	want := []string{
		"tests/test_cli_corral.py::test_with_appcontext_ctx_invoke",
		"tests/test_cli_corral.py::test_set_app_option",
	}
	if len(got) != len(want) {
		t.Fatalf("FailedTests = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("FailedTests[%d] = %q, want %q — the ' - <error>' tail must not leak into the selector", i, got[i], want[i])
		}
	}
}

// TestPythonFailedTests_NoFailuresNoSelectors pins that a clean run yields
// nothing to deselect: a caller must never build a deselect list out of a
// passing suite and quietly skip real tests.
func TestPythonFailedTests_NoFailuresNoSelectors(t *testing.T) {
	p, _ := ByName("python")
	fd := p.(FailureDeselector)
	for _, out := range []string{"", "13 passed in 0.06s", "collected 0 items"} {
		if got := fd.FailedTests(out); len(got) != 0 {
			t.Errorf("FailedTests(%q) = %v, want none", out, got)
		}
	}
}

// TestPythonDeselectArgs pins the exact argv pytest needs, one flag per
// selector — pytest does not accept a comma-joined list.
func TestPythonDeselectArgs(t *testing.T) {
	p, _ := ByName("python")
	fd := p.(FailureDeselector)

	got := fd.DeselectArgs([]string{"a.py::t1", "a.py::t2"})
	want := []string{"--deselect", "a.py::t1", "--deselect", "a.py::t2"}
	if len(got) != len(want) {
		t.Fatalf("DeselectArgs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("DeselectArgs = %v, want %v", got, want)
		}
	}
	if len(fd.DeselectArgs(nil)) != 0 {
		t.Error("DeselectArgs(nil) must be empty — never emit a bare --deselect with no argument")
	}
}

// TestNonPythonPluginsDoNotClaimDeselection pins the fail-closed default: a
// language whose runner corral cannot parse failures from must NOT implement
// the interface, so the salvage path is skipped rather than guessed at. A
// wrong selector would deselect the wrong test and silently narrow the exam.
func TestNonPythonPluginsDoNotClaimDeselection(t *testing.T) {
	for _, name := range []string{"go", "ruby", "javascript", "typescript"} {
		p, ok := ByName(name)
		if !ok {
			t.Fatalf("plugin %q not registered", name)
		}
		if _, claims := p.(FailureDeselector); claims {
			t.Errorf("%s claims FailureDeselector — only implement it with a verified failure-line parser and a real deselect flag for that runner", name)
		}
	}
}

// THE SALVAGE'S --deselect MUST SURVIVE THE SELECTION. WithAuthoredTest rebuilds
// the command from sel.Cmd whenever a selection exists and used to discard the
// passed testCmd entirely — which is exactly where salvageByDeselect had put
// the `--deselect <failing test>` it computed. Under selection (the default),
// the salvage ran the original command, failed the same way, and reported
// nothing; with an empty Selection the identical inputs salvaged one proof.
func TestWithAuthoredTestCarriesDeselectAcrossASelection(t *testing.T) {
	py, _ := ByName("python")
	ts := py.(TestSelector)
	sel := Selection{Base: []string{"python3", "-m", "pytest", "-q"}, Cmd: []string{"python3", "-m", "pytest", "-q", "tests/test_calc.py::test_add"}, Tests: []string{"tests/test_calc.py::test_add"}}
	passed := []string{"python3", "-m", "pytest", "-q", "--deselect", "tests/test_corral.py::test_wrong"}
	got := ts.WithAuthoredTest(sel, passed, "tests/test_corral.py")
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "--deselect tests/test_corral.py::test_wrong") {
		t.Fatalf("the --deselect the salvage computed was discarded by the selection: %q", joined)
	}
	if !strings.Contains(joined, "tests/test_calc.py::test_add") {
		t.Errorf("the selection's own tests must still be there: %q", joined)
	}
}
