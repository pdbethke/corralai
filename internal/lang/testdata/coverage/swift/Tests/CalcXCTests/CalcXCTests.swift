import XCTest
@testable import Calc

final class AdderTests: XCTestCase {
    func testAdds() {
        XCTAssertEqual(add(1, 1), 2)
    }
}
