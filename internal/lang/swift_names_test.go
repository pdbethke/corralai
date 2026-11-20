// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestSwiftTestNamesMatchWhatSwiftTestListPrinted: for each test file of the
// runner fixture, the ids read from its source are exactly the ids
// `swift test list` printed for its target (testdata/runners/swift-list.txt,
// Swift 6.3.2), in the same order; and the file holding a parameterised test
// lists nothing, since its id carries argument labels this reader does not
// derive.
func TestSwiftTestNamesMatchWhatSwiftTestListPrinted(t *testing.T) {
	listed, err := os.ReadFile("testdata/runners/swift-list.txt")
	if err != nil {
		t.Fatal(err)
	}
	ids := swiftParseList(string(listed))
	byTarget := map[string][]string{}
	for _, id := range ids {
		target, _, _ := strings.Cut(id, ".")
		byTarget[target] = append(byTarget[target], id)
	}
	root := "testdata/runners/src/swift"
	for _, rel := range []string{"Tests/CalcXCTests/CalcXCTests.swift", "Tests/CalcTests/CalcTests.swift"} {
		src, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		got := (swiftPlugin{}).TestNamesInSource(rel, string(src))
		target, _ := swiftTestTarget(rel)
		if strings.Contains(string(src), "arguments:") {
			if got != nil {
				t.Errorf("%s holds a parameterised test and must list nothing, got %q", rel, got)
			}
			continue
		}
		if want := byTarget[target]; len(want) == 0 || !slices.Equal(got, want) {
			t.Errorf("%s: TestNamesInSource = %q, swift test list printed %q", rel, got, want)
		}
	}
}

const swiftTestingSrc = `import Testing
@testable import Calc

// @Test func commented() {}
/* a block /* nested */ @Test func blocked() {} */
let note = "@Test func quoted() { \(String("}")) }"
let raw = #"@Test func raw() {"#
let multi = """
    @Test func multi() {
    """

@Test func top() {
    #expect(parse("7") == 7)
}

@Suite("Arithmetic, with a display name") struct Arithmetic {
    let base = 2

    @Test("adds two numbers") func adds() async throws {
        #expect(add(2, 3) == 5)
    }

    func helper() -> Int { base }
}

struct Plain {
    @Test @MainActor func inPlain() {}
}
`

func TestSwiftTestNamesReadsSwiftTesting(t *testing.T) {
	got := (swiftPlugin{}).TestNamesInSource("Tests/CalcTests/CalcTests.swift", swiftTestingSrc)
	want := []string{"CalcTests.top()", "CalcTests.Arithmetic/adds()", "CalcTests.Plain/inPlain()"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

const xctestSrc = `import XCTest
@testable import Calc

final class AdderTests: XCTestCase {
    private var fixture = 1
    static var shared = 2

    override func setUp() {}

    func testAdds() {
        XCTAssertEqual(add(2, 3), 5)
    }

    func testThrows() async throws {}

    func helper() {}
}
`

func TestSwiftTestNamesReadsXCTest(t *testing.T) {
	got := (swiftPlugin{}).TestNamesInSource("Tests/CalcXCTests/Sub/AdderTests.swift", xctestSrc)
	want := []string{"CalcXCTests.AdderTests/testAdds", "CalcXCTests.AdderTests/testThrows"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// Every shape this reader cannot name completely lists nothing, rather than
// the tests it could name: the critic is held to the list.
func TestSwiftTestNamesListsNothingItCannotNameCompletely(t *testing.T) {
	for name, src := range map[string]string{
		"parameterised":          "@Test(arguments: [1, 2]) func doubles(value: Int) {}\n",
		"nested suite":           "struct Outer {\n  struct Inner {\n    @Test func t() {}\n  }\n}\n",
		"extension":              "extension Arithmetic {\n  @Test func t() {}\n}\n",
		"generic suite":          "struct S<T> {\n  @Test func t() {}\n}\n",
		"in a function":          "func f() {\n  @Test func t() {}\n}\n",
		"conditional":            "#if os(Linux)\n@Test func t() {}\n#endif\n",
		"backticks":              "@Test func `adds two`() {}\n",
		"xctest in extension":    "import XCTest\nextension AdderTests {\n  func testMore() {}\n}\n",
		"unknown base class":     "import XCTest\nclass A: MyCase {\n  func testA() {}\n}\n",
		"inherited tests":        "import XCTest\nclass Base: XCTestCase {\n  func testA() {}\n}\nclass Sub: Base {}\n",
		"private xctest":         "import XCTest\nclass A: XCTestCase {\n  private func testA() {}\n}\n",
		"class method":           "import XCTest\nclass A: XCTestCase {\n  class func testA() {}\n}\n",
		"xctest with parameters": "import XCTest\nclass A: XCTestCase {\n  func testA(x: Int) {}\n}\n",
		"unbalanced":             "@Test func t() {\n",
		"unterminated string":    "let s = \"open\n@Test func t() {}\n",
		"unterminated comment":   "/* open\n@Test func t() {}\n",
	} {
		if got := (swiftPlugin{}).TestNamesInSource("Tests/CalcTests/X.swift", src); got != nil {
			t.Errorf("%s: got %q, want nil", name, got)
		}
	}
	for _, p := range []string{"X.swift", "Tests/X.swift", "Sources/Calc/X.swift", "Tests/my-tests/X.swift"} {
		if got := (swiftPlugin{}).TestNamesInSource(p, "@Test func t() {}\n"); got != nil {
			t.Errorf("%s is in no test target with a module name; got %q", p, got)
		}
	}
}

// The critic's names are ids in `swift test list`'s own form, so the row
// resolves each through the listing as the id it is, and each runs alone.
func TestSwiftCriticNamesResolveThroughTheListing(t *testing.T) {
	listed, err := os.ReadFile("testdata/runners/swift-list.txt")
	if err != nil {
		t.Fatal(err)
	}
	ids := swiftParseList(string(listed))
	p, _ := ByName("swift")
	inv := Cmd([]string{"swift", "test"})
	if !ResolvesByList(p, inv) {
		t.Fatal("the swift row lists the whole package, so a critic's name must be resolved through the listing")
	}
	src, err := os.ReadFile("testdata/runners/src/swift/Tests/CalcXCTests/CalcXCTests.swift")
	if err != nil {
		t.Fatal(err)
	}
	names := (swiftPlugin{}).TestNamesInSource("Tests/CalcXCTests/CalcXCTests.swift", string(src))
	if len(names) == 0 {
		t.Fatal("precondition: the XCTest file lists its tests")
	}
	for _, n := range names {
		if id, ok := ResolveForRun(p, inv, ids, n); !ok || id != n {
			t.Errorf("ResolveForRun(%q) = %q, %v", n, id, ok)
		}
	}
	if _, ok := ResolveForRun(p, inv, ids, "testAdds"); ok {
		t.Error("a bare method name is no listed id")
	}
}

// Declaration shapes beyond the fixture's own tests, as `swift test list`
// printed them with src/swift-authored/shapes/CalcTests/Shapes.swift in the
// package (swift-shapes-list.txt): a suite with no @Suite attribute, one with
// a display name (the id carries the type's name, not the display name), and
// static, async throws and doubly attributed @Test functions.
func TestSwiftTestNamesMatchTheListedShapes(t *testing.T) {
	const rel = "Tests/CalcTests/Shapes.swift"
	src, err := os.ReadFile("testdata/runners/src/swift-authored/shapes/CalcTests/Shapes.swift")
	if err != nil {
		t.Fatal(err)
	}
	got := (swiftPlugin{}).TestNamesInSource(rel, string(src))
	listed := map[string]bool{}
	for _, id := range swiftParseList(readFixture(t, "swift-shapes-list.txt")) {
		listed[id] = true
	}
	want := []string{"CalcTests.Plain/inPlain()", "CalcTests.Plain/staticOne()", "CalcTests.Plain/asyncOne()", "CalcTests.Named/t()"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q (file order)", got, want)
	}
	for _, id := range got {
		if !listed[id] {
			t.Errorf("%s is not an id `swift test list` printed", id)
		}
	}
	for id := range listed {
		if (strings.HasPrefix(id, "CalcTests.Plain/") || strings.HasPrefix(id, "CalcTests.Named/")) && !slices.Contains(got, id) {
			t.Errorf("swift test list printed %s, which the reader missed", id)
		}
	}
}
