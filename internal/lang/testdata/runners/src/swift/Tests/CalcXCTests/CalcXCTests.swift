import Foundation
import XCTest
@testable import Calc

// The class is named apart from its target (CalcXCTests), so a capture shows
// which of the two each line of XCTest output names.
final class AdderTests: XCTestCase {
    // Two tests whose names share a prefix: running testAdds alone must not
    // run testAddsTwice.
    func testAdds() {
        XCTAssertEqual(add(2, 3), 5)
    }

    func testAddsTwice() {
        XCTAssertEqual(add(add(1, 1), 3), 5)
    }

    // Fails only under CORRAL_FAIL_XCTEST, so one package gives both a passing
    // and a failing run.
    func testFails() {
        let planted = ProcessInfo.processInfo.environment["CORRAL_FAIL_XCTEST"] != nil
        XCTAssertEqual(add(2, 2), planted ? 5 : 4)
    }
}
