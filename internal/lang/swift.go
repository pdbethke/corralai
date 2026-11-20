// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/pdbethke/corralai/internal/pairing"
)

func init() { Register(swiftPlugin{}) }

// swiftPlugin audits SwiftPM packages on Linux: detection and pairing
// (TestRules), the `swift test` runner row (swift_runner.go), a compile check
// scoped to one target, the static test list (swift_names.go) and the
// writer's prompt are built against a real package's captured runs
// (testdata/runners/swift-*.txt). corral's sandbox does not run Swift
// (JailPreflight), so an audit runs on the workspace substrate. The
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
// (.build/checkouts holds every dependency's source). Declared as a
// dependency, the repo scan (reposcan.Enumerate and its fallback walk) and the
// repo reader skip both directories, and invisibly rather than accounted, as
// node_modules is. Before this they skipped neither: no dot-directory is
// skipped there, and only a repository's own .gitignore kept checkouts out of
// the candidate list.
//
// The jail seed does NOT read this declaration. It skips only .git and the
// BuildDirs below, and binds only SharedDependencies. So .build stays out of
// the seed through BuildDirs alone (and only beside a Package.swift), and
// .swiftpm, which is no BuildDir, is still copied into every jail seed. The
// review walkers already skip every dot-directory.
//
// RunWrites is what keeps both out of SharedDependencies, which is the set the
// workspace pool symlinks into every tree and the jail binds read-only: a
// swift build WRITES .build, so N trees building at once through one link
// would read each other's products (the .tox reasoning, python.go).
//
// .swiftpm is SwiftPM's configuration directory (Xcode workspace data). It is
// no source either, and a run may write to it, so it is declared the same way.
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
//
// Consequence: leaving .build out of the jail seed leaves out .build/checkouts
// too, so a package with remote dependencies cannot resolve them in the
// offline jail. That is why the default sandbox does not run Swift yet; making
// it do so needs a way to keep the resolved checkouts while dropping build
// output.
func (swiftPlugin) BuildDirs() []BuildDir {
	return []BuildDir{{Name: ".build", Marker: "Package.swift"}}
}

// CompileCheck builds ONE target, never the package (a compile check is
// scoped to the audited file and its test; AGENTS.md): the test target
// testPath is in, when it is a test (Tests/<Target>/...); otherwise the source
// target codePath is in (Sources/<Target>/...). Building a target builds the
// targets it depends on, so the test target's build also compiles the source
// under test, mutated or not.
//
// Both callers hand a test. JailValidator.CompileTest hands the authored
// test's path, so the authored test is compiled with its own target. The
// mutant gate hands the developer's own test
// (JailScorer.mutantGateTestPath), so each mutant is compiled with the test
// target that calls it, and a mutant that breaks only the tests' view of the
// source (a changed signature a test calls) is invalid rather than a kill.
// The source target is built alone only when no test is handed (single-file
// mode, which Preflight refuses for Swift, or ""), and then such a mutant
// would fail the suite's own build and count as a kill.
//
// Measured on Swift 6.3.2 (testdata/runners/swift-build-target-*.txt):
// `swift build --target <TestTarget>` built the test target and its
// dependency, left a sibling test target that did not compile unbuilt and
// passed, and failed on a source that did not compile.
//
// A path outside SwiftPM's default layout names no target, and the sequence
// is never empty (see Plugin.CompileCheck), so it is then the package's
// build with its tests: the set of targets `swift test` itself builds before
// any test runs, so it can fail on nothing the suite would not.
func (swiftPlugin) CompileCheck(codePath, testPath string) [][]string {
	if t, ok := swiftTestTarget(testPath); ok {
		return [][]string{{"swift", "build", "--target", t}}
	}
	if t, ok := swiftSourceTarget(codePath); ok {
		return [][]string{{"swift", "build", "--target", t}}
	}
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

// Preflight checks what every substrate needs before anything is spent: a
// Linux host, `swift` on PATH (or the operator's own first token), and a
// Package.swift at or above the repository root, which is where SwiftPM looks
// for the package `swift test` runs. Single-file mode and the brain have no
// repository, so they are refused here: a Swift audit always runs inside the
// project's own package.
func (swiftPlugin) Preflight(repoRoot string, testCmd []string) error {
	return swiftPreflight(runtime.GOOS, repoRoot, testCmd)
}

// swiftPackageSearchDepth bounds the upward walk for Package.swift. SwiftPM
// itself walks to the filesystem root; no real checkout is nested this deep.
const swiftPackageSearchDepth = 64

// swiftPreflight is Preflight with the host OS handed in, so the non-Linux
// refusal is tested on every host.
func swiftPreflight(goos, repoRoot string, testCmd []string) error {
	if goos != "linux" {
		return fmt.Errorf("lang: corral audits Swift on Linux only, the one platform `swift test` has been run on for it (this host is %s); run the audit on a Linux machine or in a Linux container", goos)
	}
	if err := toolOnPath(preflightBin(testCmd, "swift")); err != nil {
		return err
	}
	if strings.TrimSpace(repoRoot) == "" {
		return errors.New("lang: a Swift audit runs inside the project's SwiftPM package, and there is no repository to find its Package.swift in; pass --repo-dir with the package's directory")
	}
	abs, err := filepath.Abs(repoRoot)
	if err != nil {
		return fmt.Errorf("lang: resolving %s: %w", repoRoot, err)
	}
	if _, ok := nearestAncestorWith(abs, []string{"Package.swift"}, swiftPackageSearchDepth); !ok {
		return fmt.Errorf("lang: no Package.swift at or above %s: corral audits Swift only inside a SwiftPM package; pass --repo-dir with the package's directory", repoRoot)
	}
	return nil
}

// JailPreflight refuses every jailed run, before any spend: corral's sandbox
// does not run Swift. Its copy of the repository leaves out .build (see
// BuildDirs), and .build is also where SwiftPM keeps the dependency
// checkouts it resolved, which the offline sandbox cannot fetch again. The
// workspace substrate runs the operator's own toolchain in the checkout,
// .build included, and never asks this (see JailPreflighter).
func (swiftPlugin) JailPreflight([]string) error {
	return errors.New("lang: corral's sandbox does not run Swift yet: its copy of the repository leaves out .build, where SwiftPM keeps the dependency checkouts it resolved, and the sandbox has no network to resolve them again. Run `corral certify --repo --substrate workspace` on a scratch clone of the repository instead: it runs your own Swift toolchain in that clone, writing each mutant into the clone's files and restoring them")
}

func (swiftPlugin) PromptLang() string { return "Swift" }

// TestWriterSystem ranks the project's own tests first, as every plugin's
// must (the writer is shown them), and then asks for what SwiftPM needs of a
// file added to a test target: top-level names in a module are one
// namespace, so an authored `@Test func parses()` beside the project's own
// did not build ("invalid redeclaration", swift-authored-collision.txt),
// while the same tests inside one type of their own built and ran in both
// frameworks (swift-authored-wrapped.txt, whose type is CorralCalcTests).
// The type is named after the authored file, which the writer is told
// (advpool's RunSpec.writtenAuthoredPath, the path the scorer writes, renamed
// past any file already in the jail's workspace), so on the jail substrate a
// file the developer kept from an earlier audit, and the type in it, cannot
// share the new one's name. The workspace substrate's map is empty, so there
// nothing is renamed: the new file takes the kept file's path for the run.
func (swiftPlugin) TestWriterSystem() string {
	return `You are a TEST-WRITER. Given a security control GOAL, a target source file, and its signature surface, write executable Swift tests that verify the code SATISFIES the goal.
- MATCH THE PROJECT'S CONVENTIONS, which the developer's own tests (shown to you) use, ahead of everything below: swift-testing or XCTest, the same imports (including ` + "`@testable import`" + ` of the module under test, if they use it), naming and assertion helpers.
- Your file is added to a test target beside the project's own test files, and every top-level name in a target is shared. So put EVERYTHING you write — every test, helper, constant and fixture — inside ONE type named after the test file you are told to write: its file name without ` + "`.swift`" + ` (for Calc_corralTests.swift, ` + "`Calc_corralTests`" + `; leave out any character a Swift identifier cannot hold). corral names that file apart from every file already in the target, so the type's name is apart from theirs too. With XCTest that type is ` + "`final class ... : XCTestCase`" + ` and each test a ` + "`func test...()`" + ` method; with swift-testing it is a ` + "`@Suite struct`" + ` and each test an ` + "`@Test func`" + `. Nothing but import lines goes at the top level.
- It MUST compile against the target and MUST FAIL if the goal is violated — test the goal's boundary (what a weakened implementation would pass that a compliant one must not).
- Standard library and the test framework the project already uses only. Deterministic, no network.
Return ONLY the raw Swift source — no prose, no markdown fences.`
}

func (swiftPlugin) MutantSystem() string {
	return mutantSystem("signatures, types, and access levels (a drop-in replacement that compiles)", "compile errors")
}

// ImportPath is the module the code under test is in: the source target's
// name (Sources/<Target>/...), which a test reaches with `@testable import
// <Target>`, not a path derived from the file. ok=false outside that layout,
// and for a target whose name is no identifier: SwiftPM derives such a
// module's name, and the derivation is not guessed (swiftModuleName). exists
// is not consulted. See Plugin.ImportPath.
func (swiftPlugin) ImportPath(codePath string, _ func(string) bool) (string, bool) {
	t, ok := swiftSourceTarget(codePath)
	if !ok || !swiftModuleName.MatchString(t) {
		return "", false
	}
	return t, true
}

// ImportNote tells the writer the module to import, or that it could not be
// determined. Never "": the shared prompt otherwise tells a language without
// a note to reference the code by its file path, which a Swift test cannot.
func (swiftPlugin) ImportNote(module string, ok bool) string {
	if ok && module != "" {
		return fmt.Sprintf("The code under test is in the module %q, the target its file is in: import it with `@testable import %s`, as the developer's own tests do if they import it, and never by a file path.\n\n", module, module)
	}
	return "The module the code under test is in could not be determined from its path (it is not under Sources/<Target>/, or its target's name is not a Swift identifier). Import it exactly as the developer's own tests shown above import it, and never by a file path.\n\n"
}

// WorkspaceRunEnv is a no-op FOR NOW, and that is not a claim of safety: whether
// SwiftPM's incremental build can serve a stale product for a mutant rewritten
// in the same second on the workspace substrate (the hole CPython's
// __pycache__ has) is unmeasured, and it is to be measured
// when Swift runs on that substrate. See Plugin.WorkspaceRunEnv.
func (swiftPlugin) WorkspaceRunEnv() (env []string, cleanup func()) { return nil, func() {} }

// ConcatTests merges nothing yet: what a merged Swift file is has not been
// settled (top-level names in a module must be unique, so a merge would have to
// wrap the tests in one type). It refuses every part rather than guess; ConcatAuthored then delivers each
// proven test as its own file, with this reason, which is the contract's
// answer for a language that cannot merge precisely (see TestConcatenator).
func (swiftPlugin) ConcatTests([]AuthoredPart) (string, error) {
	return "", errors.New("lang: swift authored tests are not merged yet, so each proven test stays its own file")
}
