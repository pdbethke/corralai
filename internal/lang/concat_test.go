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

// TestJavaScriptConcatDedupesImportsAndSuffixesHelpers: JS/TS is a launch
// language, so the default writer mode must hand its operators one file too.
// A duplicated import is not merely untidy in ESM — a redeclared binding is a
// SyntaxError — and a redeclared top-level helper is the same.
func TestJavaScriptConcatDedupesImportsAndSuffixesHelpers(t *testing.T) {
	for _, name := range []string{"javascript", "typescript"} {
		c := concatenatorFor(t, name)
		out, err := c.ConcatTests([]AuthoredPart{
			{MutantID: "s0/m1", Source: "import { test } from 'node:test';\nimport assert from 'node:assert';\n\nfunction mk() { return 1; }\ntest('a', () => { assert.ok(mk()); });\n"},
			{MutantID: "s0/m2", Source: "import { test } from 'node:test';\n\nfunction mk() { return 2; }\ntest('a', () => { assert.ok(mk()); });\n"},
		})
		if err != nil {
			t.Fatalf("%s: ConcatTests: %v", name, err)
		}
		if n := strings.Count(out, "from 'node:test'"); n != 1 {
			t.Errorf("%s: the node:test import appears %d times, want 1:\n%s", name, n, out)
		}
		if !strings.Contains(out, "import assert from 'node:assert';") {
			t.Errorf("%s: the second import was dropped:\n%s", name, out)
		}
		if !strings.Contains(out, "function mk_s0m1(") || !strings.Contains(out, "function mk_s0m2(") {
			t.Errorf("%s: the colliding helper was not suffixed:\n%s", name, out)
		}
		// Two tests may share a TITLE — it is a string, not a binding — so
		// the merge must leave both `test('a', …)` calls exactly as proven.
		if n := strings.Count(out, "test('a'"); n != 2 {
			t.Errorf("%s: %d test('a') calls survived, want both — a duplicate title is legal:\n%s", name, n, out)
		}
	}
}

// TestJavaScriptConcatRefusesSameModuleDifferentSpecifiers: two imports from
// one module with different specifier lists cannot be de-duplicated by
// dropping a line (that loses a binding) and merging their braces is a rewrite
// of the model's own code. Refused, with a reason the operator can read.
func TestJavaScriptConcatRefusesSameModuleDifferentSpecifiers(t *testing.T) {
	c := concatenatorFor(t, "javascript")
	parts := []AuthoredPart{
		{MutantID: "s0/m1", Source: "import { test } from 'node:test';\ntest('a', () => {});\n"},
		{MutantID: "s0/m2", Source: "import { test, describe } from 'node:test';\ntest('b', () => {});\n"},
	}
	err := func() error { _, e := c.ConcatTests(parts); return e }()
	if err == nil {
		t.Fatal("ConcatTests merged two different specifier lists from one module")
	}
	if !strings.Contains(err.Error(), "node:test") {
		t.Errorf("the error does not name the module: %v", err)
	}
	merged, extra := ConcatAuthored(mustPlugin(t, "javascript"), parts)
	if len(extra) != 1 || extra[0].MutantID != "s0/m2" {
		t.Fatalf("extra = %+v, want the second part", extra)
	}
	if strings.TrimSpace(extra[0].Reason) == "" {
		t.Error("the unmergeable part carries no reason — the operator is told nothing")
	}
	if !strings.Contains(merged, "test('a'") {
		t.Errorf("the mergeable part was lost:\n%s", merged)
	}
}

// TestRubyConcatDedupesRequiresAndSuffixesTestDefs: in Minitest a redefined
// `def test_x` is a SILENT override — the second wins and the first proof
// vanishes with no error anywhere. Suffixing is the only safe merge.
func TestRubyConcatDedupesRequiresAndSuffixesTestDefs(t *testing.T) {
	c := concatenatorFor(t, "ruby")
	out, err := c.ConcatTests([]AuthoredPart{
		{MutantID: "s0/m1", Source: "require 'minitest/autorun'\nrequire_relative 'thing'\n\nclass ThingTest < Minitest::Test\n  def test_x\n    assert_equal 1, Thing.f\n  end\nend\n"},
		{MutantID: "s0/m2", Source: "require 'minitest/autorun'\n\nclass ThingTest < Minitest::Test\n  def test_x\n    assert_equal 2, Thing.g\n  end\nend\n"},
	})
	if err != nil {
		t.Fatalf("ConcatTests: %v", err)
	}
	if n := strings.Count(out, "require 'minitest/autorun'"); n != 1 {
		t.Errorf("the autorun require appears %d times, want 1:\n%s", n, out)
	}
	if !strings.Contains(out, "require_relative 'thing'") {
		t.Errorf("the second require was dropped:\n%s", out)
	}
	if !strings.Contains(out, "def test_x_s0m1") || !strings.Contains(out, "def test_x_s0m2") {
		t.Errorf("the colliding test defs were not suffixed — one proof silently overrides the other:\n%s", out)
	}
	// Reopening the same class is idiomatic Ruby and harmless once the test
	// names inside it are unique, so the class clause is NOT a collision.
	if n := strings.Count(out, "class ThingTest < Minitest::Test"); n != 2 {
		t.Errorf("class reopening was rewritten (%d clauses) — it is legal and must be left alone:\n%s", n, out)
	}
}

// TestRubyConcatLeavesRSpecTitlesAlone: `it "..."` is a string, not a
// binding, and two parts may legitimately use the same one.
func TestRubyConcatLeavesRSpecTitlesAlone(t *testing.T) {
	c := concatenatorFor(t, "ruby")
	out, err := c.ConcatTests([]AuthoredPart{
		{MutantID: "m1", Source: "require 'spec_helper'\n\nRSpec.describe Thing do\n  it 'works' do\n    expect(Thing.f).to eq 1\n  end\nend\n"},
		{MutantID: "m2", Source: "require 'spec_helper'\n\nRSpec.describe Thing do\n  it 'works' do\n    expect(Thing.g).to eq 2\n  end\nend\n"},
	})
	if err != nil {
		t.Fatalf("ConcatTests: %v", err)
	}
	if n := strings.Count(out, "it 'works' do"); n != 2 {
		t.Errorf("%d it-blocks survived, want both — a duplicate title is legal:\n%s", n, out)
	}
}

// TestRubyConcatRefusesMixedFrameworks: an RSpec part and a Minitest part in
// one file run under one runner and the other half never executes. Refused,
// with a reason, rather than silently delivering a file where a proven test
// cannot run.
func TestRubyConcatRefusesMixedFrameworks(t *testing.T) {
	parts := []AuthoredPart{
		{MutantID: "m1", Source: "require 'minitest/autorun'\n\nclass T < Minitest::Test\n  def test_a\n    assert true\n  end\nend\n"},
		{MutantID: "m2", Source: "require 'spec_helper'\n\nRSpec.describe Thing do\n  it 'works' do\n    expect(1).to eq 1\n  end\nend\n"},
	}
	if _, err := concatenatorFor(t, "ruby").ConcatTests(parts); err == nil {
		t.Fatal("ConcatTests merged a Minitest part with an RSpec one")
	}
	merged, extra := ConcatAuthored(mustPlugin(t, "ruby"), parts)
	if len(extra) != 1 || extra[0].MutantID != "m2" {
		t.Fatalf("extra = %+v, want the RSpec part carried out separately", extra)
	}
	if !strings.Contains(strings.ToLower(extra[0].Reason), "framework") {
		t.Errorf("the reason does not say what went wrong: %q", extra[0].Reason)
	}
	if !strings.Contains(merged, "def test_a") {
		t.Errorf("the Minitest part was lost:\n%s", merged)
	}
}

// TestEveryLaunchLanguageHasAConcatenator: the default writer mode returns N
// separately-proven files, so a language with no concatenator hands its
// operators nothing merged at all. These five are the languages corral claims
// to audit.
func TestEveryLaunchLanguageHasAConcatenator(t *testing.T) {
	for _, name := range []string{"go", "python", "javascript", "typescript", "ruby"} {
		if _, ok := mustPlugin(t, name).(TestConcatenator); !ok {
			t.Errorf("%s has no TestConcatenator — the default writer mode cannot hand its operators one file", name)
		}
	}
}

// TestPHPConcatHoistsThePHPTagAndMergesDistinctClasses: the opening `<?php`
// tag and a shared `namespace` declaration are SINGLETONS — PHP fails to
// parse a file with either repeated — so they must appear exactly once, with
// `use`/`require_once` import lines hoisted and de-duplicated like every
// other language's headers. Two parts that declare DIFFERENT class names
// merge cleanly: PHPUnit's own directory-based collection requires() a
// matching file and reflects on every TestCase subclass IT DECLARES, so two
// distinctly-named classes in one file are both discovered — see the
// fake-jail proof below.
func TestPHPConcatHoistsThePHPTagAndMergesDistinctClasses(t *testing.T) {
	c := concatenatorFor(t, "php")
	out, err := c.ConcatTests([]AuthoredPart{
		{MutantID: "s0/m1", Source: "<?php\n\nnamespace Acme\\Billing;\n\nuse PHPUnit\\Framework\\TestCase;\nrequire_once __DIR__ . '/vendor/autoload.php';\n\nclass InvoicePriceTest extends TestCase\n{\n    public function testPriceIsNeverNegative(): void\n    {\n        $this->assertGreaterThanOrEqual(0, (new Invoice(-5))->price());\n    }\n}\n"},
		{MutantID: "s0/m2", Source: "<?php\n\nnamespace Acme\\Billing;\n\nuse PHPUnit\\Framework\\TestCase;\nrequire_once __DIR__ . '/vendor/autoload.php';\n\nclass InvoiceCurrencyTest extends TestCase\n{\n    public function testCurrencyIsUSD(): void\n    {\n        $this->assertSame('USD', (new Invoice(1))->currency());\n    }\n}\n"},
	})
	if err != nil {
		t.Fatalf("ConcatTests: %v", err)
	}
	if n := strings.Count(out, "<?php"); n != 1 {
		t.Errorf("<?php appears %d times, want 1:\n%s", n, out)
	}
	if n := strings.Count(out, "namespace Acme\\Billing;"); n != 1 {
		t.Errorf("namespace declared %d times, want 1:\n%s", n, out)
	}
	if n := strings.Count(out, "use PHPUnit\\Framework\\TestCase;"); n != 1 {
		t.Errorf("the use import appears %d times, want 1:\n%s", n, out)
	}
	if n := strings.Count(out, "require_once __DIR__ . '/vendor/autoload.php';"); n != 1 {
		t.Errorf("the require_once appears %d times, want 1:\n%s", n, out)
	}
	if !strings.Contains(out, "class InvoicePriceTest extends TestCase") || !strings.Contains(out, "class InvoiceCurrencyTest extends TestCase") {
		t.Errorf("both distinctly-named classes must survive intact:\n%s", out)
	}
	if !strings.HasPrefix(out, "<?php") {
		t.Errorf("<?php must be the file's first bytes, got:\n%s", out)
	}
}

// TestPHPConcatSuffixesCollidingClassNames: the writer prompt does not
// coordinate class names across independently-authored parts, so two parts
// naming their class identically (the common case) must not silently
// override one proven test with another the way PHP's "cannot redeclare
// class" fatal error would if left alone. Unlike a shared helper referenced
// from OUTSIDE its file, an authored test's class is used only within its
// own file (PHPUnit finds it by reflection, not by name) — so suffixing
// every occurrence of the name, including its own internal `new self()`-free
// self-references, is safe.
func TestPHPConcatSuffixesCollidingClassNames(t *testing.T) {
	c := concatenatorFor(t, "php")
	out, err := c.ConcatTests([]AuthoredPart{
		{MutantID: "s0/m1", Source: "<?php\n\nuse PHPUnit\\Framework\\TestCase;\n\nclass InvoiceTest extends TestCase\n{\n    public function testA(): void\n    {\n        $this->assertTrue(true);\n    }\n}\n"},
		{MutantID: "s0/m2", Source: "<?php\n\nuse PHPUnit\\Framework\\TestCase;\n\nclass InvoiceTest extends TestCase\n{\n    public function testB(): void\n    {\n        $this->assertTrue(true);\n    }\n}\n"},
	})
	if err != nil {
		t.Fatalf("ConcatTests: %v", err)
	}
	if strings.Contains(out, "class InvoiceTest extends TestCase") {
		t.Errorf("the unsuffixed colliding class name survived — a real PHP file would fatal on 'cannot redeclare class':\n%s", out)
	}
	if !strings.Contains(out, "class InvoiceTest_s0m1 extends TestCase") || !strings.Contains(out, "class InvoiceTest_s0m2 extends TestCase") {
		t.Errorf("colliding class names were not suffixed with their mutant ids:\n%s", out)
	}
	if !strings.Contains(out, "testA") || !strings.Contains(out, "testB") {
		t.Errorf("a proven test method was lost in the merge:\n%s", out)
	}
}

// TestPHPConcatRefusesNamespaceMismatch: only one `namespace` declaration can
// govern a PHP file. Two parts that genuinely disagree cannot both be true at
// once — refused, not guessed at, with the mismatching part carried out
// separately so its proof is not dropped.
func TestPHPConcatRefusesNamespaceMismatch(t *testing.T) {
	parts := []AuthoredPart{
		{MutantID: "m1", Source: "<?php\n\nnamespace Acme\\Billing;\n\nuse PHPUnit\\Framework\\TestCase;\n\nclass ATest extends TestCase\n{\n    public function testA(): void\n    {\n        $this->assertTrue(true);\n    }\n}\n"},
		{MutantID: "m2", Source: "<?php\n\nnamespace Acme\\Refunds;\n\nuse PHPUnit\\Framework\\TestCase;\n\nclass BTest extends TestCase\n{\n    public function testB(): void\n    {\n        $this->assertTrue(true);\n    }\n}\n"},
	}
	if _, err := concatenatorFor(t, "php").ConcatTests(parts); err == nil {
		t.Fatal("ConcatTests merged two parts that disagree about the namespace")
	}
	merged, extra := ConcatAuthored(mustPlugin(t, "php"), parts)
	if len(extra) != 1 || extra[0].MutantID != "m2" {
		t.Fatalf("extra = %+v, want the second (disagreeing) part carried out separately", extra)
	}
	if !strings.Contains(strings.ToLower(extra[0].Reason), "namespace") {
		t.Errorf("the reason does not say what went wrong: %q", extra[0].Reason)
	}
	if !strings.Contains(merged, "class ATest extends TestCase") {
		t.Errorf("the mergeable first part was lost:\n%s", merged)
	}
}

// TestPHPHasATestConcatenator pins that php implements TestConcatenator (the
// interface, not yet added to TestEveryLaunchLanguageHasAConcatenator's
// list — php is not a launch language until it clears the multilang gate).
func TestPHPHasATestConcatenator(t *testing.T) {
	if _, ok := mustPlugin(t, "php").(TestConcatenator); !ok {
		t.Fatal("php plugin does not implement TestConcatenator")
	}
}
