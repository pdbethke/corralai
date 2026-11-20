// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"path"
	"strings"
	"testing"
)

// TestSwiftAuthoredTestLandsInTheDevTestsTarget: SwiftPM compiles a test only
// inside a test target's directory, and pairs Sources/<Target>/ with
// Tests/<Target>Tests/ by name alone. A source whose dev test is named for
// behaviour (Lariat's Evaluating.swift), or which only another target's test
// covers (the test evidence names), has its authored test written into THAT
// test's directory, never into a Tests/<Target>Tests/ derived from the name,
// which may not exist and which SwiftPM would then never compile: every
// survivor would read unproven while the run looked healthy.
func TestSwiftAuthoredTestLandsInTheDevTestsTarget(t *testing.T) {
	for _, tc := range []struct{ name, code, dev, want string }{
		{"a test named for the source", "Sources/Calc/Calc.swift", "Tests/CalcTests/CalcTests.swift", "Tests/CalcTests/Calc_corralTests.swift"},
		{"a test named for behaviour", "Sources/LassoParser/Evaluator.swift", "Tests/LassoParserTests/Evaluating.swift", "Tests/LassoParserTests/Evaluator_corralTests.swift"},
		{"another target's test, as evidence names it", "Sources/Lasso/Parser.swift", "Tests/IntegrationTests/RoundTrip.swift", "Tests/IntegrationTests/Parser_corralTests.swift"},
		{"a nested test directory", "Sources/Lasso/Parser.swift", "Tests/IntegrationTests/Sub/RoundTrip.swift", "Tests/IntegrationTests/Sub/Parser_corralTests.swift"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := authoredTestPath(AuthoredLanding{}, tc.code, tc.dev, nil)
			if got != tc.want {
				t.Fatalf("authoredTestPath(%q, %q) = %q, want %q", tc.code, tc.dev, got, tc.want)
			}
			if path.Dir(got) != path.Dir(tc.dev) {
				t.Fatalf("authored test %q is not in the dev test's directory %q", got, path.Dir(tc.dev))
			}
		})
	}
}

// TestTheMutantGateChecksTheDevTest: a mutant can break only the tests'
// view of the code (a changed signature a test calls), so the compile gate
// checks it with a test the tree holds: the authored test when it is a file
// the tree has (a landing Into one), else the developer's own test, which
// is always there in repo mode (the jail's workspace map holds it; on the
// workspace substrate the map is empty and the checkout it was read from
// is the tree). Only when the dev test is not in the tree (single-file
// mode, where it sits at the synthetic path under another name) or there
// is none does the gate fall back to the code alone, as before.
func TestTheMutantGateChecksTheDevTest(t *testing.T) {
	const code, dev = "Sources/Calc/Calc.swift", "Tests/CalcTests/CalcTests.swift"
	jailBase := map[string]string{code: "x", dev: "y", "Package.swift": "z"}
	for _, tc := range []struct {
		name string
		s    JailScorer
		code string
		base map[string]string
		want string
	}{
		{"jail repo mode", JailScorer{BaseFiles: jailBase, DevTestPath: dev}, code, jailBase, dev},
		{"workspace substrate", JailScorer{BaseFiles: map[string]string{}, DevTestPath: dev}, code, map[string]string{}, dev},
		{"python dev test in the tree", JailScorer{BaseFiles: map[string]string{"src/a.py": "x", "tests/test_a.py": "y"}, DevTestPath: "tests/test_a.py"}, "src/a.py", map[string]string{"src/a.py": "x", "tests/test_a.py": "y"}, "tests/test_a.py"},
		{"single-file, dev test under another name", JailScorer{DevTestPath: "my_tests.py"}, "a.py", map[string]string{"a.py": "x", "test_a.py": "y"}, "a.py"},
		{"single-file, dev test at the synthetic path", JailScorer{DevTestPath: "test_a.py"}, "a.py", map[string]string{"a.py": "x", "test_a.py": "y"}, "test_a.py"},
		{"no dev test", JailScorer{BaseFiles: map[string]string{code: "x"}}, code, map[string]string{code: "x"}, code},
	} {
		t.Run(strings.ReplaceAll(tc.name, " ", "_"), func(t *testing.T) {
			if got := tc.s.mutantGateTestPath(tc.code, tc.base); got != tc.want {
				t.Fatalf("mutantGateTestPath = %q, want %q", got, tc.want)
			}
		})
	}
}

// For Swift that dev test names the test target, so the gate builds the
// source together with the tests that call it: a mutant that compiles in
// Calc but breaks CalcTests is invalid, not a kill.
func TestSwiftMutantGateBuildsTheDevTestsTarget(t *testing.T) {
	const code, dev = "Sources/Calc/Calc.swift", "Tests/CalcTests/CalcTests.swift"
	s := JailScorer{BaseFiles: map[string]string{}, DevTestPath: dev}
	p, err := pluginFor(code)
	if err != nil {
		t.Fatal(err)
	}
	got := compileCheckFor(p, "", code, s.mutantGateTestPath(code, map[string]string{}))
	if len(got) != 1 || strings.Join(got[0], " ") != "swift build --target CalcTests" {
		t.Fatalf("the Swift mutant gate = %q, want swift build --target CalcTests", got)
	}
}

// TestTheWriterIsToldTheFileTheScorerWrites: the developer kept corral's
// earlier Tests/CalcTests/Calc_corralTests.swift, so the scorer writes the
// next authored test to Calc_corral1Tests.swift. The writer must be told that
// file, not the one the landing rule names on an empty tree: a Swift writer
// names its one type after its file, so told the earlier file it would
// declare that file's type again, the target would not build ("invalid
// redeclaration"), and every survivor would read unproven.
func TestTheWriterIsToldTheFileTheScorerWrites(t *testing.T) {
	const code, dev, kept = "Sources/Calc/Calc.swift", "Tests/CalcTests/CalcTests.swift", "Tests/CalcTests/Calc_corralTests.swift"
	s := JailScorer{BaseFiles: map[string]string{code: "x", dev: "y", kept: "z"}, DevTestPath: dev}
	written := s.AuthoredTestPath(code)
	if written == kept || path.Dir(written) != path.Dir(dev) {
		t.Fatalf("precondition: the scorer writes past the kept file in the dev test's directory, got %q", written)
	}
	rs := RunSpec{Lang: "swift", Goal: "g", CodePath: code, Code: "CODE", DevTestPath: dev, AuthoredTestPath: written, RepoDir: "/repo"}
	got := renderTestWriter(rs, nil, nil)
	if !strings.Contains(got, `"`+written+`"`) {
		t.Errorf("the writer is not told the file the scorer writes, %q:\n%s", written, got)
	}
	if strings.Contains(got, `"`+kept+`"`) {
		t.Errorf("the writer is told the developer's kept file %q:\n%s", kept, got)
	}
}
