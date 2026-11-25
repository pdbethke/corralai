import Foundation
import Testing

// A test that crashes the test process after the fixture's planted
// failures have been reported (it waits two seconds first), for
// swift-testing's record: the capture script runs the package with it
// hooked (swift-hook-crash-*.txt).
@Test func crashes() async throws {
    try await Task.sleep(nanoseconds: 2_000_000_000)
    fatalError("planted crash")
}
