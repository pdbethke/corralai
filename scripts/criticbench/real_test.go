// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/queue"
)

// A real file has no answer key: it is loaded with its paired test file, every
// test in that file is listed, and it is marked unkeyed so nothing is scored
// as right or missed against a key nobody wrote.
func TestRealFixtureLoadsAPairWithNoKey(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "wallet.go")
	if err := os.WriteFile(src, []byte("package w\n\nfunc Add(a, b int) int { return a + b }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := "package w\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {}\nfunc TestAddZero(t *testing.T) {}\nfunc helper() {}\n"
	if err := os.WriteFile(filepath.Join(dir, "wallet_test.go"), []byte(tests), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := realFixture(src)
	if err != nil {
		t.Fatal(err)
	}
	if !f.unkeyed || f.vacuous != nil {
		t.Fatalf("a real file has no answer key: %+v", f)
	}
	if !reflect.DeepEqual(f.all, []string{"TestAdd", "TestAddZero"}) {
		t.Fatalf("tests = %v", f.all)
	}
	if !strings.Contains(f.code, "func Add") || !strings.Contains(f.tests, "TestAddZero") || f.testPath != filepath.Join(dir, "wallet_test.go") {
		t.Fatalf("pair not loaded: %+v", f)
	}
}

// A source file with no paired test file is refused, not benched empty.
func TestRealFixtureNeedsAPairedTestFile(t *testing.T) {
	src := filepath.Join(t.TempDir(), "lonely.go")
	if err := os.WriteFile(src, []byte("package l\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := realFixture(src); err == nil {
		t.Fatal("want an error for a file with no _test.go beside it")
	}
}

// With no key, a flag on a test in the file is neither right nor false; only
// a flag naming a test that is not in the file is false.
func TestGradeUnkeyedCountsOnlyNamesNotInTheFile(t *testing.T) {
	f := fixture{all: []string{"TestA", "TestB"}, unkeyed: true}
	flagged, right, missed, fals := grade(f, []queue.Finding{{Target: "TestA"}, {Target: "TestGhost"}})
	if !reflect.DeepEqual(flagged, []string{"TestA"}) || right != 0 || missed != 0 || fals != 1 {
		t.Fatalf("flagged %v right %d missed %d false %d; want [TestA] 0 0 1", flagged, right, missed, fals)
	}
}
