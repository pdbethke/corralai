import Foundation
import Testing
@testable import Calc

// CORRAL_FAIL_TESTING plants three failures, one of each shape swift-testing
// prints (a plain test, a test with a display name, one argument of a
// parameterised test), so one package gives both a passing and a failing run.
let planted = ProcessInfo.processInfo.environment["CORRAL_FAIL_TESTING"] != nil

// Two top-level tests whose names share a prefix: running parses() alone must
// not run parsesTwice().
@Test func parses() {
    #expect(parse("7") == 7)
}

@Test func parsesTwice() {
    #expect(parse("7") == parse("7"))
}

@Suite struct Arithmetic {
    @Test("adds two numbers") func adds() {
        #expect(add(2, 3) == (planted ? 6 : 5))
    }

    @Test(arguments: [1, 2, 3]) func doubles(value: Int) {
        #expect(double(value) == value + value + (planted && value == 2 ? 1 : 0))
    }

    @Test func fails() {
        #expect(add(2, 2) == (planted ? 5 : 4))
    }
}
