// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/queue"
)

// plantModule writes a tiny module whose test file has three tests that fail
// only through t's own methods, and one that fails through a helper it hands
// t to, which planting must leave alone: stripping the test's own calls would
// not make it vacuous.
func plantModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module plantme\n\ngo 1.22\n")
	write("calc.go", "package calc\n\nfunc Add(a, b int) int { return a + b }\n")
	write("calc_test.go", `package calc

import "testing"

func TestAddSmall(t *testing.T) {
	if got := Add(1, 2); got != 3 {
		t.Errorf("Add(1, 2) = %d", got)
	}
}

func TestAddNegative(t *testing.T) {
	got := Add(-1, -2)
	if got != -3 {
		t.Fatalf("got %d", got)
	}
}

func TestAddTable(t *testing.T) {
	for _, c := range []struct{ a, b, want int }{{1, 1, 2}, {0, 5, 5}} {
		t.Run("case", func(t *testing.T) {
			if got := Add(c.a, c.b); got != c.want {
				t.Errorf("got %d want %d", got, c.want)
			}
		})
	}
}

func TestAddViaHelper(t *testing.T) {
	check(t, Add(2, 2), 4)
}

func check(t *testing.T, got, want int) {
	t.Helper()
	if got != want {
		t.Errorf("got %d want %d", got, want)
	}
}
`)
	return dir
}

// Planting strips every failing call from the chosen tests, leaves tests that
// hand t to a helper alone, and proves the result: the seeded file compiles,
// the planted tests pass, and none of them can call a failure method.
func TestPlantVacuousStripsFailureCallsAndProvesThem(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	dir := plantModule(t)
	f, err := plantVacuous(filepath.Join(dir, "calc.go"), 3)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.vacuous, []string{"TestAddNegative", "TestAddSmall", "TestAddTable"}) {
		t.Fatalf("planted %v; want the three tests that fail only through t", f.vacuous)
	}
	if f.unkeyed || !f.planted {
		t.Fatalf("a planted file is keyed for its planted tests only: %+v", f)
	}
	for _, call := range []string{"t.Errorf(\"Add(1, 2)", "t.Fatalf", "got %d want %d\", got, c.want"} {
		if strings.Contains(f.tests, call) {
			t.Errorf("planted file still has %q:\n%s", call, f.tests)
		}
	}
	if !strings.Contains(f.tests, "check(t, Add(2, 2), 4)") || !strings.Contains(f.tests, "t.Errorf(\"got %d want %d\", got, want)") {
		t.Errorf("the helper test and the helper must be untouched:\n%s", f.tests)
	}
	// The real test file on disk is never touched.
	disk, _ := os.ReadFile(filepath.Join(dir, "calc_test.go"))
	if !strings.Contains(string(disk), "t.Fatalf") {
		t.Fatal("planting rewrote the real test file")
	}
}

// Asking for more than the file can supply is an error, not a quietly smaller
// key.
func TestPlantVacuousRefusesTooFew(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	if _, err := plantVacuous(filepath.Join(plantModule(t), "calc.go"), 4); err == nil {
		t.Fatal("want an error: only three tests can be planted")
	}
}

// On a planted file only the planted tests are keyed: a flag on another test
// in the file is counted apart, a planted test not flagged is missed, and a
// name not in the file is still false.
func TestGradePlantedKeysOnlyThePlanted(t *testing.T) {
	f := fixture{all: []string{"TestA", "TestB", "TestC"}, vacuous: []string{"TestA", "TestB"}, planted: true}
	_, right, missed, fals, other := gradeFull(f, []queue.Finding{{Target: "TestA"}, {Target: "TestC"}, {Target: "TestGhost"}})
	if right != 1 || missed != 1 || fals != 1 || other != 1 {
		t.Fatalf("right %d missed %d false %d other %d; want 1 1 1 1", right, missed, fals, other)
	}
}

// Planting only deletes lines. A re-printed tree reformatted the code around
// each planted spot, which marked the planted tests for any reader; every
// line of the planted file must appear in the original, in order.
func TestPlantVacuousOnlyDeletesLines(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	dir := plantModule(t)
	f, err := plantVacuous(filepath.Join(dir, "calc.go"), 3)
	if err != nil {
		t.Fatal(err)
	}
	orig, _ := os.ReadFile(filepath.Join(dir, "calc_test.go"))
	lines := strings.Split(string(orig), "\n")
	i := 0
	for _, l := range strings.Split(f.tests, "\n") {
		for i < len(lines) && lines[i] != l {
			i++
		}
		if i == len(lines) {
			t.Fatalf("planted line %q is not an original line, in order:\n%s", l, f.tests)
		}
		i++
	}
}
