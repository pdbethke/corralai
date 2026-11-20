import Testing
@testable import Calc

// Declaration shapes the static test list reads that the fixture's own tests
// do not show: a suite with no @Suite attribute, a suite with a display name,
// and @Test functions that are static, async throws, or carry a second
// attribute. Copied into Tests/CalcTests by the capture script, which lists
// the package with it (swift-shapes-list.txt).
struct Plain {
    @Test @MainActor func inPlain() {}
    @Test static func staticOne() {}
    @Test func asyncOne() async throws {}
}

@Suite("A display name") struct Named {
    @Test func t() {}
}
