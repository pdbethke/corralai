// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"path/filepath"

	"github.com/pdbethke/corralai/internal/pairing"
)

func init() { Register(rustPlugin{}) }

// rustPlugin is registered with pairing (TestRules) and detection first; the
// rest of its methods give the honest minimal answer each method's contract
// defines for "not yet" (ok=false, nil, a stock command) until the tasks that
// prove them against a real crate. Registering it is what puts Rust under
// every registry-derived test in this package at once, so a method here is
// never a guess the tests have not read.
type rustPlugin struct{}

func (rustPlugin) Name() string                { return "rust" }
func (rustPlugin) Detect(codePath string) bool { return filepath.Ext(codePath) == ".rs" }

// Scaffold is empty: a Rust audit always runs inside a real crate (the
// project's own Cargo.toml), never a corral-owned single-file workspace.
func (rustPlugin) Scaffold() map[string]string { return map[string]string{} }

func (rustPlugin) TestCmd() []string { return []string{"cargo", "test"} }

// LineComment is the rust line-comment marker.
func (rustPlugin) LineComment() string { return "//" }

// DependencyDirs is `cargo vendor`'s default destination. cargo's target/ is
// deliberately NOT declared here although every run writes into it:
// treedirs already lists it as language-neutral BUILD OUTPUT, which the scan
// skips but still ACCOUNTS in the report, and declaring it a dependency would
// silently move it to the unaccounted set for every repository
// (TestEnumerateSkipsBuildOutputDirs). Whether the workspace pool must treat
// target/ as run-written belongs to the task that runs cargo there.
func (rustPlugin) DependencyDirs() []DepDir { return []DepDir{{Name: "vendor"}} }

// CompileCheck builds the test targets without running them. cargo compiles
// a crate, not a file, so there is no narrower honest scope to name here
// (cf. Go's package-scoped `go test -run ^$`); it is one command, and never
// empty — see Plugin.CompileCheck.
func (rustPlugin) CompileCheck(string, string) [][]string {
	return [][]string{{"cargo", "test", "--no-run"}}
}

// TestRules declares where a Rust source's tests live, in the order Resolve
// tries them:
//
//  1. Declared — the file the source's own out-of-line test module loads
//     (`#[path = "app_id_tests.rs"] mod tests;`, or a bare `mod tests;` and
//     rustc's default module file). The source NAMES its test; that is the
//     strongest evidence a pairing can have, so it comes first.
//  2. Inline — the source itself, when it holds `#[cfg(test)] mod tests { }`.
//     This is also Primary's answer, so an authored test lands in the
//     source file by default.
//  3. and 4. Recognize `{base}_tests.rs` and `tests.rs`: the names Rust test
//     modules are given in practice (Warp's app_id_tests.rs, a default
//     module's tests.rs), so IsTest never audits one as a source. Never
//     candidates: which source one belongs to is said by the source's own
//     module declaration, not guessed from the name.
//  5. TestDir `tests` — cargo's integration tests, a test by directory
//     alone; their file names carry no marker, which is why this needs its
//     own Shape rather than a Name (see pairing.Rule's marker invariant).
//
// Rust has no Sibling or naming-convention candidate on purpose: a file named
// like a test is only that file's test if the file says so.
func (rustPlugin) TestRules() []pairing.Rule {
	return []pairing.Rule{
		{Shape: pairing.Declared, Rank: 0},
		{Shape: pairing.Inline, Rank: 0},
		{Shape: pairing.Recognize, Name: "{base}_tests.rs"},
		{Shape: pairing.Recognize, Name: "tests.rs"},
		{Shape: pairing.TestDir, Dir: "tests"},
	}
}

// Preflight requires the runner's binary: cargo for libtest, cargo-nextest for
// nextest — or the operator's own first token (a rustup proxy at an absolute
// path) when one is given. The runner row decides; see rustRunner.
func (rustPlugin) Preflight(testCmd []string) error {
	return toolOnPath(rustRunner(testCmd).preflightBin)
}

func (rustPlugin) PromptLang() string { return "Rust" }

func (rustPlugin) TestWriterSystem() string {
	return `You are a TEST-WRITER. Given a security control GOAL, a target source file, and its signature surface, write ONE executable Rust test module that verifies the code SATISFIES the goal.
- MATCH THE PROJECT'S CONVENTIONS, which the developer's own tests (shown to you) use, ahead of everything below: their module layout, their assertion helpers, and any test crates they already depend on.
- Absent a project convention: write ` + "`#[cfg(test)] mod corral_tests { use super::*; ... }`" + ` with plain ` + "`#[test]`" + ` functions — it is compiled into the target's own module, so private items are in scope.
- It MUST compile against the target and MUST FAIL if the goal is violated — test the goal's boundary (what a weakened implementation would pass that a compliant one must not).
- Standard library only unless the project's own tests already use more. Deterministic, no network.
Return ONLY the raw Rust module content — no prose, no markdown fences.`
}

func (rustPlugin) MutantSystem() string {
	return mutantSystem("signatures, types, and module paths (a drop-in replacement that compiles)", "compile errors")
}

// ImportPath is always ok=false: the authored test is a module inside the
// target file (`use super::*;`), so there is no import path to derive. See
// lang.Plugin.ImportPath's doc comment for the general rule this follows.
func (rustPlugin) ImportPath(string, func(string) bool) (string, bool) { return "", false }

// ImportNote is always "": see ImportPath — a child test module reaches its
// parent with `use super::*;`, which needs no per-task correction.
func (rustPlugin) ImportNote(string, bool) string { return "" }

// SingleTestCmd, ListTestsCmd and ParseTestList stay ok=false/nil, and that is
// a limit of the Plugin interface, not of the runner row: none of the three is
// handed the operator's test command, so a stateless plugin cannot know
// whether the baseline runs under libtest or nextest, and answering for the
// wrong runner would score single tests under a harness the suite does not
// use. rustRunner(testCmd).singleTest and .listCmd hold the real answers for
// the caller that has the command.
func (rustPlugin) SingleTestCmd(string, string) ([]string, bool) { return nil, false }

func (rustPlugin) ListTestsCmd(string) ([]string, bool) { return nil, false }

func (rustPlugin) ParseTestList(string) []string { return nil }

// FirstFailure names the first test the runner's own summary reported as
// failing. FailureParser is handed output alone, so the grammar is read off
// the output (rustGrammarOf), never guessed from a command it does not have.
// Output the grammar does not recognise — a build failure, a runner corral
// does not understand — names nothing, which is the contract's "" (see
// FailureParser); the interface has no way to say "unrecognised" apart from
// "no failure", and parseRustRun's Recognised is where that distinction lives.
func (rustPlugin) FirstFailure(output []byte) string {
	run := parseRustRun(rustGrammarOf(output), output)
	if len(run.Failures) == 0 {
		return ""
	}
	return run.Failures[0]
}

// TestNamesInSource: no static lister for this language yet; nil is "unknown".
func (rustPlugin) TestNamesInSource(string, string) []string { return nil }

// WorkspaceRunEnv is a no-op FOR NOW, and that is not a claim of safety:
// cargo decides whether to rebuild a local crate by comparing source mtimes
// against target/'s dep-info, which may share the same-second staleness hole
// CPython's __pycache__ has on the workspace substrate. It is unmeasured, and
// the task that runs Rust on that substrate owns measuring it. See
// lang.Plugin.WorkspaceRunEnv's doc comment.
func (rustPlugin) WorkspaceRunEnv() (env []string, cleanup func()) { return nil, func() {} }
