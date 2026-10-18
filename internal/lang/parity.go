// SPDX-License-Identifier: Elastic-2.0

package lang

// paritySample is one test file declaring one test, per language, so the
// critic-list capability is judged by running TestNamesInSource rather than
// by asserting it exists.
type paritySample struct{ path, src string }

var paritySamples = map[string]paritySample{
	"go":         {"a_test.go", "package a\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {}\n"},
	"python":     {"tests/test_a.py", "def test_a():\n    assert True\n"},
	"php":        {"tests/ATest.php", "<?php\nclass ATest extends TestCase {\n    public function testA(): void {}\n}\n"},
	"rust":       {"src/a.rs", "#[test]\nfn a() {}\n"},
	"javascript": {"a.test.js", "test('a', () => {});\n"},
	"typescript": {"a.test.ts", "test('a', () => {});\n"},
	"ruby":       {"a_spec.rb", "describe 'A' do\n  it 'a' do\n  end\nend\n"},
}

// Capability is one user-facing thing corral does with a language's tests.
// Has judges it by running or asking the code that does it, never by a
// declared flag: the support table and the parity test read this list, so
// neither can claim a capability the code lacks.
type Capability struct {
	Key, Title string
	Has        func(p Plugin) bool
	// Row is set for a capability a runner row provides: the language has it
	// when any of its rows does (Has). Stock says whether the language's own
	// stock command gets it as is; when it does not, the support page names
	// the rows that do, so a Ruby user is not told their shell-wrapped stock
	// command stops at the first failure.
	Row   func(Runner) bool
	Stock func(p Plugin) bool
}

// rowCapability is a Capability judged on the language's runner rows.
func rowCapability(key, title string, row func(Runner) bool) Capability {
	return Capability{Key: key, Title: title, Row: row,
		Has: func(p Plugin) bool { return anyRow(p, row) },
		Stock: func(p Plugin) bool {
			stock := p.TestCmd()
			r, named := namedRunner(p, stock)
			return named && !cmdIsShellWrapped(stock) && row(r)
		},
	}
}

// anyRow reports whether some runner row for p's language has the capability.
// A language has it when an operator can get it by naming that runner; which
// runner, the support table's runner section says.
func anyRow(p Plugin, has func(Runner) bool) bool {
	for _, r := range runners {
		for _, l := range r.Languages {
			if l == p.Name() && has(r) {
				return true
			}
		}
	}
	return false
}

// Capabilities is every capability in the parity contract, in the order the
// support table prints them.
var Capabilities = []Capability{
	{Key: "critic-list", Title: "Critic must judge every test", Has: func(p Plugin) bool {
		s := paritySamples[p.Name()]
		return len(p.TestNamesInSource(s.path, s.src)) > 0
	}},
	rowCapability("list", "List the tests", func(r Runner) bool { return r.ListCmd != nil && r.ParseList != nil }),
	rowCapability("single", "Run one test alone (critic claims checked)", func(r Runner) bool { return r.SingleCmd != nil }),
	rowCapability("first-failure", "Name the killing test", func(r Runner) bool { return r.FirstFailure != nil }),
	rowCapability("fail-fast", "Stop at the first failure", func(r Runner) bool { return len(r.FailFast) > 0 }),
	rowCapability("salvage", "Keep the passing part of an authored test", func(r Runner) bool { return r.FailedTests != nil && r.DeselectCmd != nil }),
	{Key: "coverage", Title: "Coverage pre-flight", Has: func(p Plugin) bool { _, ok := p.(CoverageReporter); return ok }},
	{Key: "selection", Title: "Grade only the tests that run the file", Has: func(p Plugin) bool { _, ok := p.(TestSelector); return ok }},
}

// Exemption is a capability a language lacks, with the reason. The parity
// test fails on a gap with no exemption and on an exemption whose capability
// now exists, so this list only shrinks.
type Exemption struct{ Lang, Capability, Reason string }

var ParityExemptions = []Exemption{
	{"rust", "fail-fast", "nextest's --fail-fast and libtest's lack of one are not yet verified against a real run (runner table Phase 5)"},

	{"rust", "salvage", "libtest --skip and nextest filtersets are not yet verified for salvage (runner table Phase 5)"},

	{"rust", "coverage", "cargo llvm-cov is not wired yet (runner table Phase 5)"},

	{"go", "selection", "per-test coverage needs a run per test; its own spec (runner table Phase 6)"},
	{"javascript", "selection", "per-test coverage contexts per runner; its own spec (runner table Phase 6)"},
	{"typescript", "selection", "per-test coverage contexts per runner; its own spec (runner table Phase 6)"},
	{"ruby", "selection", "per-test coverage contexts per runner; its own spec (runner table Phase 6)"},
	{"php", "selection", "per-test coverage contexts per runner; its own spec (runner table Phase 6)"},
	{"rust", "selection", "per-test coverage contexts per runner; its own spec (runner table Phase 6)"},
}
