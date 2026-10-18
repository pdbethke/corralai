// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The JavaScript and TypeScript lister names each test by its titles joined
// with "::", outermost first: the form jest, vitest and node list it in.
func TestJSTestNamesInSource(t *testing.T) {
	for _, c := range []struct {
		name, src string
		want      []string
	}{
		{"top level", "test('a', () => {});\n", []string{"a"}},
		{"nested", "describe('Calc', () => {\n  describe('neg', function () {\n    it('adds', () => {});\n  });\n  it(\"subs\", async () => {});\n});\n",
			[]string{"Calc::neg::adds", "Calc::subs"}},
		{"backtick without substitution", "test(`a b`, () => {});\n", []string{"a b"}},
		{"escapes", `test('it\'s', () => {}); test("say \"hi\"", () => {});`, []string{"it's", `say "hi"`}},
		{"modifiers listed, todo dropped", "it.skip('s', f); it.only('o', f); xit('x', f); fit('f', f); test.todo('t'); xdescribe('D', () => { it('in', f); });\n",
			[]string{"s", "o", "x", "f", "D::in"}},
		{"duplicate collapses", "test('a', f);\ntest('a', f);\n", []string{"a"}},
		{"comments ignored", "// it('fake', f)\n/* test('fake2', f) */\ntest('real', f);\n", []string{"real"}},
		{"string holding brackets", "describe('Calc', () => {\n  it('closes })', () => { const s = '})'; });\n});\n", []string{"Calc::closes })"}},
		{"expression-bodied describe", "describe('G', () => it('a', f));\n", []string{"G::a"}},
		{"division is not a regex", "describe('G', () => {\n  const x = a / b / c;\n  it('a', () => { expect(x).toBe(1); });\n});\n", []string{"G::a"}},
		{"regex holding a quote and a brace", "describe('G', () => {\n  const re = /['}]/g;\n  it('a', () => { expect(re.test(\"x\")).toBe(false); });\n});\n", []string{"G::a"}},
		{"typescript generics and types", "describe('G', () => {\n  const id = <T,>(x: T): T => x;\n  it('a', (): void => { expect(id<number>(1)).toBe(1); });\n});\n", []string{"G::a"}},
		{"jsx attribute holding a brace", "describe('G', () => {\n  it('renders', () => { render(<A b=\"}\" />); });\n});\n", []string{"G::renders"}},
		{"destructured imports are not calls", "const { describe, it, test } = require('node:test');\ntest('a', f);\n", []string{"a"}},
		{"member it is not a test", "test('a', () => { const n = foo.it; });\n", []string{"a"}},
		{"no semicolons", "const calc = require('./calc')\ntest('a', f)\ntest('b', f)\n", []string{"a", "b"}},
		{"default and namespace imports", "import test from 'node:test'\nconst it = require('node:test').it\nimport { describe as d, it as i } from 'vitest'\ntest('a', f)\n", []string{"a"}},
	} {
		if got := jsTestNames(c.src); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// Any test the lister cannot name exactly lists NOTHING: the critic is held
// to the list, so a short list would be read as complete.
func TestJSTestNamesInSourceFailsClosed(t *testing.T) {
	for name, src := range map[string]string{
		"substitution in a title":    "test(`a ${x}`, f);\n",
		"each":                       "test.each([1, 2])('n %i', f);\n",
		"describe each":              "describe.each([1])('g', () => { it('a', f); });\n",
		"variable title":             "it(name, f);\n",
		"concatenated title":         "it('a' + x, f);\n",
		"test in a loop":             "for (const n of [1, 2]) {\n  it('x', f);\n}\n",
		"test in a hook":             "beforeEach(() => { it('x', f); });\n",
		"test in a function":         "function make() { test('x', f); }\n",
		"unterminated string":        "test('a, f);\n",
		"unterminated comment":       "test('a', f); /* never closed\n",
		"unterminated template":      "test(`a, f);\n",
		"unbalanced braces":          "describe('G', () => {\n  it('a', f);\n",
		"mismatched brackets":        "describe('G', () => { it('a', f); ));\n",
		"non-literal describe":       "describe(Calc, () => { it('a', f); });\n",
		"unknown modifier":           "test.concurrent.each`x`('a', f);\n",
		"test under a braceless if":  "describe('g', () => { if (env) it('a', f); });\n",
		"test under a braceless for": "describe('g', () => { for (const n of []) it('a', f) });\n",
		"test under an else":         "describe('g', () => { if (x) {} else it('a', f); });\n",
		"test in a top-level arrow":  "const make = () => it('a', f);\ndescribe('g', () => { make(); });\n",
		"aliased test function":      "const itIf = ok ? it : xit;\nitIf('b', f);\nit('a', f);\n",
		"node subtest":               "test('top', async (t) => { await t.test('sub', f); });\n",
	} {
		if got := jsTestNames(src); got != nil {
			t.Errorf("%s: want nil, got %q", name, got)
		}
	}
}

// Checked against the real runners: every name the lister gives for a
// captured fixture resolves, one to one, onto a test in that runner's
// captured list (testdata/runners, PROVENANCE.txt), and every test the
// runner listed is named.
func TestJSNamesResolveOntoRealRunnerLists(t *testing.T) {
	dir := filepath.Join("testdata", "runners")
	for _, c := range []struct {
		src, list, runner string
	}{
		{"src/calc.jest.test.js", "jest-list.json", "jest"},
		{"src/calc.vitest.test.mjs", "vitest-list.txt", "vitest"},
		{"src/calc.mocha.js", "mocha-list.json", "mocha"},
		{"src/calc.node.test.js", "node-tap.txt", "node-test"},
	} {
		js, _ := ByName("javascript")
		r, err := RunnerByID(js, c.runner)
		if err != nil {
			t.Fatal(err)
		}
		src, err := os.ReadFile(filepath.Join(dir, c.src))
		if err != nil {
			t.Fatal(err)
		}
		out, err := os.ReadFile(filepath.Join(dir, c.list))
		if err != nil {
			t.Fatal(err)
		}
		assertNamesResolveOneToOne(t, c.src, r, jsTestNames(string(src)), r.ParseList(string(out)))
	}
}

// assertNamesResolveOneToOne requires every name to resolve onto a distinct
// listed id and every listed id the runner can run alone to be named.
func assertNamesResolveOneToOne(t *testing.T, label string, r Runner, names, listed []string) {
	t.Helper()
	if len(names) == 0 || len(listed) == 0 {
		t.Fatalf("%s: names %q, listed %q: both must be non-empty", label, names, listed)
	}
	hit := map[string]string{}
	for _, n := range names {
		id, ok := resolveForRow(r, listed, n)
		if !ok {
			t.Errorf("%s: name %q resolves onto no single listed test in %q", label, n, listed)
			continue
		}
		if prev, dup := hit[id]; dup {
			t.Errorf("%s: names %q and %q both resolve onto %q", label, prev, n, id)
		}
		hit[id] = n
	}
	for _, id := range listed {
		if _, ok := hit[id]; !ok && listedName(id) == id {
			t.Errorf("%s: listed test %q is named by nothing the lister gave (%q)", label, id, names)
		}
	}
}

// Both plugins answer with the shared lister.
func TestJSAndTSPluginsListTests(t *testing.T) {
	for _, name := range []string{"javascript", "typescript"} {
		p, _ := ByName(name)
		if got := p.TestNamesInSource("a.test.js", "describe('G', () => { it('a', f); });\n"); !reflect.DeepEqual(got, []string{"G::a"}) {
			t.Errorf("%s: got %q", name, got)
		}
	}
}
