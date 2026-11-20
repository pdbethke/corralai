// swift-tools-version:6.0
// The runner fixture for `swift test` (scripts/capture-runner-fixtures.sh):
// one library and two test targets, one per test framework, with no
// dependencies so it builds offline.
import PackageDescription

let package = Package(
    name: "Calc",
    targets: [
        .target(name: "Calc"),
        .testTarget(name: "CalcTests", dependencies: ["Calc"]),
        .testTarget(name: "CalcXCTests", dependencies: ["Calc"]),
    ]
)
