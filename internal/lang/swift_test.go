// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/pairing"
)

func TestSwiftDetect(t *testing.T) {
	for path, want := range map[string]bool{
		"Sources/A/X.swift":              true,
		"X.swift":                        true,
		"Package.swift":                  false,
		"Sub/Package.swift":              false,
		"Package@swift-5.9.swift":        false,
		"X.swiftinterface":               false,
		"Sources/A/PackageManager.swift": true,
	} {
		if got := (swiftPlugin{}).Detect(path); got != want {
			t.Errorf("Detect(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestSwiftRegistered(t *testing.T) {
	p, ok := ByName("swift")
	if !ok || p.Name() != "swift" {
		t.Fatalf("swift is not in the registry (ok=%v)", ok)
	}
	if got, ok := Detect("Sources/A/X.swift"); !ok || got.Name() != "swift" {
		t.Errorf("Detect(.swift) did not resolve to the swift plugin")
	}
}

func TestSwiftPairsSourcesToTheirTestTarget(t *testing.T) {
	rules := swiftPlugin{}.TestRules()
	for src, want := range map[string]string{
		"Sources/LassoParser/Evaluator.swift":     "Tests/LassoParserTests/EvaluatorTests.swift",
		"Sources/LassoParser/Sub/Deep/Node.swift": "Tests/LassoParserTests/Sub/Deep/NodeTests.swift",
	} {
		c := pairing.Candidates(rules, src)
		if len(c) == 0 || c[0].Path != want {
			t.Errorf("Candidates(%q) = %v, want first %q", src, c, want)
		}
	}
}

// Every file under Tests/ is a test to SwiftPM, whatever its name: Lariat's
// are named for behaviour. A strict IsTest that missed them would audit tests
// as sources.
func TestSwiftTestsDirectoryHoldsTests(t *testing.T) {
	rules := swiftPlugin{}.TestRules()
	for path, want := range map[string]bool{
		"Tests/LassoParserTests/Evaluating.swift":     true,
		"Tests/LassoParserTests/EvaluatorTests.swift": true,
		"Sources/LassoParser/Evaluator.swift":         false,
	} {
		if got := pairing.IsTest(rules, path); got != want {
			t.Errorf("IsTest(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestSwiftTargets(t *testing.T) {
	if got, ok := swiftTestTarget("Tests/LassoParserTests/A/B.swift"); !ok || got != "LassoParserTests" {
		t.Errorf("swiftTestTarget = %q, %v", got, ok)
	}
	if got, ok := swiftSourceTarget("Sources/LassoParser/X.swift"); !ok || got != "LassoParser" {
		t.Errorf("swiftSourceTarget = %q, %v", got, ok)
	}
	for _, p := range []string{"X.swift", "Sources/X.swift", "Tests/X.swift", "Other/A/X.swift", "Sources/", "Sources//X.swift", ""} {
		if g, ok := swiftTestTarget(p); ok {
			t.Errorf("swiftTestTarget(%q) = %q, want false", p, g)
		}
		if g, ok := swiftSourceTarget(p); ok {
			t.Errorf("swiftSourceTarget(%q) = %q, want false", p, g)
		}
	}
	if _, ok := swiftTestTarget("Sources/A/X.swift"); ok {
		t.Error("a Sources path has no test target")
	}
	if _, ok := swiftSourceTarget("Tests/ATests/X.swift"); ok {
		t.Error("a Tests path has no source target")
	}
}

// SwiftPM writes its checkouts and build products in .build, which is
// dependency source and build output at once, and configuration in .swiftpm.
// Neither is the repository's source, and neither may be a directory pool
// trees link to one shared place: trees building at once would read each
// other's output.
func TestSwiftDeclaresBuildAndConfigDirsAsRunWritten(t *testing.T) {
	got := map[string]DepDir{}
	for _, d := range (swiftPlugin{}).DependencyDirs() {
		got[d.Name] = d
	}
	for _, name := range []string{".build", ".swiftpm"} {
		d, ok := got[name]
		if !ok || !d.RunWrites {
			t.Errorf("%s: declared=%v RunWrites=%v, want declared and RunWrites", name, ok, d.RunWrites)
		}
	}
	if bd := (swiftPlugin{}).BuildDirs(); len(bd) != 1 || bd[0] != (BuildDir{Name: ".build", Marker: "Package.swift"}) {
		t.Errorf("BuildDirs = %v", bd)
	}
}

// CompileCheck builds ONE target, never the package: the test target an
// authored test is in, or, when the path it is handed is not a test (the
// mutant gate hands the source itself when the authored test is not in the
// workspace), the target the source is in. `swift build --target` accepted
// both on Swift 6.3.2, built the target's dependencies with it, left a broken
// sibling test target unbuilt, and failed on a mutant that does not compile
// (testdata/runners/swift-build-target-*.txt).
func TestSwiftCompileCheckBuildsOneTarget(t *testing.T) {
	p := swiftPlugin{}
	for _, tc := range []struct{ code, test, want string }{
		{"Sources/Calc/Calc.swift", "Tests/CalcTests/Calc_corralTests.swift", "CalcTests"},
		{"Sources/Calc/Calc.swift", "Tests/IntegrationTests/Sub/Calc_corralTests.swift", "IntegrationTests"},
		{"Sources/Calc/Calc.swift", "Sources/Calc/Calc.swift", "Calc"},
		{"Sources/Calc/Deep/Adder.swift", "", "Calc"},
	} {
		got := p.CompileCheck(tc.code, tc.test)
		want := [][]string{{"swift", "build", "--target", tc.want}}
		if !slices.EqualFunc(got, want, slices.Equal[[]string]) {
			t.Errorf("CompileCheck(%q, %q) = %q, want %q", tc.code, tc.test, got, want)
		}
	}
	// Outside SwiftPM's default layout no target can be named, and the
	// sequence is still never empty: the package's build, the same set of
	// targets `swift test` builds anyway.
	if got := p.CompileCheck("Lib/Calc.swift", "Lib/CalcTests.swift"); len(got) != 1 || !slices.Equal(got[0], []string{"swift", "build", "--build-tests"}) {
		t.Errorf("CompileCheck outside the layout = %q", got)
	}
}

// capturedRun is a swiftcmd capture's command (its `$ ` line) and exit
// status (its `# exit:` line).
func capturedRun(t *testing.T, fixture string) (cmd string, exit string, out string) {
	t.Helper()
	out = readFixture(t, fixture)
	for _, line := range strings.Split(out, "\n") {
		if c, ok := strings.CutPrefix(line, "$ "); ok && cmd == "" {
			cmd = c
		}
		if e, ok := strings.CutPrefix(line, "# exit: "); ok {
			exit = e
		}
	}
	return cmd, exit, out
}

// The commands CompileCheck builds are the ones the captures ran, and the
// captures show what the check relies on: a test target and the source
// target each build; a test target builds while a sibling test target does
// not compile, and is never compiled with it; and a source that does not
// compile fails the test target's build.
func TestSwiftCompileCheckIsWhatTheCapturesRan(t *testing.T) {
	p := swiftPlugin{}
	join := func(seq [][]string) string { return strings.Join(seq[0], " ") }
	for _, tc := range []struct {
		fixture, want, exit string
	}{
		{"swift-build-target-test.txt", join(p.CompileCheck("Sources/Calc/Calc.swift", "Tests/CalcXCTests/Calc_corralTests.swift")), "0"},
		{"swift-build-target-source.txt", join(p.CompileCheck("Sources/Calc/Calc.swift", "Sources/Calc/Calc.swift")), "0"},
		{"swift-build-target-scoped.txt", join(p.CompileCheck("Sources/Calc/Calc.swift", "Tests/CalcXCTests/X.swift")) + " --scratch-path .build-scoped", "0"},
		{"swift-build-target-mutant.txt", join(p.CompileCheck("Sources/Calc/Calc.swift", "Tests/CalcXCTests/X.swift")), "1"},
	} {
		cmd, exit, out := capturedRun(t, tc.fixture)
		if cmd != tc.want || exit != tc.exit {
			t.Errorf("%s: ran %q (exit %s), want %q (exit %s)", tc.fixture, cmd, exit, tc.want, tc.exit)
		}
		if strings.Contains(out, "Compiling CalcTests") {
			t.Errorf("%s compiled the sibling test target CalcTests", tc.fixture)
		}
	}
	if _, _, out := capturedRun(t, "swift-build-target-scoped.txt"); !strings.Contains(out, "Compiling CalcXCTests") || !strings.Contains(out, "Compiling Calc ") {
		t.Errorf("the scoped build compiled nothing, so it shows nothing:\n%s", out)
	}
}

// The one-type rule the writer is given, as captured: a corral-named type
// built and ran in each framework beside the target's own tests (two
// swift-testing tests, one XCTest), and a top-level test named like one of
// the target's did not build.
func TestSwiftAuthoredWrapperRanInBothFrameworks(t *testing.T) {
	_, exit, out := capturedRun(t, "swift-authored-wrapped.txt")
	if exit != "0" || !strings.Contains(out, "Test run with 2 tests in 1 suite passed") || !strings.Contains(out, "Executed 1 test, with 0 failures") {
		t.Errorf("swift-authored-wrapped.txt: exit %s:\n%s", exit, out)
	}
	if _, exit, out := capturedRun(t, "swift-authored-collision.txt"); exit != "1" || !strings.Contains(out, "invalid redeclaration of 'parses()'") {
		t.Errorf("swift-authored-collision.txt: exit %s:\n%s", exit, out)
	}
}

// fakeSwiftOnPath puts an executable named swift alone on PATH.
func fakeSwiftOnPath(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "swift"), []byte("#!/bin/sh\n"), 0o700); err != nil { // #nosec G306 -- test stand-in must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
}

// swiftPackage is a directory holding a Package.swift, and a directory below it.
func swiftPackage(t *testing.T) (root, sub string) {
	t.Helper()
	root = t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Package.swift"), []byte("// swift-tools-version:6.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sub = filepath.Join(root, "Sources", "Calc")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	return root, sub
}

// Preflight refuses, naming what to do, a host that is not Linux, a swift
// that is not on PATH, and a repository with no Package.swift at or above
// it (no repository at all included), before anything is spent. The OS is
// handed in, not read, so the non-Linux refusal is tested on every host.
func TestSwiftPreflightRefusesWithTheFix(t *testing.T) {
	root, sub := swiftPackage(t)
	fakeSwiftOnPath(t)
	for _, dir := range []string{root, sub} {
		if err := swiftPreflight("linux", dir, nil); err != nil {
			t.Errorf("a package at or above %s with swift on PATH must pass: %v", dir, err)
		}
	}
	if err := (swiftPlugin{}).Preflight(root, []string{"swift", "test"}); runtime.GOOS == "linux" && err != nil {
		t.Errorf("Preflight on this Linux host: %v", err)
	}

	for name, tc := range map[string]struct {
		goos, root string
		path       bool
		want       []string
	}{
		"not linux":          {"darwin", root, true, []string{"Linux", "darwin"}},
		"no swift":           {"linux", root, false, []string{`"swift"`, "PATH"}},
		"no Package.swift":   {"linux", t.TempDir(), true, []string{"Package.swift", "--repo-dir"}},
		"no repository root": {"linux", "", true, []string{"Package.swift", "--repo-dir"}},
	} {
		t.Run(strings.ReplaceAll(name, " ", "_"), func(t *testing.T) {
			if tc.path {
				fakeSwiftOnPath(t)
			} else {
				t.Setenv("PATH", t.TempDir())
			}
			err := swiftPreflight(tc.goos, tc.root, []string{"swift", "test"})
			if err == nil {
				t.Fatal("want a refusal")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("the refusal must name %q: %v", w, err)
				}
			}
		})
	}
}

// The default sandbox does not run Swift: its copy of the repository leaves
// out .build, which holds the dependency checkouts SwiftPM resolved, and it
// has no network to resolve them again. JailPreflight says so before any
// spend and names the substrate that works today. It asks nothing of the
// host, so it refuses even where everything Preflight checks is in place.
func TestSwiftJailPreflightRefusesNamingTheWorkspaceSubstrate(t *testing.T) {
	fakeSwiftOnPath(t)
	p, _ := ByName("swift")
	err := JailPreflight(p, []string{"swift", "test"})
	if err == nil {
		t.Fatal("the sandbox does not run Swift; JailPreflight must refuse")
	}
	for _, want := range []string{"--substrate workspace", "scratch clone", ".build"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name %q: %v", want, err)
		}
	}
	for _, never := range []string{"phase", "Phase", "task", "Task"} {
		if strings.Contains(err.Error(), never) {
			t.Errorf("the refusal speaks of plans (%q): %v", never, err)
		}
	}
}

// A Swift test reaches the code under test through its target's module, so
// the writer is told that module's name rather than a file path to import.
// A target whose name is no identifier is a module name SwiftPM derives,
// which is not guessed.
func TestSwiftImportIsTheSourceTargetsModule(t *testing.T) {
	p := swiftPlugin{}
	if got, ok := p.ImportPath("Sources/LassoParser/Sub/Evaluator.swift", nil); !ok || got != "LassoParser" {
		t.Errorf("ImportPath = %q, %v", got, ok)
	}
	for _, code := range []string{"Lib/X.swift", "Sources/my-lib/X.swift", "Tests/CalcTests/X.swift"} {
		if got, ok := p.ImportPath(code, nil); ok {
			t.Errorf("ImportPath(%q) = %q, want ok=false", code, got)
		}
	}
	if note := p.ImportNote("Calc", true); !strings.Contains(note, "@testable import Calc") {
		t.Errorf("ImportNote(ok) = %q", note)
	}
	if note := p.ImportNote("", false); note == "" || strings.Contains(note, "@testable import Calc") {
		t.Errorf("ImportNote(!ok) must say the module could not be determined: %q", note)
	}
}

// The writer is told to match the project's own tests first (framework,
// imports, @testable), and to put every test and helper inside one type
// whose name is corral's, so nothing it declares at top level can collide
// with the project's: both frameworks run such a type
// (testdata/runners/swift-authored-wrapped.txt), and a top-level test named
// like one of the project's does not build (swift-authored-collision.txt).
func TestSwiftTestWriterRanksTheProjectAndWrapsInOneType(t *testing.T) {
	sys := swiftPlugin{}.TestWriterSystem()
	conv := strings.Index(sys, "MATCH THE PROJECT'S CONVENTIONS")
	if conv < 0 {
		t.Fatalf("no precedence rule for the project's conventions:\n%s", sys)
	}
	for _, want := range []string{"swift-testing", "XCTest", "@testable", "XCTestCase", "@Suite", "Corral"} {
		if !strings.Contains(sys, want) {
			t.Errorf("writer prompt lacks %q:\n%s", want, sys)
		}
	}
	if i := strings.Index(sys, "ONE type"); i < conv {
		t.Errorf("the one-type rule must come after the project's conventions (at %d, conventions at %d)", i, conv)
	}
}
