import Testing
@testable import Calc

// What the one-type rule prevents: a top-level test named like one the target
// already declares (CalcTests.swift's parses()). Copied into Tests/CalcTests
// by the capture script, whose build of the target then fails.
@Test func parses() {
    #expect(parse("12") == 12)
}
