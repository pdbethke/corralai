// SPDX-License-Identifier: Elastic-2.0

package lang

import "slices"

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
	"swift":      {"Tests/ATests/ATests.swift", "import XCTest\n\nfinal class ATests: XCTestCase {\n    func testA() {}\n}\n"},
	"ruby":       {"a_spec.rb", "describe 'A' do\n  it 'a' do\n  end\nend\n"},
}

// mergeSamples is two proven tests per language, as the test-writer hands
// them to the merge (ConcatAuthored): each a whole test file for the same
// source, declaring a different test, written the way that language's
// writer prompt asks. The merge capability is judged by merging them.
var mergeSamples = map[string][2]string{
	"go":         {"package a\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {}\n", "package a\n\nimport \"testing\"\n\nfunc TestB(t *testing.T) {}\n"},
	"python":     {"from a import f\n\ndef test_a():\n    assert f()\n", "from a import f\n\ndef test_b():\n    assert f()\n"},
	"php":        {"<?php\nuse PHPUnit\\Framework\\TestCase;\nfinal class ATest extends TestCase { public function testA(): void { $this->assertTrue(true); } }\n", "<?php\nuse PHPUnit\\Framework\\TestCase;\nfinal class ATest extends TestCase { public function testB(): void { $this->assertTrue(true); } }\n"},
	"rust":       {rustPlugin{}.LandTest("#[test]\nfn a() {}", 1, ""), rustPlugin{}.LandTest("#[test]\nfn b() {}", 1, "")},
	"javascript": {"import { test } from 'node:test';\ntest('a', () => {});\n", "import { test } from 'node:test';\ntest('b', () => {});\n"},
	"typescript": {"import { test } from 'node:test';\ntest('a', () => {});\n", "import { test } from 'node:test';\ntest('b', () => {});\n"},
	"swift":      {"import XCTest\n@testable import A\n\nfinal class A_corralTests: XCTestCase {\n    func testA() {}\n}\n", "import XCTest\n@testable import A\n\nfinal class A_corralTests: XCTestCase {\n    func testB() {}\n}\n"},
	"ruby":       {"require 'minitest/autorun'\n\nclass ATest < Minitest::Test\n  def test_a\n    assert true\n  end\nend\n", "require 'minitest/autorun'\n\nclass ATest < Minitest::Test\n  def test_b\n    assert true\n  end\nend\n"},
}

// mergesTwoParts is the merge capability judged by doing it: both of the
// language's sample tests end up in one merged file, none carried out apart.
func mergesTwoParts(p Plugin) bool {
	s, ok := mergeSamples[p.Name()]
	if !ok {
		return false
	}
	merged, extra := ConcatAuthored(p, []AuthoredPart{{MutantID: "s0/m1", Source: s[0]}, {MutantID: "s0/m2", Source: s[1]}})
	return merged != "" && len(extra) == 0
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
		Has:   func(p Plugin) bool { return anyRow(p, row) },
		Stock: stockHas(row),
	}
}

// stockHas is a Capability's Stock for a row capability: the language's own
// stock command names a row that has it, unwrapped by a shell.
func stockHas(row func(Runner) bool) func(p Plugin) bool {
	return func(p Plugin) bool {
		stock := p.TestCmd()
		r, named := namedRunner(p, stock)
		return named && !cmdIsShellWrapped(stock) && row(r)
	}
}

// runsASet is the row half of the selection capability: the row can run a
// chosen set — of tests (TestsCmd) or of units (UnitsCmd).
func runsASet(r Runner) bool { return r.TestsCmd != nil || r.UnitsCmd != nil }

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
	rowCapability("confirm-alone", "Confirm a kill by running the killing test alone", func(r Runner) bool { return r.KillerCmd != nil }),
	rowCapability("fail-fast", "Stop at the first failure", func(r Runner) bool { return len(r.FailFast) > 0 }),
	// A runner row whose ReportArgs corral adds to every graded run, so the
	// project's own report-selection setting cannot hide the lines its
	// failure readers read (pytest's -rfE over a project's -r). Only that
	// setting: an option that suppresses the summary itself (pytest's
	// --no-summary, -p no:terminal) still hides the PRINTED lines, which a
	// real-run test pins; what answers those is the next capability,
	// "failure-hook". Judged on the field
	// lang.ReportCmd applies, so the page cannot claim flags the code does
	// not add. A language without it says, in its exemption, whether its
	// runners' lines can be hidden by a project at all.
	rowCapability("readable-failures", "Name the killing test whatever the project's -r report setting", func(r Runner) bool { return len(r.ReportArgs) > 0 }),
	// A runner row whose ReportHook has the runner itself record its failing
	// tests, which the row's readers take in preference to its printed
	// lines: no report setting of the project's hides the record, and an id
	// is whole (pytest's plugin, loaded through PYTEST_PLUGINS). Judged on
	// the field lang.ReportCmd wraps the command with. The printed lines
	// stay the second source, for a command corral does not wrap (Windows,
	// where the wrapper's shell script cannot run, among them) and for a
	// record that is missing or cut short. Where it was run, and where not,
	// is in the row's Note.
	rowCapability("failure-hook", "Read the failing test from the runner's own hook, not its printed lines", func(r Runner) bool { return r.ReportHook != nil }),
	rowCapability("salvage", "Keep the passing part of an authored test", func(r Runner) bool { return r.FailedTests != nil && r.DeselectCmd != nil }),
	rowCapability("units", "Run one test unit", func(r Runner) bool { return r.ListUnits != nil && r.UnitCmd != nil }),
	// A runner row that declares a cache grading can share (go test's GOCACHE).
	// Judged on the same row test RunCacheFor reads, so the page cannot claim
	// a cache the code does not hand the sandbox.
	rowCapability("run-cache", "Share a build cache between sandboxed runs", runRowCache),
	// The language measures one run that pays a mutant's rebuild, on the
	// workspace substrate, and floors each mutant's time cap at three times
	// it (PerRunBuilder; adequacy.WorkspacePool.MeasureRebuild). Judged on
	// the interface the wiring asserts, so the page cannot claim a run the
	// code does not make. Go's floor in the sandbox comes from the run
	// cache's warming run, a different capability (run-cache); its
	// exemption here says so, because this table has one answer per
	// language and not one per substrate.
	{Key: "rebuild-floor", Title: "Floor each mutant's time limit at three times a measured rebuild run (workspace substrate)", Has: func(p Plugin) bool { _, ok := p.(PerRunBuilder); return ok }},
	// The language names the lockfile every workspace tree must hold for the
	// trees to resolve the same dependency versions (Lockfiler;
	// adequacy.WithLockfile). Judged on the interface the wiring asserts.
	{Key: "lockfile", Title: "Keep workspace trees on the checkout's locked dependency versions (one tree when they would not hold the lockfile)", Has: func(p Plugin) bool { _, ok := p.(Lockfiler); return ok }},
	// The language's evidence adds a source with no paired test to the
	// audit when a test executes it (EvidencePromoter). A declaration,
	// checked against reposcan's landing code by reposcan's
	// TestEvidencePromotionDeclarationMatchesTheLandingCode.
	{Key: "evidence-promotion", Title: "Add a file with no paired test to the audit when the evidence shows a test executes it", Has: PromotesByEvidence},
	// The language's functions and their branches are read from a source
	// file (SignatureReader, a declaration checked against repoindex's
	// reader by repoindex's TestSignatureDeclarationMatchesTheReader).
	{Key: "signatures", Title: "Read a file's functions and their branches (one mutant-generator seat per group of functions, a signature surface in the prompts, exam coverage)", Has: ReadsSignatures},
	// The proven tests are handed back as one file: judged by merging two
	// sample tests through ConcatAuthored, the fold every caller uses.
	{Key: "merge", Title: "Hand back the proven tests as one file", Has: mergesTwoParts},
	{Key: "coverage", Title: "Coverage pre-flight", Has: func(p Plugin) bool { _, ok := p.(CoverageReporter); return ok }},
	// Row and Stock as a row capability's, so the support page names the
	// runners that select when the stock command's does not (Ruby's stock
	// command is shell-wrapped; rspec selects, minitest has no UnitsCmd).
	{Key: "selection", Title: "Grade only the tests that run the file", Row: runsASet, Stock: stockHas(runsASet),
		Has: func(p Plugin) bool { return selectionCapable(p, runners) }},
}

// selectionCapable is the selection capability judged on what grades by
// evidence: the language produces evidence the selection engine reads —
// per-test (TestEvidence, Python's one instrumented run) or coverage the unit
// pass runs one unit at a time (CoverageReporter) — and some row of rows can
// run a chosen set (TestsCmd for tests, UnitsCmd for units). Either half
// alone selects nothing: evidence with no command to run its answer, or a
// command with no evidence to choose for it. rows is a parameter so the
// negative control can hand it a row with neither command.
func selectionCapable(p Plugin, rows []Runner) bool {
	_, perTest := p.(TestEvidence)
	_, perUnit := p.(CoverageReporter)
	if !perTest && !perUnit {
		return false
	}
	for _, r := range rows {
		if slices.Contains(r.Languages, p.Name()) && runsASet(r) {
			return true
		}
	}
	return false
}

// Exemption is a capability a language lacks, with the reason. The parity
// test fails on a gap with no exemption and on an exemption whose capability
// now exists, so this list only shrinks.
type Exemption struct{ Lang, Capability, Reason string }

var ParityExemptions = []Exemption{

	{"rust", "salvage", "libtest --skip and nextest filtersets are not yet verified for salvage (runner table Phase 5)"},

	{"python", "units", "its evidence is per test, from one instrumented run (lang.TestEvidence), which the selection engine reads at test grain; a unit pass would be a second, coarser answer to the same question"},

	{"rust", "run-cache", "not built. measured 2026-11-18 on one 32-core machine, outside the sandbox, as the median of 3 to 5 runs: on indexmap-rs/indexmap at 41a8708 (224 tests) `cargo test` took 4.3 s from an empty target/ and 0.9 s with a warm target/ after one source file changed, so a shared target/ would be worth building for; a larger crate was not measured. Before one is shared we must settle how a cached test result is kept from standing in for a run and how a shared target/ keeps one mutant's build output from being read by another's"},

	{"javascript", "run-cache", "not built. measured 2026-11-18 on one 32-core machine, outside the sandbox, as the median of 3 to 5 runs: jest 22.4 on iamkun/dayjs at d3e43be (794 tests, 3 of them failing on this machine in every run; Babel transform) took 3.3 s with an empty cache directory and 1.7 s with a warm one after one source file changed; vitest 4.1 on unjs/ufo at f06c800 (489 tests) took 0.37 s with its cache directories removed and 0.35 s warm, no difference. A current jest was not measured. Before jest's cache is shared its flag for never serving a cached result must be verified"},

	{"typescript", "run-cache", "not built. measured 2026-11-18 on one 32-core machine, outside the sandbox, as the median of 3 to 5 runs: vitest 4.1 on unjs/ufo at f06c800 (TypeScript, 489 tests) took 0.37 s with its cache directories removed and 0.35 s warm after one source file changed, no difference. jest with a TypeScript transform and the TypeScript compiler's own incremental cache were not measured (jest with Babel on a JavaScript project: 3.3 s cold, 1.7 s warm). Before one is shared each runner's flag for never serving a cached result must be verified"},

	{"python", "run-cache", "in the 2026-11-16 audit of Kludex/starlette (public ledger entry 20261116T171749Z-271c277da0ff-ae082f706b21) the median, across the 25 files graded, of each file's median time to grade one fault was 516 ms (range 358 ms to 7,377 ms), and no cache worth sharing has been identified; and measured 2026-11-18 on one 32-core machine, outside the sandbox, as the median of 3 to 5 runs: on Kludex/starlette at 271c277 the whole suite (1,421 tests) took 9.0 s with every bytecode cache empty, the installed packages' included, and 7.0 s warm after one source file changed. A sandboxed run already reads the installed packages' bytecode, so what a shared cache of the project's own bytecode would add is less than that difference and was not measured apart"},

	{"ruby", "run-cache", "no cache to share. measured 2026-11-18 on one 32-core machine, outside the sandbox, as the median of 3 to 5 runs: on hashie/hashie at e4aaac1 rspec's 6.6 s run (1,075 examples) spent 0.3 s loading files, which is the most a cache of loaded code could save; Ruby has no compile step, and bootsnap, which a project may add itself, was not measured"},

	{"go", "readable-failures", "not needed: go test reads no project file that sets its flags or output, so a project cannot hide its `--- FAIL:` lines (GOFLAGS, which could add -json, is the operator's environment, not the project's); gotestsum's format comes from its own flag or GOTESTSUM_FORMAT, also not a project file, and was not run here"},

	{"php", "readable-failures", "a phpunit.xml with testdox=\"true\" replaces the `N) Class::method` lines corral reads (measured 2026-11-17 on PHPUnit 11.5.57), and 11.5 has no option to turn it off (it refuses --no-testdox), so there is no flag to add"},

	{"php", "run-cache", "nothing worth sharing was found. measured 2026-11-18 on one 32-core machine, outside the sandbox, as the median of 3 to 5 runs: on ramsey/uuid at 7a16331 the PHPUnit run (2,299 tests) took 10.6 s without OPcache and 10.2 s with a warm OPcache file cache after one source file changed, a difference inside the spread of single runs (9.9 s to 11.0 s)"},

	{"go", "failure-hook", "not built: corral reads go test's printed `--- FAIL:` lines. `go test -json` is the runner's own record of each test's result and would be the hook; corral does not add it, and nothing here measured what it does to the output the other readers take"},

	{"javascript", "failure-hook", "not built: corral reads the lines jest, vitest, mocha and node --test print. Each takes a reporter module named on the command line, which is what would do it; none has been written or run, and whether one can run beside the reporters a project configures was not checked"},

	{"typescript", "failure-hook", "not built: corral reads the lines jest, vitest, mocha and node --test print. Each takes a reporter module named on the command line, which is what would do it; none has been written or run, and whether one can run beside the reporters a project configures was not checked"},

	{"php", "failure-hook", "not built: corral reads PHPUnit's printed `N) Class::method` lines. PHPUnit's `--log-junit <file>` writes its own record of each test and would be the hook; corral does not add it, and it has not been run for this"},

	{"ruby", "failure-hook", "not built: corral reads rspec's `Failed examples:` list and minitest's printed failures. rspec takes a second formatter writing to a file (`--format json --out <file>`), which would be the hook; corral does not add it, and nothing comparable was looked for in minitest"},

	{"rust", "failure-hook", "not built: corral reads the lines cargo test and cargo-nextest print. nextest writes a JUnit file when a profile in the project's .config/nextest.toml asks for one, and libtest's JSON output is an unstable option; a way to ask for either from the command line was not looked for"},

	{"swift", "confirm-alone", "not built: the failure line swift-testing prints names a failing test by its function or display name alone (`✘ Test fails() failed after ...`), not its suite or target, and XCTest's names the class and method (`Test Case 'AdderTests.testFails' failed`) but not the target the class is in (measured on Swift 6.3.2); XCTest's `error:` line before it carries the test file's path, `Tests/CalcXCTests/CalcXCTests.swift` in that run. So the killing test cannot be narrowed by name to exactly one test; kills are confirmed by repeating the grading run"},

	{"swift", "fail-fast", "`swift test` has no option to stop at the first failure: the --help of Swift 6.3.2 lists none"},

	{"swift", "readable-failures", "not built: the --help of Swift 6.3.2 names no project file that sets `swift test`'s options, so no project setting is known to hide the lines corral reads; this was read from the help text, not tested against a project that tried"},

	{"swift", "failure-hook", "not built: corral reads the lines `swift test` prints. Its `--xunit-output <path>` (in the --help of Swift 6.3.2) writes a record of each test to a file and would be the hook; corral does not add it, and it has not been run for this"},

	{"swift", "salvage", "not built: `swift test --help` (Swift 6.3.2) lists a `--skip` option, which was not run, and the failure lines name a swift-testing test without its suite or target and an XCTest test by class and method without its target (measured on Swift 6.3.2), so a skip built from them could leave out a passing test of the same name elsewhere and narrow the run"},

	{"swift", "run-cache", "not built: corral's sandbox does not run Swift, and before a build directory is shared between runs we must settle how one mutant's build output is kept from being read by another's run. What sharing would save a grading run has not been measured; the one Swift timing taken is the free `--dry-run --evidence` on taplin/Lariat at be0c5fd on 2026-11-19 (workspace substrate, Swift 6.3.2 in a container, not in the sandbox), whose unit pass builds under coverage: 1 m 17.8 s for the whole dry run with that first build, and 14.3 s re-run on the built tree with `--no-selection-cache` (the evidence-pairing page)"},
	{"rust", "evidence-promotion", "not built: a Rust source with no separate test file would have its authored test written into the source itself, which corral does not do yet, so the scan excludes it before pairing and the evidence never reaches it; a source with a separate test file is already paired by name. On indexmap-rs/indexmap at 41a8708 (workspace substrate) a unit executed src/inner/extract.rs in full and nothing was added. The line that counts evidence-paired files says this after the unit pass, and such a source is recorded as test-landing-not-built"},
	{"swift", "merge", "not built: what one merged Swift file is has not been settled. Every top-level name in a test target is shared, and each proven test puts everything in one type named after the file it was written as, so all of a file's proven tests declare the same type. Each is handed back as a file of its own, with that reason, and only one of them can be added to the test target as it stands: rename the type in each of the others first"},
	{"php", "merge", "PHPUnit 10 and 11 load one class per file, the class named after the file, and a merged file of renamed classes ran no test on PHPUnit 11.5. So the first proven test is handed back as the file, and each other one as a file of its own, to be saved under its own class name"},
	{"go", "lockfile", "not built, and not measured: Go keeps the modules it downloads outside the checkout, in a module cache every tree reads, and a build uses the versions go.mod names, so no tree has a lockfile of its own to lack; whether trees can still differ was not measured"},
	{"rust", "lockfile", "not built: cargo builds each tree's own target/, and a tree without the checkout's Cargo.lock resolves the dependencies afresh, so Rust needs this. But a cargo workspace keeps Cargo.lock at the workspace root, not beside a member crate's Cargo.toml, and finding that root is not built yet"},
	{"python", "lockfile", "not built: dependencies in .venv, venv or site-packages are linked into every tree, so the trees share one installed set and resolve none of it. A suite run through tox builds its own .tox in each tree; whether those resolve the same versions was not measured"},
	{"ruby", "lockfile", "not measured: a tree without the checkout's Gemfile.lock lets bundler resolve the Gemfile again, and whether trees then build on different versions was not measured"},
	{"javascript", "lockfile", "not built: node_modules is linked into every tree, so the trees share one installed set and resolve none of it; a test command that installs packages itself was not considered"},
	{"typescript", "lockfile", "not built: node_modules is linked into every tree, so the trees share one installed set and resolve none of it; a test command that installs packages itself was not considered"},
	{"php", "lockfile", "not built: vendor/ is linked into every tree (its autoload files are copied, so each tree loads its own code), so the trees share one installed set and resolve none of it"},
	{"go", "rebuild-floor", "not built, and not measured: on the workspace substrate no rebuild run is made, and whether a Go mutant there pays build work that the unmutated baseline does not has not been timed. In the sandbox, when the build cache is shared (\"Share a build cache between sandboxed runs\"), each mutant's time limit is floored at three times the run that warmed the cache, under the five-minute ceiling: the rule this row applies to a measured rebuild run"},
	{"rust", "rebuild-floor", "not built, because on the one crate measured the baseline already pays the mutant's rebuild: every run on the workspace substrate writes the audited file back, and cargo rebuilds a file whose timestamp changed. Measured 2026-11-19 on regex-syntax 0.8.5 (cargo 1.90, one 32-core machine, outside the sandbox, three runs each, as cargo's Finished line timed them), `cargo test --lib --no-run` took 0.44 to 0.51 s after src/hir/translate.rs was rewritten with its own bytes and 0.47 to 0.57 s after a one-line change to it; with a comment line put first, 0.61 s in one run. In the sandbox the copy each run gets leaves the checkout's target/ out, the baseline's and a mutant's alike. Other crates were not measured"},
	{"python", "rebuild-floor", "not measured: the time a mutant's run spends on work the unmutated baseline's does not was not compared. On the workspace substrate each run is given a fresh, empty bytecode cache directory, the baseline's included"},
	{"ruby", "rebuild-floor", "not measured: the time a mutant's run spends on work the unmutated baseline's does not was not compared"},
	{"javascript", "rebuild-floor", "not measured: the time a mutant's run spends on work the unmutated baseline's does not (a transform cache, for one) was not compared"},
	{"typescript", "rebuild-floor", "not measured: the time a mutant's run spends on work the unmutated baseline's does not (transpiling, a transform cache) was not compared"},
	{"php", "rebuild-floor", "not measured: the time a mutant's run spends on work the unmutated baseline's does not was not compared"},
}
