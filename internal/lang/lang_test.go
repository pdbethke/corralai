package lang

import (
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
	cmd, ok := py.SingleTestCmd("tests/test_recipes.py", "tests/test_recipes.py::RandomPermutationTests::test_full_permutation")
	if !ok || len(cmd) == 0 {
		t.Fatalf("python: want ok cmd, got ok=%v cmd=%v", ok, cmd)
	}
	joined := strings.Join(cmd, " ")
	if !strings.Contains(joined, "test_full_permutation") {
		t.Fatalf("python cmd missing selector: %v", cmd)
	}

	g, _ := ByName("go")
	gcmd, ok := g.SingleTestCmd("foo_test.go", "TestNegativeTake")
	if !ok || !strings.Contains(strings.Join(gcmd, " "), "TestNegativeTake") {
		t.Fatalf("go: %v ok=%v", gcmd, ok)
	}

	rb, _ := ByName("ruby")
	if _, ok := rb.SingleTestCmd("x", "y"); ok {
		t.Fatal("ruby should report ok=false (unimplemented)")
	}
}
