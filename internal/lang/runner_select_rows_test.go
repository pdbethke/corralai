// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"path/filepath"
	"reflect"
	"testing"
)

// The selection engine builds its commands through runner-row fields, so the
// pytest row must build exactly what pyPlugin.Select built. Select is gone
// (Task 6); what it returned for the recorded evidence and pkg/calc.py is
// written out below — the two tests that executed the file, sorted, after
// the collection-stripped base — and the engine's own answers for the same
// inputs are held to Select's recorded ones in
// internal/testmap/testdata/python-selector-golden.json.
func TestPytestRowBuildsTodaysCommands(t *testing.T) {
	p, _ := ByName("python")
	row, err := RunnerByID(p, "pytest")
	if err != nil {
		t.Fatal(err)
	}
	if row.CollectionBase == nil || row.TestsCmd == nil || row.CarryArgs == nil {
		t.Fatalf("pytest row lacks a field: CollectionBase=%v TestsCmd=%v CarryArgs=%v",
			row.CollectionBase != nil, row.TestsCmd != nil, row.CarryArgs != nil)
	}
	if row.UnitsCmd != nil || row.UnitsCoruns != nil || row.UnitForFile != nil {
		t.Error("pytest runs no unit pass: UnitsCmd, UnitsCoruns and UnitForFile stay nil")
	}
	fixture, err := filepath.Abs(filepath.Join("testdata", "pycov-fixture"))
	if err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{
		{"pytest", "tests/"},
		{"python3", "-m", "pytest", "-q", "-k", "not slow", "tests/"},
		{"pytest", "tests/test_calc.py::test_add"},
		{"pytest", "--maxfail", "3"},
		{"python3", "-m", "pytest", "-q", "tests/", "--deselect", "tests/test_calc.py::test_sub"},
		{"pytest", "--deselect=tests/test_calc.py::test_sub"},
	} {
		t.Run(joinArgv(argv), func(t *testing.T) {
			sel := Selection{Tests: []string{"tests/test_calc.py::test_add", "tests/test_other.py::test_sub"}}
			sel.Cmd = append(append([]string{}, stripPyCollectionTargets(argv, fixture)...), sel.Tests...)
			base := row.CollectionBase(argv, fixture)
			if want := stripPyCollectionTargets(argv, fixture); !reflect.DeepEqual(base, want) {
				t.Errorf("CollectionBase = %v, want %v", base, want)
			}
			cmd, ok := row.TestsCmd(base, sel.Tests)
			if !ok {
				t.Fatal("TestsCmd refused")
			}
			if !reflect.DeepEqual(cmd, sel.Cmd) {
				t.Errorf("TestsCmd = %v, Select built %v", cmd, sel.Cmd)
			}
			if got, want := row.CarryArgs(argv), deselectArgs(argv); !reflect.DeepEqual(got, want) {
				t.Errorf("CarryArgs = %v, want %v", got, want)
			}
		})
	}
}

func joinArgv(argv []string) string {
	out := ""
	for i, a := range argv {
		if i > 0 {
			out += " "
		}
		out += a
	}
	return out
}
