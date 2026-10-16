// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"slices"
	"testing"
)

func TestPhpunitParsesItsRealList(t *testing.T) {
	want := []string{"CalcTest::testAdds", "CalcTest::testAddsMore", "CalcTest::testWith"}
	if got := phpunitParseList(readFixture(t, "phpunit-list.txt")); !slices.Equal(got, want) {
		t.Errorf("phpunitParseList = %q, want %q", got, want)
	}
}

// One short class name in two namespaces is two tests; neither resolves.
// A data set by index collapses like one by name.
func TestPhpunitNamespacesAndDataSets(t *testing.T) {
	php, _ := ByName("php")
	argv := []string{"vendor/bin/phpunit"}
	out := "Available tests:\n - App\\Unit\\CalcTest::testAdds\n - App\\Feature\\CalcTest::testAdds\n - App\\Unit\\CalcTest::testWith#0\n - App\\Unit\\CalcTest::testWith#1\n"
	ids := ParseTestList(php, argv, out)
	if want := []string{"CalcTest::testAdds", "CalcTest::testAdds", "CalcTest::testWith"}; !slices.Equal(ids, want) {
		t.Fatalf("ids = %q, want %q", ids, want)
	}
	if _, ok := ResolveForRun(php, argv, ids, "CalcTest::testAdds"); ok {
		t.Error("CalcTest::testAdds resolved; two namespaces declare it")
	}
	if id, ok := ResolveForRun(php, argv, ids, "testWith"); !ok || id != "CalcTest::testWith" {
		t.Errorf("testWith = %q, %v", id, ok)
	}
}

func TestPhpunitCommands(t *testing.T) {
	argv := []string{"vendor/bin/phpunit", "--colors=never"}
	if got, ok := phpunitListCmd(argv, "tests/CalcTest.php"); !ok || !slices.Equal(got, []string{"vendor/bin/phpunit", "--colors=never", "--list-tests", "tests/CalcTest.php"}) {
		t.Errorf("list = %q, %v", got, ok)
	}
	want := []string{"vendor/bin/phpunit", "--colors=never", "--filter", `/(?:^|\\)CalcTest::testAdds(?: with data set .*)?$/`, "tests/CalcTest.php"}
	if got, ok := phpunitSingleCmd(argv, "tests/CalcTest.php", "CalcTest::testAdds"); !ok || !slices.Equal(got, want) {
		t.Errorf("single = %q, %v; want %q", got, ok, want)
	}
	for _, id := range []string{"", "testAdds", "::testAdds"} {
		if _, ok := phpunitSingleCmd(argv, "a.php", id); ok {
			t.Errorf("single accepted id %q", id)
		}
	}
	if _, ok := phpunitSingleCmd([]string{"vendor/bin/phpunit", "--filter", "x"}, "a.php", "A::b"); ok {
		t.Error("single accepted a command with its own --filter")
	}
}
