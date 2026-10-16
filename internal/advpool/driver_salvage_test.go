// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
)

// pythonSalvageSpec is the go-flavoured testRunSpec retargeted at pytest,
// whose failure lines (FAILED <id>) these tests feed as the clean-code
// failure output; any row with salvage would do (lang.DeselectorFor).
func pythonSalvageSpec() RunSpec {
	rs := testRunSpec()
	rs.Lang = "python"
	rs.CodePath = "src/pkg/target.py"
	rs.Code = "def validate(pw):\n    return True\n"
	rs.DevTestPath = "tests/test_target.py"
	rs.DevTestCode = "def test_ok():\n    assert True\n"
	rs.TestCmd = "python3 -m pytest -q"
	return rs
}

// TestTick_PoolAdequacy_SalvagesPassingTestsFromABrokenFile is the fix for the
// measured shape that cost the most: the compliant check is all-or-nothing per
// FILE, so on a real gemini-3.6-flash audit of pallets/flask, 13 authored tests
// of which TEN PASSED were all discarded because 3 carried wrong API
// assumptions. Nothing was scored at all.
//
// Deselecting exactly the failures and re-scoring the remainder does not
// depend on the model repairing itself — it is arithmetic over pytest's own
// output — so it recovers the ten instead of spending another model call and
// hoping.
func TestTick_PoolAdequacy_SalvagesPassingTestsFromABrokenFile(t *testing.T) {
	survivors := []adequacy.Mutant{{ID: "m1", Replace: "c1"}, {ID: "m2", Replace: "c2"}}
	sawDeselect := false
	scorer := &fakeScorer{
		devKillRate:      0.9,
		devSurvivors:     survivors,
		compliantFailure: "FAILED tests/test_x_corral.py::test_bad_assumption - AttributeError: ...\n1 failed, 10 passed",
		poolReportFn: func(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (adequacy.Report, error) {
			// The full file fails on clean code; the deselected remainder is
			// sound and kills m1.
			if strings.Contains(testCmd, "--deselect") {
				sawDeselect = true
				return adequacy.Report{
					CompliantPass: true, CanaryKilled: true,
					Total: 2, Killed: []string{"m1"}, Survived: []string{"m2"},
				}, nil
			}
			return adequacy.Report{CompliantPass: false, Total: 0}, nil
		},
	}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m0", Replace: "c0"}, survivors[0], survivors[1]}}
	// PYTHON: the fake's clean-code failure output is pytest's.
	d := newTestDriverWithSpec(t, 2, scorer, validator, 0.1, pythonSalvageSpec())

	v := completeRunTolerantOfWriterRetries(t, d, 2, "no findings")

	if !sawDeselect {
		t.Fatal("the salvage never ran — a clean-code failure must try deselecting the failing tests before spending another model call")
	}
	if v.PoolTestUnsound {
		t.Error("PoolTestUnsound = true, but the deselected remainder graded soundly")
	}
	if v.ProvenMissed != 1 {
		t.Errorf("ProvenMissed = %d, want 1 — the surviving tests killed m1 and that proof must be kept", v.ProvenMissed)
	}
	if len(v.ProvenMutantIDs) != 1 || v.ProvenMutantIDs[0] != "m1" {
		t.Errorf("ProvenMutantIDs = %v, want [m1]", v.ProvenMutantIDs)
	}
}

// TestTick_PoolAdequacy_SalvageRejectedWhenItProvesNothing pins the gate that
// keeps the salvage from being a downgrade: a remainder that grades soundly but
// kills NOTHING must not be accepted, because taking it would consume the run's
// one salvage and displace a retry that might have done better. The run must
// fall through to the reissue path instead.
func TestTick_PoolAdequacy_SalvageRejectedWhenItProvesNothing(t *testing.T) {
	survivors := []adequacy.Mutant{{ID: "m1", Replace: "c1"}, {ID: "m2", Replace: "c2"}}
	scorer := &fakeScorer{
		devKillRate:      0.9,
		devSurvivors:     survivors,
		compliantFailure: "FAILED tests/test_x_corral.py::test_bad - boom",
		poolReportFn: func(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (adequacy.Report, error) {
			if strings.Contains(testCmd, "--deselect") {
				// Sound, but proves NOTHING — must be rejected.
				return adequacy.Report{
					CompliantPass: true, CanaryKilled: true,
					Total: 2, Survived: []string{"m1", "m2"},
				}, nil
			}
			return adequacy.Report{CompliantPass: false, Total: 0}, nil
		},
	}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m0", Replace: "c0"}, survivors[0], survivors[1]}}
	// PYTHON: the fake's clean-code failure output is pytest's.
	d := newTestDriverWithSpec(t, 2, scorer, validator, 0.1, pythonSalvageSpec())

	v := completeRunTolerantOfWriterRetries(t, d, 2, "no findings")

	if v.ProvenMissed != 0 {
		t.Errorf("ProvenMissed = %d, want 0 — a salvage that proves nothing must never be recorded as proof", v.ProvenMissed)
	}
	if !v.PoolTestUnsound {
		t.Error("PoolTestUnsound = false, want true — with no salvage and no successful repair, the honest diagnosis still stands")
	}
}

// The salvage rerun is lang.DeselectCmd's argv, quoted for the jail's shell:
// a pytest id holding shell syntax (a parametrized case, `[x;y]`) stays one argument, where
// a space-joined string handed the `;` to the shell as a command separator.
func TestSalvageCommandIsQuotedArgv(t *testing.T) {
	survivors := []adequacy.Mutant{{ID: "m1", Replace: "c1"}, {ID: "m2", Replace: "c2"}}
	var salvageCmd string
	scorer := &fakeScorer{
		devKillRate:      0.9,
		devSurvivors:     survivors,
		compliantFailure: "FAILED tests/test_x_corral.py::test_p[x;y] - boom\n1 failed, 3 passed",
		poolReportFn: func(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (adequacy.Report, error) {
			if strings.Contains(testCmd, "--deselect") {
				salvageCmd = testCmd
				return adequacy.Report{CompliantPass: true, CanaryKilled: true, Total: 2, Killed: []string{"m1"}, Survived: []string{"m2"}}, nil
			}
			return adequacy.Report{CompliantPass: false, Total: 0}, nil
		},
	}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m0", Replace: "c0"}, survivors[0], survivors[1]}}
	d := newTestDriverWithSpec(t, 2, scorer, validator, 0.1, pythonSalvageSpec())
	completeRunTolerantOfWriterRetries(t, d, 2, "no findings")

	want := adequacy.ShellJoin([]string{"python3", "-m", "pytest", "-q", "--deselect", "tests/test_x_corral.py::test_p[x;y]"})
	if salvageCmd != want {
		t.Errorf("salvage ran %q, want %q", salvageCmd, want)
	}
}

// A command the runner cannot append a skip to — here shell-wrapped, where
// an appended argument goes to sh, not pytest — gets no salvage attempt.
func TestNoSalvageWhenTheCommandTakesNoSkip(t *testing.T) {
	survivors := []adequacy.Mutant{{ID: "m1", Replace: "c1"}, {ID: "m2", Replace: "c2"}}
	sawDeselect := false
	scorer := &fakeScorer{
		devKillRate:      0.9,
		devSurvivors:     survivors,
		compliantFailure: "FAILED tests/test_x_corral.py::test_bad - boom",
		poolReportFn: func(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (adequacy.Report, error) {
			if strings.Contains(testCmd, "--deselect") {
				sawDeselect = true
			}
			return adequacy.Report{CompliantPass: false, Total: 0}, nil
		},
	}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m0", Replace: "c0"}, survivors[0], survivors[1]}}
	rs := pythonSalvageSpec()
	rs.TestCmd = "sh -c 'python3 -m pytest -q'"
	d := newTestDriverWithSpec(t, 2, scorer, validator, 0.1, rs)
	completeRunTolerantOfWriterRetries(t, d, 2, "no findings")
	if sawDeselect {
		t.Error("a shell-wrapped command was given a --deselect")
	}
}
