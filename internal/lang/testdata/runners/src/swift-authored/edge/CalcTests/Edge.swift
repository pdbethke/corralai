import Testing
@testable import Calc

// Failure shapes the fixture's own tests do not show, for swift-testing's
// record (the `swift test` row's failure hook): a nested suite, a suite with
// a display name, a test that throws, a suite whose init throws, arguments
// that cannot be computed, and three that do not fail (a known issue, a
// disabled test, a warning). Copied into Tests/CalcTests by the capture
// script, which runs the package with it hooked (swift-hook-edge-*.txt).
struct EdgeError: Error {}

func edgeArguments() throws -> [Int] { throw EdgeError() }

@Test func throwsAnError() throws { throw EdgeError() }

@Test(arguments: try edgeArguments()) func badArguments(n: Int) {}

@Suite struct Outer {
    @Suite struct Inner {
        @Test func nested() { #expect(add(1, 1) == 3) }
    }
}

@Suite("Pretty suite") struct Pretty {
    @Test func inPretty() { #expect(add(1, 1) == 3) }
}

@Suite struct ThrowingInit {
    init() throws { throw EdgeError() }
    @Test func neverRuns() {}
}

@Test func knownIssue() { withKnownIssue { #expect(add(1, 1) == 3) } }

@Test(.disabled("off")) func disabledOne() {}

@Test func warningOnly() { Issue.record("just a warning", severity: .warning) }
