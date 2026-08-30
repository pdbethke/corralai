// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"strings"
	"testing"
)

func concatenatorFor(t *testing.T, name string) TestConcatenator {
	t.Helper()
	p, ok := ByName(name)
	if !ok {
		t.Fatalf("no %s plugin", name)
	}
	c, ok := p.(TestConcatenator)
	if !ok {
		t.Fatalf("%s plugin does not implement TestConcatenator", name)
	}
	return c
}

// TestPythonConcatDedupesImportsAndRenamesCollidingTests is the operator-facing
// promise of the per-survivor writer: N separately-proven test files must fold
// into ONE file a developer can paste into their suite. Two parts that each
// `import pytest` must not produce two import lines, and two parts that each
// named their test `test_x` must not produce a file where the second silently
// shadows the first — which would drop a PROVEN test on the floor.
func TestPythonConcatDedupesImportsAndRenamesCollidingTests(t *testing.T) {
	c := concatenatorFor(t, "python")
	out, err := c.ConcatTests([]AuthoredPart{
		{MutantID: "s0/m1", Source: "import pytest\nfrom mod import f\n\ndef test_x():\n    assert f(1) == 1\n"},
		{MutantID: "s0/m2", Source: "import pytest\n\ndef test_x():\n    assert f(2) == 2\n"},
	})
	if err != nil {
		t.Fatalf("ConcatTests: %v", err)
	}
	if n := strings.Count(out, "import pytest"); n != 1 {
		t.Errorf("import pytest appears %d times, want 1:\n%s", n, out)
	}
	if !strings.Contains(out, "from mod import f") {
		t.Errorf("the second part's imports were dropped:\n%s", out)
	}
	if !strings.Contains(out, "def test_x_s0m1(") || !strings.Contains(out, "def test_x_s0m2(") {
		t.Errorf("colliding test names were not suffixed with their mutant ids:\n%s", out)
	}
	if strings.Contains(out, "def test_x(") {
		t.Errorf("the unsuffixed colliding name survived — one proven test shadows the other:\n%s", out)
	}
}

// TestPythonConcatLeavesUniqueNamesAlone: a suffix is a REPAIR, not a policy.
// Renaming a name nothing collides with would make the concatenated file read
// differently from the test that was actually proven.
func TestPythonConcatLeavesUniqueNamesAlone(t *testing.T) {
	c := concatenatorFor(t, "python")
	out, err := c.ConcatTests([]AuthoredPart{
		{MutantID: "s0/m1", Source: "def test_a():\n    assert True\n"},
		{MutantID: "s0/m2", Source: "def test_b():\n    assert True\n"},
	})
	if err != nil {
		t.Fatalf("ConcatTests: %v", err)
	}
	if !strings.Contains(out, "def test_a(") || !strings.Contains(out, "def test_b(") {
		t.Errorf("unique names were rewritten:\n%s", out)
	}
}

// TestGoConcatEmitsOnePackageClause: a Go file has exactly one package clause,
// so N parts that each carry theirs must fold to one.
func TestGoConcatEmitsOnePackageClause(t *testing.T) {
	c := concatenatorFor(t, "go")
	out, err := c.ConcatTests([]AuthoredPart{
		{MutantID: "s0/m1", Source: "package p\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {}\n"},
		{MutantID: "s0/m2", Source: "package p\n\nimport (\n\t\"testing\"\n)\n\nfunc TestB(t *testing.T) {}\n"},
	})
	if err != nil {
		t.Fatalf("ConcatTests: %v", err)
	}
	if n := strings.Count(out, "package p"); n != 1 {
		t.Errorf("package clause appears %d times, want 1:\n%s", n, out)
	}
	if n := strings.Count(out, "\"testing\""); n != 1 {
		t.Errorf("the testing import appears %d times, want 1:\n%s", n, out)
	}
	if !strings.Contains(out, "func TestA(") || !strings.Contains(out, "func TestB(") {
		t.Errorf("a part's test was lost:\n%s", out)
	}
}

// TestGoConcatRefusesADuplicateHelper is the honest half: a helper redeclared
// by two parts cannot be merged by renaming (the call sites are the model's
// own, and rewriting them is a guess). ConcatTests says so, and the caller
// carries the part out separately rather than emitting a file that does not
// build.
func TestGoConcatRefusesADuplicateHelper(t *testing.T) {
	c := concatenatorFor(t, "go")
	parts := []AuthoredPart{
		{MutantID: "s0/m1", Source: "package p\n\nfunc helper() int { return 1 }\n\nfunc TestA(t *testing.T) { _ = helper() }\n"},
		{MutantID: "s0/m2", Source: "package p\n\nfunc helper() int { return 2 }\n\nfunc TestB(t *testing.T) { _ = helper() }\n"},
	}
	if _, err := c.ConcatTests(parts); err == nil {
		t.Fatal("ConcatTests merged two parts that both declare helper() — the result cannot compile")
	}
	merged, extra := ConcatAuthored(mustPlugin(t, "go"), parts)
	if len(extra) != 1 || extra[0].MutantID != "s0/m2" {
		t.Fatalf("extra = %+v, want exactly the unmergeable second part", extra)
	}
	if !strings.Contains(merged, "func TestA(") {
		t.Errorf("the mergeable part was lost:\n%s", merged)
	}
	if strings.Contains(merged, "func TestB(") {
		t.Errorf("the unmergeable part was merged anyway:\n%s", merged)
	}
}

// TestConcatAuthoredWithoutAConcatenator: a language with no concatenator must
// not silently drop the proven tests. Every part comes back as extra.
func TestConcatAuthoredWithoutAConcatenator(t *testing.T) {
	p := mustPlugin(t, "ruby")
	if _, ok := p.(TestConcatenator); ok {
		t.Skip("ruby grew a concatenator; this case no longer exists")
	}
	parts := []AuthoredPart{{MutantID: "m1", Source: "x"}, {MutantID: "m2", Source: "y"}}
	merged, extra := ConcatAuthored(p, parts)
	if merged != "" || len(extra) != 2 {
		t.Fatalf("merged=%q extra=%d, want the parts carried out untouched", merged, len(extra))
	}
}

// TestConcatAuthoredSinglePartIsVerbatim: one proven test must reach the
// operator exactly as it was proven — the concatenator may not rewrite a file
// it had nothing to merge it with.
func TestConcatAuthoredSinglePartIsVerbatim(t *testing.T) {
	src := "import pytest\n\ndef test_x():\n    assert True\n"
	merged, extra := ConcatAuthored(mustPlugin(t, "python"), []AuthoredPart{{MutantID: "m1", Source: src}})
	if len(extra) != 0 {
		t.Fatalf("extra = %+v, want none", extra)
	}
	if strings.TrimSpace(merged) != strings.TrimSpace(src) {
		t.Errorf("a single part was rewritten:\ngot:\n%s\nwant:\n%s", merged, src)
	}
}

func mustPlugin(t *testing.T, name string) Plugin {
	t.Helper()
	p, ok := ByName(name)
	if !ok {
		t.Fatalf("no %s plugin", name)
	}
	return p
}
