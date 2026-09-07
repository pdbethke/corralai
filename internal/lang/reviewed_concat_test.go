// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"strings"
	"testing"
)

// The two findings of review 9e3f89b00949 (a Codex reviewer on
// internal/lang, Claude Code verifying), both confirmed on adjudication.
// The reviewer's reproductions, inverted.

// R1 — a collision repair renames identifiers, never the inside of a
// string literal: a proven test asserting against "helper" keeps asserting
// against "helper" after its helper is renamed.
func TestConcatCollisionRepairLeavesStringLiteralsAlone(t *testing.T) {
	parts := []AuthoredPart{
		{MutantID: "s0/m1", Source: "const helper = () => \"helper\";\n" +
			"// helper is the thing\n" +
			"test(\"one\", () => assert.equal(helper(), 'helper'));"},
		{MutantID: "s0/m2", Source: "const helper = () => \"helper\";\n" +
			"test(\"two\", () => assert.equal(helper(), `helper`));"},
	}
	got, err := (jsPlugin{}).ConcatTests(parts)
	if err != nil {
		t.Fatalf("unexpected refusal: %v", err)
	}
	for _, literal := range []string{`"helper"`, `'helper'`, "`helper`", "// helper is the thing"} {
		if !strings.Contains(got, literal) {
			t.Errorf("a literal or comment was rewritten — %s is gone:\n%s", literal, got)
		}
	}
	for _, rewritten := range []string{`"helper_s0_m1"`, `"helper_s0_m2"`, `'helper_s0_m1'`, "`helper_s0_m2`"} {
		if strings.Contains(got, rewritten) {
			t.Errorf("a string literal was renamed to %s:\n%s", rewritten, got)
		}
	}
	for _, want := range []string{"const helper_s0_m1 = ", "helper_s0_m1()", "const helper_s0_m2 = ", "helper_s0_m2()"} {
		if !strings.Contains(got, want) {
			t.Errorf("the declaration or its call was not renamed — %s missing:\n%s", want, got)
		}
	}
	// And Python, whose strings and comments are shaped differently.
	py := []AuthoredPart{
		{MutantID: "s0/m1", Source: "def test_x():\n    # test_x is checked by name below\n    assert 'test_x' in str(test_x)\n    s = \"\"\"test_x\nspans\"\"\"\n"},
		{MutantID: "s0/m2", Source: "def test_x():\n    assert 1\n"},
	}
	got, err = (pyPlugin{}).ConcatTests(py)
	if err != nil {
		t.Fatalf("unexpected refusal: %v", err)
	}
	for _, literal := range []string{"# test_x is checked by name below", "'test_x'", "\"\"\"test_x\nspans\"\"\""} {
		if !strings.Contains(got, literal) {
			t.Errorf("python literal or comment rewritten — %s gone:\n%s", literal, got)
		}
	}
	if !strings.Contains(got, "def test_x_s0_m1(") || !strings.Contains(got, "str(test_x_s0_m1)") {
		t.Errorf("the python declaration or its reference was not renamed:\n%s", got)
	}
}

// R2 — distinct mutant ids never share a suffix: "a/b" and "a-b" used to
// both render "ab", and the merged file declared the repaired name twice.
func TestConcatDistinctIDsNeverCollapse(t *testing.T) {
	parts := []AuthoredPart{
		{MutantID: "a/b", Source: "function helper() { return 1; }\ntest(\"one\", () => helper());"},
		{MutantID: "a-b", Source: "function helper() { return 2; }\ntest(\"two\", () => helper());"},
		{MutantID: "ab", Source: "function helper() { return 3; }\ntest(\"three\", () => helper());"},
	}
	got, err := (jsPlugin{}).ConcatTests(parts)
	if err != nil {
		t.Fatalf("unexpected refusal: %v", err)
	}
	seen := map[string]int{}
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "function helper") {
			seen[line]++
		}
	}
	if len(seen) != 3 {
		t.Fatalf("three distinct ids must yield three distinct declarations, got %v:\n%s", seen, got)
	}
	for decl, n := range seen {
		if n != 1 {
			t.Fatalf("%q declared %d times:\n%s", decl, n, got)
		}
	}
	// The mapping itself: injective across the shapes that used to collide,
	// and the clean shape stays readable.
	ids := []string{"s0/m1", "s0m1", "s0_m1", "s0-m1", "a/b", "a-b", "ab", "a_b", "a//b", "/ab", "ab/", "", "🙂", "s0/m1/x2f"}
	out := map[string]string{}
	for _, id := range ids {
		s := idSuffix(id)
		if prev, dup := out[s]; dup {
			t.Errorf("ids %q and %q share suffix %q", prev, id, s)
		}
		out[s] = id
		for _, r := range s {
			if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
				t.Errorf("suffix %q for %q is not an identifier fragment", s, id)
			}
		}
	}
	if idSuffix("s0/m1") != "s0_m1" {
		t.Errorf("the shape corral mints must stay readable: %q", idSuffix("s0/m1"))
	}
}
