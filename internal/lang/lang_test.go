// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"reflect"
	"strings"
	"testing"
)

func TestRegistryByNameAndDetect(t *testing.T) {
	p, ok := ByName("go")
	if !ok {
		t.Fatal("go plugin not registered")
	}
	if p.Name() != "go" {
		t.Fatalf("Name() = %q, want go", p.Name())
	}
	d, ok := Detect("internal/auth/login.go")
	if !ok || d.Name() != "go" {
		t.Fatalf("Detect(.go) = %v,%v; want go,true", d, ok)
	}
	if _, ok := ByName("cobol"); ok {
		t.Fatal("ByName(cobol) must be false — fail closed")
	}
	if _, ok := Detect("x.cobol"); ok {
		t.Fatal("Detect(.cobol) must be false — fail closed")
	}
}

func TestSingleTestCmd(t *testing.T) {
	py, _ := ByName("python")
	cmd, ok := SingleTestCmd(py, py.TestCmd(), "tests/test_recipes.py", "tests/test_recipes.py::RandomPermutationTests::test_full_permutation")
	if !ok || len(cmd) == 0 {
		t.Fatalf("python: want ok cmd, got ok=%v cmd=%v", ok, cmd)
	}
	joined := strings.Join(cmd, " ")
	if !strings.Contains(joined, "test_full_permutation") {
		t.Fatalf("python cmd missing selector: %v", cmd)
	}

	g, _ := ByName("go")
	gcmd, ok := SingleTestCmd(g, g.TestCmd(), "foo_test.go", "TestNegativeTake")
	if !ok || !strings.Contains(strings.Join(gcmd, " "), "TestNegativeTake") {
		t.Fatalf("go: %v ok=%v", gcmd, ok)
	}

	rb, _ := ByName("ruby")
	if _, ok := SingleTestCmd(rb, rb.TestCmd(), "x", "y"); ok {
		t.Fatal("ruby should report ok=false (unimplemented)")
	}
}

func TestListTestsCmd(t *testing.T) {
	py, _ := ByName("python")
	cmd, ok := ListTestsCmd(py, py.TestCmd(), "tests/test_recipes.py")
	if !ok || strings.Join(cmd, " ") != "python3 -m pytest --collect-only -q tests/test_recipes.py" {
		t.Fatalf("python ListTestsCmd = %v ok=%v", cmd, ok)
	}
	g, _ := ByName("go")
	gcmd, ok := ListTestsCmd(g, g.TestCmd(), "recipes_test.go")
	if !ok || strings.Join(gcmd, " ") != "go test -list .* ./..." {
		t.Fatalf("go ListTestsCmd = %v ok=%v", gcmd, ok)
	}
	rb, _ := ByName("ruby")
	if _, ok := ListTestsCmd(rb, rb.TestCmd(), "x"); ok {
		t.Fatal("ruby ListTestsCmd should be ok=false")
	}
}

func TestParseTestList(t *testing.T) {
	py, _ := ByName("python")
	// pytest --collect-only -q prints one node id per line, then a summary line.
	pyOut := "tests/test_recipes.py::TakeTests::test_take\ntests/test_recipes.py::TakeTests::test_negative_take\n\n2 tests collected in 0.01s\n"
	got := ParseTestList(py, py.TestCmd(), pyOut)
	want := []string{"tests/test_recipes.py::TakeTests::test_take", "tests/test_recipes.py::TakeTests::test_negative_take"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("python ParseTestList = %v want %v", got, want)
	}

	g, _ := ByName("go")
	// go test -list prints one test name per line, then an "ok  pkg  0.001s" line.
	goOut := "TestTake\nTestNegativeTake\nExampleTake\nok  \tgithub.com/x/recipes\t0.002s\n"
	ggot := ParseTestList(g, g.TestCmd(), goOut)
	gwant := []string{"TestTake", "TestNegativeTake"} // only Test* — drop Example*/Benchmark* and the ok/PASS/FAIL trailer
	if !reflect.DeepEqual(ggot, gwant) {
		t.Fatalf("go ParseTestList = %v want %v", ggot, gwant)
	}
}
