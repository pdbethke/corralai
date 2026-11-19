// SPDX-License-Identifier: Elastic-2.0

package lang

import (
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
