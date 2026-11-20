import XCTest
@testable import Calc

// The XCTest form of the same rule: one XCTestCase subclass named for corral.
// Copied into Tests/CalcXCTests by the capture script.
final class CorralCalcTests: XCTestCase {
    func testAdds() {
        XCTAssertEqual(add(2, 3), 5)
    }
}
