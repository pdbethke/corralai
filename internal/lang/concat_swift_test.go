// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"strings"
	"testing"
)

func swiftPart(id, body string) AuthoredPart {
	return AuthoredPart{MutantID: id, Source: "import XCTest\n@testable import Calc\n\nfinal class Calc_corralTests: XCTestCase {\n" + body + "}\n"}
}

// Every proven Swift test declares the one type named after the file it was
// written as (the writer prompt's rule), so two of them always collide on it.
// The merge hoists the imports once and suffixes that type in each part with
// its mutant id, everywhere it is named outside a literal: XCTest and
// swift-testing find a test type by its declaration, not its name, so each
// suffixed type still runs: a merged file of two XCTest parts and one of two
// swift-testing parts, written by this merge, built and ran every test on
// Swift 6.3.2 and 6.4.2 (swift:6.3.2-noble and swift:6.4.2-noble, 2026-11-26:
// "Executed 3 tests" with the package's own, "Test run with 2 tests in 2
// suites passed").
func TestSwiftMergeSuffixesTheSharedTypeAndHoistsImports(t *testing.T) {
	merged, extra := ConcatAuthored(swiftPlugin{}, []AuthoredPart{
		swiftPart("s0/m1", "    func testAdds() { XCTAssertEqual(add(1, 2), 3) }\n    func testSelf() { XCTAssertTrue(Calc_corralTests.self == Calc_corralTests.self) }\n"),
		swiftPart("s0/m2", "    func testSubtracts() { XCTAssertEqual(sub(3, 2), 1) }\n"),
	})
	if len(extra) != 0 {
		t.Fatalf("parts carried out: %+v", extra)
	}
	for _, want := range []string{
		"final class Calc_corralTests_s0_m1: XCTestCase {",
		"final class Calc_corralTests_s0_m2: XCTestCase {",
		"func testAdds()", "func testSubtracts()",
		// The type's other mentions in its own part take the same suffix.
		"XCTAssertTrue(Calc_corralTests_s0_m1.self == Calc_corralTests_s0_m1.self)",
	} {
		if !strings.Contains(merged, want) {
			t.Errorf("merged file lacks %q:\n%s", want, merged)
		}
	}
	if strings.Count(merged, "import XCTest\n") != 1 || strings.Count(merged, "@testable import Calc\n") != 1 {
		t.Errorf("imports not hoisted once each:\n%s", merged)
	}
	if !strings.HasPrefix(merged, "import XCTest\n@testable import Calc\n") {
		t.Errorf("imports are not first:\n%s", merged)
	}
}

// What is not the file's type is not renamed: an extension of another type
// two parts both write, or a top-level function, would change what the
// proven test called, so a second such part is carried out with the reason.
func TestSwiftMergeRefusesACollisionThatIsNotATestType(t *testing.T) {
	for _, top := range []string{"extension Calc {\n    func twice() -> Int { 2 }\n}\n", "func helper() -> Int { 1 }\n"} {
		a := AuthoredPart{MutantID: "s0/m1", Source: "import XCTest\n" + top}
		b := AuthoredPart{MutantID: "s0/m2", Source: "import XCTest\n" + top}
		_, extra := ConcatAuthored(swiftPlugin{}, []AuthoredPart{a, b})
		if len(extra) != 1 || extra[0].MutantID != "s0/m2" || extra[0].Reason == "" {
			t.Errorf("%q: extra = %+v, want the second part carried out with a reason", top, extra)
		}
	}
}

// swift-testing's shape: an @Suite struct, the attribute on its own line.
func TestSwiftMergeHandlesASuiteStruct(t *testing.T) {
	part := func(id, test string) AuthoredPart {
		return AuthoredPart{MutantID: id, Source: "import Testing\n@testable import Calc\n\n@Suite(\"Calc\")\nstruct Calc_corralTests {\n    @Test func " + test + "() { #expect(add(1, 1) == 2) }\n}\n"}
	}
	merged, extra := ConcatAuthored(swiftPlugin{}, []AuthoredPart{part("s0/m1", "adds"), part("s0/m2", "addsAgain")})
	if len(extra) != 0 {
		t.Fatalf("parts carried out: %+v", extra)
	}
	if !strings.Contains(merged, "@Suite(\"Calc\")\nstruct Calc_corralTests_s0_m1 {") || !strings.Contains(merged, "struct Calc_corralTests_s0_m2 {") {
		t.Errorf("suite types not suffixed under their attribute:\n%s", merged)
	}
}
