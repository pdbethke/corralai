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
	if !strings.Contains(out, "def test_x_s0_m1(") || !strings.Contains(out, "def test_x_s0_m2(") {
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
		if !strings.Contains(out, "function mk_s0_m1(") || !strings.Contains(out, "function mk_s0_m2(") {
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
// one module that cannot be UNIONED safely — a default import beside a named
// one, or one local name bound two ways — are refused with a reason the
// operator can read, and the refused part is delivered on its own.
//
// Until 2026-10-26 every different specifier list was refused, on the
// ground that merging braces rewrites the model's import. On REA that
// split 5 of 6 proven tests into separate files, because writers import
// different subsets of one module ({ A } here, { A, B } there). A union of
// plain named imports cannot lose a binding — that refusal's whole
// rationale — so it is now merged (TestJavaScriptConcatUnionsNamedImports).
func TestJavaScriptConcatRefusesSameModuleDifferentSpecifiers(t *testing.T) {
	c := concatenatorFor(t, "javascript")
	parts := []AuthoredPart{
		{MutantID: "s0/m1", Source: "import { test } from 'node:test';\ntest('a', () => {});\n"},
		{MutantID: "s0/m2", Source: "import nt from 'node:test';\nnt('b', () => {});\n"},
	}
	err := func() error { _, e := c.ConcatTests(parts); return e }()
	if err == nil {
		t.Fatal("ConcatTests merged a default import with a named one from one module")
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
	// One local name bound two ways cannot be unioned either.
	_, extra = ConcatAuthored(mustPlugin(t, "typescript"), []AuthoredPart{
		{MutantID: "s0/m1", Source: "import { a as x } from 'm';\nit('a', () => x);\n"},
		{MutantID: "s0/m2", Source: "import { x } from 'm';\nit('b', () => x);\n"},
	})
	if len(extra) != 1 || !strings.Contains(extra[0].Reason, `"x"`) {
		t.Errorf("a local bound two ways must be refused naming it: %+v", extra)
	}
}

// TestJavaScriptConcatUnionsNamedImports: REA's proven tests imported
// { OwnedCommandFailure } in one part and, across lines,
// { OwnedCommandFailure, runOwnedCommand } in another. Both are plain named
// imports from one module, so the merged file imports the union once, and
// every part lands in it.
func TestJavaScriptConcatUnionsNamedImports(t *testing.T) {
	for _, name := range []string{"javascript", "typescript"} {
		merged, extra := ConcatAuthored(mustPlugin(t, name), []AuthoredPart{
			{MutantID: "s0/m1", Source: "import { OwnedCommandFailure } from \"../src/OwnedCommand.js\";\nimport type { P } from \"../src/P.js\";\nit(\"a\", () => {});\n"},
			{MutantID: "s0/m2", Source: "import {\n  OwnedCommandFailure,\n  runOwnedCommand,\n} from \"../src/OwnedCommand.js\";\nimport type { Q } from \"../src/P.js\";\nit(\"b\", () => {});\n"},
			{MutantID: "s0/m3", Source: "import { OwnedCommandFailure } from \"../src/OwnedCommand.js\";\nit(\"c\", () => {});\n"},
		})
		if len(extra) != 0 {
			t.Fatalf("%s: plain named imports must union, got refusals %+v", name, extra)
		}
		if want := "import { OwnedCommandFailure, runOwnedCommand } from \"../src/OwnedCommand.js\";"; strings.Count(merged, want) != 1 || strings.Count(merged, "OwnedCommand.js") != 1 {
			t.Errorf("%s: want exactly %s:\n%s", name, want, merged)
		}
		if want := "import type { P, Q } from \"../src/P.js\";"; !strings.Contains(merged, want) {
			t.Errorf("%s: want %s:\n%s", name, want, merged)
		}
		for _, title := range []string{`it("a"`, `it("b"`, `it("c"`} {
			if strings.Count(merged, title) != 1 {
				t.Errorf("%s: %s missing from the merge:\n%s", name, title, merged)
			}
		}
	}
	// An import only one part makes is left exactly as written.
	merged, _ := ConcatAuthored(mustPlugin(t, "typescript"), []AuthoredPart{
		{MutantID: "s0/m1", Source: "import {\n  A,\n} from \"m\";\nit(\"a\", () => A);\n"},
		{MutantID: "s0/m2", Source: "it(\"b\", () => {});\n"},
	})
	if !strings.Contains(merged, "import {\n  A,\n} from \"m\";") {
		t.Errorf("an import nothing collides with must stay verbatim:\n%s", merged)
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
	if !strings.Contains(out, "def test_x_s0_m1") || !strings.Contains(out, "def test_x_s0_m2") {
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

// TestEveryLanguageHasAConcatenator: the default writer mode returns N
// separately-proven files, so a language with no concatenator hands its
// operators nothing merged at all. Derived from the registry, so a new
// language fails here until it implements one — the hand list this replaced
// skipped php, which implements it. (Rust was exempt until its landing
// arrived; its concatenator merges landed corral_tests modules.)
func TestEveryLanguageHasAConcatenator(t *testing.T) {
	for _, name := range Names() {
		if _, ok := registry[name].(TestConcatenator); !ok {
			t.Errorf("%s has no TestConcatenator — the default writer mode cannot hand its operators one file", name)
		}
	}
}

// TestPHPConcatDoesNotMergeUnderPHPUnit10 replaces three tests that asserted
// a merge PHPUnit 10+ cannot run. The old tests rested on a hand-written
// require+reflection driver that mimicked PHPUnit 9's loader; the sixth
// review ran the real runner (11.5): a file declaring InvoiceTest_s0m1 and
// InvoiceTest_s0m2 → "Class InvoiceTest cannot be found", Tests: 1, exit 1
// — neither ran. One proven test is returned as-is; every further one rides
// separately, with the reason on it.
func TestPHPConcatDoesNotMergeUnderPHPUnit10(t *testing.T) {
	one := AuthoredPart{MutantID: "s0/m1", Source: "<?php\nuse PHPUnit\\Framework\\TestCase;\nfinal class InvoiceTest extends TestCase { public function testA(): void { $this->assertTrue(true); } }\n"}
	two := AuthoredPart{MutantID: "s0/m2", Source: "<?php\nuse PHPUnit\\Framework\\TestCase;\nfinal class InvoiceTest extends TestCase { public function testB(): void { $this->assertTrue(true); } }\n"}
	merged, extra := ConcatAuthored(phpPlugin{}, []AuthoredPart{one, two})
	if merged != one.Source {
		t.Errorf("the first proven test should be handed back as-is; got %q", merged)
	}
	if len(extra) != 1 || extra[0].MutantID != "s0/m2" || !strings.Contains(extra[0].Reason, "PHPUnit 10+") {
		t.Errorf("the second proven test should ride separately with the reason: %+v", extra)
	}
}

// MULTI-LINE IMPORTS ARE ONE HEADER. The merge hoisted an import by its
// first line only, so `import {` went to the top of the file and its
// specifier lines and `} from "…"` stayed in the body: REA's five proven
// tests merged into a file that did not parse, and the together check
// reported the parse error as tests that "interfere (shared state?)". A
// header now runs until its brackets balance, in JS/TS and in Python.
func TestConcatKeepsAMultiLineImportWhole(t *testing.T) {
	for _, name := range []string{"javascript", "typescript"} {
		c := concatenatorFor(t, name)
		multi := "import {\n  OwnedCommandFailure,\n  runOwnedCommand,\n} from \"../src/OwnedCommand.js\";\n"
		out, err := c.ConcatTests([]AuthoredPart{
			{MutantID: "s0/m1", Source: "import { it } from \"vitest\";\n" + multi + "\nit(\"a\", () => { runOwnedCommand(OwnedCommandFailure); });\n"},
			{MutantID: "s0/m2", Source: "import { it } from \"vitest\";\n" + multi + "\nit(\"b\", () => {});\n"},
		})
		if err != nil {
			t.Fatalf("%s: ConcatTests: %v", name, err)
		}
		if strings.Count(out, multi) != 1 {
			t.Errorf("%s: the multi-line import must appear once, whole:\n%s", name, out)
		}
		if i, j := strings.Index(out, multi), strings.Index(out, "it(\"a\""); i < 0 || j < i {
			t.Errorf("%s: the multi-line import must be hoisted above the tests:\n%s", name, out)
		}
	}
	py := concatenatorFor(t, "python")
	out, err := py.ConcatTests([]AuthoredPart{
		{MutantID: "s0/m1", Source: "from mod import (\n    a,\n    b,\n)\n\ndef test_a():\n    assert a\n"},
		{MutantID: "s0/m2", Source: "from mod import (\n    a,\n    b,\n)\n\ndef test_b():\n    assert b\n"},
	})
	if err != nil {
		t.Fatalf("python: ConcatTests: %v", err)
	}
	if strings.Count(out, "from mod import (\n    a,\n    b,\n)\n") != 1 || strings.Count(out, "    a,") != 1 {
		t.Errorf("python: the parenthesised import must appear once, whole:\n%s", out)
	}
}

// The specifier check must see a multi-line import's module, which is on
// its LAST line: a line scan found no module there and let a default import
// and a named one from the same module through to a file with a duplicate
// binding.
func TestJavaScriptConcatRefusesDifferentImportsAcrossLines(t *testing.T) {
	parts := []AuthoredPart{
		{MutantID: "s0/m1", Source: "import OwnedCommand from \"../src/OwnedCommand.js\";\nit(\"a\", () => {});\n"},
		{MutantID: "s0/m2", Source: "import {\n  OwnedCommandFailure,\n  runOwnedCommand,\n} from \"../src/OwnedCommand.js\";\nit(\"b\", () => {});\n"},
	}
	merged, extra := ConcatAuthored(mustPlugin(t, "typescript"), parts)
	if len(extra) != 1 || extra[0].MutantID != "s0/m2" || !strings.Contains(extra[0].Reason, "../src/OwnedCommand.js") {
		t.Fatalf("extra = %+v, want the second part refused naming the module", extra)
	}
	if strings.Contains(merged, "runOwnedCommand") {
		t.Errorf("the refused part leaked into the merge:\n%s", merged)
	}
	// The same specifiers laid out differently are the same import.
	same := []AuthoredPart{
		{MutantID: "s0/m1", Source: "import { A, B } from \"m\";\nit(\"a\", () => {});\n"},
		{MutantID: "s0/m2", Source: "import {\n  A,\n  B\n} from \"m\";\nit(\"b\", () => {});\n"},
	}
	merged, extra = ConcatAuthored(mustPlugin(t, "typescript"), same)
	if len(extra) != 0 || strings.Count(merged, "from \"m\"") != 1 {
		t.Errorf("one import laid out two ways must merge to one: extra=%+v\n%s", extra, merged)
	}
}

// A bracket in a trailing comment opens nothing: the next line is a test,
// not the rest of the import.
func TestConcatIgnoresABracketInAnImportsComment(t *testing.T) {
	out, err := concatenatorFor(t, "javascript").ConcatTests([]AuthoredPart{
		{MutantID: "s0/m1", Source: "import x from \"y\"; // see (note\nit(\"a\", () => {});\n"},
		{MutantID: "s0/m2", Source: "import x from \"y\"; // see (note\nit(\"b\", () => {});\n"},
	})
	if err != nil {
		t.Fatalf("ConcatTests: %v", err)
	}
	if strings.Count(out, "it(\"a\"") != 1 || strings.Count(out, "it(\"b\"") != 1 || strings.Count(out, "import x") != 1 {
		t.Errorf("a commented bracket swallowed a test into the import:\n%s", out)
	}
}

// `{ type X }` beside `{ X }` from one module is one binding, not two: the
// value import also names the type, and the part that imports X as a value
// proves X is one. REA's run B kept a proven test apart for exactly this.
func TestJavaScriptConcatUnifiesATypeAndAValueImportOfOneName(t *testing.T) {
	merged, extra := ConcatAuthored(mustPlugin(t, "typescript"), []AuthoredPart{
		{MutantID: "s0/m1", Source: "import { OwnedCommandFailure, type runOwnedCommand } from \"../src/OwnedCommand.js\";\nit(\"a\", () => {});\n"},
		{MutantID: "s0/m2", Source: "import { runOwnedCommand } from \"../src/OwnedCommand.js\";\nit(\"b\", () => {});\n"},
	})
	if len(extra) != 0 {
		t.Fatalf("a type and a value import of one name must merge: %+v", extra)
	}
	if want := "import { OwnedCommandFailure, runOwnedCommand } from \"../src/OwnedCommand.js\";"; !strings.Contains(merged, want) {
		t.Errorf("want %s (the value import wins):\n%s", want, merged)
	}
}
