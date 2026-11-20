import Testing
@testable import Calc

// An authored test as the Swift writer prompt asks for one: every test and
// helper inside ONE corral-named type, so nothing at the top level can
// collide with the target's own (CalcTests already declares parses() and
// `planted` there). Copied into Tests/CalcTests by the capture script.
@Suite struct CorralCalcTests {
    let planted = 2

    @Test func parses() {
        #expect(parse("12") == 12)
    }

    @Test func adds() {
        #expect(add(planted, 3) == 5)
    }
}
