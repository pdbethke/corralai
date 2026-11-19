// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"errors"
	"path"
	"path/filepath"
	"strings"

	"github.com/pdbethke/corralai/internal/pairing"
)

func init() { Register(swiftPlugin{}) }

// swiftPlugin is registered with pairing (TestRules) and detection first, as
// rustPlugin was: every other method gives the answer its contract defines
// for "not built yet" (ok=false, nil, a stock command) until the tasks that
// prove it against a real SwiftPM package. Registering it is what puts Swift
// under every registry-derived test at once. SwiftPM only, Linux only; the
// capabilities it lacks are named, with a reason each, in ParityExemptions.
type swiftPlugin struct{}

func (swiftPlugin) Name() string { return "swift" }

// Detect is the .swift extension, except SwiftPM's manifests: Package.swift
// and its versioned Package@swift-5.9.swift describe the build, they are not
// code under audit. Name-matched on the base name only, so a source called
// PackageManager.swift is still a source.
func (swiftPlugin) Detect(codePath string) bool {
	if filepath.Ext(codePath) != ".swift" {
		return false
	}
	base := filepath.Base(codePath)
	return base != "Package.swift" && !(strings.HasPrefix(base, "Package@swift-") && strings.HasSuffix(base, ".swift"))
}

// Scaffold is empty: a Swift audit always runs inside a real package (the
// project's own Package.swift), never a corral-owned single-file workspace.
func (swiftPlugin) Scaffold() map[string]string { return map[string]string{} }

func (swiftPlugin) TestCmd() []string { return []string{"swift", "test"} }

// VersionCmd: see Plugin.VersionCmd.
func (swiftPlugin) VersionCmd() []string { return []string{"swift", "--version"} }

// LineComment is the swift line-comment marker.
func (swiftPlugin) LineComment() string { return "//" }

// DependencyDirs declares the two directories SwiftPM writes beside a
// package, both RunWrites.
//
// .build is SwiftPM's build output AND its dependency checkout directory
// (.build/checkouts holds every dependency's source). What each walker did
// with it before this declaration: reposcan.Enumerate walked it (no dot-
// directory is skipped there; only a repository's own .gitignore kept
// checkouts out of the candidate list), the repo reader walked it, and the
// jail seed copied it. Declared as a dependency it is skipped by all of
// them, and invisible rather than accounted, as node_modules is.
//
// RunWrites is what keeps it out of SharedDependencies, which is the set the
// workspace pool symlinks into every tree and the jail binds read-only: a
// swift build WRITES .build, so N trees building at once through one link
// would read each other's products (the .tox reasoning, python.go).
//
// .swiftpm is SwiftPM's configuration directory (Xcode workspace data). It is
// no source either, and a run may write to it, so it is declared the same
// way. The review walkers already skip every dot-directory; the three above
// did not.
func (swiftPlugin) DependencyDirs() []DepDir {
	return []DepDir{{Name: ".build", RunWrites: true}, {Name: ".swiftpm", RunWrites: true}}
}

// BuildDirs is SwiftPM's default build directory, `.build` beside the
// Package.swift it was built from. The marker keeps a directory merely named
// .build (somebody's own) in the jail seed. A --scratch-path or --build-path
// that moves it is not followed, for rust's reason (see rustPlugin.BuildDirs):
// the default is what a checkout carries, and a miss costs seed size, never a
// wrong verdict. .swiftpm is not declared here: it is small configuration, not
// derived output.
func (swiftPlugin) BuildDirs() []BuildDir {
	return []BuildDir{{Name: ".build", Marker: "Package.swift"}}
}

// CompileCheck builds the package's targets without running tests. Not yet
// scoped to the audited file's target (Swift phase 1, task 3); never empty,
// see Plugin.CompileCheck.
func (swiftPlugin) CompileCheck(string, string) [][]string {
	return [][]string{{"swift", "build", "--build-tests"}}
}

// TestRules declares SwiftPM's convention, in the order Resolve tries them:
//
//  1. ParallelTree Sources/<Target>/<dir>/X.swift ->
//     Tests/<Target>Tests/<dir>/XTests.swift: the target's test directory is
//     the target's name plus Tests (DirSuffix), the rest of the path mirrors.
//     Rank 1 as every parallel tree is: it encodes directory evidence, a
//     sibling would be rank 0, and Swift has no sibling convention (a file
//     under Sources/ is compiled into the module; a test beside it would ship).
//  2. TestDir Tests: SwiftPM compiles every file under Tests/ as test code,
//     and a project may name them for behaviour (Lariat does), so a file's
//     directory alone makes it a test, as cargo's tests/ does. Without this a
//     behaviour-named test would be audited as a source. The capital T is
//     SwiftPM's own and is not the lowercase tests/ Generic already knows.
//
// The Name of rule 1 carries the Tests marker, so IsTest never calls a source
// a test (pairing.Rule's marker invariant).
func (swiftPlugin) TestRules() []pairing.Rule {
	return []pairing.Rule{
		{Shape: pairing.ParallelTree, Name: "{base}Tests.swift", Dir: "Tests", DirSuffix: "Tests", Rank: 1},
		{Shape: pairing.TestDir, Dir: "Tests"},
	}
}

// swiftTestTarget is the test target a path under Tests/ belongs to: the
// first segment below Tests/, SwiftPM's own definition of a test target's
// directory. ok=false outside Tests/<Target>/<file>.
func swiftTestTarget(testPath string) (string, bool) { return swiftTargetUnder("Tests", testPath) }

// swiftSourceTarget is the source target a path under Sources/ belongs to:
// the first segment below Sources/. ok=false outside Sources/<Target>/<file>.
func swiftSourceTarget(codePath string) (string, bool) { return swiftTargetUnder("Sources", codePath) }

// swiftTargetUnder reads <root>/<Target>/<more> and returns Target. A file
// directly under the root has no target directory, so it is not one.
func swiftTargetUnder(root, p string) (string, bool) {
	parts := strings.Split(path.Clean(filepath.ToSlash(p)), "/")
	if len(parts) < 3 || parts[0] != root || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

// Preflight requires `swift` on PATH, or the operator's own first token. The
// Package.swift and platform checks the spec names are a later task.
func (swiftPlugin) Preflight(_ string, testCmd []string) error {
	return toolOnPath(preflightBin(testCmd, "swift"))
}

func (swiftPlugin) PromptLang() string { return "Swift" }

func (swiftPlugin) TestWriterSystem() string {
	return `You are a TEST-WRITER. Given a security control GOAL, a target source file, and its signature surface, write executable Swift tests that verify the code SATISFIES the goal.
- MATCH THE PROJECT'S CONVENTIONS, which the developer's own tests (shown to you) use, ahead of everything below: swift-testing or XCTest, their imports, naming and assertion helpers.
- It MUST compile against the target and MUST FAIL if the goal is violated — test the goal's boundary (what a weakened implementation would pass that a compliant one must not).
- Standard library and the test framework the project already uses only. Deterministic, no network.
Return ONLY the raw Swift source — no prose, no markdown fences.`
}

func (swiftPlugin) MutantSystem() string {
	return mutantSystem("signatures, types, and access levels (a drop-in replacement that compiles)", "compile errors")
}

// ImportPath is always ok=false: a Swift test reaches the module under test by
// its target name (`@testable import <Target>`), not by a path derived from
// the file. See Plugin.ImportPath.
func (swiftPlugin) ImportPath(string, func(string) bool) (string, bool) { return "", false }

// ImportNote is always "": see ImportPath.
func (swiftPlugin) ImportNote(string, bool) string { return "" }

// TestNamesInSource is nil — no list — until the task that reads swift-testing
// and XCTest declarations (Swift phase 1, task 3). nil never means the file
// has no tests; see Plugin.TestNamesInSource.
func (swiftPlugin) TestNamesInSource(string, string) []string { return nil }

// WorkspaceRunEnv is a no-op FOR NOW, and that is not a claim of safety: whether
// SwiftPM's incremental build can serve a stale product for a mutant rewritten
// in the same second on the workspace substrate (the hole CPython's
// __pycache__ has) is unmeasured, and the task that runs Swift on that
// substrate owns measuring it. See Plugin.WorkspaceRunEnv.
func (swiftPlugin) WorkspaceRunEnv() (env []string, cleanup func()) { return nil, func() {} }

// ConcatTests merges nothing yet (Swift phase 1, task 3 owns the authored-test
// landing, which settles what a merged Swift file is: top-level names in a
// module must be unique, so the writer wraps its tests in one type). It
// refuses every part rather than guess; ConcatAuthored then delivers each
// proven test as its own file, with this reason, which is the contract's
// answer for a language that cannot merge precisely (see TestConcatenator).
func (swiftPlugin) ConcatTests([]AuthoredPart) (string, error) {
	return "", errors.New("lang: swift authored tests are not merged yet, so each proven test stays its own file")
}
