// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"path/filepath"
	"slices"
	"strings"
)

// Runner is everything corral knows about one test runner. One row per
// runner, declared once: what stops it at the first failure, what lists its
// tests, what runs one alone, how it reports a failure. These are facts about
// a RUNNER, not a language — vitest is the same tool under javascript and
// typescript, and a Python repository may be graded by pytest or unittest —
// so a per-language method had to re-detect the runner each time it was
// asked, and two languages sharing a runner held two copies of it.
//
// A nil or empty field means "this runner has none", never a guess, exactly
// as langSpec's zero fields do. Detection happens in one place, RunnerFor.
type Runner struct {
	Name      string
	Languages []string // plugin names whose commands this runner runs
	// Default marks the row RunnerFor answers for its languages when no row
	// is detected in the command. Each language has exactly one.
	Default bool
	// Words are command words that name the runner (whole-word, as cmdHasWord
	// matches; in a shell-wrapped script, the word in command position).
	Words []string
	// Detect replaces Words where a word cannot say it (go test vs go build,
	// cargo test vs cargo nextest). It reads argv only, never a script.
	Detect func(argv []string) bool

	// FailFast is the runner's stop-at-first-failure flag, appended only to a
	// command that NAMES this runner and is not shell-wrapped (see
	// FailFastArgsFor for why a wrong guess inflates the kill rate).
	FailFast []string
	// ListCmd enumerates the tests the command (or, for runners that ignore
	// it, the test file) runs; ParseList reads its stdout into ids.
	ListCmd   func(argv []string, testPath string) ([]string, bool)
	ParseList func(stdout string) []string
	// SingleCmd runs exactly one test id.
	SingleCmd func(argv []string, testPath, id string) ([]string, bool)
	// ResolveByList: a critic's file-relative name is mapped onto a listed id
	// (ResolveListedTest) before SingleCmd, because the runner's ids carry a
	// prefix the source does not (Rust's module path).
	ResolveByList bool
	// FirstFailure names the first failing test in the runner's output (a
	// label, never a grade); Flaky names tests that passed only on a retry.
	FirstFailure func(output []byte) string
	Flaky        func(output []byte) []string
	// FailedTests and DeselectArgs let a partly failing authored test be
	// salvaged: the failing ids, rendered as arguments that skip them.
	FailedTests  func(output string) []string
	DeselectArgs func(ids []string) []string
	// Disclose is what the scan line says about running this runner in
	// repoDir; PreflightBin is the binary that must be on PATH.
	Disclose     func(repoDir string, argv []string) []string
	PreflightBin func(argv []string) string
	// Note qualifies a capability the row borrows from another runner, for
	// the support page: what it really runs, or what was not run against it.
	Note string
}

// goTestCmd matches an actual `go test` invocation and nothing else: a
// Makefile wrapper or a gotestsum shape is not one.
func goTestCmd(argv []string) bool {
	return len(argv) >= 2 && isGoBinary(argv[0]) && argv[1] == "test"
}

func goListCmd(_ []string, testPath string) ([]string, bool) { return goListTestsCmd(testPath) }
func goSingleCmd(_ []string, testPath, id string) ([]string, bool) {
	return goSingleTestCmd(testPath, id)
}
func pyListCmd(_ []string, testPath string) ([]string, bool) { return pyListTestsCmd(testPath) }
func pySingleCmd(_ []string, testPath, id string) ([]string, bool) {
	return pySingleTestCmd(testPath, id)
}

// runners is the table. ORDER IS LOAD-BEARING: RunnerFor takes the first row
// that matches, so a row whose command also contains a more general row's
// word comes first (cargo nextest before cargo test, gotestsum before go
// test, jest and mocha before vitest).
var runners = []Runner{
	{
		Name: "gotestsum", Languages: []string{"go"}, Words: []string{"gotestsum"},
		ListCmd: goListCmd, ParseList: goParseTestList, SingleCmd: goSingleCmd,
		FirstFailure: goFirstFailure,
		Note:         "listing and one-test runs use `go test` itself; the killing test is read from `--- FAIL:` lines, which gotestsum passes through for a failed test but was not run here",
	},
	{
		// -failfast stops after the first test binary failure.
		Name: "go test", Languages: []string{"go"}, Default: true, Detect: goTestCmd,
		FailFast: []string{"-failfast"},
		ListCmd:  goListCmd, ParseList: goParseTestList, SingleCmd: goSingleCmd,
		FirstFailure: goFirstFailure,
	},
	{
		Name: "pytest", Languages: []string{"python"}, Default: true, Words: []string{"pytest"},
		FailFast: []string{"-x"},
		ListCmd:  pyListCmd, ParseList: pyParseTestList, SingleCmd: pySingleCmd,
		FirstFailure: pyFirstFailure, FailedTests: pyFailedTests, DeselectArgs: pyDeselectArgs,
	},
	{
		// No salvage: unittest exits 2 on pytest's --deselect, so a rerun
		// with it could never pass. No FirstFailure: pytest's parser needs a
		// `FAILED <node id>` line unittest never prints. List and single-test
		// commands are pytest's, as they always were for every Python
		// command; pytest collects unittest suites.
		Name: "unittest", Languages: []string{"python"}, Words: []string{"unittest"},
		FailFast: []string{"--failfast"},
		ListCmd:  pyListCmd, ParseList: pyParseTestList, SingleCmd: pySingleCmd,
		Note: "listing and one-test runs use pytest, which collects unittest suites; they need pytest installed",
	},
	// jest and mocha take --bail. vitest is left without one deliberately:
	// its bail spelling has changed across major versions, and an
	// unrecognised flag would make every mutant exit non-zero and read as a
	// kill. node --test has no such flag.
	{Name: "jest", Languages: []string{"javascript", "typescript"}, Words: []string{"jest"}, FailFast: []string{"--bail"}},
	{Name: "mocha", Languages: []string{"javascript", "typescript"}, Words: []string{"mocha"}, FailFast: []string{"--bail"}},
	{Name: "vitest", Languages: []string{"javascript", "typescript"}, Words: []string{"vitest"}},
	{
		Name: "node --test", Languages: []string{"javascript", "typescript"}, Default: true,
		Detect: func(argv []string) bool {
			return len(argv) > 0 && filepath.Base(argv[0]) == "node" && slices.Contains(argv, "--test")
		},
	},
	// rspec takes --fail-fast. Minitest is deliberately without one: its own
	// --fail-fast arrived in minitest 5.22, and an older minitest treats an
	// unknown option as an error, which the scorer would read as a kill.
	{Name: "rspec", Languages: []string{"ruby"}, Default: true, Words: []string{"rspec"}, FailFast: []string{"--fail-fast"}},
	{Name: "minitest", Languages: []string{"ruby"}, Words: []string{"ruby", "rake", "testrb"}},
	{
		Name: "phpunit", Languages: []string{"php"}, Default: true, Words: []string{"phpunit"},
		FailFast: []string{"--stop-on-failure"}, FirstFailure: phpFirstFailure,
	},
	{
		// FirstFailure and Flaky read the grammar off the output, not the
		// command (rustGrammarOf), so both cargo rows share them.
		Name: "cargo nextest", Languages: []string{"rust"}, Detect: isNextest,
		ListCmd: nextestListCmd, ParseList: nextestParseList, SingleCmd: nextestSingleCmd, ResolveByList: true,
		FirstFailure: rustFirstFailure, Flaky: rustFlakyTests,
		Disclose: nextestDisclose, PreflightBin: nextestPreflightBin,
	},
	{
		Name: "cargo test", Languages: []string{"rust"}, Default: true, Words: []string{"cargo"},
		ListCmd: libtestListCmd, ParseList: libtestParseList, SingleCmd: libtestSingleCmd, ResolveByList: true,
		FirstFailure: rustFirstFailure, Flaky: rustFlakyTests,
		Disclose: libtestDisclose, PreflightBin: libtestPreflightBin,
		Note: "libtest has no retries; flaky tests are named when the output is nextest's (a wrapper that runs nextest under another command)",
	},
}

// RunnerFor is the ONE place a command is mapped to a runner: the first row
// for p's language that the command names, else that language's Default row.
// ok is false only for a language with no row at all.
func RunnerFor(p Plugin, argv []string) (Runner, bool) {
	if r, ok := namedRunner(p, argv); ok {
		return r, true
	}
	for _, r := range runners {
		if r.Default && slices.Contains(r.Languages, p.Name()) {
			return r, true
		}
	}
	return Runner{}, false
}

// namedRunner is the row argv itself names, with no fallback. A
// shell-wrapped command is read through its script's command words, as
// coverage claiming reads it, so `sh -c 'pytest -q'` is still pytest's.
func namedRunner(p Plugin, argv []string) (Runner, bool) {
	if len(argv) == 0 {
		return Runner{}, false
	}
	var script []string
	if cmdIsShellWrapped(argv) {
		if i := slices.Index(argv, "-c"); i >= 0 && i+1 < len(argv) {
			script = shellCommandWords(argv[i+1])
		}
	}
	for _, r := range runners {
		if !slices.Contains(r.Languages, p.Name()) {
			continue
		}
		if script != nil {
			for _, w := range script {
				if slices.Contains(r.Words, strings.TrimSuffix(filepath.Base(w), ".cmd")) {
					return r, true
				}
			}
			continue
		}
		if r.Detect != nil {
			if r.Detect(argv) {
				return r, true
			}
			continue
		}
		for _, w := range r.Words {
			if cmdHasWord(argv, w) {
				return r, true
			}
		}
	}
	return Runner{}, false
}

// ListTestsCmd is the command that lists the tests argv runs, from its runner.
func ListTestsCmd(p Plugin, argv []string, testPath string) ([]string, bool) {
	r, ok := RunnerFor(p, argv)
	if !ok || r.ListCmd == nil {
		return nil, false
	}
	return r.ListCmd(argv, testPath)
}

// ParseTestList reads ListTestsCmd's stdout into test ids.
func ParseTestList(p Plugin, argv []string, stdout string) []string {
	r, ok := RunnerFor(p, argv)
	if !ok || r.ParseList == nil {
		return nil
	}
	return r.ParseList(stdout)
}

// SingleTestCmd is argv's runner narrowed to the one test id.
func SingleTestCmd(p Plugin, argv []string, testPath, id string) ([]string, bool) {
	r, ok := RunnerFor(p, argv)
	if !ok || r.SingleCmd == nil {
		return nil, false
	}
	return r.SingleCmd(argv, testPath, id)
}

// ResolvesByList reports whether a critic's test name must be resolved
// against the runner's own list before SingleTestCmd.
func ResolvesByList(p Plugin, argv []string) bool {
	r, ok := RunnerFor(p, argv)
	return ok && r.ResolveByList
}

// RunDisclosures is what the scan line says about running testCmd in
// repoDir that differs from how the project's own setup runs its suite —
// said, never acted on: corral runs the operator's command as given and never
// substitutes one of its own. Rust reports a project nextest.toml that
// `cargo test` bypasses, and nextest's passed-on-retry grading. nil when the
// runner has nothing to say; the one way a caller asks, so no caller
// switches on a language to decide whether there is anything to disclose.
func RunDisclosures(p Plugin, repoDir string, testCmd []string) []string {
	r, ok := RunnerFor(p, testCmd)
	if !ok || r.Disclose == nil {
		return nil
	}
	return r.Disclose(repoDir, testCmd)
}

// FailureParserFor is argv's runner as the FailureParser adequacy holds, or
// nil when the runner names no failures. It also implements FlakyReporter
// when the runner reports retries.
func FailureParserFor(p Plugin, argv []string) FailureParser {
	r, ok := RunnerFor(p, argv)
	if !ok || r.FirstFailure == nil {
		return nil
	}
	if r.Flaky != nil {
		return flakyRowParser{rowParser{r}}
	}
	return rowParser{r}
}

type rowParser struct{ r Runner }

func (p rowParser) FirstFailure(output []byte) string { return p.r.FirstFailure(output) }

type flakyRowParser struct{ rowParser }

func (p flakyRowParser) FlakyTests(output []byte) []string { return p.r.Flaky(output) }

// DeselectorFor is argv's runner's salvage pair, ok=false when it has none:
// the runner's failing ids, and the arguments that deselect them, for
// salvaging a partially broken authored test. The compliant check is
// all-or-nothing per FILE: if any test in the authored file fails against the
// unmutated code, the whole file is discarded and nothing is scored.
//
// Measured cost of that, on the first authored test corral ever retained
// (gemini-3.6-flash on pallets/flask, 2026-07-31): 13 tests, TEN PASSED, and
// all 13 were thrown away because 3 carried wrong API assumptions.
//
// Asking the model to repair itself is one answer and depends on the model
// being able to. Deselecting the failures and scoring with the remainder does
// not depend on the model at all — it is arithmetic over the runner's own
// output.
//
// Deliberately absent from rows for runners corral cannot parse failures
// from precisely, and from unittest, which exits 2 on pytest's --deselect. A wrong selector would
// deselect the wrong test and silently narrow the exam, which is worse than
// not salvaging: the run would look healthier while proving less.
func DeselectorFor(p Plugin, argv []string) (failed func(string) []string, deselect func([]string) []string, ok bool) {
	r, found := RunnerFor(p, argv)
	if !found || r.FailedTests == nil || r.DeselectArgs == nil {
		return nil, nil, false
	}
	return r.FailedTests, r.DeselectArgs, true
}

// Runners returns the runner table in its order, for readers outside the
// package (scripts/langsupport renders it). The rows are copies.
func Runners() []Runner { return slices.Clone(runners) }
