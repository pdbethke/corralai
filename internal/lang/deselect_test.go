// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"slices"
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
	failedTests, ok := DeselectorFor(p, Cmd(p.TestCmd()))
	if !ok {
		t.Fatal("pytest must offer salvage — pytest names its failures precisely and supports --deselect")
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

	got := failedTests(out)
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
	failedTests, _ := DeselectorFor(p, Cmd(p.TestCmd()))
	for _, out := range []string{"", "13 passed in 0.06s", "collected 0 items"} {
		if got := failedTests(out); len(got) != 0 {
			t.Errorf("FailedTests(%q) = %v, want none", out, got)
		}
	}
}

// TestPythonDeselectCmd pins the exact argv pytest needs, one flag per
// selector — pytest does not accept a comma-joined list — appended to the
// operator's command.
func TestPythonDeselectCmd(t *testing.T) {
	p, _ := ByName("python")
	inv := Cmd([]string{"python3", "-m", "pytest", "-q"})
	got, ok := DeselectCmd(p, inv, []string{"a.py::t1", "a.py::t2"})
	want := []string{"python3", "-m", "pytest", "-q", "--deselect", "a.py::t1", "--deselect", "a.py::t2"}
	if !ok || !slices.Equal(got, want) {
		t.Fatalf("DeselectCmd = %q, %v, want %q", got, ok, want)
	}
	if got, ok := DeselectCmd(p, inv, nil); ok {
		t.Errorf("DeselectCmd(nil) = %q — nothing to skip is no salvage, never a bare --deselect", got)
	}
}

// Salvage is shaped like a one-test run (inv.shape): through a package
// script the skip arguments go after the launcher's separator, a script
// that is not one command gets none, and neither does a shell-wrapped one.
func TestSalvageGoesThroughTheLauncher(t *testing.T) {
	withRunners(t, []Runner{{
		Name: "fake", ID: "fake", Languages: []string{"javascript"}, Default: true, Words: []string{"jest"},
		FailedTests: func(string) []string { return nil },
		DeselectCmd: func(argv, ids []string) ([]string, bool) {
			return append(slices.Clone(argv), append([]string{"--skip"}, ids...)...), true
		},
	}})
	js, _ := ByName("javascript")
	npm := Invocation{Argv: []string{"npm", "test"}, Script: []string{"jest"}, Pass: "--", Narrow: true}
	if got, ok := DeselectCmd(js, npm, []string{"a"}); !ok || !slices.Equal(got, []string{"npm", "test", "--", "--skip", "a"}) {
		t.Errorf("through npm test = %q, %v", got, ok)
	}
	compound := npm
	compound.Narrow = false
	if got, ok := DeselectCmd(js, compound, []string{"a"}); ok {
		t.Errorf("a compound script got %q", got)
	}
	unread := Invocation{Argv: []string{"npm", "test"}, Script: []string{}}
	if got, ok := DeselectCmd(js, unread, []string{"a"}); ok {
		t.Errorf("an unread script got %q", got)
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
